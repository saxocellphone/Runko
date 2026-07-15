package runkod

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/receive"
	runkov1 "github.com/saxocellphone/runko/runkod/proto/gen/runko/v1"
	"github.com/saxocellphone/runko/runkod/proto/gen/runko/v1/runkov1connect"
)

// newDeleteTestServer seeds a trunk holding a deletable project plus a
// consumer whose manifest declares an edge to it - the two halves the
// delete plan must handle.
func newDeleteTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("legacy/PROJECT.yaml", "schema: project/v1\nname: legacy\ntype: library\n")
	repo.WriteFile("legacy/lib.go", "package legacy\n")
	repo.WriteFile("legacy/inner/deep.go", "package inner\n")
	repo.WriteFile("svc/PROJECT.yaml", "# the consumer\nschema: project/v1\nname: svc\ntype: service\ndependencies:\n  - legacy\nci:\n  checks:\n    - name: svc-test\n      command: go test ./svc/...\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret",
		Principals: []Principal{{Name: "robo", Token: "robo-token", IsAgent: true}}}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, bare
}

// TestDeleteProjectArrivesAsAChange pins §13.1's delete verb end to end
// over the real Connect surface: preview lists the subtree deletions plus
// the edge-stripped manifests, delete registers an ordinary open Change
// whose tree no longer holds the project, and refusals are structured.
func TestDeleteProjectArrivesAsAChange(t *testing.T) {
	srv, bare := newDeleteTestServer(t)
	ctx := context.Background()
	projects := projectRPC(srv)

	preview, err := projects.PreviewDeleteProject(ctx, connect.NewRequest(&runkov1.PreviewDeleteProjectRequest{
		Intent: &runkov1.DeleteProjectIntent{Name: "legacy"},
	}))
	if err != nil {
		t.Fatalf("PreviewDeleteProject: %v", err)
	}
	got := map[string]string{}
	for _, op := range preview.Msg.Ops {
		got[op.Path] = op.Action
	}
	if got["legacy/PROJECT.yaml"] != "delete" || got["legacy/inner/deep.go"] != "delete" || got["svc/PROJECT.yaml"] != "modify" {
		t.Fatalf("preview ops = %v", got)
	}

	res, err := projects.DeleteProject(ctx, connect.NewRequest(&runkov1.DeleteProjectRequest{
		Intent: &runkov1.DeleteProjectIntent{Name: "legacy"},
	}))
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	change := res.Msg.Change
	if change == nil || !strings.Contains(change.Title, "Delete project legacy") {
		t.Fatalf("change = %+v", change)
	}

	// Inspect the change head's tree directly in the bare repo.
	g := gitstore.New(bare)
	headRef := core.Revision("refs/changes/" + change.Id + "/head")
	if _, err := g.GetBlob(headRef, "legacy/PROJECT.yaml"); err == nil {
		t.Fatal("deleted project's manifest still present in the change tree")
	}
	svcBlob, err := g.GetBlob(headRef, "svc/PROJECT.yaml")
	if err != nil {
		t.Fatalf("read svc manifest at change head: %v", err)
	}
	svc := string(svcBlob.Content)
	if strings.Contains(svc, "- legacy") || strings.Contains(svc, "dependencies:") {
		t.Fatalf("edge strip wrong (emptied list must drop its key):\n%s", svc)
	}
	if !strings.Contains(svc, "# the consumer") || !strings.Contains(svc, "svc-test") {
		t.Fatalf("stripping destroyed unrelated content:\n%s", svc)
	}

	// Trunk itself is untouched - deletion is a Change, not a write.
	if _, err := g.GetBlob(core.Revision("refs/heads/main"), "legacy/PROJECT.yaml"); err != nil {
		t.Fatalf("trunk must be untouched until the change lands: %v", err)
	}

	// Refusals: unknown name, the root project, an agent caller.
	if _, err := projects.DeleteProject(ctx, connect.NewRequest(&runkov1.DeleteProjectRequest{
		Intent: &runkov1.DeleteProjectIntent{Name: "ghost"},
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown: want NotFound, got %v", err)
	}

	agentClient := runkov1connectProjectClient(srv, "robo-token")
	if _, err := agentClient.DeleteProject(ctx, connect.NewRequest(&runkov1.DeleteProjectRequest{
		Intent: &runkov1.DeleteProjectIntent{Name: "svc"},
	})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("agent: want PermissionDenied, got %v", err)
	}
}

func runkov1connectProjectClient(srv *httptest.Server, token string) runkov1connect.ProjectServiceClient {
	return runkov1connect.NewProjectServiceClient(srv.Client(), srv.URL, rpcAuth(token))
}
