package main

// Watch-loop tests (§12.6, stage 18). The load-bearing assertion is
// non-interference: WorkspaceWatchSnapshot must leave `git status`, HEAD,
// and the REAL index byte-identical - it runs in the background beside an
// agent's own git (and jj's) use of the same checkout.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newWatchWorkspace clones a seeded bare origin and binds the clone to a
// workspace id the way `workspace attach`/a jj bind does.
func newWatchWorkspace(t *testing.T) (work, bare string) {
	t.Helper()
	bare = newBareRemote(t)
	work = filepath.Join(t.TempDir(), "work")
	mustGit(t, filepath.Dir(work), "clone", "-q", bare, work)
	mustGit(t, work, "config", "user.email", "t@example.com")
	mustGit(t, work, "config", "user.name", "t")
	writeFile(t, work, "tracked.txt", "v1\n")
	mustGit(t, work, "add", "-A")
	mustGit(t, work, "commit", "-q", "-m", "initial")
	mustGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	mustGit(t, work, "config", "runko.workspace", "obs")
	return work, bare
}

func TestWorkspaceWatchSnapshotLeavesCheckoutUntouched(t *testing.T) {
	work, bare := newWatchWorkspace(t)

	// WIP in all three states an agent's checkout can be in.
	writeFile(t, work, "staged.txt", "staged\n")
	mustGit(t, work, "add", "staged.txt")
	writeFile(t, work, "tracked.txt", "v2 unstaged\n")
	writeFile(t, work, "untracked.txt", "untracked\n")

	statusBefore := mustGit(t, work, "status", "--porcelain=v1")
	headBefore := mustGit(t, work, "rev-parse", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(work, ".git", "index"))
	if err != nil {
		t.Fatalf("read real index: %v", err)
	}

	ref, sha, tree, err := WorkspaceWatchSnapshot(work, "test", "")
	if err != nil {
		t.Fatalf("WorkspaceWatchSnapshot: %v", err)
	}
	if ref != "refs/workspaces/obs/head" || sha == "" || tree == "" {
		t.Fatalf("ref=%q sha=%q tree=%q", ref, sha, tree)
	}

	// The checkout is byte-identical: status, HEAD, and the real index.
	if statusAfter := mustGit(t, work, "status", "--porcelain=v1"); statusAfter != statusBefore {
		t.Fatalf("status changed:\nbefore: %q\nafter:  %q", statusBefore, statusAfter)
	}
	if headAfter := mustGit(t, work, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Fatalf("HEAD moved: %s -> %s", headBefore, headAfter)
	}
	indexAfter, err := os.ReadFile(filepath.Join(work, ".git", "index"))
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("the real index changed (err=%v)", err)
	}

	// The pushed snapshot: parented on HEAD, marker subject, full WIP.
	if got := mustGit(t, bare, "rev-parse", ref); got != sha {
		t.Fatalf("remote ref at %s, want %s", got, sha)
	}
	if parent := mustGit(t, bare, "rev-parse", sha+"^"); parent != headBefore {
		t.Fatalf("snapshot parent %s, want HEAD %s", parent, headBefore)
	}
	if subject := mustGit(t, bare, "log", "-1", "--format=%s", sha); !strings.HasPrefix(subject, snapshotSubjectPrefix) {
		t.Fatalf("snapshot subject %q lacks the marker prefix", subject)
	}
	for path, want := range map[string]string{
		"staged.txt":    "staged\n",
		"tracked.txt":   "v2 unstaged\n",
		"untracked.txt": "untracked\n",
	} {
		if got := mustGit(t, bare, "show", sha+":"+path); got != strings.TrimRight(want, "\n") {
			t.Fatalf("snapshot %s = %q, want %q", path, got, want)
		}
	}

	// Steady state: the same tree short-circuits to "nothing to push".
	_, sha2, tree2, err := WorkspaceWatchSnapshot(work, "test", tree)
	if err != nil || sha2 != "" || tree2 != tree {
		t.Fatalf("steady state: sha=%q tree=%q err=%v", sha2, tree2, err)
	}

	// More WIP moves the tree and pushes a REPLACEMENT tip (amend-at-the-
	// ref semantics: the previous snapshot commit is not its parent).
	writeFile(t, work, "untracked.txt", "more\n")
	_, sha3, tree3, err := WorkspaceWatchSnapshot(work, "test", tree)
	if err != nil || sha3 == "" || tree3 == tree {
		t.Fatalf("moved tree: sha=%q tree=%q err=%v", sha3, tree3, err)
	}
	if parent := mustGit(t, bare, "rev-parse", sha3+"^"); parent != headBefore {
		t.Fatalf("replacement snapshot parent %s, want HEAD %s (never the previous snapshot)", parent, headBefore)
	}
}

func TestWorkspaceWatchSnapshotCleanTreeIsHead(t *testing.T) {
	work, bare := newWatchWorkspace(t)
	head := mustGit(t, work, "rev-parse", "HEAD")

	ref, sha, _, err := WorkspaceWatchSnapshot(work, "test", "")
	if err != nil {
		t.Fatalf("WorkspaceWatchSnapshot: %v", err)
	}
	if sha != head {
		t.Fatalf("clean tree must snapshot as HEAD itself: %s, want %s", sha, head)
	}
	if got := mustGit(t, bare, "rev-parse", ref); got != head {
		t.Fatalf("remote ref %s, want %s", got, head)
	}
}

func TestWorkspaceWatchSnapshotUnboundDirIsStructured(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	_, _, _, err := WorkspaceWatchSnapshot(dir, "test", "")
	if err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("want the not_a_workspace structured error, got %v", err)
	}
	if !isTerminalWatchError(err) {
		t.Fatalf("an unbound dir must be terminal for the watch loop")
	}
}

func TestWorkspaceWatchOnceEmitsNDJSON(t *testing.T) {
	work, _ := newWatchWorkspace(t)
	writeFile(t, work, "wip.txt", "w\n")

	var out bytes.Buffer
	if err := WorkspaceWatch(WatchOptions{Dir: work, Once: true, JSON: true, Out: &out}); err != nil {
		t.Fatalf("WorkspaceWatch --once: %v", err)
	}
	var line map[string]string
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("NDJSON line: %v (%q)", err, out.String())
	}
	if line["ref"] != "refs/workspaces/obs/head" || line["sha"] == "" {
		t.Fatalf("NDJSON: %+v", line)
	}
}

// TestWorkspaceWatchSnapshotJJColocated pins the jj interplay: watch in a
// colocated repo must capture the working copy's WIP without jj noticing
// anything (no divergence, no new visible commits) and without .jj/
// leaking into the snapshot tree.
func TestWorkspaceWatchSnapshotJJColocated(t *testing.T) {
	requireJJ(t)
	remote := newBareRemote(t)
	dir := newColocatedJJRepo(t)

	jjCommitFile(t, dir, "README.md", "hi\n", "initial")
	seed, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve seed: %v", err)
	}
	if _, err := runGit(dir, "push", remote, seed+":refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}
	if _, err := runGit(dir, "remote", "add", "origin", remote); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	mustGit(t, dir, "config", "runko.workspace", "jj-obs")

	// WIP in jj's working copy (uncommitted from git's point of view once
	// jj snapshots @; from the watcher's, plain worktree content).
	writeTestFile(t, dir, "wip.txt", "jj wip\n")

	logBefore, err := runJJ(dir, "log", "-T", "change_id")
	if err != nil {
		t.Fatalf("jj log: %v", err)
	}

	ref, sha, _, err := WorkspaceWatchSnapshot(dir, "test", "")
	if err != nil {
		t.Fatalf("WorkspaceWatchSnapshot (jj): %v", err)
	}
	if got := mustGit(t, remote, "rev-parse", ref); got != sha {
		t.Fatalf("remote ref %s, want %s", got, sha)
	}
	if got := mustGit(t, remote, "show", sha+":wip.txt"); got != "jj wip" {
		t.Fatalf("snapshot wip.txt = %q", got)
	}
	// .jj/ must never enter a snapshot (colocated init git-ignores it; if
	// that ever regresses the watcher would durably push jj's op store).
	if lsTree := mustGit(t, remote, "ls-tree", "--name-only", sha); strings.Contains(lsTree, ".jj") {
		t.Fatalf(".jj leaked into the snapshot tree: %q", lsTree)
	}

	// jj's view of the repo is unchanged: same visible change ids, no
	// divergence markers.
	logAfter, err := runJJ(dir, "log", "-T", "change_id")
	if err != nil {
		t.Fatalf("jj log after: %v", err)
	}
	if logAfter != logBefore {
		t.Fatalf("jj log changed:\nbefore: %q\nafter:  %q", logBefore, logAfter)
	}
}

// TestChangePushAutoSnapshots pins §12.6's promised auto-snapshot: a
// workspace-bound checkout parks its snapshot ref at the submitted state
// as part of `change push`, without any watch loop running.
func TestChangePushAutoSnapshots(t *testing.T) {
	work, bare := newWatchWorkspace(t)
	// The workspace binding makes pushChange stamp push options; a plain
	// bare repo refuses those unless told to advertise support (runkod's
	// EnsureBareRepo sets this in production).
	mustGit(t, bare, "config", "receive.advertisePushOptions", "true")
	writeFile(t, work, "feature.txt", "v1\n")
	if _, err := CreateChange(work, "add a feature"); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	head := mustGit(t, work, "rev-parse", "HEAD")

	if _, err := pushChange(work, "origin", "main", false, true); err != nil {
		t.Fatalf("pushChange: %v", err)
	}
	if got := mustGit(t, bare, "rev-parse", "refs/workspaces/obs/head"); got != head {
		t.Fatalf("snapshot ref parked at %s, want the submitted tip %s", got, head)
	}

	// --no-snapshot's plumbing: a fresh commit pushed with autoSnapshot
	// false must NOT move the snapshot ref.
	writeFile(t, work, "feature.txt", "v2\n")
	if _, err := CreateChange(work, "second feature"); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := pushChange(work, "origin", "main", false, false); err != nil {
		t.Fatalf("pushChange (no snapshot): %v", err)
	}
	if got := mustGit(t, bare, "rev-parse", "refs/workspaces/obs/head"); got != head {
		t.Fatalf("snapshot ref moved to %s despite --no-snapshot, want %s", got, head)
	}
}
