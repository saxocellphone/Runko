package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

// TestRunDoctorOnNonRepoDirReturnsStructuredError closes a raw-passthrough
// gap found in the CLI robustness audit (§6.5): `doctor` against a
// directory that isn't a git repo at all used to surface git's raw
// `rev-parse --git-dir` exit-128 text, the same class of bug stage 9a
// already fixed for `project create` (cmd/runko/project.go).
func TestRunDoctorOnNonRepoDirReturnsStructuredError(t *testing.T) {
	dir := t.TempDir() // not a git repo at all

	_, err := RunDoctor(dir, "main")
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

func TestRunDoctorNoRemoteNoHook(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if report.HasRemote {
		t.Fatalf("expected no remote configured, got %+v", report)
	}
	if report.HasChangeIDHook {
		t.Fatalf("expected no commit-msg hook installed yet")
	}
}

func TestRunDoctorDetectsRemote(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")
	repo.Run("remote add origin https://example.com/monorepo.git")

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if !report.HasRemote || report.RemoteName != "origin" || report.RemoteURL != "https://example.com/monorepo.git" {
		t.Fatalf("unexpected remote detection: %+v", report)
	}
}

func TestInstallChangeIDHookIsDetected(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	if err := InstallChangeIDHook(repo.Dir); err != nil {
		t.Fatalf("InstallChangeIDHook: %v", err)
	}

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if !report.HasChangeIDHook {
		t.Fatalf("expected the installed hook to be detected")
	}
}

func TestInstalledHookActuallyAddsChangeID(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	if err := InstallChangeIDHook(repo.Dir); err != nil {
		t.Fatalf("InstallChangeIDHook: %v", err)
	}

	repo.WriteFile("a.txt", "content\n")
	repo.Commit("a commit with no trailer")

	msg, err := runGit(repo.Dir, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatalf("read commit message: %v", err)
	}
	if !strings.Contains(msg, "Change-Id: I") {
		t.Fatalf("expected the installed hook to append a Change-Id trailer, got message:\n%s", msg)
	}
}

func TestRunDoctorDetectsGitVersion(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if report.GitVersion == "" {
		t.Fatalf("expected a detected git version, got %+v", report)
	}
	if report.GitVersionError != "" {
		t.Fatalf("did not expect a git version detection error in this environment: %+v", report)
	}
}

func TestPrintCheatSheetWarnsOnOldGit(t *testing.T) {
	var buf strings.Builder
	PrintCheatSheet(&buf, DoctorReport{TrunkRef: "main", GitVersion: "2.30.0", GitVersionOK: false})
	out := buf.String()
	if !strings.Contains(out, "too old") {
		t.Fatalf("expected cheat sheet to warn about an old git version, got:\n%s", out)
	}
}

func TestPrintCheatSheetMentionsTrunkRef(t *testing.T) {
	var buf strings.Builder
	PrintCheatSheet(&buf, DoctorReport{TrunkRef: "main", HasRemote: true, RemoteName: "origin", RemoteURL: "https://x"})
	out := buf.String()
	if !strings.Contains(out, "refs/for/main") {
		t.Fatalf("expected cheat sheet to mention refs/for/main, got:\n%s", out)
	}
	if !strings.Contains(out, "runko change push") {
		t.Fatalf("expected cheat sheet to mention `runko change push`, got:\n%s", out)
	}
}
