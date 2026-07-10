package bazel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/buildadapter"
)

// scriptedDeterminator mirrors scriptedBazel: an executable standing in for
// target-determinator so these tests exercise the real clone/exec/parse
// path without the binary (unavailable in this sandbox).
func scriptedDeterminator(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-determinator")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake determinator: %v", err)
	}
	return path
}

func snapshotFixture(t *testing.T) (repo *gitfixture.Repo, base, head string) {
	t.Helper()
	repo = gitfixture.New(t)
	repo.WriteFile("MODULE.bazel", "module(name = \"x\")\n")
	base = repo.Commit("base")
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"1\")\n")
	head = repo.Commit("head")
	return repo, base, head
}

// The wrapper must run the determinator against a DISPOSABLE clone at the
// head revision - never the caller's checkout, which the tool would mutate
// - and pass the base revision positionally. The fake records its argv and
// working evidence, then prints targets.
func TestSnapshotDiffRunsInDisposableCloneAndParses(t *testing.T) {
	repo, base, head := snapshotFixture(t)
	argsFile := filepath.Join(t.TempDir(), "argv")
	bin := scriptedDeterminator(t, `echo "$@" > `+argsFile+`
echo "//svc:test"
echo ""
echo "//lib:t"`)

	e := Engine{DeterminatorBin: bin}
	got, err := e.SnapshotDiff(context.Background(), buildadapter.SnapshotDiffRequest{
		RepoDir: repo.Dir, BaseRev: base, HeadRev: head,
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if len(got.Targets) != 2 || got.Targets[0] != "//svc:test" || got.Targets[1] != "//lib:t" {
		t.Fatalf("parse: got %v", got.Targets)
	}

	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake never ran: %v", err)
	}
	s := strings.TrimSpace(string(argv))
	fields := strings.Fields(s)
	if len(fields) == 0 || fields[len(fields)-1] != base {
		t.Fatalf("base revision must be the positional argument, argv: %s", s)
	}
	workDir := ""
	for i, f := range fields {
		if f == "-working-directory" && i+1 < len(fields) {
			workDir = fields[i+1]
		}
	}
	if workDir == "" || workDir == repo.Dir {
		t.Fatalf("determinator must run against a disposable clone, not the caller's checkout, argv: %s", s)
	}
	if !strings.Contains(s, "-targets //...") {
		t.Fatalf("expected defaulted -targets, argv: %s", s)
	}
}

// The clone is disposable in both directions: whatever the determinator
// does to its working directory, the caller's repo stays byte-identical.
func TestSnapshotDiffLeavesCallerCheckoutUntouched(t *testing.T) {
	repo, base, head := snapshotFixture(t)
	// The fake vandalizes its working directory the way the real tool
	// checks out revisions: mutating tracked files.
	bin := scriptedDeterminator(t, `cd "$2" && echo vandalized > MODULE.bazel
echo "//svc:test"`)

	before, err := os.ReadFile(filepath.Join(repo.Dir, "MODULE.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	e := Engine{DeterminatorBin: bin}
	if _, err := e.SnapshotDiff(context.Background(), buildadapter.SnapshotDiffRequest{
		RepoDir: repo.Dir, BaseRev: base, HeadRev: head,
	}); err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(repo.Dir, "MODULE.bazel"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("caller checkout mutated: %q -> %q", before, after)
	}
}

func TestSnapshotDiffFailsOnNonZeroExit(t *testing.T) {
	repo, base, head := snapshotFixture(t)
	bin := scriptedDeterminator(t, `echo "no bazel workspace found" >&2
exit 1`)
	e := Engine{DeterminatorBin: bin}
	_, err := e.SnapshotDiff(context.Background(), buildadapter.SnapshotDiffRequest{
		RepoDir: repo.Dir, BaseRev: base, HeadRev: head,
	})
	if err == nil || !strings.Contains(err.Error(), "no bazel workspace") {
		t.Fatalf("want stderr surfaced in error, got %v", err)
	}
}

func TestSnapshotDiffRequiresBothRevisions(t *testing.T) {
	e := Engine{}
	if _, err := e.SnapshotDiff(context.Background(), buildadapter.SnapshotDiffRequest{RepoDir: "x", BaseRev: "a"}); err == nil {
		t.Fatalf("missing head must error")
	}
	if _, err := e.SnapshotDiff(context.Background(), buildadapter.SnapshotDiffRequest{RepoDir: "x", HeadRev: "b"}); err == nil {
		t.Fatalf("missing base must error")
	}
}
