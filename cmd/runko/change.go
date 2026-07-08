package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/receive"
)

// warnWriter receives non-fatal CLI warnings (stderr in production; tests
// swap it to capture output). Warnings never touch stdout - that belongs
// to --json (docs/cli-contract.md).
var warnWriter io.Writer = os.Stderr

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

	// Footgun guard (2026-07-08 dogfood review): trunk commits keep their
	// landed Change-Id trailer, so `runko change push` from a clean trunk
	// tip - no new commit at all - used to "succeed" by re-pushing the
	// landed commit. The daemon rejects landed Change-Ids at receive now,
	// but the honest answer is available before any push: if HEAD is
	// already reachable from the remote trunk, there is nothing to submit.
	// An unreachable remote skips the guard - the push itself will surface
	// the real transport error.
	if tip, err := lsRemoteTrunk(repoDir, remote, trunk); err == nil && tip != "" {
		onTrunk := tip == headSHA
		if !onTrunk {
			// merge-base needs the tip object locally; a stale clone that
			// hasn't fetched it simply skips this half of the guard.
			if _, err := runGit(repoDir, "merge-base", "--is-ancestor", headSHA, tip); err == nil {
				onTrunk = true
			}
		}
		if onTrunk {
			return "", &clierr.Error{
				Code:       "already_on_trunk",
				Field:      "repo",
				Message:    fmt.Sprintf("HEAD (%.12s) is already on %s's trunk - there is no new commit to submit", headSHA, remote),
				Suggestion: "make a commit first (`runko change create -m ...` or plain git commit)",
				DocURL:     "docs/change-lifecycle.md",
			}
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

	// §12.2 provenance: a worktree attached via `runko workspace
	// create/attach` carries runko.workspace/runko.branch in its git
	// config - stamp them onto the push as push options so the funnel can
	// record which workspace branch this Change (and so its stack) lives
	// on. Plain clones have neither key and push exactly as before.
	args := []string{"push"}
	if ws, _ := runGit(repoDir, "config", "runko.workspace"); ws != "" {
		// Config-split warning (2026-07-08 dogfood review): in an attached
		// worktree the key lives in WORKTREE config (workspace attach uses
		// `git config --worktree`), which outranks --local on read - so a
		// plain `git config runko.workspace <x>` writes a value that LOOKS
		// set (`--list --local` shows it) but never wins. Say so instead of
		// letting the push silently use the other value.
		if local, _ := runGit(repoDir, "config", "--local", "runko.workspace"); local != "" && local != ws {
			fmt.Fprintf(warnWriter,
				"warning: runko.workspace is %q in local git config but %q in this worktree's config - the worktree value wins\n"+
					"         (use `git config --worktree runko.workspace ...` to change it, or `git config --unset runko.workspace` to drop the shadowed local value)\n",
				local, ws)
		}
		args = append(args, "--push-option=workspace="+ws)
		branch, _ := runGit(repoDir, "config", "runko.branch")
		if branch == "" {
			branch = "head"
		}
		args = append(args, "--push-option=workspace-branch="+branch)
	}
	args = append(args, remote, "+HEAD:refs/for/"+trunk)
	if _, err := runGit(repoDir, args...); err != nil {
		return "", fmt.Errorf("push to refs/for/%s: %w", trunk, err)
	}
	return id, nil
}

// lsRemoteTrunk asks the remote for its trunk tip ("" when the remote has
// no such ref yet - an unborn trunk).
func lsRemoteTrunk(repoDir, remote, trunk string) (string, error) {
	out, err := runGit(repoDir, "ls-remote", remote, "refs/heads/"+trunk)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}
