// runko change create / requirements - the last two §19.2 CLI stubs
// (§17.1's cheat sheet: create -> push -> requirements -> land).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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
	// In a sparse-cone worktree (runko workspace attach), paths outside
	// the cone must fail the change LOUDLY with a structured error - work
	// silently left out of a commit is work lost (2026-07-08 dogfood
	// review). Git's own behavior here varies by version: newer gits fail
	// `add -A` outright (raw exit-1 advice text), older ones skip the
	// paths with a warning and stage the rest - both funnel into the same
	// clierr below via the post-add untracked check.
	addErr := func() error { _, err := runGit(repoDir, "add", "-A"); return err }()
	if skipped, err := runGit(repoDir, "ls-files", "--others", "--exclude-standard"); err == nil && skipped != "" {
		// The cone speaks the checkout's own dialect (§6.5's "a suggestion
		// the user can type"): jj owns working-copy materialization in a
		// jj colocated checkout, so git's sparse-checkout verb would not
		// widen anything there.
		widen := "widen the cone first (`git sparse-checkout add <dir>`)"
		if isJJWorkspace(repoDir) {
			widen = "widen the cone first (`jj sparse set --add <dir>`)"
		}
		return "", &clierr.Error{
			Code:       "outside_sparse_cone",
			Field:      "repo",
			Message:    "these files are outside this workspace's sparse cone and cannot be part of the change: " + strings.Join(strings.Split(skipped, "\n"), ", "),
			Suggestion: widen + ", or move the files under a materialized project",
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
		// Rewording in a jj checkout is `jj describe` - a plain-git amend
		// here would rewrite history behind jj's back (the push guard
		// refuses exactly that).
		reword := "or amend HEAD with plain git if you meant to reword"
		if isJJWorkspace(repoDir) {
			reword = "or reword with `jj describe` if you meant to"
		}
		return "", &clierr.Error{
			Code: "nothing_to_commit", Field: "repo",
			Message:    "the working tree has no changes",
			Suggestion: "edit something first, " + reword,
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
// new message carries the same Change-Id forward. jj checkouts are refused -
// mid-stack rework there is `jj squash`/`jj describe`, and amending behind
// jj's back is exactly what the push guard rejects.
func AmendChange(repoDir, message string) (changeID string, err error) {
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return "", &clierr.Error{
			Code: "not_a_repo", Field: "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run inside a runko workspace worktree",
		}
	}
	if isJJWorkspace(repoDir) {
		return "", &clierr.Error{
			Code: "jj_amend_unsupported", Field: "repo",
			Message:    "this is a jj colocated checkout - amending behind jj's back would mint a fresh Change identity",
			Suggestion: "rework in place with `jj squash` (fold @ into its parent) or reword with `jj describe`",
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
		// re-append HEAD's so the change keeps its identity (§7.4).
		args = append(args, "-m", message+"\n\nChange-Id: "+id)
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
	for _, o := range reqs.RequiredOwners {
		mark := "○ outstanding"
		for _, s := range reqs.SatisfiedOwners {
			if s == o {
				mark = "✓ approved"
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
