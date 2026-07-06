package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/project"
)

func TestCreateProjectWritesFilesAndAdvancesCurrentBranch(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	before := repo.Commit("initial")

	rev, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", Owners: []string{"group:commerce-eng"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if rev == "" || rev == before {
		t.Fatalf("expected a new commit, got %q (before %q)", rev, before)
	}

	head, err := runGit(repo.Dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if head != rev {
		t.Fatalf("expected the current branch (main) to have advanced to %s, got HEAD=%s", rev, head)
	}

	// The working tree must reflect the new commit - CreateProject must sync
	// it, since CommitOverlay only writes Git objects (internal/gitstore).
	manifestPath := filepath.Join(repo.Dir, "checkout-api", "PROJECT.yaml")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected %s to exist in the working tree: %v", manifestPath, err)
	}
	if !strings.Contains(string(content), "checkout-api") {
		t.Fatalf("expected manifest to mention checkout-api, got:\n%s", content)
	}

	if _, err := os.Stat(filepath.Join(repo.Dir, "README.md")); err != nil {
		t.Fatalf("expected the pre-existing README.md to survive: %v", err)
	}
}

func TestCreateProjectRejectsInvalidIntent(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	repo.Commit("initial")

	if _, err := CreateProject(repo.Dir, project.Intent{Name: "Not Valid!", Type: "service"}); err == nil {
		t.Fatalf("expected an invalid project name to be rejected")
	}
}
