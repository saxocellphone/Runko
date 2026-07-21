package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/agentsmd"
)

// TestEnsureAgentSkillInstallsAndExcludesIt: a monorepo that carries no
// skill of its own (anything adopted through the mirror rather than
// created by runko) still gets one in the checkout - and it must be
// ignored, because `change create` commits the WHOLE working tree and
// would otherwise sweep a local teaching file into someone's Change.
func TestEnsureAgentSkillInstallsAndExcludesIt(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	path, outcome, err := ensureAgentSkill(repo.Dir)
	if err != nil || outcome != "local" {
		t.Fatalf("ensureAgentSkill: outcome=%q err=%v", outcome, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if string(content) != agentsmd.GenerateSkill() {
		t.Fatalf("installed skill is not the generated one")
	}
	status, err := runGit(repo.Dir, "status", "--porcelain", "-uall")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.Contains(status, agentSkillConeDir) {
		t.Fatalf("the installed skill is visible to a whole-tree commit:\n%s", status)
	}

	// Idempotent: a second call neither rewrites nor re-reports it.
	if _, outcome, err := ensureAgentSkill(repo.Dir); err != nil || outcome != "present" {
		t.Fatalf("second call: outcome=%q err=%v", outcome, err)
	}
}

// TestEnsureAgentSkillLeavesTheTreesOwnAlone: tree-as-truth (§10.3). A
// monorepo that has evolved its own skill (org genesis seeds one, then
// people edit it) must never find this binary's copy written over it -
// the CLI's job there is `runko agents-md`, an explicit regeneration.
func TestEnsureAgentSkillLeavesTheTreesOwnAlone(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile(agentsmd.SkillPath, "---\nname: runko\n---\n\nthe org's own teaching\n")
	repo.Commit("seed the agent skill")

	path, outcome, err := ensureAgentSkill(repo.Dir)
	if err != nil || outcome != "tree" {
		t.Fatalf("ensureAgentSkill: outcome=%q err=%v", outcome, err)
	}
	if content, _ := os.ReadFile(path); !strings.Contains(string(content), "the org's own teaching") {
		t.Fatalf("the tree's skill was overwritten: %q", content)
	}
}

// TestDoctorReportsTheAgentSkill: the report is what tells a human (or the
// agent itself) that this checkout teaches nothing - the failure mode is
// invisible otherwise, since an unequipped agent works anyway, straight
// into the refusals the skill prevents.
func TestDoctorReportsTheAgentSkill(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("init")

	report, err := RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if report.AgentSkill != "" {
		t.Fatalf("expected no skill reported in a bare checkout, got %q", report.AgentSkill)
	}
	var sb strings.Builder
	PrintCheatSheet(&sb, report)
	if !strings.Contains(sb.String(), "agent skill:     NOT installed") {
		t.Fatalf("cheat sheet does not surface the missing skill:\n%s", sb.String())
	}

	if _, _, err := ensureAgentSkill(repo.Dir); err != nil {
		t.Fatalf("ensureAgentSkill: %v", err)
	}
	report, err = RunDoctor(repo.Dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if report.AgentSkill != "present" || report.AgentSkillPath != filepath.Join(repo.Dir, filepath.FromSlash(agentsmd.SkillPath)) {
		t.Fatalf("unexpected skill report: %q at %q", report.AgentSkill, report.AgentSkillPath)
	}
}

// TestConeWithAgentSkillMaterializesTheTreesTeaching: a per-project cone
// hides the tree's own skill from the harness working in that checkout -
// the one place it is guaranteed to matter. Widening is free (affinity
// gates what a push TOUCHES), so the cone always carries it.
func TestConeWithAgentSkillMaterializesTheTreesTeaching(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile(agentsmd.SkillPath, "---\nname: runko\n---\n\nteaching\n")
	repo.WriteFile("cli/main.go", "package main\n")
	repo.Commit("seed")
	head := repo.Head()

	got := coneWithAgentSkill(repo.Dir, []string{"", "cli"}, head)
	if len(got) != 3 || got[2] != agentSkillConeDir {
		t.Fatalf("expected the skill dir appended to the cone, got %q", got)
	}
	// Already covered, in either form: never a duplicate pattern.
	for _, cone := range [][]string{{"", "cli", agentSkillConeDir}, {"."}} {
		if got := coneWithAgentSkill(repo.Dir, cone, head); len(got) != len(cone) {
			t.Fatalf("cone %q was widened redundantly to %q", cone, got)
		}
	}
	// An empty cone is a full checkout - nothing to widen.
	if got := coneWithAgentSkill(repo.Dir, nil, head); got != nil {
		t.Fatalf("expected a full checkout's cone left empty, got %q", got)
	}
}

// TestConeUnchangedWhenTheTreeHasNoSkill: the widening must be driven by
// what the tree actually carries, not by hope - a cone naming a directory
// that does not exist at that rev is noise in every checkout of every repo
// that never adopted a skill.
func TestConeUnchangedWhenTheTreeHasNoSkill(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("cli/main.go", "package main\n")
	repo.Commit("seed")

	cone := []string{"", "cli"}
	if got := coneWithAgentSkill(repo.Dir, cone, repo.Head()); len(got) != len(cone) {
		t.Fatalf("cone widened for a tree with no skill: %q", got)
	}
}
