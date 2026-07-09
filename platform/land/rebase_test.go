package land

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
)

func TestRebaseCleanMerge(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "line1\n")
	repo.WriteFile("b.txt", "unrelated\n")
	base := repo.Commit("base")

	// Trunk moves on, touching only a.txt.
	repo.WriteFile("a.txt", "line1\ntrunk-change\n")
	trunkTip := repo.Commit("trunk moves a.txt")

	// The Change, built on the old base, touches only b.txt.
	repo.Run("checkout -q " + base)
	repo.WriteFile("b.txt", "unrelated\nchange-adds-this\n")
	changeHead := repo.Commit("change touches b.txt")

	result, err := Rebase(repo.Dir, base, trunkTip, changeHead)
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !result.Clean {
		t.Fatalf("expected a clean rebase, got conflicts: %+v", result.ConflictPaths)
	}
	if result.NewTreeSHA == "" {
		t.Fatalf("expected a tree SHA")
	}

	// The resulting tree must contain both the trunk's a.txt change and the
	// change's b.txt addition.
	store := gitstore.New(repo.Dir)
	newSHA, err := commitTree(repo.Dir, result.NewTreeSHA, trunkTip, core.CommitMeta{Message: "test"})
	if err != nil {
		t.Fatalf("commitTree: %v", err)
	}
	aBlob, err := store.GetBlob(core.Revision(newSHA), "a.txt")
	if err != nil {
		t.Fatalf("GetBlob(a.txt): %v", err)
	}
	if string(aBlob.Content) != "line1\ntrunk-change\n" {
		t.Fatalf("a.txt: want trunk's version, got %q", aBlob.Content)
	}
	bBlob, err := store.GetBlob(core.Revision(newSHA), "b.txt")
	if err != nil {
		t.Fatalf("GetBlob(b.txt): %v", err)
	}
	if string(bBlob.Content) != "unrelated\nchange-adds-this\n" {
		t.Fatalf("b.txt: want change's version, got %q", bBlob.Content)
	}
}

func TestRebaseConflict(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "line1\n")
	base := repo.Commit("base")

	repo.WriteFile("a.txt", "line1\ntrunk-version\n")
	trunkTip := repo.Commit("trunk changes a.txt")

	repo.Run("checkout -q " + base)
	repo.WriteFile("a.txt", "line1\nchange-version\n")
	changeHead := repo.Commit("change also changes a.txt")

	result, err := Rebase(repo.Dir, base, trunkTip, changeHead)
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if result.Clean {
		t.Fatalf("expected a conflicting rebase, got clean")
	}
	if len(result.ConflictPaths) != 1 || result.ConflictPaths[0] != "a.txt" {
		t.Fatalf("expected conflict on a.txt, got %+v", result.ConflictPaths)
	}
}
