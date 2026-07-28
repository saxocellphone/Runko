package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/project"
)

func TestCreateProjectWritesFilesAndAdvancesCurrentBranch(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	before := repo.Commit("initial")

	rev, _, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none", Owners: []string{"group:commerce-eng"},
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

// TestCreateProjectStampsChangeID: the create commit is born with its
// Change-Id trailer, same as `change create` (2026-07-16 dogfood review
// papercut: a trailer-less create commit had no identity until a later
// amend, so stacks could carry an identity-less intermediate step).
func TestCreateProjectStampsChangeID(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	repo.Commit("initial")

	_, changeID, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if !strings.HasPrefix(changeID, "I") || len(changeID) != 41 {
		t.Fatalf("returned change id %q is not a Change-Id", changeID)
	}
	msg, err := runGit(repo.Dir, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatalf("read commit message: %v", err)
	}
	if !strings.Contains(msg, "Change-Id: "+changeID) {
		t.Fatalf("commit message lacks the Change-Id trailer:\n%s", msg)
	}

	// A second create in the same repo must mint a DIFFERENT identity.
	_, secondID, err := CreateProject(repo.Dir, project.Intent{
		Name: "cart-api", Type: "service", API: "none",
	})
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}
	if secondID == changeID {
		t.Fatal("two creates minted the same Change-Id")
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

	rev, _, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none", Owners: []string{"group:commerce-eng"},
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

	_, _, err := CreateProject(dir, project.Intent{Name: "checkout-api", Type: "service", API: "none"})
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

	_, _, err := CreateProject(repo.Dir, project.Intent{Name: "checkout-api", Type: "service", API: "none"})
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

	_, _, err := CreateProject(repo.Dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none", Path: "commerce/checkout",
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

	if _, _, err := CreateProject(repo.Dir, project.Intent{Name: "Not Valid!", Type: "service", API: "none"}); err == nil {
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

	intent := project.Intent{Name: "checkout-api", Type: "service", API: "none", Owners: []string{"group:commerce-eng"}}
	if _, _, err := CreateProject(repo.Dir, intent); err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}

	_, _, err := CreateProject(repo.Dir, intent)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_exists" {
		t.Fatalf("want already_exists, got %v", err)
	}
	if !strings.Contains(ce.Message, "checkout-api") {
		t.Fatalf("error must name the colliding project: %q", ce.Message)
	}
}

// TestCreateProjectWithLangWritesLanguageSkeleton drives the multi-language
// path end to end against a real repo: --lang python must scaffold main.py
// and record the language verbatim in the on-disk PROJECT.yaml (§10.4).
func TestCreateProjectWithLangWritesLanguageSkeleton(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	repo.Commit("initial")

	if _, _, err := CreateProject(repo.Dir, project.Intent{
		Name: "billing-worker", Type: "job", Language: "python",
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if _, err := os.Stat(filepath.Join(repo.Dir, "billing-worker", "main.py")); err != nil {
		t.Fatalf("expected main.py in the working tree: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo.Dir, "billing-worker", "PROJECT.yaml"))
	if err != nil {
		t.Fatalf("read PROJECT.yaml: %v", err)
	}
	if !strings.Contains(string(content), "language: python") {
		t.Fatalf("expected 'language: python' recorded on disk, got:\n%s", content)
	}
}

// newBranchedColocatedJJRepo is a colocated jj workspace seeded via plain
// git then `jj git init --colocate` - the shape of an adopted monorepo and
// of a fresh --jj workspace clone before further jj ops. Pure
// `jj git init --colocate` starts on a symbolic ref (refs/heads/main);
// HEAD detaches later when jj ops leave @'s parent without a bookmark
// (e.g. after `runko change create`). CreateProject must tolerate that.
func newBranchedColocatedJJRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main", dir},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "config", "user.email", "test@runko.dev"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeTestFile(t, dir, "README.md", "# monorepo\n")
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if err := jjGitInitColocate(dir); err != nil {
		t.Fatalf("jj git init --colocate: %v", err)
	}
	for _, kv := range [][2]string{{"user.name", "Test"}, {"user.email", "test@runko.dev"}} {
		if _, err := runJJ(dir, "config", "set", "--repo", kv[0], kv[1]); err != nil {
			t.Fatalf("jj config set %s: %v", kv[0], err)
		}
	}
	return dir
}

// TestCreateProjectJJAfterChangeCreate: the CRITICAL regression. After
// `runko change create` in a colocated jj checkout, git HEAD is detached
// by design (@'s parent no longer has a bookmark). project create used to
// refuse that with detached_head; it must succeed, parent on the change
// tip (not a lagging branch), and leave files + jj status coherent.
func TestCreateProjectJJAfterChangeCreate(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	jjCommitFile(t, dir, "README.md", "# monorepo\n", "initial")

	writeTestFile(t, dir, "note.txt", "work\n")
	if _, err := CreateChange(dir, "some work", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	// Confirm the precondition this test exists for: git HEAD is detached.
	if _, err := runGit(dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("precondition: expected detached HEAD after change create in colocated jj")
	}
	tipBefore, err := jjTipCommit(dir)
	if err != nil {
		t.Fatalf("jjTipCommit before project create: %v", err)
	}

	rev, _, err := CreateProject(dir, project.Intent{
		Name: "svc", Type: "library", API: "none", Owners: []string{"group:eng"},
	})
	if err != nil {
		t.Fatalf("CreateProject after change create (detached HEAD): %v", err)
	}
	if rev == "" {
		t.Fatal("expected a new project-create commit")
	}

	// Base must be the jj tip (the change create commit), not a lagging branch.
	parents, err := runGit(dir, "rev-list", "--parents", "-n", "1", rev)
	if err != nil {
		t.Fatalf("rev-list --parents: %v", err)
	}
	fields := strings.Fields(parents)
	if len(fields) < 2 || fields[1] != tipBefore {
		t.Fatalf("create commit must parent the jj tip %s, got parents %q", tipBefore, parents)
	}

	manifestPath := filepath.Join(dir, "svc", "PROJECT.yaml")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected %s on disk: %v", manifestPath, err)
	}
	if !strings.Contains(string(content), "svc") {
		t.Fatalf("expected manifest to mention svc, got:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("expected README.md to survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note.txt")); err != nil {
		t.Fatalf("expected note.txt from the prior change to survive: %v", err)
	}

	empty, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty")
	if err != nil {
		t.Fatalf("jj log @ empty: %v", err)
	}
	if strings.TrimSpace(empty) != "true" {
		t.Fatalf("want empty working-copy commit after create, got empty=%q", empty)
	}
	parent, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("jj log @-: %v", err)
	}
	if strings.TrimSpace(parent) != rev {
		t.Fatalf("@- should be the create commit %s, got %s", rev, parent)
	}
	st, err := runJJ(dir, "status")
	if err != nil {
		t.Fatalf("jj status: %v", err)
	}
	if strings.Contains(st, "Working copy changes:") {
		t.Fatalf("jj status should report no pending changes, got:\n%s", st)
	}
}

// TestCreateProjectJJMaterializesFiles: the live bug this path fixes. In a
// colocated jj checkout, git sparse-checkout/reset are wrong, so after
// project create the new project's files must appear on disk via jj's own
// sparse set + `jj new`, and jj status must be coherent (empty @, no huge
// pending diff of the whole monorepo).
func TestCreateProjectJJMaterializesFiles(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)

	rev, _, err := CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none", Owners: []string{"group:commerce-eng"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	manifestPath := filepath.Join(dir, "checkout-api", "PROJECT.yaml")
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected %s on disk after jj create: %v", manifestPath, err)
	}
	if !strings.Contains(string(content), "checkout-api") {
		t.Fatalf("expected manifest to mention checkout-api, got:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("expected the pre-existing README.md to survive: %v", err)
	}

	// @ is a fresh empty commit on the create commit - the shape
	// createChangeJJ / jjTipCommit expect, not a dirty rewrite of the tree.
	empty, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty")
	if err != nil {
		t.Fatalf("jj log @ empty: %v", err)
	}
	if strings.TrimSpace(empty) != "true" {
		t.Fatalf("want empty working-copy commit after create, got empty=%q", empty)
	}
	parent, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("jj log @-: %v", err)
	}
	if strings.TrimSpace(parent) != rev {
		t.Fatalf("@- should be the create commit %s, got %s", rev, parent)
	}
	st, err := runJJ(dir, "status")
	if err != nil {
		t.Fatalf("jj status: %v", err)
	}
	if strings.Contains(st, "Working copy changes:") {
		t.Fatalf("jj status should report no pending changes, got:\n%s", st)
	}
}

// TestCreateProjectJJWidensSparseCone: a restricted cone (the onboarding
// workspace shape) must gain the new project's path without dropping the
// patterns already present, and the files must then be on disk.
func TestCreateProjectJJWidensSparseCone(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "README.md"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	before := jjSparsePrefixes(dir)
	if len(before) == 0 {
		t.Fatal("expected a restricted sparse set before create")
	}

	_, _, err := CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	after := jjSparsePrefixes(dir)
	foundNew := false
	for _, p := range after {
		if p == "checkout-api" {
			foundNew = true
			break
		}
	}
	if !foundNew {
		t.Fatalf("sparse set should include checkout-api after create, got %v", after)
	}
	for _, p := range before {
		found := false
		for _, q := range after {
			if p == q {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("sparse set must stay additive: lost %q (before %v, after %v)", p, before, after)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "checkout-api", "PROJECT.yaml")); err != nil {
		t.Fatalf("expected checkout-api files on disk after cone widen: %v", err)
	}
	st, err := runJJ(dir, "status")
	if err != nil {
		t.Fatalf("jj status: %v", err)
	}
	if strings.Contains(st, "Working copy changes:") {
		t.Fatalf("jj status should be clean after create, got:\n%s", st)
	}
}

// TestCreateProjectJJUnrestrictedSparseStaysUnrestricted: when the working
// copy is unrestricted (jj spells that "."), widen must be a no-op - not
// inject the project path alongside "." and risk a later narrowing.
//
// Assert against `jj sparse list` itself, not jjSparsePrefixes: that helper
// short-circuits to nil the moment it sees ".", so it cannot tell "." from
// ". + checkout-api" and a broken always-widen path still passes it.
func TestCreateProjectJJUnrestrictedSparseStaysUnrestricted(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)
	before, err := runJJ(dir, "sparse", "list")
	if err != nil {
		t.Fatalf("jj sparse list before: %v", err)
	}
	beforeLines := strings.Fields(before)
	if len(beforeLines) != 1 || beforeLines[0] != "." {
		t.Fatalf("expected unrestricted sparse [.], got %v", beforeLines)
	}

	_, _, err = CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	after, err := runJJ(dir, "sparse", "list")
	if err != nil {
		t.Fatalf("jj sparse list after: %v", err)
	}
	afterLines := strings.Fields(after)
	if len(afterLines) != 1 || afterLines[0] != "." {
		t.Fatalf("create must leave unrestricted sparse as [.] only, not inject the project path; got %v", afterLines)
	}
	for _, p := range afterLines {
		if p == "checkout-api" {
			t.Fatalf("create must not inject checkout-api into an unrestricted sparse set, got %v", afterLines)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "checkout-api", "PROJECT.yaml")); err != nil {
		t.Fatalf("expected checkout-api on disk: %v", err)
	}
}

// TestCreateProjectJJRefusesDirtyWorkingCopy: a non-empty @ must not become
// the create commit's parent. Parenting on WIP then `jj new` freezes the
// user's work into a permanent undescribed commit and leaves status empty.
func TestCreateProjectJJRefusesDirtyWorkingCopy(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)
	writeTestFile(t, dir, "wip.txt", "in progress\n")

	// Precondition: @ is non-empty (the shape jjTipCommit would return as @).
	empty, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty")
	if err != nil {
		t.Fatalf("jj log @ empty: %v", err)
	}
	if strings.TrimSpace(empty) != "false" {
		t.Fatalf("precondition: want non-empty @, got empty=%q", empty)
	}
	wipBefore, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "commit_id")
	if err != nil {
		t.Fatalf("jj log @ commit_id: %v", err)
	}
	wipBefore = strings.TrimSpace(wipBefore)

	_, _, err = CreateProject(dir, project.Intent{
		Name: "svc", Type: "library", API: "none", Owners: []string{"group:eng"},
	})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "dirty_working_copy" {
		t.Fatalf("want dirty_working_copy, got %v", err)
	}
	if !strings.Contains(ce.Suggestion, "runko change create") {
		t.Fatalf("suggestion must name the next runko verb, got %q", ce.Suggestion)
	}
	if strings.Contains(ce.Suggestion, "jj commit") {
		t.Fatalf("suggestion must not send the user to raw jj for the basic loop, got %q", ce.Suggestion)
	}

	// WIP must still be the working copy - not frozen into ancestry, not wiped.
	st, err := runJJ(dir, "status")
	if err != nil {
		t.Fatalf("jj status: %v", err)
	}
	if !strings.Contains(st, "wip.txt") {
		t.Fatalf("jj status must still show the user's WIP, got:\n%s", st)
	}
	wipAfter, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "commit_id")
	if err != nil {
		t.Fatalf("jj log @ after: %v", err)
	}
	if strings.TrimSpace(wipAfter) != wipBefore {
		t.Fatalf("refusing must not rewrite @; before %s after %s", wipBefore, wipAfter)
	}
	// No "Create project" commit may exist in ancestry.
	log, err := runJJ(dir, "log", "--no-graph", "-r", "::@", "-T", "description.first_line() ++ \"\\n\"")
	if err != nil {
		t.Fatalf("jj log ancestry: %v", err)
	}
	if strings.Contains(log, "Create project") {
		t.Fatalf("refusing must not leave a create commit; ancestry:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, "svc", "PROJECT.yaml")); err == nil {
		t.Fatal("refusing must not materialize the project on disk")
	}
}

// TestCreateProjectJJDropsPinOnError: when HEAD is detached the create path
// force-writes a temporary heads pin so jj can import the CommitOverlay SHA.
// Any failure after the pin is written (here: sparse widen) must still forget
// it - a leaked pin is a user-visible bookmark they did not create.
func TestCreateProjectJJDropsPinOnError(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	jjCommitFile(t, dir, "README.md", "# monorepo\n", "initial")
	writeTestFile(t, dir, "note.txt", "work\n")
	if _, err := CreateChange(dir, "some work", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := runGit(dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("precondition: expected detached HEAD after change create")
	}
	// Restricted cone so syncWorkingTreeJJ takes the sparse-widen branch.
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "README.md", "--add", "note.txt"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}

	// Fail only `jj sparse set` so publish still pins, then widen errors.
	realJJ, err := exec.LookPath("jj")
	if err != nil {
		t.Fatal(err)
	}
	wrapDir := t.TempDir()
	wrap := filepath.Join(wrapDir, "jj")
	script := "#!/bin/bash\n" +
		"prev=\"\"\n" +
		"for b in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"sparse\" ] && [ \"$b\" = \"set\" ]; then\n" +
		"    echo \"injected sparse failure\" >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"  prev=$b\n" +
		"done\n" +
		"exec " + realJJ + " \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err = CreateProject(dir, project.Intent{
		Name: "svc", Type: "library", API: "none", Owners: []string{"group:eng"},
	})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "jj_sparse_widen" {
		t.Fatalf("want jj_sparse_widen from injected failure, got %v", err)
	}

	// No pin bookmark of either the namespaced form or the legacy flat name.
	books, err := runJJ(dir, "bookmark", "list", "-T", `name ++ "\n"`)
	if err != nil {
		t.Fatalf("jj bookmark list: %v", err)
	}
	for _, name := range strings.Fields(books) {
		if name == "runko-project-create" || strings.HasPrefix(name, jjProjectCreatePinPrefix) {
			t.Fatalf("pin bookmark %q leaked after error path; bookmarks:\n%s", name, books)
		}
	}
	// Git refs too - forget should have dropped the heads ref.
	refs, _ := runGit(dir, "for-each-ref", "--format=%(refname)", "refs/heads/runko")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("git heads pin leaked after error path: %s", refs)
	}
}

// TestCreateProjectJJDropsPinOnSuccess: the detached-HEAD pin is forgotten
// after a successful create, not left as a lasting bookmark.
func TestCreateProjectJJDropsPinOnSuccess(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	jjCommitFile(t, dir, "README.md", "# monorepo\n", "initial")
	writeTestFile(t, dir, "note.txt", "work\n")
	if _, err := CreateChange(dir, "some work", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := runGit(dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("precondition: expected detached HEAD after change create")
	}

	if _, _, err := CreateProject(dir, project.Intent{
		Name: "svc", Type: "library", API: "none", Owners: []string{"group:eng"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	books, err := runJJ(dir, "bookmark", "list", "-T", `name ++ "\n"`)
	if err != nil {
		t.Fatalf("jj bookmark list: %v", err)
	}
	for _, name := range strings.Fields(books) {
		if name == "runko-project-create" || strings.HasPrefix(name, jjProjectCreatePinPrefix) {
			t.Fatalf("pin bookmark %q left behind after success; bookmarks:\n%s", name, books)
		}
	}
	refs, _ := runGit(dir, "for-each-ref", "--format=%(refname)", "refs/heads/runko")
	if strings.TrimSpace(refs) != "" {
		t.Fatalf("git heads pin left behind after success: %s", refs)
	}
}

// installGitPinLogger puts a git wrapper first on PATH that appends every
// `update-ref refs/heads/<jjProjectCreatePinPrefix>*` line to logPath, then
// execs the real git. Used to observe pin names mid-flight (the pin is
// forgotten before CreateProject returns, so post-hoc ref listing cannot
// see uniqueness).
func installGitPinLogger(t *testing.T, logPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapDir := t.TempDir()
	wrap := filepath.Join(wrapDir, "git")
	// Log pin publishes under the runko/ namespace (prefix/* and a fixed
	// sibling name like runko/project-create without a SHA suffix). Pass
	// every invocation through unchanged.
	script := "#!/bin/bash\n" +
		"if [ \"$1\" = \"update-ref\" ] && [[ \"$2\" == refs/heads/runko/* ]]; then\n" +
		"  printf '%s %s\\n' \"$2\" \"$3\" >> " + strconvQuote(logPath) + "\n" +
		"fi\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// strconvQuote is a tiny shell-single-quote so the log path cannot break the
// wrapper script when it contains spaces (t.TempDir sometimes does).
func strconvQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// TestCreateProjectJJPinNameUniquePerCreate: the detached-HEAD pin embeds the
// create commit's short SHA so two creates cannot share one pin name. A prior
// rework claimed uniqueness fixed a concurrent-clobber flake, but mutating
// the pin back to a fixed string left the whole suite green - the strategy
// was entirely unobserved.
//
// What this proves: two successive creates in one detached checkout publish
// DISTINCT pin refs, each equal to prefix+shortSHA(createRev). That fails the
// moment the pin becomes a fixed string again.
//
// What this does NOT prove: real concurrent interleaving is race-free under
// parallel goroutines/processes; that a fixed name would flake under load; or
// that cleanup of pin A cannot race with pin B's publish mid-flight. Those
// need a true concurrent harness, which this suite does not run.
func TestCreateProjectJJPinNameUniquePerCreate(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	jjCommitFile(t, dir, "README.md", "# monorepo\n", "initial")
	writeTestFile(t, dir, "note.txt", "work\n")
	if _, err := CreateChange(dir, "some work", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := runGit(dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("precondition: expected detached HEAD after change create")
	}

	logPath := filepath.Join(t.TempDir(), "pin-log")
	installGitPinLogger(t, logPath)

	rev1, _, err := CreateProject(dir, project.Intent{
		Name: "svc-a", Type: "library", API: "none", Owners: []string{"group:eng"},
	})
	if err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	rev2, _, err := CreateProject(dir, project.Intent{
		Name: "svc-b", Type: "library", API: "none", Owners: []string{"group:eng"},
	})
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}
	if rev1 == rev2 {
		t.Fatalf("expected two distinct create commits, both %s", rev1)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read pin log: %v (wrapper never saw a pin update-ref?)", err)
	}
	lines := strings.FieldsFunc(string(raw), func(r rune) bool { return r == '\n' })
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 pin publishes, got %d:\n%s", len(lines), raw)
	}

	wantPin := func(rev string) string {
		sha := rev
		if len(sha) > 12 {
			sha = sha[:12]
		}
		return "refs/heads/" + jjProjectCreatePinPrefix + sha
	}
	// Each line is "refs/heads/<pin> <fullsha>".
	parse := func(line string) (ref, sha string) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("malformed pin log line %q", line)
		}
		return fields[0], fields[1]
	}
	ref1, sha1 := parse(lines[0])
	ref2, sha2 := parse(lines[1])
	if ref1 != wantPin(rev1) || !strings.HasPrefix(sha1, rev1[:12]) {
		t.Fatalf("first pin: want ref %s pointing at %s, got %q", wantPin(rev1), rev1, lines[0])
	}
	if ref2 != wantPin(rev2) || !strings.HasPrefix(sha2, rev2[:12]) {
		t.Fatalf("second pin: want ref %s pointing at %s, got %q", wantPin(rev2), rev2, lines[1])
	}
	if ref1 == ref2 {
		t.Fatalf("pin names must differ across creates; both used %s", ref1)
	}
}

// TestCreateProjectJJLeftoverPinSurvivesNextCreate: a pin left behind by an
// interrupted create (simulated) must not be destroyed by the next create's
// cleanup. Unique-per-SHA names make cleanup target only THIS create's pin;
// a fixed name would have the next create's forget drop the leftover too
// (when they share the name) - or overwrite it mid-flight under concurrency.
//
// Combined with TestCreateProjectJJPinNameUniquePerCreate this covers the
// "does not collide with or get destroyed by the next create" angle without
// real parallelism. Still does not prove concurrent interleaving safety.
func TestCreateProjectJJLeftoverPinSurvivesNextCreate(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	jjCommitFile(t, dir, "README.md", "# monorepo\n", "initial")
	writeTestFile(t, dir, "note.txt", "work\n")
	if _, err := CreateChange(dir, "some work", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	tip, err := jjTipCommit(dir)
	if err != nil {
		t.Fatalf("jjTipCommit: %v", err)
	}
	// Orphan pin from a fictional prior create - name cannot match the next
	// create's SHA-derived pin (next create's tree differs, so its SHA does).
	leftover := jjProjectCreatePinPrefix + "deadbeefcafe"
	if _, err := runGit(dir, "update-ref", "refs/heads/"+leftover, tip); err != nil {
		t.Fatalf("plant leftover pin: %v", err)
	}
	// Import so jj knows the bookmark (same as a pin that survived past a jj op).
	if _, err := runJJ(dir, "bookmark", "list"); err != nil {
		t.Fatalf("jj bookmark list (import): %v", err)
	}

	if _, _, err := CreateProject(dir, project.Intent{
		Name: "svc", Type: "library", API: "none", Owners: []string{"group:eng"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	books, err := runJJ(dir, "bookmark", "list", "-T", `name ++ "\n"`)
	if err != nil {
		t.Fatalf("jj bookmark list: %v", err)
	}
	found := false
	for _, name := range strings.Fields(books) {
		if name == leftover {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("leftover pin %q must survive the next create's cleanup; bookmarks:\n%s", leftover, books)
	}
	// And the create's own pin must still have been dropped.
	for _, name := range strings.Fields(books) {
		if name != leftover && strings.HasPrefix(name, jjProjectCreatePinPrefix) {
			t.Fatalf("create's own pin %q leaked; bookmarks:\n%s", name, books)
		}
	}
}

// TestCreateProjectJJPinCleanupWithoutPriorImport: pin written via git
// update-ref, then dropJJProjectCreatePin with no intervening jj op that
// would have imported it. drop uses `jj bookmark forget`, which itself
// imports git heads - so cleanup must still remove the refs/heads pin.
// Contrasts with TestCreateProjectJJDropsPinOnError, where sparse list runs
// (and imports) before the injected failure.
//
// Note: forget errors are swallowed. If jj is unavailable on the error path,
// a real refs/heads pin can survive; that gap is intentional best-effort and
// not asserted here.
func TestCreateProjectJJPinCleanupWithoutPriorImport(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	jjCommitFile(t, dir, "README.md", "# monorepo\n", "initial")
	writeTestFile(t, dir, "note.txt", "work\n")
	if _, err := CreateChange(dir, "some work", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := runGit(dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("precondition: expected detached HEAD")
	}

	// Build a throwaway create commit the same way CreateProject does, then
	// exercise only publish+drop - no syncWorkingTreeJJ, so no jj op between
	// pin write and forget except forget itself.
	store := gitstore.New(dir)
	base, err := resolveBaseOrEmpty(dir, store)
	if err != nil {
		t.Fatalf("resolveBaseOrEmpty: %v", err)
	}
	plan, errs := project.PlanCreate(project.Intent{
		Name: "svc", Type: "library", API: "none", Owners: []string{"group:eng"},
	}, project.DefaultTemplates())
	if len(errs) > 0 {
		t.Fatalf("PlanCreate: %v", errs)
	}
	newRev, err := project.Apply(store, base, plan, core.CommitMeta{Message: "Create project svc\n"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	pin, err := publishCommitForJJ(dir, store, newRev, base)
	if err != nil {
		t.Fatalf("publishCommitForJJ: %v", err)
	}
	if pin == "" {
		t.Fatal("expected a pin on detached HEAD")
	}
	// Confirm the git ref exists and jj has not listed it yet as a bookmark
	// in this process... we deliberately do not run bookmark list first.
	ref := "refs/heads/" + pin
	if _, err := runGit(dir, "rev-parse", "--verify", ref); err != nil {
		t.Fatalf("pin ref missing after publish: %v", err)
	}

	dropJJProjectCreatePin(dir, pin)

	if out, err := runGit(dir, "rev-parse", "--verify", ref); err == nil {
		t.Fatalf("pin git ref %s survived drop without prior import (rev-parse=%s); forget should import then delete", ref, out)
	}
	books, err := runJJ(dir, "bookmark", "list", "-T", `name ++ "\n"`)
	if err != nil {
		t.Fatalf("jj bookmark list: %v", err)
	}
	for _, name := range strings.Fields(books) {
		if name == pin || strings.HasPrefix(name, jjProjectCreatePinPrefix) {
			t.Fatalf("pin bookmark %q survived drop; bookmarks:\n%s", name, books)
		}
	}
}

// TestCreateProjectJJAdvancesBranchWhenNotDetached: on a symbolic-ref HEAD
// (branched colocated checkout), publish must advance the branch - not fall
// through to a temporary pin. Mutation: skip UpdateRef and always pin;
// create still succeeds (pin+jj new works), so without this check the branch
// path is untested.
func TestCreateProjectJJAdvancesBranchWhenNotDetached(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)
	if _, err := runGit(dir, "symbolic-ref", "-q", "HEAD"); err != nil {
		t.Fatalf("precondition: expected symbolic HEAD, got %v", err)
	}
	headRef, err := runGit(dir, "symbolic-ref", "HEAD")
	if err != nil {
		t.Fatalf("symbolic-ref HEAD: %v", err)
	}
	before, err := runGit(dir, "rev-parse", headRef)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", headRef, err)
	}

	logPath := filepath.Join(t.TempDir(), "pin-log")
	installGitPinLogger(t, logPath)

	rev, _, err := CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none", Owners: []string{"group:commerce-eng"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	after, err := runGit(dir, "rev-parse", headRef)
	if err != nil {
		t.Fatalf("rev-parse %s after: %v", headRef, err)
	}
	if after != rev {
		t.Fatalf("branch %s must advance to create commit %s (was %s), got %s", headRef, rev, before, after)
	}
	if raw, err := os.ReadFile(logPath); err == nil && strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("on-branch create must not write a pin ref, pin log:\n%s", raw)
	}
}

// TestCreateProjectJJOnEmptyRepoCreatesFirstCommit: fresh colocated jj with
// nothing finished yet. jjTipCommit returns the all-zeros root; resolveBase
// must coerce that to an empty base so CommitOverlay can mint an orphan
// first commit (same bar as the plain-git empty-repo path). Mutation:
// passing the zeros SHA through as a real base fails the scan/Apply path;
// the no_commits arm of resolveBase is a different empty shape and is
// still untested here (jj always has a root parent for @-).
func TestCreateProjectJJOnEmptyRepoCreatesFirstCommit(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)

	rev, _, err := CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none", Owners: []string{"group:commerce-eng"},
	})
	if err != nil {
		t.Fatalf("CreateProject on empty jj repo: %v", err)
	}
	if rev == "" {
		t.Fatal("expected a new commit SHA")
	}
	if _, err := os.Stat(filepath.Join(dir, "checkout-api", "PROJECT.yaml")); err != nil {
		t.Fatalf("expected project files on disk: %v", err)
	}
	parents, err := runGit(dir, "rev-list", "--parents", "-n", "1", rev)
	if err != nil {
		t.Fatalf("rev-list --parents: %v", err)
	}
	if strings.Contains(parents, " ") {
		t.Fatalf("expected the first commit to have no parents, got: %q", parents)
	}
}

// TestCreateProjectJJSparseListFailureIsFailClosed: syncWorkingTreeJJ must
// not treat a failed `jj sparse list` as "unrestricted" (which would skip
// the widen and leave the new project unmaterialized). Mutation: on list
// error, set prefixes=nil and proceed - every other create test still passes.
func TestCreateProjectJJSparseListFailureIsFailClosed(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "README.md"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}

	realJJ, err := exec.LookPath("jj")
	if err != nil {
		t.Fatal(err)
	}
	wrapDir := t.TempDir()
	wrap := filepath.Join(wrapDir, "jj")
	// Fail only `jj sparse list` - the read that decides whether to widen.
	script := "#!/bin/bash\n" +
		"prev=\"\"\n" +
		"for b in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"sparse\" ] && [ \"$b\" = \"list\" ]; then\n" +
		"    echo \"injected sparse list failure\" >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"  prev=$b\n" +
		"done\n" +
		"exec " + realJJ + " \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err = CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none",
	})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "jj_sparse_list_failed" {
		t.Fatalf("want jj_sparse_list_failed (fail-closed), got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "checkout-api", "PROJECT.yaml")); err == nil {
		t.Fatal("fail-closed path must not materialize the project on disk")
	}
}

// TestCreateProjectJJWidensSparseFromSubdir: runJJStderr pins cmd.Dir to the
// repo, so sparse --add path arguments stay repo-relative even when the
// process CWD is a subdirectory (the pre-fix failure mode).
func TestCreateProjectJJWidensSparseFromSubdir(t *testing.T) {
	requireJJ(t)
	dir := newBranchedColocatedJJRepo(t)
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "README.md"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if _, _, err := CreateProject(dir, project.Intent{
		Name: "checkout-api", Type: "service", API: "none",
	}); err != nil {
		t.Fatalf("CreateProject from subdir: %v", err)
	}

	after := jjSparsePrefixes(dir)
	found := false
	for _, p := range after {
		if p == "checkout-api" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sparse set should include checkout-api when create ran from a subdir, got %v", after)
	}
	if _, err := os.Stat(filepath.Join(dir, "checkout-api", "PROJECT.yaml")); err != nil {
		t.Fatalf("expected checkout-api on disk: %v", err)
	}
}

// TestCreateProjectUnsupportedLangRequiresNoTemplate pins the escape-hatch
// pairing: an untemplated language is a loud unsupported_language error
// without --no-template, and a manifest-only create with it (§10.4).
func TestCreateProjectUnsupportedLangRequiresNoTemplate(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "# monorepo\n")
	repo.Commit("initial")

	_, _, err := CreateProject(repo.Dir, project.Intent{
		Name: "exotic-svc", Type: "service", API: "none", Language: "haskell",
	})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unsupported_language" || ce.Field != "language" {
		t.Fatalf("expected a structured unsupported_language error, got: %v", err)
	}
	if !strings.Contains(ce.Suggestion, "--no-template") {
		t.Fatalf("expected the suggestion to name --no-template, got: %q", ce.Suggestion)
	}

	if _, _, err := CreateProject(repo.Dir, project.Intent{
		Name: "exotic-svc", Type: "service", API: "none", Language: "haskell", NoTemplate: true,
	}); err != nil {
		t.Fatalf("CreateProject with NoTemplate: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo.Dir, "exotic-svc", "PROJECT.yaml"))
	if err != nil {
		t.Fatalf("read PROJECT.yaml: %v", err)
	}
	if !strings.Contains(string(content), "language: haskell") {
		t.Fatalf("expected 'language: haskell' recorded verbatim, got:\n%s", content)
	}
	entries, err := os.ReadDir(filepath.Join(repo.Dir, "exotic-svc"))
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "PROJECT.yaml", "README.md", "BUILD.bazel":
		default:
			t.Fatalf("no-template create must not scaffold %s", e.Name())
		}
	}
}
