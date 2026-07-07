package main

import (
	"fmt"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/receive"
)

// PushChange implements `runko change push` (§11.5, §17.1): ensure HEAD's
// commit message carries a Change-Id trailer (amending if it doesn't), then
// push it to refs/for/<trunk> on remote - the same magic-ref path any plain
// git client can use (§6.9's "parity rule").
//
// Forced (+HEAD:refs/for/<trunk>): unlike real Gerrit, whose customized
// receive-pack redirects a magic-ref push to a per-Change ref server-side,
// runkod (§28.3 stage 10) keeps refs/for/<trunk> as a literal, repeatedly-
// overwritten ref - the simpler design vanilla git (no Git-in-Go, §28.2
// rule 4) allows without reimplementing Gerrit's ref-rewriting. That means
// amending and re-pushing the same Change is a non-fast-forward update to
// that literal ref, which a plain push refuses; force is exactly correct
// here since the ref is meant to always reflect the Change's latest commit,
// never a history to preserve.
func PushChange(repoDir, remote, trunk string) (changeID string, err error) {
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code:       "not_a_repo",
			Field:      "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run `git init` first, then retry `runko change push`",
			DocURL:     "docs/design.md#67-empty-states-and-education",
		}
	}
	if _, err := runGit(repoDir, "symbolic-ref", "-q", "HEAD"); err != nil {
		return "", &clierr.Error{
			Code:       "detached_head",
			Field:      "repo",
			Message:    "HEAD is not on a branch (detached HEAD)",
			Suggestion: "check out a branch first, e.g. `git checkout -b my-branch`",
			DocURL:     "docs/design.md#69-the-closed-trunk-moment-human-git-ux",
		}
	}

	headSHA, err := runGit(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", &clierr.Error{
			Code:       "no_commits",
			Field:      "repo",
			Message:    "HEAD has no commits yet - nothing to push",
			Suggestion: "run `runko project create` or make a commit first",
			DocURL:     "docs/design.md#67-empty-states-and-education",
		}
	}

	msg, err := runGit(repoDir, "log", "-1", "--format=%B")
	if err != nil {
		return "", fmt.Errorf("read HEAD commit message: %w", err)
	}

	id, newMsg := receive.EnsureChangeID(msg, headSHA)
	if newMsg != msg {
		if _, err := runGit(repoDir, "commit", "--amend", "-m", newMsg); err != nil {
			return "", fmt.Errorf("amend commit with Change-Id trailer: %w", err)
		}
	}

	if _, err := runGit(repoDir, "push", remote, "+HEAD:refs/for/"+trunk); err != nil {
		return "", fmt.Errorf("push to refs/for/%s: %w", trunk, err)
	}
	return id, nil
}
