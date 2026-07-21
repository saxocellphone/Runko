package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRepo builds a bare "deploy repo" plus a working clone with one
// commit on branch, the shape pushBump runs against.
func seedRepo(t *testing.T) (d *deployer, remote, work string) {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	remote = filepath.Join(root, "deploy.git")
	work = filepath.Join(root, "work")
	d = &deployer{repoURL: remote, branch: "main"}
	mustGit(t, d, "", "init", "--bare", "--initial-branch=main", remote)
	mustGit(t, d, "", "clone", remote, work)
	writeCommit(t, d, work, "kustomization.yaml", "seed\n", "seed")
	if err := d.git(ctx, work, "push", "origin", "HEAD:main"); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	return d, remote, work
}

func mustGit(t *testing.T, d *deployer, dir string, args ...string) {
	t.Helper()
	if err := d.git(t.Context(), dir, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}

func writeCommit(t *testing.T, d *deployer, dir, file, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, d, dir, "add", file)
	mustGit(t, d, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-m", msg)
}

// TestPushBumpRebasesPastAConcurrentPush: the deploy repo moved between
// our clone and our push - the one failure a retry actually fixes.
func TestPushBumpRebasesPastAConcurrentPush(t *testing.T) {
	ctx := t.Context()
	d, remote, work := seedRepo(t)

	// Someone else lands in the deploy repo first.
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, d, "", "clone", remote, other)
	writeCommit(t, d, other, "ingress.yaml", "someone else\n", "human edit")
	mustGit(t, d, other, "push", "origin", "HEAD:main")

	// Our pin, written against the pre-move clone, still lands.
	writeCommit(t, d, work, "kustomization.yaml", "our pin\n", "image bump")
	if err := d.pushBump(ctx, work); err != nil {
		t.Fatalf("a moved remote must be rebased past, got: %v", err)
	}
	log, err := d.gitOut(ctx, work, "log", "--oneline", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "image bump") || !strings.Contains(log, "human edit") {
		t.Fatalf("both commits must survive the rebase:\n%s", log)
	}
}

// TestPushBumpReportsWhatGitSaid is the regression this file exists for.
// Prod's deploy repo grew a branch ruleset that admits only bypass
// actors; the deployer answered "could not push the bump after 3
// attempts" for four dead-lettered deliveries and threw git's actual
// refusal away, so the cause had to be found through the GitHub API
// (2026-07-21). A non-race rejection must now name itself, once.
func TestPushBumpReportsWhatGitSaid(t *testing.T) {
	ctx := t.Context()
	d, _, work := seedRepo(t)
	// A non-bare remote refuses a push to the branch it has checked out
	// - a stand-in for any policy rejection: deterministic, and no retry
	// can help.
	protected := filepath.Join(t.TempDir(), "protected")
	mustGit(t, d, "", "clone", work, protected)
	mustGit(t, d, work, "remote", "set-url", "origin", protected)
	writeCommit(t, d, work, "kustomization.yaml", "our pin\n", "image bump")

	err := d.pushBump(ctx, work)
	if err == nil {
		t.Fatal("pushing to a checked-out branch must fail")
	}
	if !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("git's own reason must reach the caller, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not by a race") {
		t.Fatalf("a deterministic rejection must say retrying cannot help, got: %v", err)
	}
	if strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("a deterministic rejection must not be retried at all, got: %v", err)
	}
}

func TestRacyPushRejection(t *testing.T) {
	for _, s := range []string{
		"git push origin HEAD:main: exit status 1: ! [rejected] main -> main (fetch first)",
		"! [rejected] main -> main (non-fast-forward)",
		"Updates were rejected because the remote contains work that you do not have locally",
	} {
		if !racyPushRejection(errString(s)) {
			t.Errorf("must be treated as a race: %q", s)
		}
	}
	for _, s := range []string{
		"refusing to allow an OAuth App to create or update workflow",
		"! [remote rejected] main -> main (protected branch hook declined)",
		"remote: Permission to acme/gitops.git denied",
	} {
		if racyPushRejection(errString(s)) {
			t.Errorf("must NOT be retried: %q", s)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestSetPushAuthRedactsTheTokenOnFailure: the authenticated URL is the
// one credential that rides argv, and git() quotes argv back on failure.
func TestSetPushAuthRedactsTheTokenOnFailure(t *testing.T) {
	d := &deployer{
		repoURL: "https://github.com/acme/gitops.git",
		tokenFn: func() (string, error) { return "ghs_supersecret", nil },
	}
	// No repo at this path, so `git remote set-url` fails.
	err := d.setPushAuth(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("set-url in a non-repo must fail")
	}
	if strings.Contains(err.Error(), "ghs_supersecret") {
		t.Fatalf("the token must never reach an error string: %v", err)
	}
	if !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("want the redaction marker, got: %v", err)
	}
}
