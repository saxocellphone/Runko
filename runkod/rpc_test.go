package runkod

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	runkov1 "github.com/saxocellphone/runko/gen/runko/v1"
	"github.com/saxocellphone/runko/gen/runko/v1/runkov1connect"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
	"github.com/saxocellphone/runko/search"
)

// rpcAuth injects the bearer token on every Connect call, the same way the
// web client does (web/src/api/client.ts's interceptor).
func rpcAuth(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		}
	}))
}

// errorDetail digs the runko.v1.ErrorDetail out of a Connect error - the
// §6.5 structured shape clients branch on (proto/README.md item 4).
func errorDetail(t *testing.T, err error) *runkov1.ErrorDetail {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("not a connect error: %v", err)
	}
	for _, d := range cerr.Details() {
		if msg, verr := d.Value(); verr == nil {
			if detail, ok := msg.(*runkov1.ErrorDetail); ok {
				return detail
			}
		}
	}
	t.Fatalf("no ErrorDetail attached to: %v", err)
	return nil
}

func TestRPCRequiresAuth(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	client := runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL)
	_, err := client.GetChange(context.Background(), connect.NewRequest(&runkov1.GetChangeRequest{ChangeId: changeID}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want CodeUnauthenticated without a token, got %v", err)
	}

	authed := runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))
	resp, err := authed.GetChange(context.Background(), connect.NewRequest(&runkov1.GetChangeRequest{ChangeId: changeID}))
	if err != nil {
		t.Fatalf("authed GetChange: %v", err)
	}
	if resp.Msg.Change.GetId() != changeID {
		t.Fatalf("GetChange id: want %s, got %s", changeID, resp.Msg.Change.GetId())
	}
}

// TestRPCCORSPreflight pins what a browser needs before any RPC works
// cross-origin (the Vite dev server and any deployed web origin): an
// unauthenticated OPTIONS answered with the CORS allow headers.
func TestRPCCORSPreflight(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/runko.v1.ChangeService/GetChange", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,connect-protocol-version")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status: want 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin: want *, got %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatalf("Allow-Headers missing")
	}
}

// TestRPCChangeReadSurface drives the read RPCs the Change page issues on
// load (GetChange + stack + diff + merge requirements + affected) against
// the ci.checks fixture, asserting the declared-but-unreported "unit" check
// gates exactly as the REST view reports it (§14.9, stage 11c).
func TestRPCChangeReadSurface(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	list, err := client.ListChanges(ctx, connect.NewRequest(&runkov1.ListChangesRequest{}))
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(list.Msg.Changes) != 1 || list.Msg.Changes[0].GetState() != runkov1.ChangeState_CHANGE_STATE_OPEN {
		t.Fatalf("ListChanges default-open: %+v", list.Msg.Changes)
	}

	stack, err := client.GetChangeStack(ctx, connect.NewRequest(&runkov1.GetChangeStackRequest{ChangeId: changeID}))
	if err != nil {
		t.Fatalf("GetChangeStack: %v", err)
	}
	if len(stack.Msg.Changes) != 1 || stack.Msg.Position != 0 {
		t.Fatalf("single-change stack: want [self]@0, got %d@%d", len(stack.Msg.Changes), stack.Msg.Position)
	}

	diff, err := client.GetChangeDiff(ctx, connect.NewRequest(&runkov1.GetChangeDiffRequest{ChangeId: changeID}))
	if err != nil {
		t.Fatalf("GetChangeDiff: %v", err)
	}
	if len(diff.Msg.Files) != 1 || diff.Msg.Files[0].GetPath() != "commerce/checkout/main.go" {
		t.Fatalf("diff files: %+v", diff.Msg.Files)
	}
	if diff.Msg.Files[0].GetStatus() != runkov1.FileDiffStatus_FILE_DIFF_STATUS_ADDED {
		t.Fatalf("main.go should be ADDED, got %v", diff.Msg.Files[0].GetStatus())
	}
	if diff.Msg.Files[0].GetProject() != "checkout-api" {
		t.Fatalf("project tag: want checkout-api, got %q", diff.Msg.Files[0].GetProject())
	}

	reqs, err := client.GetMergeRequirements(ctx, connect.NewRequest(&runkov1.GetMergeRequirementsRequest{ChangeId: changeID}))
	if err != nil {
		t.Fatalf("GetMergeRequirements: %v", err)
	}
	r := reqs.Msg.Requirements
	if r.GetMergeable() {
		t.Fatalf("declared-but-unreported check must block (§14.9): %+v", r)
	}
	if got := r.GetChecks().GetRequired(); len(got) != 1 || got[0] != "unit" {
		t.Fatalf("required checks: want [unit], got %v", got)
	}

	aff, err := client.GetAffected(ctx, connect.NewRequest(&runkov1.GetAffectedRequest{
		Target: &runkov1.GetAffectedRequest_ChangeId{ChangeId: changeID},
	}))
	if err != nil {
		t.Fatalf("GetAffected(change): %v", err)
	}
	if got := aff.Msg.Affected.GetProjects(); len(got) != 1 || got[0].GetName() != "checkout-api" {
		t.Fatalf("affected projects: %+v", got)
	}

	affPaths, err := client.GetAffected(ctx, connect.NewRequest(&runkov1.GetAffectedRequest{
		Target: &runkov1.GetAffectedRequest_Paths{Paths: &runkov1.ChangePaths{Paths: []string{"commerce/checkout/api.go"}}},
	}))
	if err != nil {
		t.Fatalf("GetAffected(paths): %v", err)
	}
	if got := affPaths.Msg.Affected.GetProjects(); len(got) != 1 || got[0].GetName() != "checkout-api" {
		t.Fatalf("affected-by-paths projects: %+v", got)
	}
}

// TestRPCApproveAndLandFlow is the owner-gate loop the web UI drives:
// approving a non-required owner fails with the structured
// not_a_required_owner detail, landing while the owner gate is outstanding
// fails FailedPrecondition with not_mergeable (the same core REST 409s
// through), and after the right approval the Change lands - then an
// idempotent replay reports the same success and the inbox moves it to
// landed.
func TestRPCApproveAndLandFlow(t *testing.T) {
	srv, _, changeID, _ := newApproveTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	_, err := client.ApproveChange(ctx, connect.NewRequest(&runkov1.ApproveChangeRequest{
		ChangeId: changeID, OwnerRef: "group:nobody", ApprovedBy: "reviewer",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("approving a non-required owner: want InvalidArgument, got %v", err)
	}
	if detail := errorDetail(t, err); detail.GetCode() != "not_a_required_owner" {
		t.Fatalf("detail code: want not_a_required_owner, got %q", detail.GetCode())
	}

	_, err = client.LandChange(ctx, connect.NewRequest(&runkov1.LandChangeRequest{ChangeId: changeID}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("land with outstanding owner: want FailedPrecondition, got %v", err)
	}
	if detail := errorDetail(t, err); detail.GetCode() != "not_mergeable" {
		t.Fatalf("detail code: want not_mergeable, got %q", detail.GetCode())
	}

	approve, err := client.ApproveChange(ctx, connect.NewRequest(&runkov1.ApproveChangeRequest{
		ChangeId: changeID, OwnerRef: "group:commerce-eng", ApprovedBy: "reviewer",
	}))
	if err != nil {
		t.Fatalf("ApproveChange: %v", err)
	}
	if !approve.Msg.Requirements.GetMergeable() {
		t.Fatalf("after approval, want mergeable: %+v", approve.Msg.Requirements)
	}

	land, err := client.LandChange(ctx, connect.NewRequest(&runkov1.LandChangeRequest{ChangeId: changeID}))
	if err != nil {
		t.Fatalf("LandChange: %v", err)
	}
	if !land.Msg.Landed || land.Msg.LandedSha == "" {
		t.Fatalf("land response: %+v", land.Msg)
	}

	again, err := client.LandChange(ctx, connect.NewRequest(&runkov1.LandChangeRequest{ChangeId: changeID}))
	if err != nil || !again.Msg.Landed || again.Msg.LandedSha != land.Msg.LandedSha {
		t.Fatalf("idempotent land replay: %+v, %v", again, err)
	}

	landed, err := client.ListChanges(ctx, connect.NewRequest(&runkov1.ListChangesRequest{State: runkov1.ChangeState_CHANGE_STATE_LANDED}))
	if err != nil {
		t.Fatalf("ListChanges(landed): %v", err)
	}
	if len(landed.Msg.Changes) != 1 || landed.Msg.Changes[0].GetLandedSha() != land.Msg.LandedSha {
		t.Fatalf("landed inbox: %+v", landed.Msg.Changes)
	}
}

// TestRPCStackDerivation seeds two stacked Changes (B based on A's head)
// plus one unrelated Change, and asserts GetChangeStack derives exactly the
// documented relation from either member's viewpoint.
func TestRPCStackDerivation(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	ctx := context.Background()

	repo.WriteFile("proj/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	resA := processor.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headA, Ref: "refs/for/main"}, nil)

	repo.WriteFile("proj/b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, headB := pushCommit(t, repo, bare, "refs/for/main")
	resB := processor.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, nil)

	if !resA.Accepted || !resB.Accepted {
		t.Fatalf("seed pushes rejected: %+v / %+v", resA, resB)
	}
	// B's recorded base is merge-base(headB, trunk) = trunk tip, not A's
	// head (prereceive's computeBaseSHA) - so hand the stack relation the
	// bases the proto describes by pointing B's base at A's head directly.
	// This mirrors how a stacked workflow records bases once change-per-
	// commit pushes exist; the Store is the source for the derivation.
	chB, _, _ := store.GetChange(ctx, resB.ChangeID)
	if _, err := store.CreateOrUpdateChange(ctx, resB.ChangeID, headA, chB.HeadSHA, chB.GitRef, chB.Title, "", "", ""); err != nil {
		t.Fatalf("re-base change B: %v", err)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	for wantPos, id := range []string{resA.ChangeID, resB.ChangeID} {
		stack, err := client.GetChangeStack(ctx, connect.NewRequest(&runkov1.GetChangeStackRequest{ChangeId: id}))
		if err != nil {
			t.Fatalf("GetChangeStack(%s): %v", id, err)
		}
		if len(stack.Msg.Changes) != 2 {
			t.Fatalf("stack size from %s: want 2, got %d", id, len(stack.Msg.Changes))
		}
		if stack.Msg.Changes[0].GetId() != resA.ChangeID || stack.Msg.Changes[1].GetId() != resB.ChangeID {
			t.Fatalf("stack order: %+v", stack.Msg.Changes)
		}
		if int(stack.Msg.Position) != wantPos {
			t.Fatalf("position of %s: want %d, got %d", id, wantPos, stack.Msg.Position)
		}
	}
}

func TestRPCProjectsAndOwners(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewProjectServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	list, err := client.ListProjects(ctx, connect.NewRequest(&runkov1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list.Msg.Projects) != 1 || list.Msg.Projects[0].GetName() != "checkout-api" {
		t.Fatalf("projects: %+v", list.Msg.Projects)
	}
	if list.Msg.Projects[0].GetType() != runkov1.ProjectType_PROJECT_TYPE_SERVICE {
		t.Fatalf("project type: %v", list.Msg.Projects[0].GetType())
	}

	filtered, err := client.ListProjects(ctx, connect.NewRequest(&runkov1.ListProjectsRequest{Query: "no-such"}))
	if err != nil || len(filtered.Msg.Projects) != 0 {
		t.Fatalf("query filter: %+v, %v", filtered.Msg.Projects, err)
	}

	detail, err := client.GetProject(ctx, connect.NewRequest(&runkov1.GetProjectRequest{Project: "checkout-api"}))
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if detail.Msg.Project.GetPath() != "commerce/checkout" {
		t.Fatalf("project path: %q", detail.Msg.Project.GetPath())
	}

	_, err = client.GetProject(ctx, connect.NewRequest(&runkov1.GetProjectRequest{Project: "ghost"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetProject(ghost): want NotFound, got %v", err)
	}

	byPath, err := client.WhoOwns(ctx, connect.NewRequest(&runkov1.WhoOwnsRequest{
		Target: &runkov1.WhoOwnsRequest_Path{Path: "commerce/checkout/deep/file.go"},
	}))
	if err != nil {
		t.Fatalf("WhoOwns(path): %v", err)
	}
	// The fixture project declares no owners - resolution falls through to
	// the (empty) org default, visibly (§7.3).
	if byPath.Msg.Owners.GetSource() != runkov1.OwnersSource_OWNERS_SOURCE_ORG_DEFAULT {
		t.Fatalf("owners source: %v", byPath.Msg.Owners.GetSource())
	}
}

func TestRPCWorkspaces(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewWorkspaceServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	_, err := client.CreateWorkspace(ctx, connect.NewRequest(&runkov1.CreateWorkspaceRequest{
		Name: "fix-1", Owner: "alice", Projects: []string{"ghost"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown project: want InvalidArgument, got %v", err)
	}
	if detail := errorDetail(t, err); detail.GetCode() != "unknown_project" {
		t.Fatalf("detail code: %q", detail.GetCode())
	}

	created, err := client.CreateWorkspace(ctx, connect.NewRequest(&runkov1.CreateWorkspaceRequest{
		Name: "fix-1", Owner: "alice", Projects: []string{"checkout-api"},
	}))
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	ws := created.Msg.Workspace
	if ws.GetSnapshotRef() != "refs/workspaces/fix-1/head" || ws.GetStatus() != runkov1.WorkspaceStatus_WORKSPACE_STATUS_ACTIVE {
		t.Fatalf("workspace: %+v", ws)
	}
	if got := ws.GetWriteAllowlist(); len(got) != 1 || got[0] != "commerce/checkout" {
		t.Fatalf("write allowlist: %v", got)
	}

	_, err = client.CreateWorkspace(ctx, connect.NewRequest(&runkov1.CreateWorkspaceRequest{
		Name: "fix-1", Owner: "alice", Projects: []string{"checkout-api"},
	}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate workspace: want AlreadyExists, got %v", err)
	}

	got, err := client.GetWorkspace(ctx, connect.NewRequest(&runkov1.GetWorkspaceRequest{Id: "fix-1"}))
	if err != nil || got.Msg.Workspace.GetOwner() != "alice" {
		t.Fatalf("GetWorkspace: %+v, %v", got, err)
	}

	list, err := client.ListWorkspaces(ctx, connect.NewRequest(&runkov1.ListWorkspacesRequest{}))
	if err != nil || len(list.Msg.Workspaces) != 1 {
		t.Fatalf("ListWorkspaces: %+v, %v", list, err)
	}
}

func TestRPCSearchCode(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	searcher := stubSearcher{result: search.Result{
		Query: "Price",
		Hits: []search.Hit{
			{Path: "commerce/checkout/pricing.go", LineNumber: 12, Line: "func Price() {}"},
			{Path: "unowned/notes.txt", LineNumber: 1, Line: "Price notes"},
		},
	}}
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: newTestProcessor(bare, store), Token: "sekret", Searcher: searcher}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewSearchServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	res, err := client.SearchCode(ctx, connect.NewRequest(&runkov1.SearchCodeRequest{Query: "Price"}))
	if err != nil {
		t.Fatalf("SearchCode: %v", err)
	}
	if len(res.Msg.Hits) != 2 || res.Msg.Hits[0].GetProjectId() != "checkout-api" {
		t.Fatalf("hits: %+v", res.Msg.Hits)
	}

	scoped, err := client.SearchCode(ctx, connect.NewRequest(&runkov1.SearchCodeRequest{Query: "Price", Project: "checkout-api"}))
	if err != nil || len(scoped.Msg.Hits) != 1 {
		t.Fatalf("project-scoped hits: %+v, %v", scoped, err)
	}

	_, err = client.SearchCode(ctx, connect.NewRequest(&runkov1.SearchCodeRequest{Query: ""}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty query: want InvalidArgument, got %v", err)
	}
}

// TestRPCSearchNotConfigured pins the §8.2 stance across this transport
// too: no Zoekt means a structured "not configured" Unavailable, never a
// git-grep fallback.
func TestRPCSearchNotConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	client := runkov1connect.NewSearchServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	_, err := client.SearchCode(context.Background(), connect.NewRequest(&runkov1.SearchCodeRequest{Query: "anything"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	if detail := errorDetail(t, err); detail.GetCode() != "search_not_configured" {
		t.Fatalf("detail code: %q", detail.GetCode())
	}
}

func TestRPCRepoBrowser(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.WriteFile("README.md", "# hello\n")
	repo.WriteFile("assets/logo.bin", "\x00\x01\x02")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: newTestProcessor(bare, store), Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewRepoServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	root, err := client.GetTree(ctx, connect.NewRequest(&runkov1.GetTreeRequest{Path: ""}))
	if err != nil {
		t.Fatalf("GetTree(root): %v", err)
	}
	if root.Msg.GetRev() == "" {
		t.Fatalf("resolved rev missing")
	}
	// Dirs before files, both alphabetical (repo.proto).
	var names []string
	for _, e := range root.Msg.Entries {
		names = append(names, e.GetName())
	}
	if len(names) != 3 || names[0] != "assets" || names[1] != "commerce" || names[2] != "README.md" {
		t.Fatalf("root entries: %v", names)
	}
	if root.Msg.Entries[2].GetSize() != int64(len("# hello\n")) {
		t.Fatalf("README size: %d", root.Msg.Entries[2].GetSize())
	}

	sub, err := client.GetTree(ctx, connect.NewRequest(&runkov1.GetTreeRequest{Path: "commerce/checkout"}))
	if err != nil {
		t.Fatalf("GetTree(subdir): %v", err)
	}
	if len(sub.Msg.Entries) != 2 || sub.Msg.Entries[0].GetProject() != "checkout-api" {
		t.Fatalf("subdir entries: %+v", sub.Msg.Entries)
	}
	if sub.Msg.Entries[0].GetPath() != "commerce/checkout/PROJECT.yaml" {
		t.Fatalf("entry path: %q", sub.Msg.Entries[0].GetPath())
	}

	_, err = client.GetTree(ctx, connect.NewRequest(&runkov1.GetTreeRequest{Path: "no/such/dir"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing dir: want NotFound, got %v", err)
	}

	blob, err := client.GetBlob(ctx, connect.NewRequest(&runkov1.GetBlobRequest{Path: "commerce/checkout/main.go"}))
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if blob.Msg.GetContent() != "package main\n" || blob.Msg.GetBinary() || blob.Msg.GetProject() != "checkout-api" {
		t.Fatalf("blob: %+v", blob.Msg)
	}

	bin, err := client.GetBlob(ctx, connect.NewRequest(&runkov1.GetBlobRequest{Path: "assets/logo.bin"}))
	if err != nil {
		t.Fatalf("GetBlob(binary): %v", err)
	}
	if !bin.Msg.GetBinary() || bin.Msg.GetContent() != "" || bin.Msg.GetSize() != 3 {
		t.Fatalf("binary blob: %+v", bin.Msg)
	}

	_, err = client.GetBlob(ctx, connect.NewRequest(&runkov1.GetBlobRequest{Path: "ghost.txt"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing blob: want NotFound, got %v", err)
	}
}

// TestRPCCheckReportedViaRESTGatesRPCView proves the two transports share
// one truth: a check posted through the REST report-check endpoint (what
// runko-ci sends) flips the RPC merge-requirements view.
func TestRPCCheckReportedViaRESTGatesRPCView(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()
	ctx := context.Background()
	client := runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL, rpcAuth("sekret"))

	before, err := client.GetMergeRequirements(ctx, connect.NewRequest(&runkov1.GetMergeRequirementsRequest{ChangeId: changeID}))
	if err != nil || before.Msg.Requirements.GetMergeable() {
		t.Fatalf("pre-report: want blocked, got %+v, %v", before, err)
	}

	// Post the declared "unit" check green via REST, as runko-ci would.
	body := `{"name":"unit","external_id":"run-1","status":"completed","conclusion":"success","reporter":"ci"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/checks", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := srv.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("report-check: %v, %+v", err, resp)
	}
	resp.Body.Close()

	after, err := client.GetMergeRequirements(ctx, connect.NewRequest(&runkov1.GetMergeRequirementsRequest{ChangeId: changeID}))
	if err != nil {
		t.Fatalf("post-report GetMergeRequirements: %v", err)
	}
	r := after.Msg.Requirements
	if !r.GetMergeable() || len(r.GetChecks().GetPassing()) != 1 {
		t.Fatalf("post-report: want mergeable with unit passing, got %+v", r)
	}
}
