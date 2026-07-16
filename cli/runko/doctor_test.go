package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// rawGitCommit stages everything and commits the way a human or agent
// would type it - a direct git subprocess, NOT runGit (which marks itself
// RUNKO_INTERNAL_GIT=1) - returning the commit's stderr, where hooks speak.
func rawGitCommit(t *testing.T, dir, msg string, extraEnv []string) string {
	t.Helper()
	if _, err := runGit(dir, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("raw git commit: %v: %s", err, stderr.String())
	}
	return stderr.String()
}

// TestVerbNudgeHookFiresOnRawGitCommitOnly is the fresh-agent moment the
// nudge exists for (§6.9's rejection UX one moment earlier): a raw
// `git commit` still succeeds - plain git is a contract, not a fallback -
// AND prints the native verbs to stderr; the same commit run the way
// runko's own verbs run git (RUNKO_INTERNAL_GIT=1) stays silent, so
// `change create`/`workspace snapshot` never nudge about themselves.
func TestVerbNudgeHookFiresOnRawGitCommitOnly(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")
	if installed, err := InstallVerbNudgeHook(repo.Dir); err != nil || !installed {
		t.Fatalf("InstallVerbNudgeHook: installed=%v err=%v", installed, err)
	}

	repo.WriteFile("a.txt", "content\n")
	stderr := rawGitCommit(t, repo.Dir, "raw commit", nil)
	if !strings.Contains(stderr, "runko change create") {
		t.Fatalf("expected the verb nudge on a raw git commit, stderr:\n%s", stderr)
	}

	repo.WriteFile("b.txt", "content\n")
	stderr = rawGitCommit(t, repo.Dir, "runko-verb commit", []string{"RUNKO_INTERNAL_GIT=1"})
	if strings.Contains(stderr, "runko change create") {
		t.Fatalf("expected silence when git runs under a runko verb, stderr:\n%s", stderr)
	}
}

// TestVerbNudgeHookTeachesRunkoEvenInJJColocatedCheckouts: the runko CLI
// is the primary interface for basic operations EVERYWHERE (§21,
// repositioned 2026-07-11) - a jj colocated checkout (a .jj dir at the
// top level) gets the same runko verbs, plus a note that jj is the
// surgical tool there. jj itself runs no git hooks, so only a raw git
// commit ever sees any of this.
func TestVerbNudgeHookTeachesRunkoEvenInJJColocatedCheckouts(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")
	if err := os.MkdirAll(filepath.Join(repo.Dir, ".jj"), 0o755); err != nil {
		t.Fatalf("mkdir .jj: %v", err)
	}
	if installed, err := InstallVerbNudgeHook(repo.Dir); err != nil || !installed {
		t.Fatalf("InstallVerbNudgeHook: installed=%v err=%v", installed, err)
	}

	repo.WriteFile("a.txt", "content\n")
	stderr := rawGitCommit(t, repo.Dir, "raw commit in a colocated checkout", nil)
	if !strings.Contains(stderr, "runko change create") {
		t.Fatalf("expected the runko verbs even in a colocated checkout, stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "surgery") {
		t.Fatalf("expected the jj-is-surgical note in a colocated checkout, stderr:\n%s", stderr)
	}
	if strings.Contains(stderr, "jj commit") {
		t.Fatalf("the nudge must not teach jj commit as a basic verb, stderr:\n%s", stderr)
	}
}

// TestVerbNudgeHookRefusesToClobberForeignPreCommit: the nudge is advisory
// sugar - a pre-commit hook someone actually wrote always wins, and the
// report must not claim the nudge is installed.
func TestVerbNudgeHookRefusesToClobberForeignPreCommit(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	hooksDir, err := hooksDirectory(repo.Dir)
	if err != nil {
		t.Fatalf("hooksDirectory: %v", err)
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	foreign := "#!/bin/sh\n# somebody's lint gate\nexit 0\n"
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	installed, err := InstallVerbNudgeHook(repo.Dir)
	if err != nil {
		t.Fatalf("InstallVerbNudgeHook: %v", err)
	}
	if installed {
		t.Fatalf("expected the foreign pre-commit hook to be left alone")
	}
	content, err := os.ReadFile(hookPath)
	if err != nil || string(content) != foreign {
		t.Fatalf("foreign hook was clobbered: %q err=%v", content, err)
	}

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if report.HasVerbNudgeHook {
		t.Fatalf("report claims the nudge is installed over a foreign hook: %+v", report)
	}
}

// TestInstallVerbNudgeHookIsDetectedAndIdempotent: doctor reports it, and
// re-installing over our own hook is fine (picks up new wording).
func TestInstallVerbNudgeHookIsDetectedAndIdempotent(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	for i := 0; i < 2; i++ {
		if installed, err := InstallVerbNudgeHook(repo.Dir); err != nil || !installed {
			t.Fatalf("install %d: installed=%v err=%v", i, installed, err)
		}
	}
	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if !report.HasVerbNudgeHook {
		t.Fatalf("expected HasVerbNudgeHook after install: %+v", report)
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

// TestInstallChangeIDHookIfAbsentNeverTouchesAnExistingHook: the implicit
// installer (workspace materialization, §6.10) writes into a bare hooks
// dir, but an EXISTING commit-msg hook - foreign (Gerrit's own also mints
// Change-Ids) or ours - is never rewritten by an implicit path.
func TestInstallChangeIDHookIfAbsentNeverTouchesAnExistingHook(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	installed, err := InstallChangeIDHookIfAbsent(repo.Dir)
	if err != nil || !installed {
		t.Fatalf("fresh repo: installed=%v err=%v", installed, err)
	}
	report, err := RunDoctor(repo.Dir, "main")
	if err != nil || !report.HasChangeIDHook {
		t.Fatalf("expected the hook detected after install: %+v err=%v", report, err)
	}

	hooksDir, err := hooksDirectory(repo.Dir)
	if err != nil {
		t.Fatalf("hooksDirectory: %v", err)
	}
	hookPath := filepath.Join(hooksDir, "commit-msg")
	foreign := "#!/bin/sh\n# gerrit's own change-id hook\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}
	installed, err = InstallChangeIDHookIfAbsent(repo.Dir)
	if err != nil || installed {
		t.Fatalf("existing hook must be left alone: installed=%v err=%v", installed, err)
	}
	if content, _ := os.ReadFile(hookPath); string(content) != foreign {
		t.Fatalf("existing commit-msg hook was rewritten: %q", content)
	}
}

// TestWorkspaceHooksWireTheWholeRawGitLoop: materializing a workspace
// (installWorkspaceHooks runs on every create/attach) leaves the shared
// clone with BOTH client hooks - the §6.9 verb nudge and the Change-Id
// trailer - so a raw `git commit` in any worktree is nudged AND pushable
// without a separate `runko doctor --install-hook` onboarding step.
func TestWorkspaceHooksWireTheWholeRawGitLoop(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	if err := installWorkspaceHooks(repo.Dir); err != nil {
		t.Fatalf("installWorkspaceHooks: %v", err)
	}
	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if !report.HasVerbNudgeHook || !report.HasChangeIDHook {
		t.Fatalf("expected both client hooks after materialization, got %+v", report)
	}
}
