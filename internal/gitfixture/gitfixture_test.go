package gitfixture

import (
	"strings"
	"testing"
	"time"
)

// TestRepoCommitLog exercises the whole harness end to end - repo builder,
// WriteFile/Commit, and golden-file comparison - so a broken harness fails
// loudly here rather than silently in every downstream package's tests.
func TestRepoCommitLog(t *testing.T) {
	repo := New(t)
	repo.WriteFile("PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("add checkout-api project")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("scaffold checkout-api entrypoint")

	Golden(t, "repo_commit_log", repo.Log())
}

func TestRunScript(t *testing.T) {
	repo := New(t)
	repo.WriteFile("README.md", "# hello\n")
	repo.Run("add README.md", "commit -q -m initial")

	if got := repo.Log(); got != "c1 initial" {
		t.Fatalf("Run script: want %q, got %q", "c1 initial", got)
	}
}

func TestIDSeq(t *testing.T) {
	seq := NewIDSeq("chg")
	got := []string{seq.Next(), seq.Next(), seq.Next()}
	want := []string{"chg_1", "chg_2", "chg_3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("IDSeq: want %v, got %v", want, got)
	}
}

func TestFakeClockIsDeterministic(t *testing.T) {
	c := NewFakeClock()
	first := c.Now()
	second := c.Advance(time.Second)
	if !first.Before(second) {
		t.Fatalf("FakeClock: Advance should move time forward, got first=%v second=%v", first, second)
	}
	if NewFakeClock().Now() != first {
		t.Fatalf("FakeClock: two fresh clocks should start at the same epoch")
	}
}
