// jj as the primary client (§7.4, §17.1; decided 2026-07-08): Runko's
// change model IS jj's - stable change identity across rewrites, commits
// as versions of a Change - so jj's native workflow (amend anywhere in a
// stack, descendants auto-rebase) is the intended daily driver, with the
// receive funnel's series processing turning one tip push into an update
// of every Change in the stack. Git stays the substrate (§22.2) and the
// parity path: everything here works in a COLOCATED jj repo (`jj git init
// --colocate` / `jj git clone --colocate`), where plain git and the
// existing smart-HTTP transport keep working unchanged.
//
// Change identity: jj cannot run a commit-msg hook (it has no git hooks),
// but it has something better - `templates.commit_trailers` with the
// built-in format_gerrit_change_id_trailer(self), which derives the
// Change-Id trailer deterministically from jj's own change id. Same id
// across every rewrite, no randomness needed, and exactly §7.4's "jj-style
// change IDs" made literal. `runko doctor --install-hook` configures it.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// jjTrailerTemplate is the repo-level jj config that makes every commit
// carry a Change-Id trailer derived from its jj change id.
const jjTrailerTemplate = `format_gerrit_change_id_trailer(self)`

// jjRepoRoot resolves dir to the absolute repo top level - what `jj -R`
// requires, since it does not discover upward from a subdirectory the way
// git does. Falls back to dir's own absolute form when the top level cannot
// be read (a non-repo, which jj will reject with its own error); returning
// the input unchanged would only move the same failure one call later.
func jjRepoRoot(dir string) string {
	if top, err := runGit(dir, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		if abs, err := filepath.Abs(top); err == nil {
			return abs
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// isJJWorkspace reports whether dir is inside a jj workspace (colocated or
// not): a `.jj` directory at the repo's top level.
func isJJWorkspace(repoDir string) bool {
	top, err := runGit(repoDir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		top = repoDir
	}
	info, err := os.Stat(filepath.Join(top, ".jj"))
	return err == nil && info.IsDir()
}

// jjGitInitColocate turns an existing plain-git checkout into a colocated
// jj workspace (jj + .git side by side). runJJ can't drive this one: -R
// wants an existing jj repo, and this is the command that creates it. jj
// itself refuses to colocate inside a git WORKTREE - which is exactly why
// --jj workspaces are standalone clones (workspace.go).
func jjGitInitColocate(dir string) error {
	if _, err := exec.LookPath("jj"); err != nil {
		return &clierr.Error{
			Code: "jj_not_found", Field: "jj",
			Message:    "setting up a jj colocated checkout needs the jj binary on PATH",
			Suggestion: "install jj (https://jj-vcs.github.io), or drop --jj for a plain-git worktree",
		}
	}
	if out, err := exec.Command("jj", "git", "init", "--colocate", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("jj git init --colocate: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runJJ(repoDir string, args ...string) (string, error) {
	out, _, err := runJJStderr(repoDir, args...)
	return out, err
}

// runJJStderr runs jj and returns stdout and stderr SEPARATELY. jj reports
// consequential conditions as warnings on stderr while exiting zero - the
// "Refused to snapshot some files" notice being the one that silently drops
// work out of the working-copy commit - so callers that must not miss those
// read stderr explicitly rather than through runJJ's success path.
func runJJStderr(repoDir string, args ...string) (stdout, stderr string, err error) {
	if _, err := exec.LookPath("jj"); err != nil {
		return "", "", &clierr.Error{
			Code: "jj_not_found", Field: "jj",
			Message:    "this is a jj workspace but the jj binary is not on PATH",
			Suggestion: "install jj (https://jj-vcs.github.io) or use plain git in a non-jj clone",
		}
	}
	// Both -R and the CWD must name the repo ROOT, absolutely.
	//
	// -R tells jj WHICH repo; it does NOT decide how jj prints paths. Every
	// path-bearing output (`diff --summary`, the "Refused to snapshot"
	// warning) is rendered relative to the process CWD, and every relative
	// path argument (`sparse set --add`) is resolved against it. Callers
	// reach us from anywhere - `-w <ws>` resolves a workspace directory
	// without chdir-ing - so leaving CWD alone produced paths like
	// "../../../../tmp/ws/tool" that no repo-relative os.Stat could match:
	// the artifact guard skipped every candidate and never once fired.
	//
	// But -R does NOT walk up the way git's discovery does: `jj -R <subdir>`
	// is a hard "There is no jj repo in <subdir>". Callers legitimately hand
	// us a subdirectory - `isJJWorkspace` accepts one, and `--dir .` from
	// anywhere inside the checkout is the ordinary case - so pinning the CWD
	// without also normalizing the path made every verb fail from a
	// subdirectory of a colocated repo. Resolve the top level once here so
	// the two can never disagree.
	root := jjRepoRoot(repoDir)
	cmd := exec.Command("jj", append([]string{"-R", root}, args...)...)
	cmd.Dir = root
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		if strings.TrimSpace(errBuf.String()) != "" {
			detail = ": " + strings.TrimSpace(errBuf.String())
		}
		return "", errBuf.String(), fmt.Errorf("jj %s: %w%s", strings.Join(args, " "), err, detail)
	}
	return strings.TrimRight(string(out), "\n"), errBuf.String(), nil
}

// jjSnapshotSizeCap is the ceiling `--allow-large` raises jj's per-file
// snapshot limit to for the duration of one command. jj's own default is
// 1MiB - stricter than runko's 5MiB artifact heuristic - and a file over it
// is EXCLUDED from the working-copy commit rather than rejected, so opting
// in has to lift jj's limit too or the file would still go missing.
const jjSnapshotSizeCap = 1 << 30 // 1 GiB

// jjAllowLargeArgs is the global flag prefix that lifts the snapshot cap for
// a single jj invocation (never written to repo config - the opt-in is
// per-command, like the git path's --allow-large).
func jjAllowLargeArgs(allowLarge bool) []string {
	if !allowLarge {
		return nil
	}
	return []string{"--config", fmt.Sprintf("snapshot.max-new-file-size=%d", jjSnapshotSizeCap)}
}

// jjSnapshotRefusalsChecked names files jj declined to snapshot because they
// exceed snapshot.max-new-file-size. jj prints this as a warning and exits
// ZERO, so without this the file is simply absent from @ - and would be
// silently absent from the change, which is the one outcome `change create`
// must never produce (verified 2026-07-24).
//
// Fail closed: a `jj status` failure is returned, not treated as "no
// refusals". "No sparse/size problem" is success with empty stderr (or no
// refusal block), never a non-zero exit.
func jjSnapshotRefusalsChecked(repoDir string) ([]string, error) {
	_, stderr, err := runJJStderr(repoDir, "status")
	if err != nil {
		return nil, &clierr.Error{
			Code: "jj_status_failed", Field: "repo",
			Message:    "could not run `jj status` to check for unsnapshotted files: " + err.Error(),
			Suggestion: "fix `jj status` in this repo, then retry; work-loss guards refuse to proceed when they cannot verify",
		}
	}
	if !strings.Contains(stderr, "Refused to snapshot") {
		return nil, nil
	}
	var refused []string
	inBlock := false
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "Refused to snapshot") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		// The block ends at the first non-indented line (jj's "Hint:").
		if !strings.HasPrefix(line, "  ") {
			break
		}
		// "  <path>: 5.7MiB (6000000 bytes); the maximum ..."
		entry := strings.TrimSpace(line)
		if path, size, ok := strings.Cut(entry, ": "); ok {
			detail, _, _ := strings.Cut(size, ";")
			refused = append(refused, fmt.Sprintf("  %s (%s)", path, strings.TrimSpace(detail)))
		}
	}
	return refused, nil
}

// jjTipCommit resolves the commit `runko change push` should submit from a
// jj workspace: the working-copy commit @ when it has real content or a
// description, else its parent - jj's @ is usually an empty WIP commit
// sitting on top of the finished stack, and pushing that would mint an
// empty Change.
func jjTipCommit(repoDir string) (string, error) {
	out, err := runJJ(repoDir, "log", "--no-graph", "-r", "@",
		"-T", `commit_id ++ if(empty && description == "", " wip", " real")`)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 2 && fields[1] == "real" {
		return fields[0], nil
	}
	parents, err := runJJ(repoDir, "log", "--no-graph", "-r", "@-", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return "", err
	}
	parent := strings.Fields(parents)
	if len(parent) == 0 {
		return "", &clierr.Error{
			Code: "no_commits", Field: "repo",
			Message:    "the working copy has no finished commit to push",
			Suggestion: "describe your work first: `jj commit -m \"...\"`",
		}
	}
	return parent[0], nil
}

// jjTrailerConfigured reports whether the repo's jj config already derives
// Change-Id trailers.
func jjTrailerConfigured(repoDir string) bool {
	ok, err := jjTrailerConfiguredChecked(repoDir)
	return err == nil && ok
}

// jjTrailerConfiguredChecked distinguishes "jj ran and the template is not
// set" from "jj could not run at all". Both jj verbs consult this FIRST, so
// collapsing the two - as the bool form must - made every jj failure surface
// as "this workspace does not derive Change-Id trailers", pointing the user
// at `doctor --install-hook`, which then reports the trailers ARE wired. A
// missing binary, an unreadable repo, and a genuinely unconfigured template
// all read identically, and the suggested remedy loops.
//
// `config get` on an UNSET key is an error, not empty output, so the two
// cases have to be told apart by message rather than by exit status alone.
func jjTrailerConfiguredChecked(repoDir string) (bool, error) {
	out, stderr, err := runJJStderr(repoDir, "config", "get", "templates.commit_trailers")
	if err == nil {
		return strings.Contains(out, "format_gerrit_change_id_trailer"), nil
	}
	// jj reports an absent key as a config error naming it; anything else
	// (no binary, no repo, a stale working copy) is a real failure the
	// caller must not mistake for "not configured".
	if strings.Contains(stderr, "No such config value") || strings.Contains(stderr, "templates.commit_trailers") {
		return false, nil
	}
	return false, err
}

// jjSparsePrefixesChecked lists the working copy's sparse patterns, or
// (nil, nil) when it is unrestricted (jj spells "everything" as "."). These
// are the only paths jj can SEE - anything else on disk is invisible to it,
// which is what jjOutsideSparseChecked exists to catch.
//
// `jj sparse list` emits ONE PATTERN PER LINE (verified): splitting on all
// whitespace would shatter a pattern like "my dir" into {"my","dir"} and
// refuse every in-cone path under it. Line-split preserves spaces.
//
// Fail closed on command failure. Unrestricted is not an error path: jj
// prints a lone "." and exits zero (verified). Only a non-zero `jj sparse
// list` (missing binary, corrupt repo, bad -R) returns an error — returning
// nil prefixes on that path used to read as "unrestricted" and disarm the
// cone guard entirely.
func jjSparsePrefixesChecked(repoDir string) ([]string, error) {
	out, err := runJJ(repoDir, "sparse", "list")
	if err != nil {
		return nil, &clierr.Error{
			Code: "jj_sparse_list_failed", Field: "repo",
			Message:    "could not read jj sparse patterns to check for out-of-cone work: " + err.Error(),
			Suggestion: "run `jj sparse list` in this repo, then retry; work-loss guards refuse to proceed when they cannot verify",
		}
	}
	var prefixes []string
	for _, p := range strings.Split(out, "\n") {
		if p == "" {
			continue
		}
		if p == "." {
			return nil, nil // unrestricted: nothing can fall outside
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

// jjSparsePrefixes is the slice-only form kept for TESTS that assert on the
// cone and treat a read failure as a test failure of their own. Production
// callers use jjSparsePrefixesChecked: discarding the error here reads as
// "unrestricted", which would silently disarm the cone guard.
func jjSparsePrefixes(repoDir string) []string {
	prefixes, _ := jjSparsePrefixesChecked(repoDir)
	return prefixes
}

// jjOutsideSparseChecked names on-disk work that jj's sparse set HIDES - the
// jj analogue of the plain-git `outside_sparse_cone` guard, and a sharper
// failure than git's: where git's sparse checkout makes `add -A` fail or
// warn, jj reports "The working copy has no changes" and the edit is
// silently left out of the change (verified 2026-07-24).
//
// Discriminator (verified empirically): a path jj de-materialized is not a
// directory entry and shows up in git status as a deletion (" D path"); real
// lost work — a file or symlink the user created outside the cone — IS a
// directory entry. os.Lstat (not Stat) is required: Stat follows symlinks, so
// a dangling or looping out-of-cone symlink fails the existence check and is
// treated as a phantom deletion (verified: broken link IsNotExist via Stat,
// Lstat succeeds; symlink-loop Stat is ELOOP, Lstat succeeds). Only pure
// non-existence is skipped; any other Lstat error still counts as present
// (unreadable parent, etc.) so the guard cannot drop real entries.
//
// Porcelain without -z also quotes paths that contain spaces or non-ASCII
// (git does that, not jj), which would break prefix matching against
// `"src/my file.txt"`; -z is NUL-terminated and unquoted.
//
// Fail closed: `jj sparse list` or `git status` failure is returned, not
// treated as "nothing outside the cone".
func jjOutsideSparseChecked(repoDir string) ([]string, error) {
	// Paths from git status are repo-root-relative; Lstat must join the
	// same root. Callers may hand a subdirectory (same as runJJStderr),
	// and joining that would IsNotExist every out-of-cone path and
	// silently drop them from the guard.
	root := jjRepoRoot(repoDir)
	prefixes, err := jjSparsePrefixesChecked(root)
	if err != nil {
		return nil, err
	}
	if len(prefixes) == 0 {
		return nil, nil
	}
	out, err := runGit(root, "status", "--porcelain", "-uall", "-z")
	if err != nil {
		return nil, &clierr.Error{
			Code: "git_status_failed", Field: "repo",
			Message:    "could not run `git status` to find work outside jj's sparse cone: " + err.Error(),
			Suggestion: "run `git status` in this repo, then retry; work-loss guards refuse to proceed when they cannot verify",
		}
	}
	if out == "" {
		return nil, nil
	}
	var lost []string
	// -z records are NUL-separated. Each entry is "XY <path>"; rename/copy
	// entries carry a second path field (orig) after another NUL.
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); {
		entry := fields[i]
		i++
		if entry == "" {
			continue
		}
		if len(entry) < 3 {
			continue
		}
		status := entry[:2]
		path := entry[3:] // skip "XY" and the following space
		// Rename/copy: consume the paired orig-path field so the next
		// iteration does not treat it as a status line.
		if status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C' {
			if i < len(fields) {
				i++
			}
		}
		if path == "" || withinPrefixes(path, prefixes) {
			continue
		}
		// Phantom deletions from jj sparse de-materialization are not
		// directory entries; only refuse work that would actually be left
		// out of @. Lstat: see doc comment — Stat misses dangling links.
		if _, err := os.Lstat(filepath.Join(root, path)); err != nil && os.IsNotExist(err) {
			continue
		}
		lost = append(lost, path)
	}
	return lost, nil
}

// withinPrefixes reports whether path sits under one of the sparse
// patterns: an exact file/dir match, or anything beneath a directory
// prefix. Trailing slashes on the pattern are normalized so "alpha/" and
// "alpha" mean the same cone entry; a bare strings.HasPrefix would also
// treat "alpha" as covering "alphabet/...", which is exactly the silent
// work-loss this guard exists to prevent.
func withinPrefixes(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// jjEnsureIdentity applies the no-identity fallback to jj. Describing without
// a configured identity still succeeds (verified jj 0.43): jj writes an empty
// author and the colocated git export becomes the literal sentinel
// JJ_EMPTY_STRING, with a warning that such commits cannot be pushed to
// remotes. Snapshotting/pushing from a fresh VM or agent container therefore
// needs a real identity, matching the git path's Runko fallback. A configured
// identity always wins - the repo scope is written only when user.email
// resolves empty.
func jjEnsureIdentity(repoDir string) error {
	if email, _ := runJJ(repoDir, "config", "get", "user.email"); strings.TrimSpace(email) != "" {
		return nil
	}
	for _, kv := range [][2]string{{"user.name", "Runko"}, {"user.email", "runko@localhost"}} {
		if _, err := runJJ(repoDir, "config", "set", "--repo", kv[0], kv[1]); err != nil {
			return fmt.Errorf("configure a fallback jj identity: %w", err)
		}
	}
	return nil
}

// SetupJJChangeIDs configures the repo-level trailer template (the jj
// analog of installing the commit-msg hook). An existing UNRELATED
// commit_trailers template is left alone with a loud error rather than
// clobbered - jj has exactly one trailers slot and the user's template may
// carry other trailers.
func SetupJJChangeIDs(repoDir string) error {
	if jjTrailerConfigured(repoDir) {
		return nil
	}
	if existing, err := runJJ(repoDir, "config", "get", "templates.commit_trailers"); err == nil && strings.TrimSpace(existing) != "" {
		return &clierr.Error{
			Code: "jj_trailers_conflict", Field: "jj",
			Message:    "this repo already sets templates.commit_trailers to something else",
			Suggestion: "append `format_gerrit_change_id_trailer(self)` to your existing template by hand (`jj config edit --repo`)",
		}
	}
	if _, err := runJJ(repoDir, "config", "set", "--repo", "templates.commit_trailers", jjTrailerTemplate); err != nil {
		return fmt.Errorf("configure jj commit trailers: %w", err)
	}
	return nil
}
