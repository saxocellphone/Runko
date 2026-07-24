package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/agentsmd"
)

func TestCmdProjectWrongSubcommandIsUsageError(t *testing.T) {
	err := execCLI("project", "destroy")
	if err == nil {
		t.Fatalf("expected an error for an unrecognized project subcommand")
	}
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected a usageError (exit code 2), got %T: %v", err, err)
	}
}

func TestCmdChangeWrongSubcommandIsUsageError(t *testing.T) {
	err := execCLI("change", "pull")
	if err == nil {
		t.Fatalf("expected an error for an unrecognized change subcommand")
	}
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected a usageError (exit code 2), got %T: %v", err, err)
	}
}

func TestCmdProjectMissingNameIsNotUsageError(t *testing.T) {
	// A recognized subcommand with a missing required flag is a runtime
	// validation failure (exit 1), not a usage error (exit 2) - it parsed
	// fine syntactically. The error keeps the bare first line and adds a
	// next-step suggestion.
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")

	err := execCLI("project", "create", "--repo", repo.Dir)
	if err == nil {
		t.Fatalf("expected an error when --name is omitted")
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatalf("expected a validation error, not a usageError, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "project create: --name is required") {
		t.Fatalf("kept first line, got %q", msg)
	}
	if !strings.Contains(msg, "  -> ") {
		t.Fatalf("expected a suggestion line, got %q", msg)
	}
}

// captureStdout redirects os.Stdout for the duration of fn - needed since
// cmdDoctor/cmdProject/cmdChange's --json path writes straight to
// os.Stdout, matching how a real CLI invocation behaves.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestCmdDoctorJSONOutput(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("doctor", "--repo", repo.Dir, "--json")
	})
	if cmdErr != nil {
		t.Fatalf("cmdDoctor: %v", cmdErr)
	}
	var report DoctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if report.RepoDir != repo.Dir {
		t.Fatalf("expected RepoDir %q, got %+v", repo.Dir, report)
	}
}

func TestCmdProjectJSONOutput(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("project", "create", "--repo", repo.Dir, "--name", "checkout-api", "--type", "service", "--api", "none", "--json")
	})
	if cmdErr != nil {
		t.Fatalf("cmdProject: %v", cmdErr)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if result["name"] != "checkout-api" || result["rev"] == "" {
		t.Fatalf("expected name+rev in JSON output, got %+v", result)
	}
}

func TestCmdChangeJSONOutput(t *testing.T) {
	remote := newBareRemote(t)
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	if _, err := runGit(repo.Dir, "push", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}
	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature")

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("change", "push", "--repo", repo.Dir, "--json")
	})
	if cmdErr != nil {
		t.Fatalf("cmdChange: %v", cmdErr)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if result["change_id"] == "" || result["ref"] != "refs/for/main" {
		t.Fatalf("expected change_id+ref in JSON output, got %+v", result)
	}
}

func TestCmdAgentsMDWritesFile(t *testing.T) {
	dir := t.TempDir()

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("agents-md", "--repo", dir)
	})
	if cmdErr != nil {
		t.Fatalf("cmdAgentsMD: %v", cmdErr)
	}
	if !strings.Contains(out, "generated") {
		t.Fatalf("expected a human summary line, got %q", out)
	}

	content, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("expected AGENTS.md to be written: %v", err)
	}
	if !strings.Contains(string(content), "runko doctor") {
		t.Fatalf("expected the generated command inventory, got:\n%s", content)
	}

	skill, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(agentsmd.SkillPath)))
	if err != nil {
		t.Fatalf("expected the agent skill to be written alongside AGENTS.md: %v", err)
	}
	if !strings.HasPrefix(string(skill), "---\nname: runko\n") {
		t.Fatalf("expected the skill to open with frontmatter, got:\n%.120s", skill)
	}
}

func TestCmdAgentsMDJSONOutput(t *testing.T) {
	dir := t.TempDir()

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("agents-md", "--repo", dir, "--json")
	})
	if cmdErr != nil {
		t.Fatalf("cmdAgentsMD: %v", cmdErr)
	}
	var result struct {
		Path       string   `json:"path"`
		SkillPath  string   `json:"skill_path"`
		SkillPaths []string `json:"skill_paths"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if result.Path != filepath.Join(dir, "AGENTS.md") {
		t.Fatalf("expected path in JSON output, got %+v", result)
	}
	// skill_path stays the reference skill, for callers written against the
	// single-skill shape; skill_paths is every skill actually written.
	if result.SkillPath != filepath.Join(dir, filepath.FromSlash(agentsmd.SkillPath)) {
		t.Fatalf("expected skill_path in JSON output, got %+v", result)
	}
	if len(result.SkillPaths) != len(agentsmd.Skills()) {
		t.Fatalf("expected skill_paths to list every generated skill, got %+v", result.SkillPaths)
	}
	for _, s := range agentsmd.Skills() {
		want := filepath.Join(dir, filepath.FromSlash(s.Path))
		if !slices.Contains(result.SkillPaths, want) {
			t.Fatalf("skill_paths is missing %q, got %+v", want, result.SkillPaths)
		}
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected %s on disk: %v", want, err)
		}
	}
}

func TestCmdAgentsMDCustomOut(t *testing.T) {
	dir := t.TempDir()

	if err := execCLI("agents-md", "--repo", dir, "--out", "TEACHING.md"); err != nil {
		t.Fatalf("cmdAgentsMD: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "TEACHING.md")); err != nil {
		t.Fatalf("expected --out to control the written filename: %v", err)
	}
	// --out moves AGENTS.md only; the skill's location is part of how
	// harnesses discover it and stays fixed relative to --repo.
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(agentsmd.SkillPath))); err != nil {
		t.Fatalf("expected the skill at its fixed path regardless of --out: %v", err)
	}
}

// TestAgentTokenSecret: `workspace create --as X --token` accepts the
// "name:token" form `runko agent create` prints (papercut #1), while a bare
// token and a mismatched name are left untouched.
func TestAgentTokenSecret(t *testing.T) {
	cases := []struct{ as, token, want string }{
		{"agent-x", "agent-x:SECRET", "SECRET"},         // the name:token form agent create prints
		{"agent-x", "SECRET", "SECRET"},                 // bare token unchanged
		{"agent-x", "agent-y:SECRET", "agent-y:SECRET"}, // name mismatch: don't strip, fail honestly
		{"", "SECRET", "SECRET"},
	}
	for _, c := range cases {
		if got := agentTokenSecret(c.as, c.token); got != c.want {
			t.Errorf("agentTokenSecret(%q,%q)=%q want %q", c.as, c.token, got, c.want)
		}
	}
}

// TestOwnerCredentialMismatch: the pre-flight error (papercut #2) names both
// principals and hands back the exact login command for the owner.
func TestOwnerCredentialMismatch(t *testing.T) {
	err := ownerCredentialMismatch("agent-ci-init-aa0a", "admin")
	if err.Code != "workspace_owner_mismatch" {
		t.Fatalf("code = %q", err.Code)
	}
	if !strings.Contains(err.Message, "agent-ci-init-aa0a") || !strings.Contains(err.Message, "admin") {
		t.Errorf("message should name both principals: %q", err.Message)
	}
	if !strings.Contains(err.Suggestion, "auth login --name agent-ci-init-aa0a") {
		t.Errorf("suggestion should give the owner login command: %q", err.Suggestion)
	}
}

// TestCheckPushIdentity: the pre-flight errors only on the silent
// stored-login mismatch, and skips every case where an explicit override
// (RUNKO_TOKEN) or no resolvable credential is in play - never a false
// block of a working setup (Fable r4 B1).
func TestCheckPushIdentity(t *testing.T) {
	repo := gitfixture.New(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_TOKEN", "")

	// No runko.owner: not a workspace-bound worktree -> skip.
	if err := checkPushIdentity(repo.Dir); err != nil {
		t.Fatalf("non-workspace worktree should skip: %v", err)
	}
	if _, err := runGit(repo.Dir, "config", "runko.owner", "agent-a-1111"); err != nil {
		t.Fatal(err)
	}
	// Owner set but nothing resolvable -> skip.
	if err := checkPushIdentity(repo.Dir); err != nil {
		t.Fatalf("no credential should skip: %v", err)
	}
	// A NAMED stored login that isn't the owner: the exact refusal we catch.
	if _, err := saveCredential(Credential{URL: "https://x.example/o/o", Name: "admin", Secret: "deadbeefdeadbeef"}); err != nil {
		t.Fatal(err)
	}
	if err := checkPushIdentity(repo.Dir); err == nil {
		t.Fatal("stored admin != agent-a-1111 should error")
	}
	// RUNKO_TOKEN override in play: the push uses the env token, not the
	// store, so skip (the B1 false-positive this guards against).
	t.Setenv("RUNKO_TOKEN", "agent-a-1111:deadbeef")
	if err := checkPushIdentity(repo.Dir); err != nil {
		t.Fatalf("RUNKO_TOKEN override should skip the stored-login check: %v", err)
	}
	// Stored login IS the owner: no error.
	t.Setenv("RUNKO_TOKEN", "")
	if _, err := saveCredential(Credential{URL: "https://x.example/o/o", Name: "agent-a-1111", Secret: "deadbeefdeadbeef"}); err != nil {
		t.Fatal(err)
	}
	if err := checkPushIdentity(repo.Dir); err != nil {
		t.Fatalf("stored owner login should pass: %v", err)
	}
}
