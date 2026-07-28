// runko change create / requirements - the last two §19.2 CLI stubs
// (§17.1's cheat sheet: create -> push -> requirements -> land).
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/receive"
)

// CreateChange commits everything in the working tree (tracked + new
// files, the workspace-snapshot convention) as ONE commit carrying a
// Change-Id trailer - the §7.4 identity that survives amends and rebases.
// Deliberately no auto-push: `runko change push` is the explicit "submit
// for review" step (§11.5), and splitting them keeps plain-git parity
// (commit locally offline, push when ready).
func CreateChange(repoDir, message string, allowLarge bool) (changeID string, err error) {
	if message == "" {
		return "", &clierr.Error{
			Code: "missing_message", Field: "m",
			Message:    "a change needs a message",
			Suggestion: `runko change create -m "Reject invalid SKUs"`,
		}
	}
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code: "not_a_repo", Field: "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "clone the monorepo first (see `runko doctor`)",
		}
	}
	// jj owns the working copy in a colocated checkout, so staging and
	// committing with git here would mint an identity jj does not honour
	// (see createChangeJJ).
	if isJJWorkspace(repoDir) {
		return createChangeJJ(repoDir, message, allowLarge)
	}
	// In a sparse-cone worktree (runko workspace attach), paths outside
	// the cone must fail the change LOUDLY with a structured error - work
	// silently left out of a commit is work lost (2026-07-08 dogfood
	// review). Git's own behavior here varies by version: newer gits fail
	// `add -A` outright (raw exit-1 advice text), older ones skip the
	// paths with a warning and stage the rest - both funnel into the same
	// clierr below via the post-add untracked check.
	addErr := func() error { _, err := runGit(repoDir, "add", "-A"); return err }()
	if skipped, err := runGit(repoDir, "ls-files", "--others", "--exclude-standard"); err == nil && skipped != "" {
		return "", &clierr.Error{
			Code:       "outside_sparse_cone",
			Field:      "repo",
			Message:    "these files are outside this workspace's sparse cone and cannot be part of the change: " + strings.Join(strings.Split(skipped, "\n"), ", "),
			Suggestion: "widen the cone first (`git sparse-checkout add <dir>`), or move the files under a materialized project",
		}
	}
	if addErr != nil {
		return "", fmt.Errorf("stage changes: %w", addErr)
	}
	// Build-artifact guard (FIX #4): `change create` stages the WHOLE tree, so
	// a stray `go build` output binary at the repo root (executable, multi-MB,
	// binary) rode into the commit silently - a 7.5MB junk blob plus phantom
	// size + affinity violations at push. Refuse when a NEWLY-added file looks
	// like an artifact, naming each; --allow-large is the escape hatch for an
	// intentional large/binary asset. Only added files are inspected - an
	// already-tracked file is the reviewer's call, not this heuristic's.
	if !allowLarge {
		suspects, err := suspectArtifacts(repoDir)
		if err != nil {
			return "", err
		}
		if len(suspects) > 0 {
			return "", &clierr.Error{
				Code:       "suspect_artifact",
				Field:      "repo",
				Message:    "these newly-added files look like build artifacts, not source:\n" + strings.Join(suspects, "\n"),
				Suggestion: "remove them or add them to .gitignore (build output never belongs in a change); if the addition is intentional, re-run with --allow-large",
			}
		}
	}
	staged, err := runGit(repoDir, "diff", "--cached", "--name-only")
	if err != nil {
		return "", err
	}
	if staged == "" {
		return "", &clierr.Error{
			Code: "nothing_to_commit", Field: "repo",
			Message:    "the working tree has no changes",
			Suggestion: "edit something first, or amend HEAD with plain git if you meant to reword",
		}
	}

	// Bake the Change-Id in from the first commit (rather than letting
	// `change push` amend it in later): the id should be stable the moment
	// the Change exists locally. The seed must be globally unique, not
	// reproducible: HEAD + staged path names alone collide the moment two
	// clones at the same tip touch the same file (two engineers - or two
	// agents - would fight over one Change identity on push), so mix in the
	// staged CONTENT (the index's tree hash), the message, and random bytes.
	// Determinism in receive.GenerateChangeID is for the server-side seed
	// (the commit SHA, already unique); a client minting a fresh identity
	// wants entropy.
	head, _ := runGit(repoDir, "rev-parse", "HEAD") // "" on an unborn branch is fine
	stagedTree, _ := runGit(repoDir, "write-tree")
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate change id nonce: %w", err)
	}
	seed := strings.Join([]string{head, stagedTree, staged, message, hex.EncodeToString(nonce)}, "|")
	id, msgWithID := receive.EnsureChangeID(message, seed)

	// Same identity fallback as workspace snapshot: committing must work
	// on a machine with no configured git identity.
	commitArgs := []string{"commit", "-m", msgWithID}
	if email, _ := runGit(repoDir, "config", "user.email"); email == "" {
		commitArgs = append([]string{"-c", "user.name=Runko", "-c", "user.email=runko@localhost"}, commitArgs...)
	}
	if _, err := runGit(repoDir, commitArgs...); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// createChangeJJ is `change create` in a colocated jj checkout. jj's
// working copy IS the change, so there is nothing to stage: describing @
// names it, and the Change-Id trailer derives from jj's own change id
// (jj.go's trailer template) rather than the git path's nonce.
//
// Deriving from the change id up front makes the identity stable across
// later rewrites that re-render the trailer template from the same jj
// change id (§7.4). The template APPENDS when a description already
// carries a Change-Id — it does not replace — so any Change-Id the user
// pasted into -m is stripped before describe (same as amendChangeJJ's
// reword path). Without that strip, describe leaves two trailers and
// ParseChangeID takes the first (the foreign one).
//
// `jj new` at the end parks a fresh empty @ above the described commit, the
// shape jjTipCommit already expects (an empty undescribed @ sitting on the
// finished stack) and what makes the next `change create` a new Change
// rather than an amend of this one. That only holds when @ was undescribed
// to begin with: if @ is already an established Change (mid-stack `jj edit`),
// describing it rewords and `jj new` forks the stack - refused below.
func createChangeJJ(repoDir, message string, allowLarge bool) (changeID string, err error) {
	configured, err := jjTrailerConfiguredChecked(repoDir)
	if err != nil {
		return "", err
	}
	if !configured {
		return "", &clierr.Error{
			Code: "jj_change_ids_not_configured", Field: "jj",
			Message:    "this jj workspace does not derive Change-Id trailers, so the change would have no stable identity",
			Suggestion: "run `runko doctor --install-hook` once in this repo, then retry `runko change create`",
		}
	}
	// Work jj cannot see is work silently dropped from the change - the same
	// contract as the plain-git cone guard, in the checkout's own dialect.
	lost, err := jjOutsideSparseChecked(repoDir)
	if err != nil {
		return "", err
	}
	if len(lost) > 0 {
		return "", &clierr.Error{
			Code:       "outside_sparse_cone",
			Field:      "repo",
			Message:    "these files are outside this workspace's sparse cone and jj cannot see them, so they would be silently left out of the change: " + strings.Join(lost, ", "),
			Suggestion: "widen the cone first (`jj sparse set --add <dir>`), or move the files under a materialized project",
		}
	}
	// jj EXCLUDES a file over snapshot.max-new-file-size from the working
	// copy and merely warns, so without this the change would ship missing
	// exactly the file the author cared most about. Same class of failure as
	// the cone guard above: refuse loudly, and let --allow-large opt in.
	if !allowLarge {
		refused, err := jjSnapshotRefusalsChecked(repoDir)
		if err != nil {
			return "", err
		}
		if len(refused) > 0 {
			return "", &clierr.Error{
				Code:       "suspect_artifact",
				Field:      "repo",
				Message:    "jj refused to snapshot these files, so they would be silently left out of the change:\n" + strings.Join(refused, "\n"),
				Suggestion: "remove them or add them to .gitignore (build output never belongs in a change); if the addition is intentional, re-run with --allow-large",
			}
		}
	}
	large := jjAllowLargeArgs(allowLarge)
	// jj auto-snapshots the working copy on every command, so @ already
	// holds the edits; empty-and-undescribed means there is nothing to name.
	empty, err := runJJ(repoDir, append(large, "log", "--no-graph", "-r", "@", "-T", "empty")...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(empty) == "true" {
		return "", &clierr.Error{
			Code: "nothing_to_commit", Field: "repo",
			Message:    "the working copy has no changes",
			Suggestion: "edit something first, or reword with `jj describe` if you meant to",
		}
	}
	// change create means NEW change. A non-empty description on @ means @ is
	// already an established Change (typical after mid-stack `jj edit`):
	// describing it would silently reword that Change and `jj new` would fork
	// the stack, orphaning descendants so `change push` drops them. Children
	// alone are not the signal - an empty undescribed insert mid-stack is a
	// different case; a described tip with no children still must not be
	// reworded by create.
	descNow, err := runJJ(repoDir, append(large, "log", "--no-graph", "-r", "@", "-T", "description")...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(descNow) != "" {
		// Do NOT suggest `runko change amend`: in this state amend runs
		// `jj squash` of @ into @-, which merges two established Changes and
		// annihilates the upper one's identity (verified 2026-07-24). jj has
		// already auto-snapshotted edits into this Change, so the safe next
		// step is push; a genuinely NEW Change needs `jj new` first.
		return "", &clierr.Error{
			Code:  "already_a_change",
			Field: "repo",
			Message: "the working copy is already an established Change (it has a description); " +
				"`change create` would reword it and fork the stack instead of creating a new Change",
			Suggestion: "the edits are already part of this Change (jj auto-snapshots); submit with `runko change push`, or start a new Change above with `jj new` then `runko change create`",
		}
	}
	if !allowLarge {
		suspects, err := suspectArtifactsJJ(repoDir)
		if err != nil {
			return "", err
		}
		if len(suspects) > 0 {
			return "", &clierr.Error{
				Code:       "suspect_artifact",
				Field:      "repo",
				Message:    "these newly-added files look like build artifacts, not source:\n" + strings.Join(suspects, "\n"),
				Suggestion: "remove them or add them to .gitignore (build output never belongs in a change); if the addition is intentional, re-run with --allow-large",
			}
		}
	}
	if err := jjEnsureIdentity(repoDir); err != nil {
		return "", err
	}
	// Strip pasted Change-Id trailers so the template stamps exactly one
	// (the jj-derived id). Leaving a foreign trailer first would make
	// ParseChangeID — and the server — take the wrong identity.
	body := strings.TrimRight(stripChangeIDTrailers(message), "\n")
	// A -m that was ONLY a pasted trailer strips to nothing. jj stamps no
	// trailer on an empty description, so ParseChangeID below would fail and
	// report "jj stamped no Change-Id trailer" in a repo whose template is
	// configured correctly - the same self-contradicting diagnosis this
	// migration already had to fix once. Name the real problem instead: the
	// message had no content of its own.
	if strings.TrimSpace(body) == "" {
		return "", &clierr.Error{
			Code: "missing_message", Field: "m",
			Message:    "the message is only a Change-Id trailer, leaving nothing to describe the change",
			Suggestion: `say what changed and why: runko change create -m "Reject invalid SKUs"`,
		}
	}
	if _, err := runJJ(repoDir, append(large, "describe", "-m", body)...); err != nil {
		return "", fmt.Errorf("describe the change: %w", err)
	}
	desc, err := runJJ(repoDir, "log", "--no-graph", "-r", "@", "-T", "description")
	if err != nil {
		return "", err
	}
	id, ok := receive.ParseChangeID(desc)
	if !ok {
		return "", &clierr.Error{
			Code: "jj_change_ids_not_configured", Field: "jj",
			Message:    "jj described the change but stamped no Change-Id trailer, so it has no stable identity",
			Suggestion: "check `jj config get templates.commit_trailers`, then re-run `runko doctor --install-hook`",
		}
	}
	if _, err := runJJ(repoDir, "new"); err != nil {
		return "", fmt.Errorf("start a fresh working copy above the change: %w", err)
	}
	return id, nil
}

// suspectArtifactsJJ is suspectArtifacts for a jj checkout (FIX #4's guard,
// jj dialect): jj has no index to inspect, so the added files come from
// `jj diff -r @` and their size/mode from the working copy on disk rather
// than from `cat-file`/`ls-files --stage`.
func suspectArtifactsJJ(repoDir string) ([]string, error) {
	// --summary lists status+path without content, so a huge binary costs a
	// stat rather than a read.
	out, err := runJJ(repoDir, "diff", "-r", "@", "--summary")
	if err != nil {
		return nil, err
	}
	// jj renders diff paths relative to the repo ROOT (runJJStderr pins CWD
	// there), so resolving them against a caller-supplied repoDir - which is
	// routinely a subdirectory, `--dir .` from anywhere inside the checkout -
	// made every Stat miss, every candidate skip, and the guard report a
	// clean tree from any subdirectory. Third instance of this class in this
	// migration; resolve the root once and join against that only.
	root := jjRepoRoot(repoDir)
	var suspects []string
	for _, line := range strings.Split(out, "\n") {
		// "A path", "M path", "D path"; only additions are this guard's
		// concern (a modified tracked file is the reviewer's call).
		if !strings.HasPrefix(line, "A ") {
			continue
		}
		path := strings.TrimSpace(line[2:])
		if path == "" {
			continue
		}
		full := filepath.Join(root, path)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		size := info.Size()
		executable := info.Mode().Perm()&0o111 != 0
		switch {
		// jj's own snapshot.max-new-file-size (1MiB by default) refuses
		// anything this large before it reaches @, so jjSnapshotRefusalsChecked
		// normally fires first and this arm only carries repos that raised the
		// cap. Kept rather than deleted: the size rule is the guard's contract,
		// not an artifact of jj's current default.
		case size >= suspectArtifactThreshold:
			suspects = append(suspects, fmt.Sprintf("  %s (%.1f MiB)", path, float64(size)/(1<<20)))
		case executable && isBinaryFile(full):
			suspects = append(suspects, fmt.Sprintf("  %s (executable binary)", path))
		}
	}
	return suspects, nil
}

// isBinaryFile applies git's own heuristic - a NUL byte in the first 8000
// bytes - so the jj guard flags the same files the git one does.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8000)
	n, _ := f.Read(buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// suspectArtifactThreshold: a newly-added file at or above this size is
// flagged whatever its type - normal source/docs rarely reach it, and a
// checked-in blob this big deserves a second look. A compiled binary
// (executable AND binary content) is flagged at ANY size, which is what
// actually caught the stray `go build` output.
const suspectArtifactThreshold = 5 << 20 // 5 MiB

// suspectArtifacts returns "<path> (<reason>)" for each newly-added STAGED
// file that looks like a build artifact rather than source (FIX #4). It runs
// on the index after `add -A`, so it sees exactly what the commit would
// capture. Cheap: one numstat pass names the added+binary files, then a
// size/mode probe per candidate (added files in a normal change are few).
func suspectArtifacts(repoDir string) ([]string, error) {
	// -z: NUL-separated records "added\tdeleted\tpath", no path quoting;
	// binary files report added/deleted as "-". --diff-filter=A: only files
	// this change introduces (a modified tracked file is not our concern).
	out, err := runGit(repoDir, "diff", "--cached", "--numstat", "-z", "--no-renames", "--diff-filter=A")
	if err != nil {
		return nil, err
	}
	var suspects []string
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		fields := strings.SplitN(rec, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		binary := fields[0] == "-"
		path := fields[2]

		var size int64
		if s, err := runGit(repoDir, "cat-file", "-s", ":"+path); err == nil {
			fmt.Sscan(strings.TrimSpace(s), &size)
		}
		executable := false
		if st, err := runGit(repoDir, "ls-files", "--stage", "--", path); err == nil {
			executable = strings.HasPrefix(st, "100755")
		}

		switch {
		case size >= suspectArtifactThreshold:
			suspects = append(suspects, fmt.Sprintf("  %s (%.1f MiB)", path, float64(size)/(1<<20)))
		case executable && binary:
			suspects = append(suspects, fmt.Sprintf("  %s (executable binary)", path))
		}
	}
	return suspects, nil
}

// AmendChange folds the working tree into HEAD's existing Change (FIX #6):
// the native verb for what agents otherwise did with a raw `git commit
// --amend`, which fails on a checkout with no configured git author identity
// (fresh VM, agent container). It re-stages the tree with the same
// sparse-cone guard as create, amends with the §7.5 Runko-identity fallback,
// and PRESERVES the Change-Id trailer so the change keeps its identity across
// the amend. message == "" keeps HEAD's message (just folds in the WIP); a
// new message carries the same Change-Id forward. In a jj colocated checkout
// this routes to amendChangeJJ (`jj squash`); allowLarge is ignored on the
// plain-git path (no snapshot cap there) and matches create's --allow-large
// on the jj path.
func AmendChange(repoDir, message string, allowLarge bool) (changeID string, err error) {
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code: "not_a_repo", Field: "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run inside a runko workspace worktree",
		}
	}
	if isJJWorkspace(repoDir) {
		return amendChangeJJ(repoDir, message, allowLarge)
	}
	// Landed trunk commits keep their Change-Id trailer. Amending HEAD when
	// it is already on the local remote-tracking trunk rewrites shared
	// history and re-submits a landed Change — refuse offline.
	if head, err := runGit(repoDir, "rev-parse", "HEAD"); err == nil {
		if err := refuseAmendOnTrunk(repoDir, strings.TrimSpace(head), "HEAD"); err != nil {
			return "", err
		}
	}
	id, err := headChangeID(repoDir)
	if err != nil {
		return "", err
	}
	if _, err := runGit(repoDir, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage changes: %w", err)
	}
	if skipped, err := runGit(repoDir, "ls-files", "--others", "--exclude-standard"); err == nil && skipped != "" {
		return "", &clierr.Error{
			Code:       "outside_sparse_cone",
			Field:      "repo",
			Message:    "these files are outside this workspace's sparse cone and cannot be part of the change: " + strings.Join(strings.Split(skipped, "\n"), ", "),
			Suggestion: "widen the cone first (`git sparse-checkout add <dir>`), or move the files under a materialized project",
		}
	}

	args := []string{"commit", "--amend"}
	if message == "" {
		args = append(args, "--no-edit")
	} else {
		// Carry the existing Change-Id forward: a new -m message has none, so
		// re-append HEAD's so the change keeps its identity (§7.4). Strip any
		// Change-Id the caller pasted into -m first — ParseChangeID takes the
		// first match, so a foreign trailer would otherwise win and open a
		// duplicate Change server-side.
		args = append(args, "-m", messageWithPreservedChangeID(message, id))
	}
	// Same no-identity fallback as create/snapshot (§7.5): amending must work
	// on a machine with no git identity configured.
	if email, _ := runGit(repoDir, "config", "user.email"); email == "" {
		args = append([]string{"-c", "user.name=Runko", "-c", "user.email=runko@localhost"}, args...)
	}
	if _, err := runGit(repoDir, args...); err != nil {
		return "", fmt.Errorf("amend: %w", err)
	}
	return id, nil
}

// amendChangeJJ folds the working copy into the change below it - `jj
// squash`, which is precisely amend semantics in jj's vocabulary. The
// intended shape is the empty undescribed @ that `change create` parks
// above a finished Change; @ is folded into @-, which keeps @-'s identity.
//
// Identity rules (verified 2026-07-24):
//   - templates.commit_trailers APPENDS on rewrite when a description already
//     carries a Change-Id (git-minted history is full of these) — squash and
//     describe -m both. After squash we keep the first trailer and drop the
//     rest; on reword we re-stamp the PRE-reword id with the template off.
//   - A bare `jj describe -m` discards the old description, so a git-minted
//     id would be replaced by the jj-derived one and open a duplicate Change.
//   - A user -m that itself carries a foreign Change-Id would win
//     ParseChangeID (first match); messageWithPreservedChangeID strips those.
//
// When @ is itself an established Change (mid-stack `jj edit`), squash would
// merge two Changes and destroy the upper one — refused below. Squash always
// passes --use-destination-message so a non-TTY never opens an editor.
func amendChangeJJ(repoDir, message string, allowLarge bool) (changeID string, err error) {
	configured, err := jjTrailerConfiguredChecked(repoDir)
	if err != nil {
		return "", err
	}
	if !configured {
		return "", &clierr.Error{
			Code: "jj_change_ids_not_configured", Field: "jj",
			Message:    "this jj workspace does not derive Change-Id trailers, so the amended change would have no stable identity",
			Suggestion: "run `runko doctor --install-hook` once in this repo, then retry `runko change amend`",
		}
	}
	lost, err := jjOutsideSparseChecked(repoDir)
	if err != nil {
		return "", err
	}
	if len(lost) > 0 {
		return "", &clierr.Error{
			Code:       "outside_sparse_cone",
			Field:      "repo",
			Message:    "these files are outside this workspace's sparse cone and jj cannot see them, so they would be silently left out of the amend: " + strings.Join(lost, ", "),
			Suggestion: "widen the cone first (`jj sparse set --add <dir>`), or move the files under a materialized project",
		}
	}
	// Same snapshot-cap guard as create: jj excludes oversize files with a
	// zero-exit warning. --allow-large raises the cap on the jj invocations
	// that snapshot the working copy so the file enters @ and then folds;
	// once snapshotted, later commands see it without the raised cap.
	if !allowLarge {
		refused, err := jjSnapshotRefusalsChecked(repoDir)
		if err != nil {
			return "", err
		}
		if len(refused) > 0 {
			return "", &clierr.Error{
				Code:       "suspect_artifact",
				Field:      "repo",
				Message:    "jj refused to snapshot these files, so they would be silently left out of the amend:\n" + strings.Join(refused, "\n"),
				Suggestion: "remove them or add them to .gitignore (build output never belongs in a change); if the addition is intentional, re-run with --allow-large",
			}
		}
		// The same build-artifact heuristic create applies. Amend is the verb
		// agents reach for most often AFTER create, so guarding only create
		// let a stray executable binary fold straight into the change on the
		// second step - and jj's own size cap does not catch a small one.
		suspects, err := suspectArtifactsJJ(repoDir)
		if err != nil {
			return "", err
		}
		if len(suspects) > 0 {
			return "", &clierr.Error{
				Code:       "suspect_artifact",
				Field:      "repo",
				Message:    "these newly-added files look like build artifacts, not source:\n" + strings.Join(suspects, "\n"),
				Suggestion: "remove them or add them to .gitignore (build output never belongs in a change); if the addition is intentional, re-run with --allow-large",
			}
		}
	}
	large := jjAllowLargeArgs(allowLarge)
	// already_a_change BEFORE the @- id lookup: a described @ whose parent
	// has no Change-Id used to report no_change_id ("create first"), and
	// create then reported already_a_change ("push") — a suggestion loop.
	// The working copy is itself the Change; that is the diagnosis that
	// must win.
	descAt, err := runJJ(repoDir, append(large, "log", "--no-graph", "-r", "@", "-T", "description")...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(descAt) != "" {
		return "", &clierr.Error{
			Code:  "already_a_change",
			Field: "repo",
			Message: "the working copy is itself an established Change; " +
				"`change amend` would squash it into its parent and destroy its identity",
			Suggestion: "the edits are already part of this Change (jj auto-snapshots); submit with `runko change push`, or reword in place with `jj describe -m \"...\"`",
		}
	}
	// After workspace attach, @ is empty undescribed on the trunk tip — the
	// normal post-attach shape. @- is then a landed commit (still carrying
	// its Change-Id), so the no_change_id guard does not fire and a reword
	// rewrites shared trunk history. Refuse when @- is on the local
	// remote-tracking trunk (offline; same ref statusStack uses).
	parentSHA, err := runJJ(repoDir, append(large, "log", "--no-graph", "-r", "@-", "-T", "commit_id")...)
	if err != nil {
		return "", err
	}
	if err := refuseAmendOnTrunk(repoDir, strings.TrimSpace(parentSHA), "the commit below the working copy"); err != nil {
		return "", err
	}
	// The change being amended is @'s parent (`change create` parks an empty
	// @ above it); squashing an empty @ into it is a no-op that still lets a
	// message-only reword through. Capture the established id BEFORE any
	// rewrite — reword must re-stamp this exact id, not whatever the template
	// would derive from the parent's jj change id.
	target, err := runJJ(repoDir, append(large, "log", "--no-graph", "-r", "@-", "-T", "description")...)
	if err != nil {
		return "", err
	}
	id, ok := receive.ParseChangeID(target)
	if !ok {
		return "", &clierr.Error{
			Code: "no_change_id", Field: "repo",
			Message:    "the commit below the working copy has no Change-Id trailer to amend into",
			Suggestion: "create the change first with `runko change create -m \"...\"`",
		}
	}
	if err := jjEnsureIdentity(repoDir); err != nil {
		return "", err
	}
	empty, err := runJJ(repoDir, append(large, "log", "--no-graph", "-r", "@", "-T", "empty")...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(empty) != "true" {
		// --use-destination-message keeps the parent's description and never
		// opens an editor. Without it, when both sides are described jj asks
		// for a combined message (fails non-interactively with a temp-file
		// path in the error). We already refuse a described @ above, so the
		// source description is empty and -u is a pure non-interactive guard.
		if _, err := runJJ(repoDir, append(large, "squash", "--use-destination-message")...); err != nil {
			return "", &clierr.Error{
				Code:       "jj_squash_failed",
				Field:      "repo",
				Message:    "could not fold the working copy into the change below it",
				Suggestion: "check `jj status` for conflicts or an unexpected stack shape, then retry `runko change amend`",
			}
		}
	}
	if message != "" {
		// Reword must preserve the established identity. Bare describe -m
		// discards the old trailer and the template stamps a jj-derived one
		// (different when the parent was git-minted). Write the new body plus
		// the pre-reword id with the template off so nothing appends a second.
		final := messageWithPreservedChangeID(message, id)
		args := append(append([]string{}, large...),
			"--config", `templates.commit_trailers=""`,
			"describe", "-r", "@-", "-m", final)
		if _, err := runJJ(repoDir, args...); err != nil {
			return "", &clierr.Error{
				Code:       "jj_describe_failed",
				Field:      "m",
				Message:    "could not reword the change",
				Suggestion: "retry `runko change amend -m \"...\"` with a non-empty message",
			}
		}
	} else {
		// Squash re-renders trailers and APPENDS when the parent already
		// carried a Change-Id (git-minted history is full of these). Keep
		// the first - that is the established identity ParseChangeID returns
		// both client- and server-side - and drop the rest without mangling
		// other trailers.
		if err := jjEnsureSingleChangeIDTrailer(repoDir, "@-"); err != nil {
			return "", err
		}
	}
	// Re-read: after squash+dedup the first trailer is the identity; after
	// reword we forced `id`. Either way return what the description carries.
	after, err := runJJ(repoDir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		return "", err
	}
	if got, ok := receive.ParseChangeID(after); ok {
		return got, nil
	}
	return id, nil
}

// changeIDTrailerLine matches a full Change-Id trailer line (same shape as
// receive.ParseChangeID). Used only to strip/dedup - parsing stays in
// receive so client and server never disagree on which id wins.
var changeIDTrailerLine = regexp.MustCompile(`^Change-Id: I[0-9a-f]{40}$`)

// stripChangeIDTrailers removes every Change-Id trailer line. Other trailers
// and the body are untouched. Used when a reword must re-stamp a known id
// without leaving a pasted foreign trailer first in the message.
func stripChangeIDTrailers(desc string) string {
	lines := strings.Split(desc, "\n")
	out := lines[:0:0]
	for _, line := range lines {
		if changeIDTrailerLine.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// messageWithPreservedChangeID builds an amend -m description that keeps the
// Change's established identity. Any Change-Id already in the user message is
// stripped first: a pasted foreign id would otherwise be first and win
// ParseChangeID, opening a duplicate Change server-side. Same contract as the
// plain-git amend path.
func messageWithPreservedChangeID(message, establishedID string) string {
	body := strings.TrimRight(stripChangeIDTrailers(message), "\n")
	return body + "\n\nChange-Id: " + establishedID + "\n"
}

// dedupChangeIDTrailers keeps the first Change-Id trailer and drops every
// later one. Other trailer lines (Signed-off-by, Co-Authored-By, ...) and the
// body are untouched - order of non-Change-Id lines is preserved.
func dedupChangeIDTrailers(desc string) string {
	// strings.Split on a trailing-newline string yields a final "" entry;
	// preserve that so re-joining does not strip a terminator the caller
	// may rely on for equality checks.
	lines := strings.Split(desc, "\n")
	seen := false
	out := lines[:0:0]
	for _, line := range lines {
		if changeIDTrailerLine.MatchString(line) {
			if seen {
				continue
			}
			seen = true
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// jjEnsureSingleChangeIDTrailer rewrites rev's description so it carries at
// most one Change-Id. The rewrite must disable templates.commit_trailers for
// that one describe: leaving it on would re-append the derived id the moment
// we write a message that still contains the first Change-Id (verified
// 2026-07-24).
func jjEnsureSingleChangeIDTrailer(repoDir, rev string) error {
	desc, err := runJJ(repoDir, "log", "--no-graph", "-r", rev, "-T", "description")
	if err != nil {
		return err
	}
	cleaned := dedupChangeIDTrailers(desc)
	if cleaned == desc {
		return nil
	}
	_, err = runJJ(repoDir, "--config", "templates.commit_trailers=\"\"",
		"describe", "-r", rev, "-m", cleaned)
	if err != nil {
		return fmt.Errorf("dedup Change-Id trailers: %w", err)
	}
	return nil
}

// refuseAmendOnTrunk refuses amend when commitSHA is already landed - equal
// to or an ancestor of a local remote-tracking trunk ref. Offline by design:
// amend is an offline verb, so this reads local tracking refs rather than
// making lsRemoteTrunk's network call. No tracking ref → no refusal (it
// cannot be told offline; the same fail-open statusStack takes).
//
// The trunk NAME comes from the checkout's runko.trunk binding (what
// `workspace sync` reads), defaulting to main - amend takes no --trunk flag,
// unlike push and land. Every remote is checked, not just origin: hardcoding
// refs/remotes/origin/main silently disabled the guard for any repo whose
// trunk is not main or whose remote is not origin, which is precisely the
// repo most likely to have a landed commit sitting under an empty @.
// what names the subject in the message ("HEAD" or "the commit below...").
func refuseAmendOnTrunk(repoDir, commitSHA, what string) error {
	if commitSHA == "" {
		return nil
	}
	trunk, _ := runGit(repoDir, "config", "runko.trunk")
	if strings.TrimSpace(trunk) == "" {
		trunk = "main"
	}
	refs, err := runGit(repoDir, "for-each-ref", "--format=%(refname)",
		"refs/remotes/*/"+strings.TrimSpace(trunk))
	if err != nil || strings.TrimSpace(refs) == "" {
		return nil
	}
	landed := false
	for _, ref := range strings.Fields(refs) {
		if _, err := runGit(repoDir, "merge-base", "--is-ancestor", commitSHA, ref); err == nil {
			landed = true
			break
		}
	}
	if !landed {
		return nil
	}
	return &clierr.Error{
		Code:  "already_on_trunk",
		Field: "repo",
		Message: what + " is already on trunk - there is no open Change to amend " +
			"(landed commits keep their Change-Id trailer)",
		// The post-attach empty-@ shape lands here when someone meant create.
		Suggestion: `start a new Change with runko change create -m "..."`,
	}
}

// headChangeID reads the Change-Id trailer from HEAD - the default target
// for change-scoped verbs run inside a checkout (requirements, land,
// automerge, describe, comment, ...).
func headChangeID(repoDir string) (string, error) {
	msg, err := runGit(repoDir, "log", "-1", "--format=%B")
	if err != nil {
		return "", &clierr.Error{
			Code: "no_commits", Field: "repo",
			Message:    "HEAD has no commits to read a Change-Id from",
			Suggestion: "pass --change <Id> explicitly, or run `runko change create` first",
		}
	}
	id, ok := receive.ParseChangeID(msg)
	if !ok {
		return "", &clierr.Error{
			Code: "no_change_id", Field: "repo",
			Message:    "HEAD's commit message has no Change-Id trailer",
			Suggestion: "run `runko change push` once (it amends one in), or pass --change <Id>",
		}
	}
	return id, nil
}

// ChangeRequirements fetches the §13.5 merge gates for one Change.
func ChangeRequirements(ctx context.Context, client *http.Client, cred Credential, changeID string) (checks.MergeRequirements, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(cred.URL, "/")+"/api/changes/"+changeID+"/merge-requirements", nil)
	if err != nil {
		return checks.MergeRequirements{}, err
	}
	req.Header.Set("Authorization", cred.AuthHeader())
	resp, err := client.Do(req)
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("contact %s: %w", cred.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return checks.MergeRequirements{}, changeUnknownError(changeID)
	}
	if resp.StatusCode != http.StatusOK {
		return checks.MergeRequirements{}, decodeAPIError(resp, "merge-requirements")
	}
	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("decode response: %w", err)
	}
	return reqs, nil
}

// printRequirements renders the gates the way the web's merge-requirements
// card does: every requirement with its state, then the plain-language
// blockers (§6.6).
func printRequirements(changeID string, reqs checks.MergeRequirements) {
	if reqs.Mergeable {
		fmt.Printf("%s: ready to land\n", changeID)
	} else {
		fmt.Printf("%s: blocked from landing\n", changeID)
	}
	if reqs.AutoApproved {
		fmt.Println("  auto-approve zone: approvals waived by trunk's manifests, not granted by a human")
	}
	for _, o := range reqs.RequiredOwners {
		mark := "○ outstanding"
		for _, s := range reqs.SatisfiedOwners {
			if s != o {
				continue
			}
			// Under an auto-approve zone a satisfied owner may have been
			// WAIVED rather than approved (and a mixed change carries some
			// of each), so the per-row word drops to the one that is true
			// either way - the banner above says who did the satisfying.
			mark = "✓ approved"
			if reqs.AutoApproved {
				mark = "✓ satisfied"
			}
		}
		fmt.Printf("  owner  %-40s %s\n", o, mark)
	}
	for _, c := range reqs.RequiredChecks {
		mark := "○ not reported"
		for _, n := range reqs.PassingChecks {
			if n == c {
				mark = "✓ passing"
			}
		}
		for _, n := range reqs.FailingChecks {
			if n == c {
				mark = "✕ failing"
			}
		}
		for _, n := range reqs.PendingChecks {
			if n == c {
				mark = "● running"
			}
		}
		fmt.Printf("  check  %-40s %s\n", c, mark)
	}
	for _, b := range reqs.Blockers {
		fmt.Printf("  -> %s\n", b)
	}
}
