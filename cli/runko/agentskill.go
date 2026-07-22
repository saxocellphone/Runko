// The agent skill, installed with the checkout (§6.10's implicit
// installers, §8.8's generated teaching). Org genesis seeds AGENTS.md and
// the loadable skill into a new org's trunk, so an agent pointed at a
// fresh clone of a runko-created monorepo already has both. Two ordinary
// situations leave an agent with neither, and they are the common ones:
// a monorepo ADOPTED through the mirror (§18) never had a genesis commit,
// and a workspace's sparse cone materializes only the projects you asked
// for - so in a `--project cli` checkout of a repo that does carry the
// skill, the file exists in the tree and not on disk, where the harness
// looks for it.
//
// Both are fixed at materialization: the cone gains the skill's directory
// when the tree owns one (read the repo's own teaching, whatever it has
// evolved into), and otherwise this CLI writes the generated skill locally
// and keeps it out of every snapshot and Change - a file the tree does not
// own must never ride into one, since `change create` commits the whole
// working tree.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/platform/agentsmd"
)

// agentSkillConeDir is the top-level directory the skill lives under
// (".claude" today), derived from the path itself so the two can never
// disagree - it is what a sparse cone must include for a harness to find
// the tree's skill at all.
var agentSkillConeDir = strings.SplitN(agentsmd.SkillPath, "/", 2)[0]

// ensureAgentSkill puts every runko agent skill where a skill-loading
// harness will find it in dir's checkout, and reports what it did for the
// SET (agentsmd.Skills): the reference skill and the workspace-discipline
// skill are installed together, because an agent that loads one and not the
// other gets half the teaching.
//
//	"tree"    - the repo owns them; nothing written, they are the repo's to
//	            evolve (`runko agents-md` regenerates them)
//	"present" - already on disk untracked; never clobbered
//	"local"   - this CLI wrote what was missing and excluded it
//
// A mixed checkout (one skill tracked, another absent) resolves per skill
// and reports the outcome of the ones it had to write, so a repo that
// predates a newly added skill still gains it. path is the reference
// skill's, kept for callers (doctor) that report a single location.
//
// Never an error the caller must handle as fatal: materializing a checkout
// that works is worth more than a teaching file, so callers log and move
// on (the InstallVerbNudgeHook posture).
func ensureAgentSkill(dir string) (path, outcome string, err error) {
	path, outcome, err = agentSkillStatus(dir)
	if err != nil {
		return "", "", err
	}

	wrote := false
	for _, s := range agentsmd.Skills() {
		p, own, err := skillStatus(dir, s.Path)
		if err != nil {
			return "", "", err
		}
		if own != "" {
			continue // the tree's, or already there: never clobber
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", "", fmt.Errorf("agent skill: %w", err)
		}
		if err := os.WriteFile(p, []byte(s.Content), 0o644); err != nil {
			return "", "", fmt.Errorf("agent skill: write %s: %w", p, err)
		}
		if _, err := excludeFromSnapshots(dir, s.Path,
			"# runko: the installed agent skill is local teaching, never Change content"); err != nil {
			return "", "", err
		}
		wrote = true
	}
	if wrote {
		return path, "local", nil
	}
	return path, outcome, nil
}

// agentSkillStatus is ensureAgentSkill's read-only half for the REFERENCE
// skill - where it would live, and what is there now: "tree" (tracked),
// "present" (an untracked file), or "" (nothing, so an installer may write
// one). `runko doctor` reports it without writing anything.
func agentSkillStatus(dir string) (path, outcome string, err error) {
	return skillStatus(dir, agentsmd.SkillPath)
}

// skillStatus answers agentSkillStatus's question for one skill path.
func skillStatus(dir, treePath string) (path, outcome string, err error) {
	top, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("agent skill: resolve worktree root: %w", err)
	}
	path = filepath.Join(top, filepath.FromSlash(treePath))

	// Tracked wins outright, materialized or not: the tree is the source
	// of truth (§10.3), and a repo that has evolved its own skill must not
	// find this CLI's copy shadowing it. When the cone left it unchecked
	// out, coneWithAgentSkill is what brings it onto disk.
	if _, err := runGit(dir, "ls-files", "--error-unmatch", "--", treePath); err == nil {
		return path, "tree", nil
	}
	if _, err := os.Stat(path); err == nil {
		return path, "present", nil
	}
	return path, "", nil
}

// coneWithAgentSkill widens a workspace's sparse cone to materialize the
// tree's own agent skill. A cone that omits it hides the repo's agent
// instructions from the harness working in that very checkout - the one
// place they are guaranteed to matter - and reading them costs nothing:
// affinity gates the paths a push TOUCHES, never what was materialized.
// An empty cone means a full checkout, and a tree with no skill has
// nothing to widen for; both return the patterns unchanged.
func coneWithAgentSkill(dir string, patterns []string, rev string) []string {
	if len(patterns) == 0 || !treeHasAgentSkill(dir, rev) {
		return patterns
	}
	for _, p := range patterns {
		if p == agentSkillConeDir || p == "." {
			return patterns
		}
	}
	return append(patterns, agentSkillConeDir)
}

// treeHasAgentSkill reports whether rev's tree carries ANY generated skill.
// Any is the right test: they share one cone directory, so one tracked
// skill is reason enough to materialize it, and a repo that predates a
// newly added skill still gets its existing one checked out. ls-tree walks
// trees only - in a blobless clone that is the local, network-free
// question, where `cat-file -e` on the blob would reach for the promisor
// remote.
func treeHasAgentSkill(dir, rev string) bool {
	for _, s := range agentsmd.Skills() {
		out, err := runGit(dir, "ls-tree", "--name-only", rev, "--", s.Path)
		if err == nil && strings.TrimSpace(out) != "" {
			return true
		}
	}
	return false
}
