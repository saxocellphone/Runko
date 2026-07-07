package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// buildRunkod compiles the real runkod binary once per test run - the
// end-to-end test below drives it as a real subprocess over real HTTP with
// real git commands (§28.2 rule 4: shell out to git, never reimplement it;
// same spirit applies to testing our own daemon as a black box, not via
// in-process shortcuts that could hide real wiring bugs like the auth gap
// found while building this package - see runkod/api.go).
func buildRunkod(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "runkod")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build runkod: %v\n%s", err, out)
	}
	return bin
}

// scriptedCleanGitleaks is a fake gitleaks binary that always reports no
// findings - real gitleaks itself is unit-tested in runkod/gitleaks_test.go;
// this end-to-end test only needs the daemon to accept a clean push.
func scriptedCleanGitleaks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-gitleaks")
	script := "#!/bin/sh\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--report-path\" ]; then echo '[]' > \"$arg\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gitleaks: %v", err)
	}
	return path
}

var servingAddrPattern = regexp.MustCompile(`at (http://127\.0\.0\.1:\d+)`)

// startDaemon runs the real compiled binary as a subprocess and returns its
// base URL once it's ready to accept connections.
func startDaemon(t *testing.T, bin, repoDir, token string) string {
	t.Helper()
	fakeGitleaks := scriptedCleanGitleaks(t)
	cmd := exec.Command(bin, "serve",
		"--repo-dir", repoDir, "--addr", "127.0.0.1:0", "--trunk", "main",
		"--token", token, "--gitleaks-bin", fakeGitleaks,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runkod: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if m := servingAddrPattern.FindStringSubmatch(line); m != nil {
				addrCh <- m[1]
				return
			}
		}
	}()

	select {
	case addr := <-addrCh:
		return addr
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for runkod to report its listen address")
	}
	return ""
}

func runGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.dev", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.dev")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// TestEndToEndDaemon is the DAG stage 10 bar, driven for real: a real
// compiled runkod binary, real git pushes over real HTTP (not local-path),
// a real installed pre-receive hook shelling back into the same binary,
// and a real REST API round-trip - "push to refs/for/main creates a
// Change; direct trunk push gets the §6.9 script; report-check round-trips
// against it."
func TestEndToEndDaemon(t *testing.T) {
	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	baseURL := startDaemon(t, bin, repoDir, token)

	remoteURL := strings.Replace(baseURL, "http://", "http://runko:"+token+"@", 1) + "/" + filepath.Base(repoDir) + "/"

	work := t.TempDir()
	if _, err := runGit(t, work, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := runGit(t, work, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(t, work, "commit", "-q", "-m", "add readme"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(t, work, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	// 1. Magic-ref push must succeed and create a Change.
	out, err := runGit(t, work, "push", "origin", "+HEAD:refs/for/main")
	if err != nil {
		t.Fatalf("push to refs/for/main should succeed: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(I[0-9a-f]{40}) -> refs/for/main`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a Change-Id in the push output, got:\n%s", out)
	}
	changeID := m[1]

	// 2. Direct trunk push must be rejected with the §6.9 script.
	out, err = runGit(t, work, "push", "origin", "HEAD:main")
	if err == nil {
		t.Fatalf("expected a direct push to main to be rejected, got:\n%s", out)
	}
	if !strings.Contains(out, "refs/for/main") || !strings.Contains(out, "runko change push") {
		t.Fatalf("expected the §6.9 rejection script, got:\n%s", out)
	}

	// 3. runko-ci report-check round-trips against the REST API.
	client := &http.Client{Timeout: 5 * time.Second}
	body, _ := json.Marshal(map[string]string{
		"name": "unit", "external_id": "job-1", "status": "completed", "conclusion": "success", "reporter": "github-actions",
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/checks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST checks: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from report-check, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	mrReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/changes/"+changeID+"/merge-requirements", nil)
	mrReq.Header.Set("Authorization", "Bearer "+token)
	mrResp, err := client.Do(mrReq)
	if err != nil {
		t.Fatalf("GET merge-requirements: %v", err)
	}
	defer mrResp.Body.Close()
	var mr struct {
		Mergeable bool
	}
	if err := json.NewDecoder(mrResp.Body).Decode(&mr); err != nil {
		t.Fatalf("decode merge-requirements: %v", err)
	}
	if !mr.Mergeable {
		t.Fatalf("expected the Change to be mergeable after a successful check report")
	}
}
