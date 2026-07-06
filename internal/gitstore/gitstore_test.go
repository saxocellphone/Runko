package gitstore

import (
	"testing"

	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

func TestResolveRefAndGetTreeAndGetBlob(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hello\n")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	head := repo.Commit("initial")

	s := New(repo.Dir)

	rev, err := s.ResolveRef("HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if string(rev) != head {
		t.Fatalf("ResolveRef: want %s, got %s", head, rev)
	}

	root, err := s.GetTree(rev, "")
	if err != nil {
		t.Fatalf("GetTree(root): %v", err)
	}
	if len(root) != 2 {
		t.Fatalf("GetTree(root): want 2 entries, got %d: %+v", len(root), root)
	}

	sub, err := s.GetTree(rev, "commerce/checkout")
	if err != nil {
		t.Fatalf("GetTree(sub): %v", err)
	}
	if len(sub) != 1 || sub[0].Path != "main.go" || sub[0].Type != "blob" {
		t.Fatalf("GetTree(sub): unexpected result %+v", sub)
	}

	blob, err := s.GetBlob(rev, "README.md")
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if string(blob.Content) != "hello\n" {
		t.Fatalf("GetBlob: want %q, got %q", "hello\n", blob.Content)
	}
	if blob.Size != int64(len(blob.Content)) {
		t.Fatalf("GetBlob: size mismatch: %d vs %d", blob.Size, len(blob.Content))
	}
}

func TestCommitOverlayCreateModifyDelete(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("keep.txt", "keep\n")
	repo.WriteFile("remove.txt", "gone\n")
	base := repo.Commit("initial")

	s := New(repo.Dir)

	rev, err := s.CommitOverlay(core.Revision(base), core.Overlay{
		Changes: []core.FileChange{
			{Path: "new.txt", Content: []byte("new content\n")},
			{Path: "keep.txt", Content: []byte("modified\n")},
			{Path: "remove.txt", Delete: true},
		},
	}, core.CommitMeta{AuthorName: "Test", AuthorEmail: "t@x.com", Message: "overlay commit"})
	if err != nil {
		t.Fatalf("CommitOverlay: %v", err)
	}

	entries, err := s.GetTree(rev, "")
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Path] = true
	}
	if names["remove.txt"] {
		t.Fatalf("expected remove.txt to be deleted, entries: %+v", entries)
	}
	if !names["new.txt"] || !names["keep.txt"] {
		t.Fatalf("expected new.txt and keep.txt to be present, entries: %+v", entries)
	}

	modified, err := s.GetBlob(rev, "keep.txt")
	if err != nil {
		t.Fatalf("GetBlob(keep.txt): %v", err)
	}
	if string(modified.Content) != "modified\n" {
		t.Fatalf("keep.txt: want %q, got %q", "modified\n", modified.Content)
	}
}

func TestCommitOverlayNoParent(t *testing.T) {
	repo := gitfixture.New(t)
	// Repo exists but has zero commits; CommitOverlay with base="" must build
	// a root commit from scratch (no read-tree, no -p).
	s := New(repo.Dir)

	rev, err := s.CommitOverlay("", core.Overlay{
		Changes: []core.FileChange{{Path: "first.txt", Content: []byte("first\n")}},
	}, core.CommitMeta{Message: "root commit"})
	if err != nil {
		t.Fatalf("CommitOverlay(no parent): %v", err)
	}

	entries, err := s.GetTree(rev, "")
	if err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "first.txt" {
		t.Fatalf("unexpected root tree: %+v", entries)
	}
}

func TestUpdateRefWithAndWithoutCAS(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "a\n")
	c1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b\n")
	c2 := repo.Commit("c2")

	s := New(repo.Dir)

	if err := s.UpdateRef("refs/heads/feature", core.Revision(c1), nil); err != nil {
		t.Fatalf("UpdateRef (unconditional create): %v", err)
	}
	got, err := s.ResolveRef("refs/heads/feature")
	if err != nil || string(got) != c1 {
		t.Fatalf("expected refs/heads/feature = %s, got %s (err %v)", c1, got, err)
	}

	oldRev := core.Revision(c1)
	if err := s.UpdateRef("refs/heads/feature", core.Revision(c2), &oldRev); err != nil {
		t.Fatalf("UpdateRef (CAS, correct old value): %v", err)
	}
	got, _ = s.ResolveRef("refs/heads/feature")
	if string(got) != c2 {
		t.Fatalf("expected refs/heads/feature = %s after CAS, got %s", c2, got)
	}

	staleRev := core.Revision(c1) // no longer current; CAS must fail
	if err := s.UpdateRef("refs/heads/feature", core.Revision(c1), &staleRev); err == nil {
		t.Fatalf("expected UpdateRef CAS with stale old value to fail")
	}
}

func TestListHistory(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("first")
	repo.WriteFile("b.txt", "b\n")
	repo.Commit("second")
	repo.WriteFile("a.txt", "a2\n")
	repo.Commit("third touches a.txt")

	s := New(repo.Dir)

	all, err := s.ListHistory("", core.HistoryOptions{})
	if err != nil {
		t.Fatalf("ListHistory(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListHistory(all): want 3 entries, got %d: %+v", len(all), all)
	}
	if all[0].Message != "third touches a.txt" {
		t.Fatalf("ListHistory(all): want newest-first, got %+v", all)
	}

	onlyA, err := s.ListHistory("a.txt", core.HistoryOptions{})
	if err != nil {
		t.Fatalf("ListHistory(a.txt): %v", err)
	}
	if len(onlyA) != 2 {
		t.Fatalf("ListHistory(a.txt): want 2 entries (first, third), got %d: %+v", len(onlyA), onlyA)
	}

	limited, err := s.ListHistory("", core.HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListHistory(limit=1): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("ListHistory(limit=1): want 1 entry, got %d", len(limited))
	}
}
