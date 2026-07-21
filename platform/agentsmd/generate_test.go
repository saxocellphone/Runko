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

// TestGenerateSkillWrapsGenerate: the skill is Generate()'s body under
// loadable-skill frontmatter - byte-identical below the divider, so the two
// teaching surfaces cannot drift apart.
func TestGenerateSkillWrapsGenerate(t *testing.T) {
	got := GenerateSkill()
	if !strings.HasPrefix(got, "---\nname: runko\n") {
		t.Fatalf("expected skill frontmatter to open with the skill name, got:\n%s", got[:min(len(got), 120)])
	}
	// Frontmatter = everything through the second "---" line; exactly one
	// name and one single-line description between the markers.
	rest, ok := strings.CutPrefix(got, "---\n")
	if !ok {
		t.Fatalf("expected the skill to open a frontmatter block")
	}
	front, body, ok := strings.Cut(rest, "---\n")
	if !ok {
		t.Fatalf("expected the skill frontmatter block to close")
	}
	if !strings.Contains(front, "\ndescription: ") {
		t.Fatalf("expected a description: line in the skill frontmatter, got:\n%s", front)
	}
	if strings.TrimPrefix(body, "\n") != Generate() {
		t.Fatalf("expected the skill body to be Generate()'s output verbatim")
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
		"git sparse-checkout add <dir>",  // the cone does not follow build deps
		"forks from your CURRENT HEAD",   // parallel lines must fork at the base
		"`runko workspace path <name>`",  // worktrees stay transparent
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected the generated teaching to contain %q", want)
		}
	}
}
