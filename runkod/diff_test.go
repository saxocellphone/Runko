package runkod

import (
	"context"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
)

// TestParseUnifiedDiffHunkMath pins the parser's line-number accounting on a
// crafted patch: hunk starts, per-line old/new numbers, the section heading
// after the second @@, and the "\ No newline" marker being metadata.
func TestParseUnifiedDiffHunkMath(t *testing.T) {
	patch := `diff --git a/pkg/main.go b/pkg/main.go
index 0000000..1111111 100644
--- a/pkg/main.go
+++ b/pkg/main.go
@@ -10,4 +10,5 @@ func main() {
 keep
-drop
+add1
+add2
 tail
\ No newline at end of file
`
	files, err := parseUnifiedDiff(patch)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Path != "pkg/main.go" || f.Status != "modified" || f.Binary {
		t.Fatalf("unexpected file shape: %+v", f)
	}
	if f.Adds != 2 || f.Dels != 1 {
		t.Fatalf("want +2/-1, got +%d/-%d", f.Adds, f.Dels)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.OldStart != 10 || h.OldLines != 4 || h.NewStart != 10 || h.NewLines != 5 {
		t.Fatalf("hunk ranges: %+v", h)
	}
	if h.Header != "func main() {" {
		t.Fatalf("hunk header: %q", h.Header)
	}
	want := []diffLine{
		{Type: "context", Content: "keep", OldLine: 10, NewLine: 10},
		{Type: "removed", Content: "drop", OldLine: 11},
		{Type: "added", Content: "add1", NewLine: 11},
		{Type: "added", Content: "add2", NewLine: 12},
		{Type: "context", Content: "tail", OldLine: 12, NewLine: 13},
	}
	if len(h.Lines) != len(want) {
		t.Fatalf("want %d lines, got %d: %+v", len(want), len(h.Lines), h.Lines)
	}
	for i, w := range want {
		if h.Lines[i] != w {
			t.Fatalf("line %d: want %+v, got %+v", i, w, h.Lines[i])
		}
	}
}

// TestComputeChangeDiffAgainstRealGit drives the whole pipeline against a
// real repo: add, modify, delete, pure rename, and a binary blob in one
// Change, statuses and paths per repo.proto's FileDiff contract.
func TestComputeChangeDiffAgainstRealGit(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	repo.WriteFile("proj/mod.txt", "line1\nline2\nline3\n")
	repo.WriteFile("proj/del.txt", "gone\n")
	repo.WriteFile("proj/old.txt", "same content\nstays identical\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("proj/mod.txt", "line1\nline2 changed\nline3\n")
	repo.Run("rm -q proj/del.txt", "mv proj/old.txt proj/renamed.txt")
	repo.WriteFile("proj/added.txt", "fresh\n")
	repo.WriteFile("proj/bin.dat", "\x00\x01\x02binary")
	repo.Commit("edit everything\n\nChange-Id: Iaaaa456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}
	change, _, _ := store.GetChange(context.Background(), result.ChangeID)

	files, err := computeChangeDiff(bare, change.BaseSHA, change.HeadSHA)
	if err != nil {
		t.Fatalf("computeChangeDiff: %v", err)
	}
	byPath := map[string]fileDiff{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if len(files) != 5 {
		t.Fatalf("want 5 files, got %d: %+v", len(files), byPath)
	}

	if f := byPath["proj/mod.txt"]; f.Status != "modified" || f.Adds != 1 || f.Dels != 1 {
		t.Fatalf("mod.txt: %+v", f)
	}
	if f := byPath["proj/del.txt"]; f.Status != "deleted" || f.Dels != 1 {
		t.Fatalf("del.txt: %+v", f)
	}
	if f := byPath["proj/renamed.txt"]; f.Status != "renamed" || f.OldPath != "proj/old.txt" || len(f.Hunks) != 0 {
		t.Fatalf("renamed.txt: %+v", f)
	}
	if f := byPath["proj/added.txt"]; f.Status != "added" || f.Adds != 1 {
		t.Fatalf("added.txt: %+v", f)
	}
	if f := byPath["proj/bin.dat"]; f.Status != "added" || !f.Binary || len(f.Hunks) != 0 {
		t.Fatalf("bin.dat: %+v", f)
	}
}
