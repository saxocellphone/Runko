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
	if _, err := exec.LookPath("jj"); err != nil {
		return "", &clierr.Error{
			Code: "jj_not_found", Field: "jj",
			Message:    "this is a jj workspace but the jj binary is not on PATH",
			Suggestion: "install jj (https://jj-vcs.github.io) or use plain git in a non-jj clone",
		}
	}
	cmd := exec.Command("jj", append([]string{"-R", repoDir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("jj %s: %w%s", strings.Join(args, " "), err, detail)
	}
	return strings.TrimRight(string(out), "\n"), nil
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
	out, err := runJJ(repoDir, "config", "get", "templates.commit_trailers")
	return err == nil && strings.Contains(out, "format_gerrit_change_id_trailer")
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
