package runkod

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/saxocellphone/runko/internal/gitfixture"
	runkov1 "github.com/saxocellphone/runko/proto/gen/runko/v1"
)

// newWorkspaceDeleteFixture: a bare repo with two snapshot refs for
// workspace "payments-fix" (head + a side branch) and its registry row.
func newWorkspaceDeleteFixture(t *testing.T) (*Server, *MemStore) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("wip")
	pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")
	pushCommit(t, repo, bare, "refs/workspaces/payments-fix/spike")

	store := NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "payments-fix", Owner: "alice",
		BaseRevision: "abc", SnapshotRef: "refs/workspaces/payments-fix/head", Status: "active",
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return &Server{RepoDir: bare, TrunkRef: "main", Store: store}, store
}

func workspaceRefs(t *testing.T, repoDir, id string) []string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", repoDir, "for-each-ref",
		"--format=%(refname)", "refs/workspaces/"+id+"/").Output()
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	return strings.Fields(string(out))
}

// TestDeleteWorkspaceRemovesRowAndRefs is the happy path: registry row
// gone, every snapshot ref (all branches, not just head) gone, and the id
// immediately reusable.
func TestDeleteWorkspaceRemovesRowAndRefs(t *testing.T) {
	srv, store := newWorkspaceDeleteFixture(t)
	ctx := context.Background()

	if refs := workspaceRefs(t, srv.RepoDir, "payments-fix"); len(refs) != 2 {
		t.Fatalf("fixture sanity: want 2 snapshot refs, got %v", refs)
	}
	if apiErr := srv.deleteWorkspaceCore(ctx, "payments-fix", nil); apiErr != nil {
		t.Fatalf("delete: %+v", apiErr)
	}
	if _, ok, _ := store.GetWorkspace(ctx, "payments-fix"); ok {
		t.Fatalf("registry row must be gone")
	}
	if refs := workspaceRefs(t, srv.RepoDir, "payments-fix"); len(refs) != 0 {
		t.Fatalf("snapshot refs must be gone, got %v", refs)
	}
	// The id is reusable - deletion is a hard delete, not a tombstone.
	if _, err := store.CreateWorkspace(ctx, Workspace{
		ID: "payments-fix", Owner: "bob",
		SnapshotRef: "refs/workspaces/payments-fix/head", Status: "active",
	}); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

func TestDeleteWorkspaceNotFound(t *testing.T) {
	srv, _ := newWorkspaceDeleteFixture(t)
	apiErr := srv.deleteWorkspaceCore(context.Background(), "ghost", nil)
	if apiErr == nil || apiErr.Status != http.StatusNotFound || apiErr.Err.Code != "workspace_not_found" {
		t.Fatalf("want 404 workspace_not_found, got %+v", apiErr)
	}
}

// TestDeleteWorkspaceRefusedWhileOpenChangesRemain: open changes born in
// the workspace (§12.2) block deletion by name; abandoning them unblocks.
// Landed/abandoned provenance is history, never a lock.
func TestDeleteWorkspaceRefusedWhileOpenChangesRemain(t *testing.T) {
	srv, store := newWorkspaceDeleteFixture(t)
	ctx := context.Background()

	if _, err := store.CreateOrUpdateChange(ctx, "Iaa", "b", "h1", "r", "t", "alice", "payments-fix", "head"); err != nil {
		t.Fatalf("seed change: %v", err)
	}
	apiErr := srv.deleteWorkspaceCore(ctx, "payments-fix", nil)
	if apiErr == nil || apiErr.Status != http.StatusConflict || apiErr.Err.Code != "workspace_has_open_changes" {
		t.Fatalf("want 409 workspace_has_open_changes, got %+v", apiErr)
	}
	if !strings.Contains(apiErr.Err.Message, "Iaa") {
		t.Fatalf("the refusal must NAME the blocking changes, got %q", apiErr.Err.Message)
	}
	if _, ok, _ := store.GetWorkspace(ctx, "payments-fix"); !ok {
		t.Fatalf("a refused delete must leave the workspace intact")
	}
	if refs := workspaceRefs(t, srv.RepoDir, "payments-fix"); len(refs) != 2 {
		t.Fatalf("a refused delete must leave the refs intact, got %v", refs)
	}

	if _, err := store.MarkChangeAbandoned(ctx, "Iaa"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if apiErr := srv.deleteWorkspaceCore(ctx, "payments-fix", nil); apiErr != nil {
		t.Fatalf("abandoned provenance must not block deletion: %+v", apiErr)
	}
}

// TestDeleteWorkspaceOwnerOnly pins the principal matrix: a named
// non-owner is refused, the owner and an operator pass, and the anonymous
// deploy token passes (the §15.1 everyone-credential, same rule as
// snapshot pushes).
func TestDeleteWorkspaceOwnerOnly(t *testing.T) {
	srv, _ := newWorkspaceDeleteFixture(t)
	ctx := context.Background()

	mallory := &Principal{Name: "mallory"}
	apiErr := srv.deleteWorkspaceCore(ctx, "payments-fix", mallory)
	if apiErr == nil || apiErr.Status != http.StatusForbidden || apiErr.Err.Code != "not_workspace_owner" {
		t.Fatalf("want 403 not_workspace_owner for a non-owner, got %+v", apiErr)
	}

	// The owner deletes their own workspace.
	if apiErr := srv.deleteWorkspaceCore(ctx, "payments-fix", &Principal{Name: "alice"}); apiErr != nil {
		t.Fatalf("owner delete: %+v", apiErr)
	}
}

func TestDeleteWorkspaceOperatorOverride(t *testing.T) {
	srv, _ := newWorkspaceDeleteFixture(t)
	op := &Principal{Name: "operator", Admin: true}
	if apiErr := srv.deleteWorkspaceCore(context.Background(), "payments-fix", op); apiErr != nil {
		t.Fatalf("operator delete: %+v", apiErr)
	}
}

// TestRPCDeleteWorkspace drives the Connect handler mapping: a refused
// delete surfaces as the mapped code, success round-trips.
func TestRPCDeleteWorkspace(t *testing.T) {
	srv, _ := newWorkspaceDeleteFixture(t)
	r := &rpcServer{s: srv}
	ctx := context.Background()

	_, err := r.DeleteWorkspace(ctx, connect.NewRequest(&runkov1.DeleteWorkspaceRequest{Id: "ghost"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v", err)
	}
	if _, err := r.DeleteWorkspace(ctx, connect.NewRequest(&runkov1.DeleteWorkspaceRequest{Id: "payments-fix"})); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
}
