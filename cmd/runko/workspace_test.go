package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
		"payments-fix", "head", cloneDir, restored); err != nil {
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
		"ghost", "head", filepath.Join(t.TempDir(), "mono"), filepath.Join(t.TempDir(), "ghost"))
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("expected not_found, got %T: %v", err, err)
	}
}

// TestWorkspaceAttachDocumentedArgumentOrderWorks pins the exact form the
// help text prints - `workspace attach <id> --runkod-url ...`, id FIRST.
// stdlib flag stops parsing at the first positional, so before the
// pop-the-id fix in cmdWorkspace this documented invocation failed with a
// required-flag error every single time - only the undocumented flags-first
// order worked, violating §6.9's rule that a printed command must be
// copy-pasteable. Caught by the user in a live test, not by CI: every
// earlier test called WorkspaceAttach directly, skipping the flag-parsing
// layer the bug lived in - which is exactly why this test goes through
// cmdWorkspace instead.
func TestWorkspaceAttachDocumentedArgumentOrderWorks(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "mono")
	wsDir := filepath.Join(root, "payments-fix")

	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"payments-fix", "alice", []string{"checkout-api"}, cloneDir, wsDir); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	if err := os.RemoveAll(wsDir); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	out := captureStdout(t, func() {
		if err := cmdWorkspace([]string{"attach", "payments-fix",
			"--runkod-url", srv.URL, "--token", "sekret",
			"--clone-dir", cloneDir, "--dir", filepath.Join(root, "restored")}); err != nil {
			t.Errorf("documented id-first attach form failed: %v", err)
		}
	})
	if t.Failed() {
		t.FailNow()
	}
	if !strings.Contains(out, "payments-fix") {
		t.Fatalf("unexpected attach output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "restored", "commerce/checkout/main.go")); err != nil {
		t.Fatalf("expected the restored cone: %v", err)
	}

	// The flags-first order with a trailing id keeps working too. (Detach
	// the first worktree before re-attaching - two live worktrees of one
	// workspace is deferred --shared scope, not what this test is about.)
	if err := os.RemoveAll(filepath.Join(root, "restored")); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	captureStdout(t, func() {
		if err := cmdWorkspace([]string{"attach",
			"--runkod-url", srv.URL, "--token", "sekret",
			"--clone-dir", cloneDir, "--dir", filepath.Join(root, "restored2"),
			"payments-fix"}); err != nil {
			t.Errorf("flags-first attach form failed: %v", err)
		}
	})
}

// TestWorkspaceListColumnsAreAligned pins the tabwriter fix: the human
// `workspace list` output used raw \t, which renders as terminal tab stops
// and visually runs columns together for IDs near a stop's width.
func TestWorkspaceListColumnsAreAligned(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	root := t.TempDir()
	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"money-fix", "alice", []string{"money-lib"}, filepath.Join(root, "mono"), filepath.Join(root, "money-fix")); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := cmdWorkspace([]string{"list", "--runkod-url", srv.URL, "--token", "sekret"}); err != nil {
			t.Errorf("workspace list: %v", err)
		}
	})
	if strings.Contains(out, "\t") {
		t.Fatalf("expected tabwriter-padded output, got raw tabs: %q", out)
	}
	if !regexp.MustCompile(`money-fix\s{2,}active`).MatchString(out) {
		t.Fatalf("expected space-padded columns, got: %q", out)
	}
}

// TestProjectListShowsTrunkIndexedProjects covers `runko project list`
// (GET /api/projects) - the CLI verb runkod's unknown_project suggestion
// now names, so it has to actually exist and list what's indexed at trunk.
func TestProjectListShowsTrunkIndexedProjects(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	out := captureStdout(t, func() {
		if err := cmdProjectList([]string{"--runkod-url", srv.URL, "--token", "sekret"}); err != nil {
			t.Errorf("project list: %v", err)
		}
	})
	for _, want := range []string{"checkout-api", "money-lib", "commerce/checkout", "libs/money"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected project list to mention %q, got: %q", want, out)
		}
	}
}

// One workspace, parallel work (§12.2 workspace branches): fork a branch in
// place, snapshot both lines to sibling refs, then attach each branch into
// its own worktree and confirm they coexist with diverged content - the
// same shared object store underneath.
func TestWorkspaceBranchParallelWork(t *testing.T) {
	srv, bare := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "mono")
	wsDir := filepath.Join(root, "payments-fix")

	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"payments-fix", "alice", []string{"checkout-api"}, cloneDir, wsDir); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	// Line one on the default branch.
	writeFile(t, wsDir, "commerce/checkout/approach-a.go", "package main // approach A\n")
	if ref, err := WorkspaceSnapshot(wsDir, ""); err != nil || ref != "refs/workspaces/payments-fix/head" {
		t.Fatalf("snapshot head: ref=%q err=%v", ref, err)
	}

	// Fork: same worktree switches to a parallel line; the fork point is
	// durable immediately.
	ref, err := WorkspaceBranch(wsDir, "approach-b")
	if err != nil {
		t.Fatalf("WorkspaceBranch: %v", err)
	}
	if ref != "refs/workspaces/payments-fix/approach-b" {
		t.Fatalf("branch ref: %q", ref)
	}
	writeFile(t, wsDir, "commerce/checkout/approach-b.go", "package main // approach B\n")
	if _, err := WorkspaceSnapshot(wsDir, ""); err != nil {
		t.Fatalf("snapshot approach-b: %v", err)
	}

	// Both lines are durable, and diverged.
	headSHA := mustGit(t, bare, "rev-parse", "refs/workspaces/payments-fix/head")
	branchSHA := mustGit(t, bare, "rev-parse", "refs/workspaces/payments-fix/approach-b")
	if headSHA == branchSHA {
		t.Fatalf("branches should have diverged")
	}

	// Invalid branch names are refused client-side with the §6.5 shape.
	var ce *clierr.Error
	if _, err := WorkspaceBranch(wsDir, "a/b"); !errors.As(err, &ce) || ce.Code != "invalid_branch_name" {
		t.Fatalf("expected invalid_branch_name, got %v", err)
	}

	// Parallel in full: attach BOTH branches of the one workspace into
	// separate worktrees off the same clone - possible only because local
	// branches are ws/<id>/<branch>, not one shared ws/<id>.
	dirHead := filepath.Join(root, "payments-fix-line-a")
	if _, err := WorkspaceAttach(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"payments-fix", "head", cloneDir, dirHead); err != nil {
		t.Fatalf("attach head: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirHead, "commerce/checkout/approach-b.go")); !os.IsNotExist(err) {
		t.Fatalf("head worktree must NOT contain the parallel branch's file")
	}
	// The original worktree IS the approach-b line now.
	if _, err := os.Stat(filepath.Join(wsDir, "commerce/checkout/approach-b.go")); err != nil {
		t.Fatalf("approach-b worktree missing its own file: %v", err)
	}

	// Attaching a branch that's already materialized in a local worktree is
	// the single-writer rule (§12.2) - refused with the structured shape,
	// never a raw git exit 128.
	if _, err := WorkspaceAttach(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"payments-fix", "approach-b", cloneDir, filepath.Join(root, "dup")); !errors.As(err, &ce) || ce.Code != "branch_in_use" {
		t.Fatalf("expected branch_in_use, got %v", err)
	}

	// Snapshots from each worktree go to their OWN refs.
	writeFile(t, wsDir, "commerce/checkout/approach-b.go", "package main // approach B v2\n")
	if ref, err := WorkspaceSnapshot(wsDir, ""); err != nil || ref != "refs/workspaces/payments-fix/approach-b" {
		t.Fatalf("snapshot from branch worktree: ref=%q err=%v", ref, err)
	}
	writeFile(t, dirHead, "commerce/checkout/approach-a.go", "package main // approach A v2\n")
	if ref, err := WorkspaceSnapshot(dirHead, ""); err != nil || ref != "refs/workspaces/payments-fix/head" {
		t.Fatalf("snapshot from head worktree: ref=%q err=%v", ref, err)
	}
}

// A relative --dir must resolve against the CALLER's cwd, not the shared
// clone: `git -C <clone> worktree add <dir>` resolves relative dirs against
// the clone, which silently nested the worktree inside it (found live -
// every earlier test passed absolute paths).
func TestWorkspaceCreateRelativeDirLandsInCallerCwd(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	root := t.TempDir()
	t.Chdir(root)

	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"rel-dir", "alice", []string{"checkout-api"}, "mono", "rel-dir"); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "rel-dir", "commerce/checkout/main.go")); err != nil {
		t.Fatalf("worktree should be at <cwd>/rel-dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mono", "rel-dir")); !os.IsNotExist(err) {
		t.Fatalf("worktree must NOT be nested inside the shared clone")
	}
}
