package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

func TestCmdProjectWrongSubcommandIsUsageError(t *testing.T) {
	err := cmdProject([]string{"delete"})
	if err == nil {
		t.Fatalf("expected an error for an unrecognized project subcommand")
	}
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected a usageError (exit code 2), got %T: %v", err, err)
	}
}

func TestCmdChangeWrongSubcommandIsUsageError(t *testing.T) {
	err := cmdChange([]string{"pull"})
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
	// fine syntactically.
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")

	err := cmdProject([]string{"create", "--repo", repo.Dir})
	if err == nil {
		t.Fatalf("expected an error when --name is omitted")
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatalf("expected a validation error, not a usageError, got %v", err)
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
		cmdErr = cmdDoctor([]string{"--repo", repo.Dir, "--json"})
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
		cmdErr = cmdProject([]string{"create", "--repo", repo.Dir, "--name", "checkout-api", "--type", "service", "--json"})
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
		cmdErr = cmdChange([]string{"push", "--repo", repo.Dir, "--json"})
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
