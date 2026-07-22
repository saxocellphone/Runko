package agentsmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateStaysUnderTheLineBudget(t *testing.T) {
	// docs/design.md §28.2's context-budget rule: "repo AGENTS.md <= 150 lines".
	got := Generate()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) > 150 {
		t.Fatalf("generated AGENTS.md has %d lines, want <= 150 (docs/design.md §28.2)", len(lines))
	}
}

func TestGenerateListsEveryCommand(t *testing.T) {
	got := Generate()
	for _, c := range Commands {
		if !strings.Contains(got, "`"+c.Binary+" "+c.Usage+"`") {
			t.Fatalf("expected generated AGENTS.md to list %s %s verbatim", c.Binary, c.Usage)
		}
		if !strings.Contains(got, c.Description) {
			t.Fatalf("expected generated AGENTS.md to include the description for %s %s", c.Binary, c.Usage)
		}
	}
}

func TestGenerateIncludesExitCodesAndErrorShape(t *testing.T) {
	got := Generate()
	for _, want := range []string{"`0`", "`1`", "`2`", "-> <suggestion>", "docs/cli-contract.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected generated AGENTS.md to contain %q", want)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	if Generate() != Generate() {
		t.Fatalf("expected Generate to be a pure function of Commands")
	}
}

// TestSkillsAreWellFormed: every scaffolded skill opens with a frontmatter
// block a harness can parse - the name it is discovered by and the
// single-line description it is chosen by.
func TestSkillsAreWellFormed(t *testing.T) {
	for _, s := range Skills() {
		rest, ok := strings.CutPrefix(s.Content, "---\nname: "+s.Name+"\n")
		if !ok {
			t.Fatalf("skill %q must open with frontmatter naming it, got:\n%s", s.Name, s.Content[:min(len(s.Content), 120)])
		}
		front, body, ok := strings.Cut(rest, "---\n")
		if !ok {
			t.Fatalf("skill %q frontmatter block does not close", s.Name)
		}
		if !strings.Contains(front, "description: ") {
			t.Fatalf("skill %q has no description: line, got:\n%s", s.Name, front)
		}
		if strings.Count(front, "\ndescription: ") > 1 || strings.TrimSpace(body) == "" {
			t.Fatalf("skill %q frontmatter/body is malformed", s.Name)
		}
		if !strings.HasPrefix(s.Path, ".claude/skills/") || !strings.HasSuffix(s.Path, "/SKILL.md") {
			t.Fatalf("skill %q path %q is not a harness-discoverable location", s.Name, s.Path)
		}
		if !strings.Contains(s.Path, "/"+s.Name+"/") {
			t.Fatalf("skill %q lives at %q - the directory must match the name", s.Name, s.Path)
		}
	}
}

// TestSkillsSplitByJob: the two skills exist so each has ONE unambiguous
// load trigger. The reference must carry the command table and not the
// workflow; the workspaces skill the reverse. If both grow the same
// content, a harness has no basis to choose and we are back to one skill.
func TestSkillsSplitByJob(t *testing.T) {
	ref, work := GenerateSkill(), GenerateWorkspacesSkill()

	if !strings.Contains(ref, "## Commands") {
		t.Fatalf("the runko skill must carry the command inventory")
	}
	if strings.Contains(ref, "## Workspaces: the writing discipline") ||
		strings.Contains(ref, "## What bites agents") {
		t.Fatalf("the runko skill must not duplicate the workflow - that is runko-workspaces' job")
	}

	for _, want := range []string{
		"## Workspaces: the writing discipline",
		"## The worktree is transparent",
		"## What bites agents",
	} {
		if !strings.Contains(work, want) {
			t.Fatalf("the runko-workspaces skill is missing %q", want)
		}
	}
	if strings.Contains(work, "## Commands") {
		t.Fatalf("the runko-workspaces skill must not duplicate the command table")
	}
	// Each points at the other, so an agent that loaded the wrong one is
	// one hop from the right one.
	if !strings.Contains(ref, "runko-workspaces") {
		t.Fatalf("the runko skill should point at the workflow skill")
	}
	if !strings.Contains(work, "`runko` skill") {
		t.Fatalf("the runko-workspaces skill should point at the reference skill")
	}
}

// TestWorkspacesSkillIsRepoAgnostic: this skill is scaffolded into EVERY
// Runko-managed monorepo, so nothing specific to the repo that generated
// it may leak in - no host, no build tool, no check or deploy command.
// Repo-specific teaching belongs in that repo's own AGENTS.md/CLAUDE.md.
func TestWorkspacesSkillIsRepoAgnostic(t *testing.T) {
	got := GenerateWorkspacesSkill()
	for _, leak := range []string{
		"runko.victornazzaro.com",                // this deployment's host
		"bazel", "gazelle", "make check", "sqlc", // this repo's build/check tooling
		"kubectl", "argo", "maas-dev", // this repo's deploy path
		"go test", "npm", "vitest", // any language's test runner
	} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(leak)) {
			t.Fatalf("the scaffolded workspaces skill leaks repo-specific %q - it ships to every Runko monorepo", leak)
		}
	}
}

func TestSkillPathIsProjectScoped(t *testing.T) {
	if !strings.HasPrefix(SkillPath, ".claude/skills/") || !strings.HasSuffix(SkillPath, "/SKILL.md") {
		t.Fatalf("SkillPath %q is not a harness-discoverable skill location", SkillPath)
	}
}

// baseCommand strips flags/usage args off a Usage string, e.g.
// "project create --name <n> --type <t>" -> "project create". Looks for
// " --" or " [" (flag markers preceded by a space) rather than a bare "-",
// since a command name itself can contain a hyphen (e.g. "report-check").
func baseCommand(usage string) string {
	cut := len(usage)
	for _, marker := range []string{" --", " [", " <"} {
		if i := strings.Index(usage, marker); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(usage[:cut])
}

// commandCellPattern matches a `| \`runko ...\` | ... |` table row in
// docs/cli-contract.md's --json output table specifically (anchored to line
// start so prose mentioning a command in passing, e.g. the "not yet
// implemented" sentence naming `runko auth`, isn't mistaken for a row in
// the table this test is meant to cross-check).
var commandCellPattern = regexp.MustCompile(`(?m)^\| ` + "`" + `(runko(?:-ci)? [a-z][a-z -]*)` + "`" + ` \|`)

// TestCommandsMatchesCLIContract cross-checks this package's Commands table
// against docs/cli-contract.md's own command table, so the two documents
// (one hand-maintained for humans, one rendered for agents) can't silently
// drift - a command added to one without the other is exactly the kind of
// gap this test exists to catch.
func TestCommandsMatchesCLIContract(t *testing.T) {
	data, err := os.ReadFile("../../docs/cli-contract.md")
	if err != nil {
		t.Fatalf("read docs/cli-contract.md: %v", err)
	}

	haveBase := map[string]bool{}
	for _, c := range Commands {
		haveBase[c.Binary+" "+baseCommand(c.Usage)] = true
	}

	matches := commandCellPattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatalf("expected to find at least one `runko ...` command cell in docs/cli-contract.md")
	}
	for _, m := range matches {
		cell := strings.TrimSpace(m[1])
		if !haveBase[cell] {
			t.Fatalf("docs/cli-contract.md documents %q but agentsmd.Commands has no matching entry - keep the two in sync", cell)
		}
	}
}

// TestGenerateTeachesRawGitIsTransportOnly: a fresh agent's default scm
// verb is git out of sheer training-data muscle memory - the generated
// orientation must say, up front, that raw git is transport only and name
// the native verbs (the same rule the workspace pre-commit nudge and the
// receive funnel's §6.9 rejection teach at their own moments).
func TestGenerateTeachesRawGitIsTransportOnly(t *testing.T) {
	got := Generate()
	for _, want := range []string{
		"Raw git is transport only",
		"`runko change create`",
		"never `git commit`/`git push`",
		"jj is for surgical history work",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected the generated orientation to contain %q", want)
		}
	}
}

// TestGenerateTeachesTheGatesThatBlockAgents: every gate below cost real
// dogfooding time precisely because it fails LATE and silently - a missing
// description blocks the merge long after a clean push, an approval an
// agent can never give itself, a whole-tree commit that swept in build
// output, a snapshot that ate the content of the next change. The
// generated teaching earns its place by naming them before they happen;
// this test keeps a future edit from quietly dropping one.
func TestGenerateTeachesTheGatesThatBlockAgents(t *testing.T) {
	got := Generate()
	for _, want := range []string{
		"runko change describe",          // unmergeable without it (§8.7)
		"You cannot approve",             // agents never satisfy the owner gate
		"affinity is fixed",              // decided at workspace create, not later
		"commits the WHOLE working tree", // change create is not staged-only
		"COMMITS the working tree onto",  // snapshot is not out-of-band
		"FULL content of every file",     // size caps over-count by design
		"refuses the ENTIRE series",      // one denied path fails the push
		"git sparse-checkout add <dir>",  // the fallback for an undeclared import
		"forks from your CURRENT HEAD",   // parallel lines must fork at the base
		"`runko workspace path <name>`",  // the escape hatch, when -w cannot serve
		"Never `cd` into a worktree",     // worktrees stay transparent (§12.7)
		"-w <name[@branch]>",             // ...and this is how
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected the generated teaching to contain %q", want)
		}
	}
}

// TestEverySurfaceTeachesTransparency: the cd habit spreads by being copied
// out of whatever an agent happened to read, so no generated surface may
// omit the rule - AGENTS.md and the workspaces skill both state it, and
// neither may print a cd into a worktree while doing so.
func TestEverySurfaceTeachesTransparency(t *testing.T) {
	for name, got := range map[string]string{
		"AGENTS.md":        Generate(),
		"runko-workspaces": GenerateWorkspacesSkill(),
	} {
		if !strings.Contains(got, "Never `cd` into a worktree") {
			t.Errorf("%s does not state the transparency rule", name)
		}
		if strings.Contains(got, "cd $(") || strings.Contains(got, "cd <") {
			t.Errorf("%s hands out a cd-into-worktree command while teaching not to", name)
		}
	}
}
