package runkod

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBareRepoCreatesAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo.git")
	if err := EnsureBareRepo(dir, "main"); err != nil {
		t.Fatalf("EnsureBareRepo (create): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Fatalf("expected a bare repo to exist at %s: %v", dir, err)
	}
	out, err := exec.Command("git", "-C", dir, "config", "http.receivepack").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("expected http.receivepack=true, got %q (err=%v)", out, err)
	}

	// Calling again on an existing repo must not fail or reset anything.
	if err := EnsureBareRepo(dir, "main"); err != nil {
		t.Fatalf("EnsureBareRepo (idempotent call): %v", err)
	}
}

func TestInstallPreReceiveHookWritesExecutableScript(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo.git")
	if err := EnsureBareRepo(dir, "main"); err != nil {
		t.Fatalf("EnsureBareRepo: %v", err)
	}
	if err := InstallPreReceiveHook(dir, "http://127.0.0.1:9999", "sekret"); err != nil {
		t.Fatalf("InstallPreReceiveHook: %v", err)
	}

	hookPath := filepath.Join(dir, "hooks", "pre-receive")
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("expected a pre-receive hook file: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("expected the hook to be executable, got mode %v", info.Mode())
	}
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	for _, want := range []string{"hook pre-receive", "--addr \"http://127.0.0.1:9999\"", "--token \"sekret\""} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("expected hook script to contain %q, got:\n%s", want, content)
		}
	}
}

func TestRepoMountName(t *testing.T) {
	if got := RepoMountName("/data/monorepo.git/"); got != "monorepo.git" {
		t.Fatalf("RepoMountName = %q, want monorepo.git", got)
	}
}

// The org-named git mount: an org server advertises <org>.git and serves
// it by rewriting onto the on-disk repo.git mount; the root default server
// (no OrgName) keeps advertising its repo-dir basename unchanged.
func TestRepoMountOrgAlias(t *testing.T) {
	s := &Server{RepoDir: "/data/orgs/acme/repo.git", OrgName: "acme"}
	if got := s.repoMount(); got != "acme.git" {
		t.Fatalf("org server repoMount = %q, want acme.git", got)
	}
	root := &Server{RepoDir: "/data/monorepo.git"}
	if got := root.repoMount(); got != "monorepo.git" {
		t.Fatalf("root server repoMount = %q, want monorepo.git", got)
	}

	var sawPath string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
	})
	h := rewriteGitMount("acme.git", "repo.git", inner)
	req := httptest.NewRequest("GET", "/acme.git/info/refs?service=git-upload-pack", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if sawPath != "/repo.git/info/refs" {
		t.Fatalf("rewritten path = %q, want /repo.git/info/refs", sawPath)
	}
}

func TestGitHTTPHandlerLocatesBackend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo.git")
	if err := EnsureBareRepo(dir, "main"); err != nil {
		t.Fatalf("EnsureBareRepo: %v", err)
	}
	h, err := GitHTTPHandler(dir)
	if err != nil {
		t.Fatalf("GitHTTPHandler: %v", err)
	}
	if h.Path == "" {
		t.Fatalf("expected a resolved git-http-backend path")
	}
}

// TestPruneDanglingChangeRefs: a change ref pointing at a missing object
// bricks the repo (git connectivity check rejects every push); boot-time
// pruning heals it. migration-findings #34.
func TestPruneDanglingChangeRefs(t *testing.T) {
	repo := newBareRepo(t)
	work := t.TempDir()
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.dev", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.dev")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run(work, "clone", repo, ".")
	run(work, "commit", "--allow-empty", "-m", "real")
	run(work, "push", "origin", "HEAD:refs/heads/main")
	realSHA := strings.TrimSpace(run(work, "rev-parse", "HEAD"))

	// A healthy change ref (kept) and a dangling one pointing at a SHA the
	// repo does not have (pruned).
	run(repo, "update-ref", "refs/changes/Ihealthy/head", realSHA)
	// A dangling ref can't be made via update-ref (git validates the
	// object); write the loose ref file directly, exactly the on-disk
	// state a crash-after-ref-write-before-quarantine-migration leaves.
	danglingDir := filepath.Join(repo, "refs", "changes", "Idangling")
	if err := os.MkdirAll(danglingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(danglingDir, "head"), []byte("0000000000000000000000000000000000000001\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PruneDanglingChangeRefs(repo); err != nil {
		t.Fatalf("prune: %v", err)
	}
	refs := run(repo, "for-each-ref", "refs/changes/")
	if !strings.Contains(refs, "Ihealthy") {
		t.Fatalf("healthy change ref must survive, got:\n%s", refs)
	}
	if strings.Contains(refs, "Idangling") {
		t.Fatalf("dangling change ref must be pruned, got:\n%s", refs)
	}
}
