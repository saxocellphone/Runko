package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWorkspaceGCReclaimsClosedSyncedWorktree is §12.7's core loop: a
// closed workspace whose worktree matches its snapshot ref is disposable
// by construction - plan says so, --apply removes the directory, prunes
// the store's worktree metadata, and drops the registry row.
func TestWorkspaceGCReclaimsClosedSyncedWorktree(t *testing.T) {
	srv, _, store := startWorkspaceServerStore(t)
	ctx := context.Background()

	_, dir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"gc-done", "alice", []string{"checkout-api"}, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	writeFile(t, dir, "commerce/checkout/done.go", "package main // done\n")
	if _, err := WorkspaceSnapshot(dir, "task finished"); err != nil {
		t.Fatalf("WorkspaceSnapshot: %v", err)
	}
	if err := store.SetWorkspaceStatus(ctx, "gc-done", "closed"); err != nil {
		t.Fatalf("SetWorkspaceStatus: %v", err)
	}

	plan, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{})
	if err != nil {
		t.Fatalf("WorkspaceGC plan: %v", err)
	}
	c := findCandidate(t, plan, "gc-done")
	if !c.Reclaim || c.SizeBytes == 0 {
		t.Fatalf("expected a reclaimable candidate with a size, got %+v", c)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("plan-only gc must not touch the directory: %v", err)
	}

	if _, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{Apply: true}); err != nil {
		t.Fatalf("WorkspaceGC apply: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected the worktree reclaimed")
	}
	rows, err := loadMaterializations()
	if err != nil || len(rows) != 0 {
		t.Fatalf("expected the registry row dropped, got %v (%v)", rows, err)
	}
}

// TestWorkspaceGCWatchParkedDirtyIsReclaimable pins the predicate against
// the COMMON closed shape: `change push` auto-snapshots out-of-band
// (§12.6), so the ref carries the final tree while HEAD stays put and
// `git status` stays dirty. Tree-equality with the snapshot ref - not
// working-tree cleanliness - is what proves durability.
func TestWorkspaceGCWatchParkedDirtyIsReclaimable(t *testing.T) {
	srv, _, store := startWorkspaceServerStore(t)
	ctx := context.Background()

	_, dir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"gc-parked", "alice", []string{"checkout-api"}, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	writeFile(t, dir, "commerce/checkout/wip.go", "package main // parked\n")
	if _, _, _, err := WorkspaceWatchSnapshot(dir, "parked by change push", ""); err != nil {
		t.Fatalf("WorkspaceWatchSnapshot: %v", err)
	}
	if out := mustGit(t, dir, "status", "--porcelain"); out == "" {
		t.Fatalf("precondition: the watch-parked worktree should look dirty")
	}
	if err := store.SetWorkspaceStatus(ctx, "gc-parked", "closed"); err != nil {
		t.Fatalf("SetWorkspaceStatus: %v", err)
	}

	plan, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{})
	if err != nil {
		t.Fatalf("WorkspaceGC: %v", err)
	}
	if c := findCandidate(t, plan, "gc-parked"); !c.Reclaim {
		t.Fatalf("watch-parked dirty worktree should be reclaimable, got %+v", c)
	}
}

// TestWorkspaceGCFailClosedSkips: everything doubtful is kept, with the
// reason named - unsnapshotted edits, local commits past the snapshot,
// open workspaces without --idle.
func TestWorkspaceGCFailClosedSkips(t *testing.T) {
	srv, _, store := startWorkspaceServerStore(t)
	ctx := context.Background()

	mk := func(name string) string {
		t.Helper()
		_, dir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
			name, "alice", []string{"checkout-api"}, MaterializeOptions{})
		if err != nil {
			t.Fatalf("WorkspaceCreate %s: %v", name, err)
		}
		return dir
	}

	dirty := mk("gc-dirty")
	writeFile(t, dirty, "commerce/checkout/never-pushed.go", "package main\n")
	if err := store.SetWorkspaceStatus(ctx, "gc-dirty", "closed"); err != nil {
		t.Fatal(err)
	}

	ahead := mk("gc-ahead")
	writeFile(t, ahead, "commerce/checkout/snapped.go", "package main\n")
	if _, err := WorkspaceSnapshot(ahead, "snapped"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ahead, "commerce/checkout/after.go", "package main // after the snapshot\n")
	mustGit(t, ahead, "add", "-A")
	mustGit(t, ahead, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "local only")
	if err := store.SetWorkspaceStatus(ctx, "gc-ahead", "closed"); err != nil {
		t.Fatal(err)
	}

	open := mk("gc-open")
	_ = open

	plan, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{Apply: true})
	if err != nil {
		t.Fatalf("WorkspaceGC: %v", err)
	}
	for name, wantReason := range map[string]string{
		"gc-dirty": "unpushed work",
		"gc-ahead": "differs from the last snapshot",
		"gc-open":  "workspace is open",
	} {
		c := findCandidate(t, plan, name)
		if c.Reclaim {
			t.Fatalf("%s must not be reclaimable: %+v", name, c)
		}
		if !strings.Contains(c.Reason, wantReason) {
			t.Fatalf("%s: reason %q should name %q", name, c.Reason, wantReason)
		}
	}
	for _, dir := range []string{dirty, ahead, open} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("--apply must not remove a skipped worktree: %v", err)
		}
	}
}

// TestWorkspaceGCIdleSweepsOpenSynced: --idle extends the sweep to OPEN
// workspaces that are provably synced - their durable state is
// server-side and re-attach recreates the directory in seconds.
func TestWorkspaceGCIdleSweepsOpenSynced(t *testing.T) {
	srv, _, _ := startWorkspaceServerStore(t)
	ctx := context.Background()

	_, dir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"gc-idle", "alice", []string{"checkout-api"}, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	plan, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{Idle: time.Millisecond})
	if err != nil {
		t.Fatalf("WorkspaceGC: %v", err)
	}
	c := findCandidate(t, plan, "gc-idle")
	if !c.Reclaim || !strings.Contains(c.Reason, "idle") {
		t.Fatalf("expected the idle open workspace reclaimable (never left base), got %+v", c)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("plan-only: %v", err)
	}
}

// TestWorkspaceGCScanAdoptsLegacyWorktrees: --scan walks a store's
// worktrees and adopts the runko-bound ones into the registry - the
// migration path for every pre-§12.7 layout (in-tree worktrees, per-task
// clones), and the registry-is-a-rebuildable-cache proof.
func TestWorkspaceGCScanAdoptsLegacyWorktrees(t *testing.T) {
	srv, _, _ := startWorkspaceServerStore(t)
	ctx := context.Background()
	root := t.TempDir()
	storeDir := filepath.Join(root, "legacy-store")

	_, dir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"legacy-ws", "alice", []string{"checkout-api"},
		MaterializeOptions{CloneDir: storeDir, Dir: filepath.Join(root, "legacy-ws")})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	// Pre-registry world: the row does not exist.
	p, err := materializationsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatalf("simulate pre-registry: %v", err)
	}

	plan, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{Scan: []string{storeDir}})
	if err != nil {
		t.Fatalf("WorkspaceGC --scan: %v", err)
	}
	c := findCandidate(t, plan, "legacy-ws")
	if c.Path != dir {
		t.Fatalf("adopted row path = %q, want %q", c.Path, dir)
	}
	rows, err := loadMaterializations()
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected the adopted row persisted, got %v (%v)", rows, err)
	}
}

// TestWorkspaceCreateRecyclesClosedWorktree is §12.7's reuse decision:
// create rebinds a reclaimable worktree instead of adding a fresh one -
// ignored caches (the expensive bytes) survive the task turnover, the
// dead task's untracked residue does not, and the binding flips to the
// new workspace.
func TestWorkspaceCreateRecyclesClosedWorktree(t *testing.T) {
	srv, _, store := startWorkspaceServerStore(t)
	ctx := context.Background()

	_, oldDir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"rec-old", "alice", []string{"checkout-api"}, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate rec-old: %v", err)
	}
	// The expensive ignored cache (a stand-in for node_modules) and some
	// untracked residue; the out-of-band snapshot parks the residue on the
	// ref (durable) while the cache stays out of history (*.cache is
	// gitignored in the seed).
	writeFile(t, oldDir, "commerce/checkout/deps.cache", "expensive to rebuild\n")
	writeFile(t, oldDir, "commerce/checkout/residue.txt", "dead task scratch\n")
	if _, _, _, err := WorkspaceWatchSnapshot(oldDir, "final state", ""); err != nil {
		t.Fatalf("WorkspaceWatchSnapshot: %v", err)
	}
	if err := store.SetWorkspaceStatus(ctx, "rec-old", "closed"); err != nil {
		t.Fatal(err)
	}

	_, newDir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"rec-new", "alice", []string{"money-lib"}, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate rec-new: %v", err)
	}

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("expected the old directory rebound away, still exists")
	}
	if _, err := os.Stat(filepath.Join(newDir, "commerce/checkout/deps.cache")); err != nil {
		t.Fatalf("ignored cache must survive recycling: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "commerce/checkout/residue.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked residue must be cleaned on recycle")
	}
	if _, err := os.Stat(filepath.Join(newDir, "libs/money/money.go")); err != nil {
		t.Fatalf("new cone not materialized: %v", err)
	}
	if id := mustGit(t, newDir, "config", "--worktree", "runko.workspace"); id != "rec-new" {
		t.Fatalf("worktree binding = %q, want rec-new", id)
	}
	// Exactly one registry row - the recycled one.
	rows, err := loadMaterializations()
	if err != nil || len(rows) != 1 || rows[0].Workspace != "rec-new" || rows[0].Path != newDir {
		t.Fatalf("registry after recycle = %v (%v)", rows, err)
	}
	// And the new workspace still works end to end: snapshot pushes.
	writeFile(t, newDir, "libs/money/wip.go", "package money // recycled\n")
	if _, err := WorkspaceSnapshot(newDir, "on recycled worktree"); err != nil {
		t.Fatalf("WorkspaceSnapshot on recycled worktree: %v", err)
	}
}

// TestWorkspaceGCScanNeutralizesDeadCredentialStore pins the finding #49
// cleanup lesson: a pre-§12.7 store embeds its creating agent's (long
// expired) token in the origin URL - the legacy no-double-auth rule then
// blocks header injection, the dead credential yields the anonymous
// advertisement, refs/workspaces stays hidden, and a perfectly synced
// worktree misdiagnoses as unpushed. Adoption must neutralize the store
// first, after which the invoking principal's credential sees the ref and
// the worktree evaluates reclaimable.
func TestWorkspaceGCScanNeutralizesDeadCredentialStore(t *testing.T) {
	srv, _, store := startWorkspaceServerStore(t)
	ctx := context.Background()
	root := t.TempDir()
	storeDir := filepath.Join(root, "dead-cred-store")

	_, dir, err := WorkspaceCreate(ctx, http.DefaultClient, srv.URL, "sekret",
		"dead-cred-ws", "alice", []string{"checkout-api"},
		MaterializeOptions{CloneDir: storeDir, Dir: filepath.Join(root, "dead-cred-ws")})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	writeFile(t, dir, "commerce/checkout/done.go", "package main\n")
	if _, err := WorkspaceSnapshot(dir, "done"); err != nil {
		t.Fatalf("WorkspaceSnapshot: %v", err)
	}
	if err := store.SetWorkspaceStatus(ctx, "dead-cred-ws", "closed"); err != nil {
		t.Fatal(err)
	}

	// Regress to the pre-§12.7 shape with an EXPIRED credential baked in,
	// and forget the registry row (pre-registry world).
	u, err := url.Parse(mustGit(t, storeDir, "config", "remote.origin.url"))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("agent-dead-1234", "expired-token")
	mustGit(t, storeDir, "config", "remote.origin.url", u.String())
	mustGit(t, storeDir, "config", "--unset", "credential.helper")
	p, err := materializationsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	plan, err := WorkspaceGC(ctx, http.DefaultClient, srv.URL, "sekret", GCOptions{Scan: []string{storeDir}})
	if err != nil {
		t.Fatalf("WorkspaceGC --scan: %v", err)
	}
	c := findCandidate(t, plan, "dead-cred-ws")
	if !c.Reclaim {
		t.Fatalf("adoption should neutralize the dead credential and prove the sync, got %+v", c)
	}
	if pu, err := url.Parse(mustGit(t, storeDir, "config", "remote.origin.url")); err != nil || pu.User != nil {
		t.Fatalf("expected the scanned store neutralized")
	}
}

func findCandidate(t *testing.T, plan []GCCandidate, workspace string) GCCandidate {
	t.Helper()
	for _, c := range plan {
		if c.Workspace == workspace {
			return c
		}
	}
	t.Fatalf("no plan row for %q in %+v", workspace, plan)
	return GCCandidate{}
}
