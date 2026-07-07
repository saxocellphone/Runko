package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/receive"
	"github.com/saxocellphone/runko/runkod"
)

// startWorkspaceServer runs a real runkod.Server (registry API + git
// smart-HTTP) over httptest, backed by a bare repo seeded with two projects
// on trunk. Pushes here bypass the pre-receive hook (installing it is
// cmd/runkod serve's job) - the funnel side of snapshots is covered by
// runkod/snapshot_test.go and the compiled-binary e2e test; these tests
// cover the CLI's local git mechanics and API round-trips.
func startWorkspaceServer(t *testing.T) (srv *httptest.Server, bare string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "monorepo.git")
	if err := runkod.EnsureBareRepo(bare, "main"); err != nil {
		t.Fatalf("EnsureBareRepo: %v", err)
	}

	seed := t.TempDir()
	mustGit(t, seed, "init", "-q", "-b", "main")
	mustGit(t, seed, "config", "user.email", "t@example.com")
	mustGit(t, seed, "config", "user.name", "t")
	writeFile(t, seed, "commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	writeFile(t, seed, "commerce/checkout/main.go", "package main\n")
	writeFile(t, seed, "libs/money/PROJECT.yaml", "schema: project/v1\nname: money-lib\ntype: library\n")
	writeFile(t, seed, "libs/money/money.go", "package money\n")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-q", "-m", "initial")
	mustGit(t, seed, "push", "-q", bare, "HEAD:refs/heads/main")

	store := runkod.NewMemStore()
	processor := &runkod.Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	server := &runkod.Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, bare
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestWorkspaceCreateSnapshotAttachRoundTrip is the core §12.2 loop at the
// CLI level: create materializes only the cone; snapshot makes WIP durable
// on refs/workspaces/<id>/head (amending, not stacking); deleting the whole
// directory loses nothing - attach restores the WIP from the snapshot ref.
func TestWorkspaceCreateSnapshotAttachRoundTrip(t *testing.T) {
	srv, bare := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "mono")
	wsDir := filepath.Join(root, "payments-fix")

	info, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"payments-fix", "alice", []string{"checkout-api"}, cloneDir, wsDir)
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	if info.ID != "payments-fix" || len(info.SparsePatterns) != 1 {
		t.Fatalf("unexpected info: %+v", info)
	}
	// The cone materializes checkout-api and NOT money-lib.
	if _, err := os.Stat(filepath.Join(wsDir, "commerce/checkout/main.go")); err != nil {
		t.Fatalf("expected the cone's file materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "libs/money")); !os.IsNotExist(err) {
		t.Fatalf("expected libs/money OUTSIDE the cone to not be materialized")
	}

	// WIP -> snapshot -> durable ref on the served repo.
	writeFile(t, wsDir, "commerce/checkout/wip.go", "package main // wip v1\n")
	ref, err := WorkspaceSnapshot(wsDir, "")
	if err != nil {
		t.Fatalf("WorkspaceSnapshot: %v", err)
	}
	if ref != "refs/workspaces/payments-fix/head" {
		t.Fatalf("unexpected snapshot ref %q", ref)
	}
	snap1 := mustGit(t, bare, "rev-parse", ref)

	// Second snapshot amends (§12.2 amend-by-default): still exactly one
	// commit between base and the snapshot tip, not a growing stack.
	writeFile(t, wsDir, "commerce/checkout/wip.go", "package main // wip v2\n")
	if _, err := WorkspaceSnapshot(wsDir, "iterating"); err != nil {
		t.Fatalf("WorkspaceSnapshot (second): %v", err)
	}
	snap2 := mustGit(t, bare, "rev-parse", ref)
	if snap1 == snap2 {
		t.Fatalf("expected the second snapshot to move the ref")
	}
	if n := mustGit(t, bare, "rev-list", "--count", info.BaseRevision+".."+snap2); n != "1" {
		t.Fatalf("expected exactly 1 snapshot commit above base (amend, not stack), got %s", n)
	}

	// Delete the ENTIRE worktree - then attach restores the v2 WIP.
	if err := os.RemoveAll(wsDir); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	restored := filepath.Join(root, "payments-fix-restored")
	if _, err := WorkspaceAttach(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"payments-fix", cloneDir, restored); err != nil {
		t.Fatalf("WorkspaceAttach: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(restored, "commerce/checkout/wip.go"))
	if err != nil {
		t.Fatalf("expected the snapshot's WIP restored: %v", err)
	}
	if !strings.Contains(string(content), "wip v2") {
		t.Fatalf("expected the LATEST snapshot restored, got %q", content)
	}
}

// TestWorkspaceTwoWorktreesOneObjectStore is §12.3's "multiple workstreams
// = multiple worktrees": two workspaces, different projects, sharing one
// blobless clone - each materializes only its own cone.
func TestWorkspaceTwoWorktreesOneObjectStore(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "mono")
	ctx := context.Background()

	if _, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"payments-fix", "alice", []string{"checkout-api"}, cloneDir, filepath.Join(root, "payments-fix")); err != nil {
		t.Fatalf("create ws1: %v", err)
	}
	if _, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"risk-refactor", "alice", []string{"money-lib"}, cloneDir, filepath.Join(root, "risk-refactor")); err != nil {
		t.Fatalf("create ws2: %v", err)
	}

	worktrees := mustGit(t, cloneDir, "worktree", "list")
	if !strings.Contains(worktrees, "payments-fix") || !strings.Contains(worktrees, "risk-refactor") {
		t.Fatalf("expected both worktrees off one clone, got:\n%s", worktrees)
	}
	if _, err := os.Stat(filepath.Join(root, "payments-fix", "libs/money")); !os.IsNotExist(err) {
		t.Fatalf("ws1 must not materialize ws2's cone")
	}
	if _, err := os.Stat(filepath.Join(root, "risk-refactor", "commerce")); !os.IsNotExist(err) {
		t.Fatalf("ws2 must not materialize ws1's cone")
	}

	list, err := WorkspaceList(ctx, http.DefaultClient, srv.URL, "sekret")
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 registry rows, got %+v (err %v)", list, err)
	}
}

// TestWorkspaceUpdateBase: trunk advances after the workspace was created;
// update-base rebases the workspace onto the new tip and records it in the
// registry.
func TestWorkspaceUpdateBase(t *testing.T) {
	srv, bare := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "mono")
	wsDir := filepath.Join(root, "w1")
	ctx := context.Background()

	info, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"w1", "alice", []string{"checkout-api"}, cloneDir, wsDir)
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	// Local WIP in the workspace, then trunk moves in an unrelated project.
	writeFile(t, wsDir, "commerce/checkout/wip.go", "package main // wip\n")
	if _, err := WorkspaceSnapshot(wsDir, ""); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	adv := t.TempDir()
	mustGit(t, adv, "clone", "-q", "--filter=blob:none", bare, "trunkclone")
	advRepo := filepath.Join(adv, "trunkclone")
	mustGit(t, advRepo, "config", "user.email", "t@example.com")
	mustGit(t, advRepo, "config", "user.name", "t")
	writeFile(t, advRepo, "libs/money/rates.go", "package money // new\n")
	mustGit(t, advRepo, "add", "-A")
	mustGit(t, advRepo, "commit", "-q", "-m", "advance trunk")
	mustGit(t, advRepo, "push", "-q", "origin", "HEAD:refs/heads/main")
	newTip := mustGit(t, bare, "rev-parse", "refs/heads/main")

	got, err := WorkspaceUpdateBase(ctx, http.DefaultClient, srv.URL, "sekret", wsDir)
	if err != nil {
		t.Fatalf("WorkspaceUpdateBase: %v", err)
	}
	if got != newTip {
		t.Fatalf("expected new base %s, got %s", newTip, got)
	}
	// The WIP commit is now rebased onto the new tip.
	if mb := mustGit(t, wsDir, "merge-base", "HEAD", newTip); mb != newTip {
		t.Fatalf("expected the workspace rebased onto %s, merge-base says %s", newTip, mb)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "commerce/checkout/wip.go")); err != nil {
		t.Fatalf("expected WIP preserved across update-base: %v", err)
	}

	// The registry recorded it.
	var reg WorkspaceInfo
	if err := apiJSON(ctx, http.DefaultClient, http.MethodGet, srv.URL+"/api/workspaces/w1", "sekret", nil, &reg); err != nil {
		t.Fatalf("GET workspace: %v", err)
	}
	if reg.BaseRevision != newTip {
		t.Fatalf("expected registry base %s, got %s", newTip, reg.BaseRevision)
	}
	_ = info
}

func TestWorkspaceSnapshotOutsideWorkspaceIsStructuredError(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	_, err := WorkspaceSnapshot(dir, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_a_workspace" {
		t.Fatalf("expected not_a_workspace, got %T: %v", err, err)
	}
}

func TestWorkspaceAttachUnknownIsStructuredError(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	_, err := WorkspaceAttach(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"ghost", filepath.Join(t.TempDir(), "mono"), filepath.Join(t.TempDir(), "ghost"))
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("expected not_found, got %T: %v", err, err)
	}
}
