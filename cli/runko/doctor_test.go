package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// TestRunDoctorOnNonRepoDirReturnsStructuredError closes a raw-passthrough
// gap found in the CLI robustness audit (§6.5): `doctor` against a
// directory that isn't a git repo at all used to surface git's raw
// `rev-parse --git-dir` exit-128 text, the same class of bug stage 9a
// already fixed for `project create` (cli/runko/project.go).
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

// TestInstalledHookGeneratesDistinctChangeIDs pins the 2026-07-08 dogfood
// finding: the hook's seed was `git var GIT_COMMITTER_IDENT` alone, so
// plain commits from one identity (within the ident's one-second timestamp
// resolution) all got the SAME Change-Id - distinct work collapsed into one
// Change on push. The seed now mixes tree/parents/idents/message/random
// bytes, so even byte-identical circumstances must yield distinct ids.
func TestInstalledHookGeneratesDistinctChangeIDs(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")
	if err := InstallChangeIDHook(repo.Dir); err != nil {
		t.Fatalf("InstallChangeIDHook: %v", err)
	}

	seen := map[string]bool{}
	for i, name := range []string{"a.txt", "b.txt", "c.txt"} {
		repo.WriteFile(name, "content\n")
		// Same message every time - the worst case for a weak seed.
		repo.Commit("do the thing")
		msg, err := runGit(repo.Dir, "log", "-1", "--format=%B")
		if err != nil {
			t.Fatalf("read commit message: %v", err)
		}
		id, ok := receive.ParseChangeID(msg)
		if !ok {
			t.Fatalf("commit %d: no valid Change-Id trailer in:\n%s", i, msg)
		}
		if seen[id] {
			t.Fatalf("commit %d: Change-Id %s repeats an earlier commit's id", i, id)
		}
		seen[id] = true
	}
}

// TestRunDoctorRedactsRemoteURLPassword: remote URLs here embed the
// smart-HTTP credential (user:pass@host is the documented transport form),
// and doctor printed it verbatim - in --json too.
func TestRunDoctorRedactsRemoteURLPassword(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")
	repo.Run("remote add origin https://saxo:hunter2@example.com/monorepo.git")

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if strings.Contains(report.RemoteURL, "hunter2") {
		t.Fatalf("remote URL leaks the password: %q", report.RemoteURL)
	}
	if report.RemoteURL != "https://saxo:***@example.com/monorepo.git" {
		t.Fatalf("unexpected redacted form: %q", report.RemoteURL)
	}
}
