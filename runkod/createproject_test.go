package runkod

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	runkov1 "github.com/saxocellphone/runko/gen/runko/v1"
	"github.com/saxocellphone/runko/gen/runko/v1/runkov1connect"
)

func projectRPC(srv *httptest.Server) runkov1connect.ProjectServiceClient {
	return runkov1connect.NewProjectServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))
}

func changeRPC(srv *httptest.Server) runkov1connect.ChangeServiceClient {
	return runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))
}

// The whole §10.1 story over the wire: preview shows the exact files,
// create registers an open Change (trunk untouched), the Change's diff IS
// the plan, and landing it through the ordinary §13.5 gates is what makes
// the project appear in ListProjects - tree-as-truth end to end.
func TestCreateProjectArrivesAsAChangeAndLands(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	projects := projectRPC(srv)
	changes := changeRPC(srv)

	intent := &runkov1.CreateProjectIntent{
		Name:   "payments-api",
		Type:   "service",
		Owners: []string{"group:commerce"},
	}

	preview, err := projects.PreviewCreateProject(ctx, connect.NewRequest(&runkov1.PreviewCreateProjectRequest{Intent: intent}))
	if err != nil {
		t.Fatalf("PreviewCreateProject: %v", err)
	}
	var previewPaths []string
	for _, f := range preview.Msg.Files {
		previewPaths = append(previewPaths, f.Path)
	}
	if len(previewPaths) == 0 || !containsString(previewPaths, "PROJECT.yaml") {
		t.Fatalf("preview should plan a PROJECT.yaml, got %v", previewPaths)
	}

	created, err := projects.CreateProject(ctx, connect.NewRequest(&runkov1.CreateProjectRequest{Intent: intent}))
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	change := created.Msg.Change
	if change.GetState() != runkov1.ChangeState_CHANGE_STATE_OPEN {
		t.Fatalf("created change state: want OPEN, got %v", change.GetState())
	}
	if !strings.HasPrefix(change.GetGitRef(), "refs/changes/") {
		t.Fatalf("created change ref: want refs/changes/<id>/head, got %q", change.GetGitRef())
	}

	// Trunk must be untouched (§6.9): the project doesn't exist yet.
	list, err := projects.ListProjects(ctx, connect.NewRequest(&runkov1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, p := range list.Msg.Projects {
		if p.Name == "payments-api" {
			t.Fatalf("project visible before its change landed - trunk was written directly")
		}
	}

	// The Change's own diff is exactly the plan.
	diff, err := changes.GetChangeDiff(ctx, connect.NewRequest(&runkov1.GetChangeDiffRequest{ChangeId: change.GetId()}))
	if err != nil {
		t.Fatalf("GetChangeDiff: %v", err)
	}
	var diffPaths []string
	for _, f := range diff.Msg.Files {
		diffPaths = append(diffPaths, f.Path)
	}
	if !containsString(diffPaths, "payments-api/PROJECT.yaml") {
		t.Fatalf("change diff should carry the planned files, got %v", diffPaths)
	}

	// Ordinary gates: blocked until the declared owner approves.
	if _, err := changes.LandChange(ctx, connect.NewRequest(&runkov1.LandChangeRequest{ChangeId: change.GetId()})); err == nil {
		t.Fatalf("land before owner approval should be refused")
	}
	if _, err := changes.ApproveChange(ctx, connect.NewRequest(&runkov1.ApproveChangeRequest{
		ChangeId: change.GetId(), OwnerRef: "group:commerce", ApprovedBy: "user:val",
	})); err != nil {
		t.Fatalf("ApproveChange: %v", err)
	}
	landed, err := changes.LandChange(ctx, connect.NewRequest(&runkov1.LandChangeRequest{ChangeId: change.GetId()}))
	if err != nil || !landed.Msg.Landed {
		t.Fatalf("LandChange: landed=%v err=%v", landed.Msg.GetLanded(), err)
	}

	// Now, and only now, the project exists.
	list, err = projects.ListProjects(ctx, connect.NewRequest(&runkov1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects after land: %v", err)
	}
	found := false
	for _, p := range list.Msg.Projects {
		found = found || p.Name == "payments-api"
	}
	if !found {
		t.Fatalf("landed project missing from ListProjects")
	}
}

func TestCreateProjectRejectsDuplicatesAndInvalidIntent(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	projects := projectRPC(srv)

	// newTestServer seeds checkout-api at commerce/checkout on trunk.
	_, err := projects.CreateProject(ctx, connect.NewRequest(&runkov1.CreateProjectRequest{
		Intent: &runkov1.CreateProjectIntent{Name: "checkout-api", Type: "service"},
	}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate name: want CodeAlreadyExists, got %v", err)
	}
	if detail := errorDetail(t, err); detail.Code != "already_exists" {
		t.Fatalf("duplicate name detail code: want already_exists, got %q", detail.Code)
	}

	_, err = projects.PreviewCreateProject(ctx, connect.NewRequest(&runkov1.PreviewCreateProjectRequest{
		Intent: &runkov1.CreateProjectIntent{Name: "", Type: "service"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty name: want CodeInvalidArgument, got %v", err)
	}
	if detail := errorDetail(t, err); detail.Field == "" {
		t.Fatalf("validation detail should name the offending field")
	}
}

// A brand-new monorepo's FIRST project: trunk is unborn, the create still
// arrives as a Change (base ""), and landing it bootstraps trunk via
// land.Land's zero-OID CAS (§28.3 stage 11b).
func TestCreateProjectBootstrapsUnbornTrunk(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	ctx := context.Background()

	created, err := projectRPC(srv).CreateProject(ctx, connect.NewRequest(&runkov1.CreateProjectRequest{
		Intent: &runkov1.CreateProjectIntent{Name: "genesis", Type: "library", Owners: []string{"group:eng"}},
	}))
	if err != nil {
		t.Fatalf("CreateProject on unborn trunk: %v", err)
	}
	if created.Msg.Change.GetBaseSha() != "" {
		t.Fatalf("unborn trunk base: want empty, got %q", created.Msg.Change.GetBaseSha())
	}

	changes := changeRPC(srv)
	if _, err := changes.ApproveChange(ctx, connect.NewRequest(&runkov1.ApproveChangeRequest{
		ChangeId: created.Msg.Change.GetId(), OwnerRef: "group:eng", ApprovedBy: "user:val",
	})); err != nil {
		t.Fatalf("ApproveChange: %v", err)
	}
	landed, err := changes.LandChange(ctx, connect.NewRequest(&runkov1.LandChangeRequest{ChangeId: created.Msg.Change.GetId()}))
	if err != nil || !landed.Msg.Landed {
		t.Fatalf("bootstrap land: landed=%v err=%v", landed.Msg.GetLanded(), err)
	}
}

func containsString(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}
