package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/dbtest"
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

// buildRunko compiles the real runko CLI - the workspace e2e test drives it
// as a subprocess, exactly the way a user would.
func buildRunko(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "runko")
	cmd := exec.Command("go", "build", "-o", bin, "../runko")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build runko: %v\n%s", err, out)
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
// base URL once it's ready to accept connections. The subprocess is killed
// automatically at test cleanup.
func startDaemon(t *testing.T, bin, repoDir, token string, extraArgs ...string) string {
	t.Helper()
	addr, stop := startDaemonProcess(t, bin, repoDir, token, extraArgs...)
	t.Cleanup(stop)
	return addr
}

// startDaemonProcess is startDaemon without automatic cleanup registration -
// for tests that need to stop ONE daemon instance mid-test (e.g. to prove
// state survives a restart) before starting a second.
func startDaemonProcess(t *testing.T, bin, repoDir, token string, extraArgs ...string) (addr string, stop func()) {
	t.Helper()
	fakeGitleaks := scriptedCleanGitleaks(t)
	args := append([]string{"serve",
		"--repo-dir", repoDir, "--addr", "127.0.0.1:0", "--trunk", "main",
		"--token", token, "--gitleaks-bin", fakeGitleaks,
	}, extraArgs...)
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runkod: %v", err)
	}
	stop = func() {
		cmd.Process.Kill()
		cmd.Wait()
	}

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
	case addr = <-addrCh:
		return addr, stop
	case <-time.After(10 * time.Second):
		stop()
		t.Fatalf("timed out waiting for runkod to report its listen address")
	}
	return "", stop
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

	// 4. Land it (§13.5, §28.3 stage 11b) - the wire-level verb that was
	// missing entirely before this stage: land.Land and the merge-
	// requirements gate both existed and were both fully tested, but
	// nothing called them from the write path. Confirm via a real
	// `git ls-remote` (not just trusting the REST response) that trunk
	// actually advanced on the served bare repo.
	landReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/land", nil)
	landReq.Header.Set("Authorization", "Bearer "+token)
	landResp, err := client.Do(landReq)
	if err != nil {
		t.Fatalf("POST land: %v", err)
	}
	defer landResp.Body.Close()
	if landResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(landResp.Body)
		t.Fatalf("expected 200 from land, got %d: %s", landResp.StatusCode, body)
	}
	var landOutcome struct {
		Landed    bool
		LandedSHA string
	}
	if err := json.NewDecoder(landResp.Body).Decode(&landOutcome); err != nil {
		t.Fatalf("decode land response: %v", err)
	}
	if !landOutcome.Landed || landOutcome.LandedSHA == "" {
		t.Fatalf("expected a landed outcome, got %+v", landOutcome)
	}

	lsRemote, err := runGit(t, work, "ls-remote", remoteURL, "refs/heads/main")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if !strings.Contains(lsRemote, landOutcome.LandedSHA) {
		t.Fatalf("expected refs/heads/main to have advanced to %s, got:\n%s", landOutcome.LandedSHA, lsRemote)
	}

	changeReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/changes/"+changeID, nil)
	changeReq.Header.Set("Authorization", "Bearer "+token)
	changeResp, err := client.Do(changeReq)
	if err != nil {
		t.Fatalf("GET change after land: %v", err)
	}
	defer changeResp.Body.Close()
	var landedChange struct{ State string }
	if err := json.NewDecoder(changeResp.Body).Decode(&landedChange); err != nil {
		t.Fatalf("decode change after land: %v", err)
	}
	if landedChange.State != "landed" {
		t.Fatalf("expected the Change's state to be 'landed', got %q", landedChange.State)
	}
}

// TestEndToEndDaemonRequiredCheckBlocksLandWithZeroRunsPosted is a real,
// compiled-binary-over-real-HTTP regression test for a review finding: "a
// Change with zero checks and zero approvals lands successfully" (§28.3
// stage 11b's follow-up). Required check names used to be derived from
// whatever had already been POSTED, so a project that never got a single
// check report was trivially mergeable regardless of policy. Here the
// project's PROJECT.yaml declares a required "unit" check via ci.checks
// (§14.9) and NOTHING is ever posted for it - land must be rejected with
// not_mergeable, not silently succeed.
func TestEndToEndDaemonRequiredCheckBlocksLandWithZeroRunsPosted(t *testing.T) {
	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	baseURL := startDaemon(t, bin, repoDir, token)

	remoteURL := strings.Replace(baseURL, "http://", "http://runko:"+token+"@", 1) + "/" + filepath.Base(repoDir) + "/"

	work := t.TempDir()
	if _, err := runGit(t, work, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(work, "commerce", "checkout"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := "schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n"
	if err := os.WriteFile(filepath.Join(work, "commerce", "checkout", "PROJECT.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write PROJECT.yaml: %v", err)
	}
	if _, err := runGit(t, work, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(t, work, "commit", "-q", "-m", "add checkout-api"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(t, work, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	out, err := runGit(t, work, "push", "origin", "+HEAD:refs/for/main")
	if err != nil {
		t.Fatalf("push to refs/for/main: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(I[0-9a-f]{40}) -> refs/for/main`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a Change-Id in the push output, got:\n%s", out)
	}
	changeID := m[1]

	client := &http.Client{Timeout: 5 * time.Second}

	mrReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/changes/"+changeID+"/merge-requirements", nil)
	mrReq.Header.Set("Authorization", "Bearer "+token)
	mrResp, err := client.Do(mrReq)
	if err != nil {
		t.Fatalf("GET merge-requirements: %v", err)
	}
	defer mrResp.Body.Close()
	var mr struct {
		Checks    struct{ Required, Pending []string }
		Mergeable bool
	}
	if err := json.NewDecoder(mrResp.Body).Decode(&mr); err != nil {
		t.Fatalf("decode merge-requirements: %v", err)
	}
	if mr.Mergeable {
		t.Fatalf("expected NOT mergeable with a declared-required check never posted, got %+v", mr)
	}
	if len(mr.Checks.Required) != 1 || mr.Checks.Required[0] != "unit" {
		t.Fatalf("expected 'unit' to be reported required (declared via ci.checks), got %+v", mr.Checks)
	}
	if len(mr.Checks.Pending) != 1 || mr.Checks.Pending[0] != "unit" {
		t.Fatalf("expected 'unit' pending (never reported), got %+v", mr.Checks)
	}

	landReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/land", nil)
	landReq.Header.Set("Authorization", "Bearer "+token)
	landResp, err := client.Do(landReq)
	if err != nil {
		t.Fatalf("POST land: %v", err)
	}
	defer landResp.Body.Close()
	if landResp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(landResp.Body)
		t.Fatalf("expected 409 (not mergeable), got %d: %s", landResp.StatusCode, body)
	}
	var ce struct{ Code string }
	if err := json.NewDecoder(landResp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode land error response: %v", err)
	}
	if ce.Code != "not_mergeable" {
		t.Fatalf("expected code not_mergeable, got %+v", ce)
	}

	lsRemote, err := runGit(t, work, "ls-remote", remoteURL, "refs/heads/main")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if strings.TrimSpace(lsRemote) != "" {
		t.Fatalf("expected refs/heads/main to still be unborn (land must not have happened), got:\n%s", lsRemote)
	}
}

// TestEndToEndDaemonOwnerApprovalAndRequiredCheckGateLand is §28.3 stage
// 11c's owners bar, end to end: real compiled daemon, real git push of a
// project declaring BOTH a required check (ci.checks) and an owner. Land
// must be refused pre-check AND pre-approval, stay refused when only the
// check is green, and succeed only after the owner approval too - §13.5's
// first two gate rows working at the wire level, not decoratively.
func TestEndToEndDaemonOwnerApprovalAndRequiredCheckGateLand(t *testing.T) {
	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	baseURL := startDaemon(t, bin, repoDir, token)

	remoteURL := strings.Replace(baseURL, "http://", "http://runko:"+token+"@", 1) + "/" + filepath.Base(repoDir) + "/"

	work := t.TempDir()
	if _, err := runGit(t, work, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(work, "commerce", "checkout"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := "schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n"
	if err := os.WriteFile(filepath.Join(work, "commerce", "checkout", "PROJECT.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write PROJECT.yaml: %v", err)
	}
	if _, err := runGit(t, work, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(t, work, "commit", "-q", "-m", "add checkout-api"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(t, work, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	out, err := runGit(t, work, "push", "origin", "+HEAD:refs/for/main")
	if err != nil {
		t.Fatalf("push to refs/for/main: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(I[0-9a-f]{40}) -> refs/for/main`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a Change-Id in the push output, got:\n%s", out)
	}
	changeID := m[1]

	client := &http.Client{Timeout: 5 * time.Second}
	postLand := func() (int, string) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/land", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST land: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Pre-check AND pre-approval: refused.
	if code, body := postLand(); code != http.StatusConflict {
		t.Fatalf("expected 409 with check pending and approval outstanding, got %d: %s", code, body)
	}

	// Green check alone must NOT be enough - the owner is still outstanding.
	checkBody, _ := json.Marshal(map[string]string{
		"name": "unit", "external_id": "job-1", "status": "completed", "conclusion": "success", "reporter": "github-actions",
	})
	checkReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/checks", bytes.NewReader(checkBody))
	checkReq.Header.Set("Authorization", "Bearer "+token)
	checkReq.Header.Set("Content-Type", "application/json")
	checkResp, err := client.Do(checkReq)
	if err != nil {
		t.Fatalf("POST checks: %v", err)
	}
	checkResp.Body.Close()
	if code, body := postLand(); code != http.StatusConflict {
		t.Fatalf("expected 409 with the check green but the approval still outstanding, got %d: %s", code, body)
	}

	// Approve, then land succeeds.
	approveBody, _ := json.Marshal(map[string]string{"owner_ref": "group:commerce-eng", "approved_by": "alice"})
	approveReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/approve", bytes.NewReader(approveBody))
	approveReq.Header.Set("Authorization", "Bearer "+token)
	approveReq.Header.Set("Content-Type", "application/json")
	approveResp, err := client.Do(approveReq)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	defer approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(approveResp.Body)
		t.Fatalf("expected 200 from approve, got %d: %s", approveResp.StatusCode, body)
	}
	var mr struct {
		Owners    struct{ Satisfied []string }
		Mergeable bool
	}
	if err := json.NewDecoder(approveResp.Body).Decode(&mr); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	if !mr.Mergeable || len(mr.Owners.Satisfied) != 1 {
		t.Fatalf("expected mergeable with the owner satisfied after approve, got %+v", mr)
	}

	code, body := postLand()
	if code != http.StatusOK {
		t.Fatalf("expected 200 land after check + approval, got %d: %s", code, body)
	}
	var landOutcome struct {
		Landed    bool
		LandedSHA string
	}
	if err := json.Unmarshal([]byte(body), &landOutcome); err != nil {
		t.Fatalf("decode land response: %v", err)
	}
	lsRemote, err := runGit(t, work, "ls-remote", remoteURL, "refs/heads/main")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if !strings.Contains(lsRemote, landOutcome.LandedSHA) {
		t.Fatalf("expected refs/heads/main at %s, got:\n%s", landOutcome.LandedSHA, lsRemote)
	}
}

func TestParseBotLane(t *testing.T) {
	lane, err := parseBotLane("name=image-bumper;token=tok2;paths=deploy/**,charts/**;checks=manifest-lint")
	if err != nil {
		t.Fatalf("parseBotLane: %v", err)
	}
	if lane.Name != "image-bumper" || lane.Token != "tok2" {
		t.Fatalf("unexpected lane identity: %+v", lane)
	}
	if len(lane.PathAllowlist) != 2 || lane.PathAllowlist[0] != "deploy/**" || lane.PathAllowlist[1] != "charts/**" {
		t.Fatalf("unexpected allowlist: %+v", lane.PathAllowlist)
	}
	if len(lane.RequiredChecks) != 1 || lane.RequiredChecks[0] != "manifest-lint" {
		t.Fatalf("unexpected checks: %+v", lane.RequiredChecks)
	}

	// §14.10.2: a lane without its own required-check set is an unchecked
	// auto-land grant - refused at parse time, not silently permitted.
	for _, bad := range []string{
		"name=x;token=t;paths=deploy/**",           // no checks
		"name=x;token=t;checks=lint",               // no paths
		"token=t;paths=deploy/**;checks=lint",      // no name
		"name=x;paths=deploy/**;checks=lint",       // no token
		"name=x;token=t;paths=deploy/**;checks=",   // empty checks
		"name=x;token=t;paths=deploy/**;lint",      // not key=value
		"name=x;token=t;paths=deploy/**;what=ever", // unknown key
	} {
		if _, err := parseBotLane(bad); err == nil {
			t.Fatalf("expected parseBotLane(%q) to fail", bad)
		}
	}
}

// TestEndToEndDaemonBotLaneAutoLands is §14.10.2 over the wire: a real
// compiled daemon started with --bot-lane, a project with a human owner
// requirement, and two principals gating the same Change - the deploy
// token is refused (owner approval outstanding), the lane token lands it
// once the lane's own required check is green, without any approval ever
// posted. The GitOps writer flow: an image bump must not need a human
// click, but only inside its allowlist and only with its check green.
func TestEndToEndDaemonBotLaneAutoLands(t *testing.T) {
	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	laneToken := "lane-token"
	baseURL := startDaemon(t, bin, repoDir, token,
		"--bot-lane", "name=image-bumper;token="+laneToken+";paths=deploy/**;checks=manifest-lint")

	remoteURL := strings.Replace(baseURL, "http://", "http://runko:"+token+"@", 1) + "/" + filepath.Base(repoDir) + "/"

	work := t.TempDir()
	if _, err := runGit(t, work, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(work, "deploy"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := "schema: project/v1\nname: deploy-config\ntype: library\nowners:\n  - group:platform-eng\n"
	if err := os.WriteFile(filepath.Join(work, "deploy", "PROJECT.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write PROJECT.yaml: %v", err)
	}
	if _, err := runGit(t, work, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(t, work, "commit", "-q", "-m", "bump image tag"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(t, work, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	out, err := runGit(t, work, "push", "origin", "+HEAD:refs/for/main")
	if err != nil {
		t.Fatalf("push to refs/for/main: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(I[0-9a-f]{40}) -> refs/for/main`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a Change-Id in the push output, got:\n%s", out)
	}
	changeID := m[1]

	client := &http.Client{Timeout: 5 * time.Second}
	postLandAs := func(bearer string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/land", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST land: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Deploy token: the human gate applies - owner approval outstanding.
	if code, body := postLandAs(token); code != http.StatusConflict {
		t.Fatalf("expected 409 for the deploy token (owner outstanding), got %d: %s", code, body)
	}
	// Lane token: blocked only on ITS check while unreported.
	if code, body := postLandAs(laneToken); code != http.StatusConflict || !strings.Contains(body, "manifest-lint") {
		t.Fatalf("expected 409 naming manifest-lint for the lane, got %d: %s", code, body)
	}

	checkBody, _ := json.Marshal(map[string]string{
		"name": "manifest-lint", "external_id": "job-1", "status": "completed", "conclusion": "success", "reporter": "ci",
	})
	checkReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+changeID+"/checks", bytes.NewReader(checkBody))
	checkReq.Header.Set("Authorization", "Bearer "+token)
	checkReq.Header.Set("Content-Type", "application/json")
	checkResp, err := client.Do(checkReq)
	if err != nil {
		t.Fatalf("POST checks: %v", err)
	}
	checkResp.Body.Close()

	code, body := postLandAs(laneToken)
	if code != http.StatusOK {
		t.Fatalf("expected 200 lane land with manifest-lint green and NO approval posted, got %d: %s", code, body)
	}
	var landOutcome struct {
		Landed    bool
		LandedSHA string
	}
	if err := json.Unmarshal([]byte(body), &landOutcome); err != nil {
		t.Fatalf("decode land response: %v", err)
	}
	lsRemote, err := runGit(t, work, "ls-remote", remoteURL, "refs/heads/main")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if !strings.Contains(lsRemote, landOutcome.LandedSHA) {
		t.Fatalf("expected refs/heads/main at %s, got:\n%s", landOutcome.LandedSHA, lsRemote)
	}
}

// TestEndToEndDaemonWorkspaces is the §28.3 stage 12b bar, verbatim: "two
// concurrent workspaces, one user, different projects; delete the local
// directory → attach restores from the snapshot ref, nothing lost; §3.3's
// 'editable workspace < 60s' measured." Real compiled daemon (pre-receive
// hook installed, so snapshot pushes traverse the real funnel) driven by
// the real compiled runko binary - the CLI a user actually runs.
func TestEndToEndDaemonWorkspaces(t *testing.T) {
	runkodBin := buildRunkod(t)
	runkoBin := buildRunko(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	baseURL := startDaemon(t, runkodBin, repoDir, token)
	remoteURL := strings.Replace(baseURL, "http://", "http://runko:"+token+"@", 1) + "/" + filepath.Base(repoDir) + "/"

	runko := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(runkoBin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	mustRunko := func(dir string, args ...string) string {
		t.Helper()
		out, err := runko(dir, args...)
		if err != nil {
			t.Fatalf("runko %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	// Seed trunk through the only write path a fresh daemon has: push a
	// Change carrying two projects, land it (eval profile).
	work := t.TempDir()
	if _, err := runGit(t, work, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, p := range [][2]string{
		{"commerce/checkout", "checkout-api"},
		{"libs/money", "money-lib"},
	} {
		if err := os.MkdirAll(filepath.Join(work, p[0]), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		manifest := "schema: project/v1\nname: " + p[1] + "\ntype: service\n"
		if err := os.WriteFile(filepath.Join(work, p[0], "PROJECT.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write PROJECT.yaml: %v", err)
		}
	}
	if _, err := runGit(t, work, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(t, work, "commit", "-q", "-m", "two projects"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(t, work, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	out, err := runGit(t, work, "push", "origin", "+HEAD:refs/for/main")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(I[0-9a-f]{40}) -> refs/for/main`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no Change-Id in push output:\n%s", out)
	}
	landReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/changes/"+m[1]+"/land", nil)
	landReq.Header.Set("Authorization", "Bearer "+token)
	landResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(landReq)
	if err != nil || landResp.StatusCode != http.StatusOK {
		body := ""
		if landResp != nil {
			b, _ := io.ReadAll(landResp.Body)
			body = string(b)
		}
		t.Fatalf("seed land failed: %v %s", err, body)
	}
	landResp.Body.Close()

	// The measured §3.3 bar starts here: registry row -> blobless clone ->
	// worktree -> sparse cone -> editable file on disk.
	root := t.TempDir()
	cloneDir := filepath.Join(root, "mono")
	ws1 := filepath.Join(root, "payments-fix")
	editableStart := time.Now()
	mustRunko(root, "workspace", "create", "--runkod-url", baseURL, "--token", token,
		"--name", "payments-fix", "--by", "alice", "--project", "checkout-api",
		"--clone-dir", cloneDir, "--dir", ws1)
	if _, err := os.Stat(filepath.Join(ws1, "commerce/checkout/PROJECT.yaml")); err != nil {
		t.Fatalf("expected the cone materialized and editable: %v", err)
	}
	editable := time.Since(editableStart)
	t.Logf("editable workspace in %s (§3.3 bar: < 60s)", editable)
	if editable >= 60*time.Second {
		t.Fatalf("§3.3 bar missed: editable workspace took %s", editable)
	}

	// Second concurrent workspace, same user, different project, same
	// shared object store.
	ws2 := filepath.Join(root, "risk-refactor")
	mustRunko(root, "workspace", "create", "--runkod-url", baseURL, "--token", token,
		"--name", "risk-refactor", "--by", "alice", "--project", "money-lib",
		"--clone-dir", cloneDir, "--dir", ws2)
	if _, err := os.Stat(filepath.Join(ws2, "libs/money/PROJECT.yaml")); err != nil {
		t.Fatalf("ws2 cone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws1, "libs/money")); !os.IsNotExist(err) {
		t.Fatalf("ws1 must not materialize ws2's cone")
	}
	listOut := mustRunko(root, "workspace", "list", "--runkod-url", baseURL, "--token", token)
	if !strings.Contains(listOut, "payments-fix") || !strings.Contains(listOut, "risk-refactor") {
		t.Fatalf("workspace list should show both workstreams, got:\n%s", listOut)
	}

	// WIP in ws1 -> snapshot -> through the REAL pre-receive hook to a
	// durable ref on the served repo.
	if err := os.WriteFile(filepath.Join(ws1, "commerce/checkout/wip.go"), []byte("package main // precious WIP\n"), 0o644); err != nil {
		t.Fatalf("write wip: %v", err)
	}
	mustRunko(ws1, "workspace", "snapshot", "--dir", ws1)
	lsRemote, err := runGit(t, work, "ls-remote", remoteURL, "refs/workspaces/payments-fix/head")
	if err != nil || !strings.Contains(lsRemote, "refs/workspaces/payments-fix/head") {
		t.Fatalf("expected the snapshot ref on the served repo, got %q (err %v)", lsRemote, err)
	}

	// The funnel's registry check, over the wire: a snapshot ref for a
	// workspace nobody registered is rejected by the real hook.
	out, err = runGit(t, work, "push", "origin", "+HEAD:refs/workspaces/ghost/head")
	if err == nil {
		t.Fatalf("expected an unregistered workspace snapshot push to be rejected, got:\n%s", out)
	}
	if !strings.Contains(out, "ghost") || !strings.Contains(out, "workspace create") {
		t.Fatalf("expected the rejection to name the workspace and the fix, got:\n%s", out)
	}

	// Laptop loss: delete the whole worktree, attach restores the WIP from
	// the snapshot ref. Nothing durable lived in the deleted directory.
	if err := os.RemoveAll(ws1); err != nil {
		t.Fatalf("remove ws1: %v", err)
	}
	restored := filepath.Join(root, "payments-fix-restored")
	mustRunko(root, "workspace", "attach", "--runkod-url", baseURL, "--token", token,
		"--clone-dir", cloneDir, "--dir", restored, "payments-fix")
	content, err := os.ReadFile(filepath.Join(restored, "commerce/checkout/wip.go"))
	if err != nil {
		t.Fatalf("expected the WIP restored from the snapshot ref: %v", err)
	}
	if !strings.Contains(string(content), "precious WIP") {
		t.Fatalf("restored WIP content mismatch: %q", content)
	}
}

// TestEndToEndDaemonSearchNotConfigured proves the real compiled daemon,
// started with no --search-url (the default in every other test in this
// file), answers GET /api/search with the structured §6.5 "not configured"
// error over real HTTP - not a git-grep fallback, per §8.2.
func TestEndToEndDaemonSearchNotConfigured(t *testing.T) {
	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	baseURL := startDaemon(t, bin, repoDir, token)

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/search?q=foo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with no --search-url, got %d", resp.StatusCode)
	}
	var body struct{ Code string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "search_not_configured" {
		t.Fatalf("expected code search_not_configured, got %+v", body)
	}
}

// TestEndToEndDaemonSearchProxiesToZoektWebserver starts the real compiled
// daemon with --search-url pointed at an httptest.Server standing in for a
// real zoekt-webserver (the real binary isn't installed in this sandbox -
// see search/doc.go; the wire format itself is covered by search/zoekt_test.go
// against the exact JSON shape read from zoekt's own source). This proves
// the --search-url flag actually reaches ZoektSearcher and the REST layer
// round-trips a real HTTP response through search_code end to end.
func TestEndToEndDaemonSearchProxiesToZoektWebserver(t *testing.T) {
	zoekt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"FileMatches":[{"FileName":"a.go","Matches":[{"LineNum":1,"Fragments":[{"Pre":"","Match":"foo","Post":""}]}]}]}}`))
	}))
	defer zoekt.Close()

	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"
	baseURL := startDaemon(t, bin, repoDir, token, "--search-url", zoekt.URL)

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/search?q=foo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with --search-url configured, got %d", resp.StatusCode)
	}
	var result struct {
		Hits []struct{ Path string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Path != "a.go" {
		t.Fatalf("expected the zoekt-webserver stub's hit to round-trip, got %+v", result.Hits)
	}
}

// TestEndToEndDaemonPersistsAcrossRestartWithPostgres closes the loop on a
// real gap found in review: serve previously constructed an in-memory Store
// unconditionally, so a fully-tested PostgresStore sat unused while the
// daemon forgot every Change on restart. This test proves --database-url
// actually survives a restart: create a Change against one daemon process,
// kill it, start a SECOND daemon process pointed at the same repo AND the
// same database, and confirm the Change is still there - not by inspecting
// Postgres directly, but by asking the new process's REST API, exactly as
// a real client would after a daemon redeploy.
//
// Skips unless RUNKO_TEST_DATABASE_URL is set (see internal/dbtest,
// db/README.md) - no Postgres in this sandbox; verified for real via
// `make check-db` in CI (docs/design.md §28.3 stage 9d).
func TestEndToEndDaemonPersistsAcrossRestartWithPostgres(t *testing.T) {
	dbtest.Connect(t) // resets the schema; skips this test if RUNKO_TEST_DATABASE_URL is unset
	dsn := os.Getenv("RUNKO_TEST_DATABASE_URL")

	bin := buildRunkod(t)
	repoDir := filepath.Join(t.TempDir(), "monorepo.git")
	token := "sekret-token"

	baseURL1, stop1 := startDaemonProcess(t, bin, repoDir, token, "--database-url", dsn)

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
	remoteURL1 := strings.Replace(baseURL1, "http://", "http://runko:"+token+"@", 1) + "/" + filepath.Base(repoDir) + "/"
	if _, err := runGit(t, work, "remote", "add", "origin", remoteURL1); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	out, err := runGit(t, work, "push", "origin", "+HEAD:refs/for/main")
	if err != nil {
		t.Fatalf("push to refs/for/main: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(I[0-9a-f]{40}) -> refs/for/main`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected a Change-Id in the push output, got:\n%s", out)
	}
	changeID := m[1]

	// Kill the first daemon entirely - the whole point is that nothing
	// in-process survives; only Postgres does.
	stop1()

	baseURL2 := startDaemon(t, bin, repoDir, token, "--database-url", dsn)
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, baseURL2+"/api/changes/"+changeID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET change from the restarted daemon: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the Change to survive the restart via Postgres, got status %d", resp.StatusCode)
	}
}
