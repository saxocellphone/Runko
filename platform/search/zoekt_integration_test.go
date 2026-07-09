//go:build zoekt_integration

// This file only builds with `go test -tags zoekt_integration`. It requires
// real zoekt-git-index and zoekt-webserver binaries on PATH, neither of
// which exist in this sandbox (no Zoekt install here - see CLAUDE.md); it
// exists so a real Zoekt install (a dev machine, CI with the tag enabled)
// can verify ZoektIndexer + ZoektSearcher against the genuine binaries, not
// just the scripted fakes in indexer_test.go/zoekt_test.go - the same
// pattern buildadapter/bazel/bazel_integration_test.go uses for Bazel.
package search

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIndexAndSearchAgainstRealZoekt(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@runko.dev")
	runGit(t, repoDir, "config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(repoDir, "commerce", "checkout"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "commerce", "checkout", "main.go"), []byte("package main\n\nfunc ZoektIntegrationMarker() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-q", "-m", "seed")

	indexDir := t.TempDir()
	indexer := ZoektIndexer{IndexDir: indexDir}
	if err := indexer.Index(context.Background(), repoDir); err != nil {
		t.Fatalf("Index against real zoekt-git-index: %v", err)
	}

	addr := "127.0.0.1:6072"
	cmd := exec.Command("zoekt-webserver", "-listen", addr, "-index", indexDir, "-html=false")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start zoekt-webserver: %v", err)
	}
	defer cmd.Process.Kill()

	baseURL := "http://" + addr
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("zoekt-webserver never became healthy: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	searcher := ZoektSearcher{BaseURL: baseURL}
	result, err := searcher.Search(context.Background(), "ZoektIntegrationMarker", SearchOptions{})
	if err != nil {
		t.Fatalf("Search against real zoekt-webserver: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatalf("expected at least one hit for a marker symbol only this test writes")
	}
	found := false
	for _, h := range result.Hits {
		if strings.Contains(h.Path, "commerce/checkout/main.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a hit in commerce/checkout/main.go, got %+v", result.Hits)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
