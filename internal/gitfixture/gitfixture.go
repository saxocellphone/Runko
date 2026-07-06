// Package gitfixture is the terse git-fixture test harness called for by
// docs/design.md §28.2 rule 3: throwaway repos built from short scripts,
// golden-file assertions, and a fake clock + seeded IDs so tests are
// reproducible. Every receive/land/affected/checks test builds its git state
// through this package - never by mocking git (design.md, AGENTS.md).
//
// Shells out to the system git binary (never a Git-in-Go library), matching
// the rule that applies to product code too (§28.2 rule 4).
package gitfixture

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Repo is a throwaway git repository rooted in a t.TempDir(), with a fake
// clock and deterministic identity so its history is reproducible run to run.
type Repo struct {
	t     testing.TB
	Dir   string
	Clock *FakeClock
}

// New creates and initializes a new throwaway repo. It is removed automatically
// when the test finishes (t.TempDir() semantics).
func New(t testing.TB) *Repo {
	t.Helper()
	dir := t.TempDir()
	r := &Repo{t: t, Dir: dir, Clock: NewFakeClock()}
	r.git("init", "-q", "-b", "main")
	return r
}

// git runs a git subcommand in the repo, with global/system config disabled
// and a fixed test identity, so results never depend on the host's git config.
func (r *Repo) git(args ...string) string {
	r.t.Helper()
	base := []string{
		"-c", "user.name=Runko Test",
		"-c", "user.email=test@runko.dev",
		"-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main",
	}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Runko Test", "GIT_AUTHOR_EMAIL=test@runko.dev",
		"GIT_COMMITTER_NAME=Runko Test", "GIT_COMMITTER_EMAIL=test@runko.dev",
		"GIT_AUTHOR_DATE="+r.Clock.Now().Format(time.RFC3339),
		"GIT_COMMITTER_DATE="+r.Clock.Now().Format(time.RFC3339),
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return strings.TrimRight(out.String(), "\n")
}

// Run executes a short fixture script, one git subcommand per line, e.g.:
//
//	repo.Run("add file.txt", "commit -m initial")
//
// Each line is split on whitespace, so arguments containing spaces are not
// supported - use WriteFile + Commit for anything beyond trivial plumbing.
func (r *Repo) Run(script ...string) *Repo {
	r.t.Helper()
	for _, line := range script {
		r.git(strings.Fields(line)...)
	}
	return r
}

// WriteFile writes content to a path relative to the repo root, creating
// parent directories as needed. It does not stage or commit.
func (r *Repo) WriteFile(path, content string) *Repo {
	r.t.Helper()
	full := filepath.Join(r.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("gitfixture: mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("gitfixture: write %s: %v", path, err)
	}
	return r
}

// Commit stages every change and commits it, advancing the fake clock by one
// second first so successive commits get distinct, reproducible timestamps.
// Returns the new commit SHA.
func (r *Repo) Commit(message string) string {
	r.t.Helper()
	r.Clock.Advance(time.Second)
	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)
	return r.Head()
}

// Head returns the current HEAD commit SHA.
func (r *Repo) Head() string {
	r.t.Helper()
	return r.git("rev-parse", "HEAD")
}

// Log returns `git log --oneline` output with commit SHAs replaced by stable
// placeholders (c1 = oldest ... cN = newest), so golden files don't churn on
// every real SHA. Use this instead of raw SHAs in golden-file assertions.
func (r *Repo) Log() string {
	r.t.Helper()
	raw := r.git("log", "--reverse", "--format=%H %s")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		out = append(out, fmt.Sprintf("c%d %s", i+1, parts[1]))
	}
	return strings.Join(out, "\n")
}
