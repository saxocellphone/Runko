package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
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

// TestCreateProjectOnEmptyRepoCreatesFirstCommit exercises §6.7's "Empty
// monorepo: single CTA Create your first project" bar for real: a freshly
// `git init`'d repo with zero commits (gitfixture.New does not commit) must
// let `project create` succeed by building the repo's first commit, not
// reject with git's raw unborn-HEAD "unknown revision" error.
func TestCreateProjectOnEmptyRepoCreatesFirstCommit(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)

	rev, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", Owners: []string{"group:commerce-eng"},
	})
	if err != nil {
		t.Fatalf("CreateProject on an empty repo: %v", err)
	}
	if rev == "" {
		t.Fatalf("expected a new commit SHA")
	}

	head, err := runGit(repo.Dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if head != rev {
		t.Fatalf("expected main to have advanced to %s, got HEAD=%s", rev, head)
	}

	// A first commit has no parent - confirm CommitOverlay actually built an
	// orphan commit rather than silently requiring something to rebase onto.
	parents, err := runGit(repo.Dir, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		t.Fatalf("rev-list --parents: %v", err)
	}
	if strings.Contains(parents, " ") {
		t.Fatalf("expected the first commit to have no parents, got: %q", parents)
	}

	manifestPath := filepath.Join(repo.Dir, "checkout-api", "PROJECT.yaml")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("expected %s to exist in the working tree: %v", manifestPath, err)
	}
}

func TestCreateProjectOnNonRepoDirReturnsStructuredError(t *testing.T) {
	dir := t.TempDir() // not a git repo at all

	_, err := CreateProject(dir, project.Intent{Name: "checkout-api", Type: "service"})
	if err == nil {
		t.Fatalf("expected an error for a non-repo directory")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error with resolve-or-explain guidance, got %T: %v", err, err)
	}
	if ce.Code != "not_a_repo" {
		t.Fatalf("expected code not_a_repo, got %+v", ce)
	}
}

func TestCreateProjectOnDetachedHeadReturnsStructuredError(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	rev := repo.Commit("initial")
	if _, err := runGit(repo.Dir, "checkout", "--detach", "--quiet", rev); err != nil {
		t.Fatalf("checkout --detach: %v", err)
	}

	_, err := CreateProject(repo.Dir, project.Intent{Name: "checkout-api", Type: "service"})
	if err == nil {
		t.Fatalf("expected an error in detached HEAD")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error with resolve-or-explain guidance, got %T: %v", err, err)
	}
	if ce.Code != "detached_head" {
		t.Fatalf("expected code detached_head, got %+v", ce)
	}
}

// TestCreateProjectWithBuildCapabilityWritesBuildFile is the real,
// end-to-end version of project's own PlanCreate unit test: a genuine `git`
// repo, a real commit, a real BUILD.bazel materialized on disk - the
// greenfield golden path bar from docs/design.md §14.5.4 (DAG stage 9c),
// "zero hand-authored BUILD lines".
func TestCreateProjectWithBuildCapabilityWritesBuildFile(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	repo.Commit("initial")

	_, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", Path: "commerce/checkout",
		Capabilities: []string{"build"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	buildPath := filepath.Join(repo.Dir, "commerce", "checkout", "BUILD.bazel")
	content, err := os.ReadFile(buildPath)
	if err != nil {
		t.Fatalf("expected a generated BUILD.bazel on disk: %v", err)
	}
	if !strings.Contains(string(content), "//commerce/checkout/...") {
		t.Fatalf("expected the generated BUILD.bazel to reference its target pattern, got:\n%s", content)
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

// TestCreateProjectRefusesDuplicateName mirrors the daemon-side guard
// (runkod/createproject.go): the CLI happily committed a second "Create
// project checkout-api" (2026-07-08 dogfood review) that would thrash the
// tree when pushed.
func TestCreateProjectRefusesDuplicateName(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	repo.Commit("initial")

	intent := project.Intent{Name: "checkout-api", Type: "service", Owners: []string{"group:commerce-eng"}}
	if _, err := CreateProject(repo.Dir, intent); err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}

	_, err := CreateProject(repo.Dir, intent)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_exists" {
		t.Fatalf("want already_exists, got %v", err)
	}
	if !strings.Contains(ce.Message, "checkout-api") {
		t.Fatalf("error must name the colliding project: %q", ce.Message)
	}
}
