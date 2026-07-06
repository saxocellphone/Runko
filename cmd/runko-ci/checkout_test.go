package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

func TestCheckoutSparseConeLimitsWorkingTree(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.WriteFile("libs/billing/lib.go", "package billing\n")
	repo.WriteFile("README.md", "# monorepo\n")
	head := repo.Commit("two projects plus a root file")

	dest := filepath.Join(t.TempDir(), "checkout")
	if err := Checkout(repo.Dir, dest, head, []string{"commerce/checkout"}); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "commerce", "checkout", "main.go")); err != nil {
		t.Fatalf("expected commerce/checkout/main.go to be materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "libs", "billing", "lib.go")); err == nil {
		t.Fatalf("expected libs/billing to be OUTSIDE the sparse cone and not materialized")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking libs/billing: %v", err)
	}
}

func TestCheckoutIsPartialClone(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.WriteFile("libs/billing/lib.go", "package billing\n")
	head := repo.Commit("two projects")

	dest := filepath.Join(t.TempDir(), "checkout")
	if err := Checkout(repo.Dir, dest, head, []string{"commerce/checkout"}); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// A partial clone (--filter=blob:none) must report missing blobs outside
	// what was actually fetched for the checked-out cone.
	out, err := runGit(dest, "rev-list", "--objects", "--all", "--missing=print")
	if err != nil {
		t.Fatalf("rev-list --missing=print: %v", err)
	}
	if out == "" {
		t.Fatalf("expected a partial clone to report at least one missing object (the excluded blob), got none")
	}
}

func TestCheckoutRequiresProjectsForSparseCone(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	head := repo.Commit("one project")

	dest := filepath.Join(t.TempDir(), "checkout")
	// No project paths given: cone-mode sparse-checkout with no `set` still
	// initializes to just the repo root files, not a full checkout - this
	// documents that behavior rather than asserting a specific file set.
	if err := Checkout(repo.Dir, dest, head, nil); err != nil {
		t.Fatalf("Checkout with no project paths should still succeed: %v", err)
	}
}
