package runkod

import (
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
