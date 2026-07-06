package main

import (
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

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
