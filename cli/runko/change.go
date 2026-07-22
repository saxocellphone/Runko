package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/receive"
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
	return pushChange(repoDir, remote, trunk, true, true)
}

// pushChange with autoSync=true rebases a stale base onto the remote
// trunk tip BEFORE pushing (2026-07-10, the sync feature): a stale base
// only postpones the same rebase to the §13.5 revalidation loop, with a
// round of checks wasted in between. --no-sync opts out. A CONFLICTING
// sync no longer blocks the submit (2026-07-17): conflicts gate LANDING
// (the land engine refuses a conflicting rebase server-side), not review
// or CI - so the checkout is restored, a warning names the files, and
// the stale base is pushed.
//
// autoSnapshot=true is §12.6's promised auto-snapshot on change push: a
// workspace-bound checkout parks its snapshot ref at the submitted state
// (out-of-band, watch.go's mechanics) right before pushing. Best-effort
// by contract - a snapshot failure warns and never blocks the submit;
// --no-snapshot opts out. Land's internal revalidation re-push passes
// false: it re-submits server-known state, there is nothing new to save.
func pushChange(repoDir, remote, trunk string, autoSync, autoSnapshot bool) (changeID string, err error) {
	if autoSync && staleBase(repoDir, remote, trunk) {
		if _, err := syncToTrunk(repoDir, remote, trunk, true); err != nil {
			var ce *clierr.Error
			if !errors.As(err, &ce) || ce.Code != "sync_conflict" {
				return "", err
			}
			// syncToTrunk(restoreOnConflict) has put the checkout back to
			// its pre-sync state, so the stale base is intact and safe to
			// submit - review and checks start now instead of after the
			// resolution.
			fmt.Fprintf(warnWriter,
				"warning: %s\n         pushing the stale base anyway - the change can collect review and checks but will NOT land until the conflict is resolved (%s)\n",
				ce.Message, ce.Suggestion)
		}
	}
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code:       "not_a_repo",
			Field:      "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run `git init` (or `jj git init --colocate`) first, then retry `runko change push`",
		}
	}
	// jj mode (§7.4, jj.go): the tip is resolved from jj's working copy,
	// not git HEAD - a colocated jj repo keeps git HEAD detached by
	// design, so the detached-HEAD guard is a plain-git concern only.
	jj := isJJWorkspace(repoDir)
	var headSHA string
	if jj {
		var err error
		headSHA, err = jjTipCommit(repoDir)
		if err != nil {
			return "", err
		}
	} else {
		if _, err := runGit(repoDir, "symbolic-ref", "-q", "HEAD"); err != nil {
			return "", &clierr.Error{
				Code:       "detached_head",
				Field:      "repo",
				Message:    "HEAD is not on a branch (detached HEAD)",
				Suggestion: "check out a branch first, e.g. `git checkout -b my-branch`",
			}
		}

		var err error
		headSHA, err = runGit(repoDir, "rev-parse", "HEAD")
		if err != nil {
			return "", &clierr.Error{
				Code:       "no_commits",
				Field:      "repo",
				Message:    "HEAD has no commits yet - nothing to push",
				Suggestion: "run `runko project create` or make a commit first",
			}
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
			// The alternative verb matches the checkout: jj commits in a jj
			// workspace, plain git elsewhere.
			commit := "plain git commit"
			if jj {
				commit = "`jj commit`"
			}
			return "", &clierr.Error{
				Code:       "already_on_trunk",
				Field:      "repo",
				Message:    fmt.Sprintf("HEAD (%.12s) is already on %s's trunk - there is no new commit to submit", headSHA, remote),
				Suggestion: "make a commit first (`runko change create -m ...` or " + commit + ")",
				DocURL:     "docs/change-lifecycle.md",
			}
		}
	}

	msg, err := runGit(repoDir, "log", "-1", "--format=%B", headSHA)
	if err != nil {
		return "", fmt.Errorf("read tip commit message: %w", err)
	}

	id, newMsg := receive.EnsureChangeID(msg, headSHA)
	if newMsg != msg {
		if jj {
			// Never amend behind jj's back: identity must come from jj's
			// own change id via the trailer template, or every rewrite
			// would mint a fresh Change (§7.4).
			return "", &clierr.Error{
				Code:       "jj_change_ids_not_configured",
				Field:      "jj",
				Message:    "this jj workspace does not derive Change-Id trailers, so pushed commits have no stable identity",
				Suggestion: "run `runko doctor --install-hook` once in this repo, then `jj describe` (any no-op rewrite) to stamp existing commits",
			}
		}
		if _, err := runGit(repoDir, "commit", "--amend", "-m", newMsg); err != nil {
			return "", fmt.Errorf("amend commit with Change-Id trailer: %w", err)
		}
	}

	if autoSnapshot {
		if ws, _ := runGit(repoDir, "config", "runko.workspace"); ws != "" {
			if _, _, _, serr := WorkspaceWatchSnapshot(repoDir, "auto on change push", ""); serr != nil {
				fmt.Fprintf(warnWriter, "warning: auto-snapshot before push failed (the push continues): %v\n", serr)
			}
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
	source := "HEAD"
	if jj {
		source = headSHA // git HEAD is meaningless in a colocated jj repo
	}
	args = append(args, remote, "+"+source+":refs/for/"+trunk)
	if _, err := runGitNet(repoDir, args...); err != nil {
		if te := transportRejection(err); te != nil {
			return "", te
		}
		return "", fmt.Errorf("push to refs/for/%s: %w", trunk, err)
	}
	return id, nil
}

// transportRejection maps a raw git push TRANSPORT failure to the structured
// §6.5 error shape (FIX #2). The prod control plane sits behind Cloudflare,
// where git's chunked pack upload gets a bare "error: RPC failed; HTTP 400 ...
// unexpected disconnect while reading sideband packet / the remote end hung
// up unexpectedly" - a broken pipe at the HTTP layer, opaque and defeating
// the structured-error contract. It is DISTINCT from a daemon POLICY
// rejection, which arrives as "remote: <message>" lines with the pack POST
// itself succeeding; those must pass through untouched, so this only fires on
// the transport signatures and returns nil otherwise (the caller keeps the
// original error, daemon message intact).
func transportRejection(err error) *clierr.Error {
	s := err.Error()
	// Auth failures (401/403/407) are transport too but need a different
	// remedy - leave them to the raw error rather than misattributing them to
	// the pack buffer.
	if strings.Contains(s, "HTTP 401") || strings.Contains(s, "HTTP 403") || strings.Contains(s, "HTTP 407") || strings.Contains(s, "Authentication failed") {
		return nil
	}
	transport := false
	for _, sig := range []string{
		"RPC failed",
		"unexpected disconnect while reading sideband packet",
		"the remote end hung up unexpectedly",
		"HTTP 400",
		"HTTP 5", // 5xx
		"curl 22",
		"curl 55",
		"curl 56",
		"early EOF",
	} {
		if strings.Contains(s, sig) {
			transport = true
			break
		}
	}
	if !transport {
		return nil
	}
	return &clierr.Error{
		Code:       "push_transport_failed",
		Field:      "remote",
		Message:    "the push to the control plane failed at the HTTP transport layer, not on policy (a proxy dropped the pack upload): " + firstNonEmptyLine(s),
		Suggestion: "raise git's HTTP post buffer and pin HTTP/1.1, then retry `runko change push`: `git config http.postBuffer 524288000 && git config http.version HTTP/1.1` (a fresh `runko workspace create` now stamps both for you)",
	}
}

// firstNonEmptyLine trims the git error to its first meaningful line so the
// structured message stays a one-liner (git's own multi-line RPC dump lands
// in --verbose, not the §6.5 message).
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

// lsRemoteTrunk asks the remote for its trunk tip ("" when the remote has
// no such ref yet - an unborn trunk).
func lsRemoteTrunk(repoDir, remote, trunk string) (string, error) {
	out, err := runGitNet(repoDir, "ls-remote", remote, "refs/heads/"+trunk)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}
