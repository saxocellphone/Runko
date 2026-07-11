package agentsmd

import (
	"fmt"
	"strings"
)

// Command is one CLI subcommand's entry in the generated inventory.
type Command struct {
	Binary      string // "runko" or "runko-ci"
	Usage       string // e.g. "project create --name <n> --type <t>"
	Description string
	JSONOutput  string // "" if the command has no --json output
}

// Commands is the CLI command inventory, kept in sync with
// docs/cli-contract.md's table by hand (both describe the same commands;
// this one renders into AGENTS.md, that one is the human-facing doc) - see
// TestCommandsMatchesCLIContract, which fails if the two ever drift.
var Commands = []Command{
	{"runko", "doctor [--install-hook] [--json]", "check remotes/hooks, print a cheat-sheet (§6.9)", "DoctorReport"},
	{"runko", "project create --name <n> --type <t> [--lang l] [--no-template] [--owners a,b] [--json]", "create a project from an intent (§10.1); --lang: go|python|ts|rust|java|cpp, others need --no-template", `{"name","path","rev"}`},
	{"runko", "project list --runkod-url <url> --token <t> [--json]", "list projects indexed at trunk (§10.3) - needs a live runkod", "[]IndexedProject"},
	{"runko", "change push [--remote origin] [--trunk main] [--no-sync] [--json]", "ensure a Change-Id trailer, auto-sync a stale base onto the trunk tip, push to refs/for/<trunk> (§11.5)", `{"change_id","ref"}`},
	{"runko", "change land --change <id> --runkod-url <url> --token <t> [--json]", "land a mergeable change (§13.5) - on requires_revalidation it syncs, re-pushes, waits for checks, retries - needs a live runkod", "land.Outcome"},
	{"runko", "change approve --change <id> --owner <ref> --by <who> --runkod-url <url> --token <t> [--json]", "record a required owner's approval (§13.5) - needs a live runkod", "MergeRequirements"},
	{"runko", "change list [--state open] --runkod-url <url> --token <t> [--json]", "list changes, newest first (§7.4) - needs a live runkod", "[]ChangeInfo"},
	{"runko", "change abandon --change <id> --runkod-url <url> --token <t> [--json]", "abandon an open change (§7.4) - needs a live runkod", "ChangeInfo"},
	{"runko", "change rerun-check --change <id> --name <check> --runkod-url <url> --token <t> [--json]", "reset a required check to queued + emit the rerun webhook (§14.4.2) - needs a live runkod", "MergeRequirements"},
	{"runko", "auth login --runkod-url <url> [--name <you>] [--token <t>]", "store a validated credential (0600); commands then need no --runkod-url/--token flags", "text"},
	{"runko", "auth status", "who the stored credential resolves to - needs a live runkod", "text"},
	{"runko", "auth logout", "forget the stored credential", "text"},
	{"runko", "change create [--dir .] [--json] -m <msg>", "commit ALL working-tree changes as one Change with its Change-Id (§7.4); push separately", `{"change_id"}`},
	{"runko", "change requirements [--change <Id>] [--dir .] [--json]", "the §13.5 gates for a Change (default: HEAD's Change-Id) - needs a live runkod", "checks.MergeRequirements"},
	{"runko", "change comment --change <id> -m <text> [--file <p> --line <n> --side head] [--reply-to <id>] [--json]", "anchored review comment bound to the current head (§13.4.1) - agents comment, never approve - needs a live runkod", "CommentInfo"},
	{"runko", "change comments [--change <Id>] [--dir .] [--json]", "list review threads, resolved/outdated marked (§13.4.1) - needs a live runkod", `{"comments":[CommentInfo],"next_page_token"}`},
	{"runko", "change resolve <comment-id> [--undo] [--change <Id>] [--json]", "resolve or reopen a review thread root (§13.4.1) - needs a live runkod", "CommentInfo"},
	{"runko", "change request-review <reviewer> [--change <Id>] [--json]", "ask a principal or group to review - they enter the derived attention set (§13.4.2) - needs a live runkod", `{"reviewer"}`},
	{"runko", "workspace create --name <n> --project <p> --by <who> --runkod-url <url> --token <t> [--json]", "worktree + sparse cone + registry row (§12.3) - needs a live runkod", "WorkspaceInfo"},
	{"runko", "workspace list --runkod-url <url> --token <t> [--json]", "list workstreams, their cones and base revisions - needs a live runkod", "[]WorkspaceInfo"},
	{"runko", "workspace attach <id> --runkod-url <url> --token <t> [--branch <b>] [--json]", "restore a workspace branch from its snapshot ref (§12.2) - needs a live runkod", "WorkspaceInfo"},
	{"runko", "workspace delete <id> --runkod-url <url> --token <t> [--json]", "delete the registry row + snapshot refs - refused while the workspace has open changes; owner-only (§12.2) - needs a live runkod", `{"deleted"}`},
	{"runko", "workspace snapshot [--dir .] [-m <msg>] [--json]", "make WIP durable: commit -> refs/workspaces/<id>/<branch> (§12.2)", `{"ref"}`},
	{"runko", "workspace branch <name> [--dir .] [--json]", "fork a parallel line of work: snapshots now target refs/workspaces/<id>/<name> (§12.2)", `{"ref"}`},
	{"runko", "workspace sync --runkod-url <url> --token <t> [--dir .] [--json]", "sync onto the trunk tip - fetch + rebase (jj-aware), record the new base (§12.3) - needs a live runkod", `{"base_revision"}`},
	{"runko", "org create --name <org> [--json]", "new org owning its own repo at /o/<org>/ (§7.1) - humans/operators only, agents are refused (§8.7)", "OrgInfo"},
	{"runko", "org list [--json]", "orgs your credential can reach (role + git URL) - needs a live runkod", "[]OrgInfo"},
	{"runko", "org add-member --org <org> --name <account> [--role member] [--json]", "grant an account org access (org admins/operators) - needs a live runkod", `{"org","name","role"}`},
	{"runko", "release create --project <p> [--version x.y.z] --runkod-url <url> --token <t> [--json]", "cut an immutable release (§14.10.3): server-minted annotated tag + changelog derived from landed changes since the previous release - needs a live runkod", "ReleaseInfo"},
	{"runko", "release list --project <p> --runkod-url <url> --token <t> [--json]", "the project's releases, newest first (§14.10.3) - needs a live runkod", "[]ReleaseInfo"},
	{"runko", "agents-md [--out AGENTS.md] [--json]", "regenerate this file from the CLI's own command inventory (§8.8)", `{"path"}`},
	{"runko-ci", "affected --base <rev> [--head HEAD] [--engine bazel]", "compute the affected project set for a base..head range (§13.3)", "affected.Result (always JSON)"},
	{"runko-ci", "checks --base <rev> [--head HEAD]", "resolve the affected closure's manifest-declared ci.checks for a CI executor (§14.9)", `{"run_everything","checks":[{"project","name","command"}]} (always JSON)`},
	{"runko-ci", "checkout --remote <url> --dest <dir> --rev <rev> [--json]", "partial-clone + sparse-checkout a rev for CI (§14.4.4)", `{"rev","dest"}`},
	{"runko-ci", "report-check --url <u> --name <n> --external-id <id> --reporter <r> [--json]", "POST a CheckRun result to the Checks API (§14.4.1)", `{"name","status","external_id"}`},
}

// Generate renders AGENTS.md's content: static orientation bullets (§8.8's
// example snippet), the Commands table, the exit-code contract, and the
// §6.5 structured-error shape - everything an agent needs to use this CLI
// correctly without reading source.
func Generate() string {
	var b strings.Builder

	b.WriteString("# Monorepo agent instructions (generated by `runko agents-md`)\n\n")
	b.WriteString("This file is generated from the CLI's own command inventory - regenerate\n")
	b.WriteString("with `runko agents-md` after a CLI change, don't hand-edit it.\n\n")

	b.WriteString("## Orientation\n\n")
	for _, line := range []string{
		"Use the `runko`/`runko-ci` CLI (`--json` output); do not full-clone.",
		"Prefer `runko project create` over hand-authoring PROJECT.yaml.",
		"Stay within workspace affinity; use `runko-ci checkout` for deps/prefetch.",
		"Open a Change (`runko change push`) before large refactors; respect who_owns.",
	} {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	b.WriteString("\n")

	b.WriteString("## Workspaces: the writing discipline\n\n")
	b.WriteString("Changes are born in workspaces - the server refuses a refs/for push\n")
	b.WriteString("that declares no registered workspace origin. The model:\n\n")
	for _, line := range []string{
		"One workspace = one WORKSTREAM (yours, long-lived). Do NOT mint one per change: `runko workspace create --name <stream> --project <p> --by <you>` once, keep using it.",
		"One branch = one stack = one reviewable line. `head` is the default; parallel work gets `runko workspace branch <name>` - the server refuses a second unrelated stack on one branch.",
		"Work INSIDE the workspace worktree: the sparse cone stops out-of-scope edits before the server has to, and `runko change push` stamps your origin claim automatically.",
		"Snapshot early and often: `runko workspace snapshot` - durable, secret-scanned WIP; a killed session loses nothing, `workspace attach` restores it.",
		"One task = one fresh workspace: start every new task with `runko workspace create`; never attach or bind a workspace you didn't create. Agent workspaces CLOSE when their last change lands or is abandoned - a push into a closed workspace is refused, so reuse is not a shortcut, it is a dead end.",
		"Submit: `runko change create -m <msg>` then `runko change push`. Stacks land BOTTOM-UP; a child is not mergeable until its parent lands.",
		"Trunk moved (land says revalidate): `runko change land` already runs the recovery loop itself (sync, re-push, wait, retry); `runko workspace sync` is the manual form. Never force.",
		"Done or dead: land it or `runko change abandon`. An abandoned change stays visible only while something still stacks on it - rebase dependents off it or reopen it by re-pushing.",
		"Never claim a workspace you don't own or didn't work in - origin claims are validated and owner-bound, and they drive review views.",
	} {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	b.WriteString("\n")

	b.WriteString("## Commands\n\n")
	b.WriteString("| Command | Does | `--json` output |\n")
	b.WriteString("|---|---|---|\n")
	for _, c := range Commands {
		jsonOut := c.JSONOutput
		if jsonOut == "" {
			jsonOut = "(none)"
		}
		fmt.Fprintf(&b, "| `%s %s` | %s | `%s` |\n", c.Binary, c.Usage, c.Description, jsonOut)
	}
	b.WriteString("\n")

	b.WriteString("## Exit codes\n\n")
	b.WriteString("| Code | Meaning |\n|---|---|\n")
	b.WriteString("| `0` | Success |\n")
	b.WriteString("| `1` | Command understood but failed - a structured error is printed to stderr |\n")
	b.WriteString("| `2` | Usage error - unknown command, wrong subcommand, or unparseable flags |\n\n")

	b.WriteString("## Structured errors (§6.5)\n\n")
	b.WriteString("Errors with a known cause print as:\n\n")
	b.WriteString("```text\n<message>\n  -> <suggestion>\n  see <doc_url>\n```\n\n")
	b.WriteString("to stderr, exit code `1`. Read `<suggestion>` before retrying - it names\n")
	b.WriteString("the exact next command or fix, not a generic hint.\n\n")

	b.WriteString("Full contract, including per-command flag details: `docs/cli-contract.md`.\n")

	return b.String()
}
