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
	{"runko", "doctor [--install-hook] [--json]", "check remotes/hooks, print a cheat-sheet", "DoctorReport"},
	{"runko", "project create --name <n> --type <t> [--lang l] [--no-template] [--owners a,b] [--json]", "create a project from an intent; --lang: go|python|ts|rust|java|cpp, others need --no-template", `{"name","path","rev"}`},
	{"runko", "project list --runkod-url <url> --token <t> [--json]", "list projects indexed at trunk - needs a live runkod", "[]IndexedProject"},
	{"runko", "project delete --name <p> [--json]", "open the deletion change: subtree removed, every other manifest's edges to it stripped - needs a live runkod", `{"change_id","title"}`},
	{"runko", "change push [-w <ws>] [--remote origin] [--trunk main] [--no-sync] [--json]", "ensure a Change-Id trailer, auto-sync a stale base onto the trunk tip, push to refs/for/<trunk>", `{"change_id","ref"}`},
	{"runko", "change land --change <id> --runkod-url <url> --token <t> [--json]", "land a mergeable change - on requires_revalidation it syncs, re-pushes, waits for checks, retries - needs a live runkod", "land.Outcome"},
	{"runko", "change approve --change <id> --owner <ref> --by <who> --runkod-url <url> --token <t> [--json]", "record a required owner's approval - needs a live runkod", "MergeRequirements"},
	{"runko", "change list [--state open] --runkod-url <url> --token <t> [--json]", "list changes, newest first - needs a live runkod", "[]ChangeInfo"},
	{"runko", "change abandon --change <id> --runkod-url <url> --token <t> [--json]", "abandon an open change - needs a live runkod", "ChangeInfo"},
	{"runko", "change describe [--change <Id>] [--description <text>] [--test-plan <text>] [--json]", "set the review summary on an open change (default: HEAD's Change-Id): what it does and how it was verified - agents SHOULD set this after push; shows on the change page, feeds release changelogs; an omitted flag preserves the stored value, an explicit \"\" clears - needs a live runkod", "ChangeInfo"},
	{"runko", "change rerun-check --change <id> --name <check> --runkod-url <url> --token <t> [--json]", "reset a required check to queued + emit the rerun webhook - needs a live runkod", "MergeRequirements"},
	{"runko", "auth signup --runkod-url <host> --name <you> --org <o> --create|--join [--invite-code <c>]", "first contact: register the account, create or join the org, and store the credential pointed at its mount - signup IS login; refusals are the server's structured shapes", "text"},
	{"runko", "auth login --runkod-url <url> [--name <you>] [--token <t>]", "store a validated credential (0600); commands then need no --runkod-url/--token flags", "text"},
	{"runko", "auth status", "who the stored credential resolves to - needs a live runkod", "text"},
	{"runko", "auth logout", "forget the stored credential", "text"},
	{"runko", "auth git-credential <action>", "git's credential-helper protocol (workspace stores stamp it): answers `get` from the INVOKING principal's stored login, own host only - called by git, not humans", "credential key=value lines"},
	{"runko", "change create [--dir . | -w <ws>] [--allow-large] [--json] -m <msg>", "commit ALL working-tree changes as one Change with its Change-Id; push separately. Refuses large/executable untracked files as suspected build artifacts unless --allow-large", `{"change_id"}`},
	{"runko", "change amend [--dir . | -w <ws>] [-m <msg>] [--json]", "fold the working tree into HEAD's existing Change (keeps its Change-Id) - the native `git commit --amend`, with the identity fallback so it works with no configured git author", `{"change_id"}`},
	{"runko", "change requirements [--change <Id>] [--dir . | -w <ws>] [--json]", "the merge gates for a Change (default: HEAD's Change-Id) - needs a live runkod", "checks.MergeRequirements"},
	{"runko", "change comment --change <id> -m <text> [--file <p> --line <n> --side head] [--reply-to <id>] [--json]", "anchored review comment bound to the current head - agents comment, never approve - needs a live runkod", "CommentInfo"},
	{"runko", "change comments [--change <Id>] [--dir . | -w <ws>] [--json]", "list review threads, resolved/outdated marked - needs a live runkod", `{"comments":[CommentInfo],"next_page_token"}`},
	{"runko", "change resolve <comment-id> [--undo] [--change <Id>] [--json]", "resolve or reopen a review thread root - needs a live runkod", "CommentInfo"},
	{"runko", "change request-review <reviewer> [--change <Id>] [--json]", "ask a principal or group to review - they enter the derived attention set - needs a live runkod", `{"reviewer"}`},
	{"runko", "workspace create --name <n> --project <p> [--by <who>] [--json]", "worktree + sparse cone + registry row; --by defaults to the stored login - needs a live runkod", "WorkspaceInfo"},
	{"runko", "workspace list --runkod-url <url> --token <t> [--json]", "list workstreams, their cones and base revisions - needs a live runkod", "[]WorkspaceInfo"},
	{"runko", "workspace attach <id> --runkod-url <url> --token <t> [--branch <b>] [--json]", "restore a workspace branch from its snapshot ref - needs a live runkod", "WorkspaceInfo"},
	{"runko", "workspace delete <id> --runkod-url <url> --token <t> [--json]", "delete the registry row + snapshot refs - refused while the workspace has open changes; owner-only - needs a live runkod", `{"deleted"}`},
	{"runko", "change automerge --change <id> [--disable] [--json]", "arm the when-ready land: the server lands it once the gates go green, attributed to the armer; survives amends - needs a live runkod", `{"ChangeKey", "Automerge", "AutomergeBy"}`},
	{"runko", "agent create --task <slug> --runkod-url <url> --token <t> [--ttl 8h] [--json]", "mint an ephemeral task identity (agent-<task>-<suffix>, token shown ONCE; agents cannot mint) - needs a live runkod", "AgentIdentity"},
	{"runko", "agent list --runkod-url <url> --token <t> [--json]", "live/expired/revoked task identities - needs a live runkod", "[]AgentIdentity"},
	{"runko", "agent revoke <name> --runkod-url <url> --token <t> [--json]", "immediate credential kill; the row survives for attribution - needs a live runkod", `{"revoked"}`},
	{"runko", "agent event --kind <k> --detail <text> [--from-hook] [--session <id>] [--json]", "report one activity event (read|edit|command|search|note) to the workspace's live feed; --from-hook derives it from a post-tool-use hook JSON on stdin; RUNKO_RUNKOD_URL/RUNKO_TOKEN env fallback - needs a live runkod", `{"recorded"}`},
	{"runko", "agent hooks [--install [--dir . | -w <ws>]] [--json]", "print the harness hooks snippet wiring post-tool-use calls to `agent event --from-hook`; --install merges it into the worktree's .claude/settings.local.json (opt-in, snapshot-excluded) - local only", `hooks JSON snippet; --install: {"path","installed"}`},
	{"runko", "workspace snapshot [--dir . | -w <ws>] [-m <msg>] [--json]", "make WIP durable: commit -> refs/workspaces/<id>/<branch>", `{"ref"}`},
	{"runko", "workspace watch [--dir . | -w <ws>] [--interval 15s] [--once] [--json]", "auto-snapshot loop feeding the live workspace view: out-of-band commits, never touches HEAD/index - run it in the background while you work", "NDJSON {\"ref\",\"sha\"} per push"},
	{"runko", "workspace branch <name> [--dir . | -w <ws>] [--json]", "fork a parallel line of work: snapshots now target refs/workspaces/<id>/<name>", `{"ref"}`},
	{"runko", "workspace sync --runkod-url <url> --token <t> [--dir . | -w <ws>] [--json]", "sync onto the trunk tip - fetch + rebase (jj-aware), record the new base - needs a live runkod", `{"base_revision"}`},
	{"runko", "workspace path [<name>] [--json]", "print a workspace's local directory - for what -w cannot cover: editing files, or a verb that takes only --repo; no name: the current checkout answers for itself - local only", `{"workspace","branch","path"}`},
	{"runko", "workspace gc [--apply] [--idle <dur>] [--scan <store>] [--json]", "reclaim materializations whose durable state is server-side (closed + synced with the snapshot ref): plan-only by default, fail-closed skips name their reason; --scan adopts pre-registry worktrees - needs a live runkod", "[]GCCandidate"},
	{"runko", "org create --name <org> [--no-switch] [--json]", "new org at /o/<org>/, genesis-seeded and ready to work in; rebinds the stored login to it unless --no-switch - humans/operators only, agents are refused", "OrgInfo"},
	{"runko", "org list [--json]", "orgs your credential can reach (role + git URL) - needs a live runkod", "[]OrgInfo"},
	{"runko", "org add-member --org <org> --name <account> [--role member] [--json]", "grant an account org access (org admins/operators) - needs a live runkod", `{"org","name","role"}`},
	{"runko", "org bootstrap [--json]", "ownerless org (nothing can land under default-deny)? opens the self-landable root-OWNERS change naming the caller (governance retrofit) - humans/org admins only, agents suggest it to a human", `{"seeded_genesis","change_id","title"}`},
	{"runko", "org agent-policy <get|set|reset> [--allow-all-paths|--allow-workflows|--allow-owners|--can-land|--denylist <globs>|--from-json <src>|--org <name>]", "read (get), set (read-modify-write), or reset an org's agent policy - operator only. This is how an operator lets an org's agents edit paths the default denies (OWNERS/PROJECT.yaml, CI workflows); set warns when it loosens workflows or enables can-land", `{"org","overridden","policy"}`},
	{"runko", "github connect --repo <owner/name> [--json]", "wire the org to a GitHub repo in one call: the server verifies its GitHub App can push (repo reachable, App installed, token minted), persists the wiring in org settings, and arms the mirror AND native CI dispatch live (2026-07-16/17) - org admins/operators; agents are refused", "GithubConnectResult"},
	{"runko", "github status [--json]", "the org's outbound mirror state: target URL, per-ref cursors, freezes, last error - needs a live runkod", "MirrorStatus"},
	{"runko", "release create --project <p> [--version x.y.z] --runkod-url <url> --token <t> [--json]", "cut an immutable release: server-minted annotated tag + changelog derived from landed changes since the previous release - needs a live runkod", "ReleaseInfo"},
	{"runko", "release list --project <p> --runkod-url <url> --token <t> [--json]", "the project's releases, newest first - needs a live runkod", "[]ReleaseInfo"},
	{"runko", "agents-md [--out AGENTS.md] [--json]", "regenerate this file AND the agent skill (.claude/skills/runko/SKILL.md) from the CLI's own command inventory", `{"path","skill_path"}`},
	{"runko", "ci init [--images] [--force] [--json]", "scaffold the generic CI/CD GitHub Actions workflows into .github/workflows/: a local file-writer copying templates/ci/*.yml (which download runko-ci and read every project/check/image/registry fact from the tree); --images adds the CD workflow. Then wire ci.checks + `github connect` + the RUNKO_URL/RUNKO_CI_TOKEN secrets", "ciInitResult"},
	{"runko", "self-update [--check] [--repo owner/name] [--json]", "replace this binary with the rolling cli-latest GitHub release build - checksum-verified, atomic swap; --check only reports; `update` is an alias", "UpdateOutcome"},
	{"runko", "version [--json]", "which binary is this: vcs revision + build time + toolchain from the Go build stamp (doctor reprints it first); report this when behavior differs from the docs", "BuildIdentity"},
	{"runko-ci", "affected --base <rev> [--head HEAD] [--engine bazel]", "compute the affected project set for a base..head range", "affected.Result (always JSON)"},
	{"runko-ci", "checks --base <rev> [--head HEAD]", "resolve the affected closure's manifest-declared ci.checks for a CI executor", `{"run_everything","checks":[{"project","name","command"}]} (always JSON)`},
	{"runko-ci", "images --base <rev> [--head HEAD]", "resolve which deployable images a base..head range must rebuild, with build config, for a CI executor", `{"run_everything","images":[{"name","image_ref","context","dockerfile","build_args"}]} (always JSON)`},
	{"runko-ci", "binaries --base <rev> [--head HEAD]", "resolve which rolling binary releases a base..head range must republish, from the tree's deploy.binaries declarations, for a CI executor", `{"run_everything","releases":[{"tag","binaries":[{"name","path"}]}]} (always JSON)`},
	{"runko-ci", "checkout --remote <url> --dest <dir> --rev <rev> [--json]", "partial-clone + sparse-checkout a rev for CI", `{"rev","dest"}`},
	{"runko-ci", "report-check --url <u> --name <n> --external-id <id> --reporter <r> [--json]", "POST a CheckRun result to the Checks API", `{"name","status","external_id"}`},
}

// SkillPath is where GenerateSkill's output lives in a managed monorepo's
// tree: the project-scoped location skill-loading harnesses (Claude Code
// and compatible) discover skills at. A tree path, not a filesystem path -
// callers writing to disk convert with filepath.FromSlash.
const SkillPath = ".claude/skills/runko/SKILL.md"

// WorkspacesSkillPath is where GenerateWorkspacesSkill's output lives. The
// two skills are split by JOB, not by size: this one answers "how do I work
// here" and is loaded before any change; SkillPath's answers "what exactly
// do I type" and is loaded when a flag or a --json shape is in question.
// One trigger each, so a harness never has to guess which to pull in.
const WorkspacesSkillPath = ".claude/skills/runko-workspaces/SKILL.md"

// Skill is one generated, loadable agent skill: where it belongs in a
// managed monorepo's tree, and the content that goes there. Every skill
// runko scaffolds is generated - a repo that has evolved its own keeps it
// (the tree is the source of truth), but nothing here is hand-maintained.
type Skill struct {
	Name    string // frontmatter name, and the directory under .claude/skills
	Path    string // tree path, forward slashes
	Content string
}

// Skills is every skill `runko agents-md` writes into a managed monorepo.
// Callers that install or refresh skills range over this rather than naming
// the paths, so adding a skill is a one-line change here.
func Skills() []Skill {
	return []Skill{
		{Name: "runko", Path: SkillPath, Content: GenerateSkill()},
		{Name: "runko-workspaces", Path: WorkspacesSkillPath, Content: GenerateWorkspacesSkill()},
	}
}

// Generate renders AGENTS.md's content: the orientation bullets (§8.8's
// example snippet), the workspace discipline, the traps, the Commands
// table, the exit-code contract, and the §6.5 structured-error shape -
// everything an agent needs to use this CLI correctly without reading
// source. AGENTS.md is the ambient file, so it carries all of it; the two
// skills below carve the same content by job.
func Generate() string {
	var b strings.Builder

	b.WriteString("# Monorepo agent instructions (generated by `runko agents-md`)\n\n")
	b.WriteString("Generated from the CLI's own command inventory - regenerate with `runko agents-md` after a CLI change, don't hand-edit.\n\n")

	writeOrientation(&b)
	writeDiscipline(&b)
	writeTransparency(&b, false)
	writeTraps(&b)
	writeCommands(&b)
	writeContract(&b)

	return b.String()
}

// writeOrientation is the five-line "you are not in a normal git repo"
// preamble - the first thing any surface says.
func writeOrientation(b *strings.Builder) {
	b.WriteString("## Orientation\n\n")
	for _, line := range []string{
		"Use the `runko`/`runko-ci` CLI (`--json` output); do not full-clone.",
		"Raw git is transport only (clone/fetch): commit with `runko change create`, submit with `runko change push` - never `git commit`/`git push`. jj is for surgical history work (`jj edit`/`jj split`), not the basic loop.",
		"Prefer `runko project create` over hand-authoring PROJECT.yaml.",
		"Stay within workspace affinity; use `runko-ci checkout` for deps/prefetch.",
		"Open a Change (`runko change push`) before large refactors; respect who_owns.",
	} {
		fmt.Fprintf(b, "- %s\n", line)
	}
	b.WriteString("\n")
}

// writeDiscipline is the workspace model: the rules that decide whether a
// push is accepted at all, in the order they bite.
func writeDiscipline(b *strings.Builder) {
	b.WriteString("## Workspaces: the writing discipline\n\n")
	b.WriteString("Changes are born in workspaces - the server refuses a refs/for push\n")
	b.WriteString("that declares no registered workspace origin. The model:\n\n")
	for _, line := range []string{
		"One workspace = one TASK: `runko workspace create --name <task> --project <p>` starts it, and it is done when its changes are. A task holds as many changes and branches as it needs - what it may not do is outlive itself: an agent workspace CLOSES when its last change lands or is abandoned, and further pushes into it are refused. Never attach or bind one you did not create; reuse is a dead end, not a shortcut.",
		"Name every project you will touch at CREATE time - affinity is fixed there and no verb widens it later. Root-owned paths (AGENTS.md, Makefile, .claude/**, top-level config) belong to the ROOT project: add it too, or the push is refused whole. Widening means deleting and recreating the workspace, which only works while no change is open.",
		"One branch = one stack = one reviewable line. `head` is the default; parallel work gets `runko workspace branch <name>` - the server refuses a second unrelated stack on one branch. That verb forks from your CURRENT HEAD, not the workspace base, so fork every planned parallel line right after `workspace create`, before you commit anything.",
		"Run the verbs from wherever you already are with `-w <workspace[@branch]>` - never `cd` into a worktree and never hand a human its path (see the section below). Wherever you run them, `runko change push` stamps your origin claim automatically; a push's write scope is your AFFINITY, not the cone, so it is refused WHOLE if it touches a dependency dir the cone materialized only for reading.",
		"The cone auto-expands to your `--project` dirs, the root files, AND those projects' compile-time closure (transitive deps + `consumes` contract surfaces), so building and testing across them just works. Those dependency dirs are READ-ONLY by convention: affinity gates what a push TOUCHES, so a change that edits one is refused whole - revert stray edits before you push or snapshot. (`git sparse-checkout add <dir>` still covers a rare import no manifest declares.)",
		"Snapshot early and often: `runko workspace snapshot -w <ws>` - durable, secret-scanned WIP; a killed session loses nothing, `workspace attach` restores it. Better: keep `runko workspace watch -w <ws>` running in the background - it auto-snapshots out-of-band (never touches HEAD or the index) and your work stays live on the workspace page.",
		"Report what you are doing: `runko agent hooks --install -w <ws>` wires your harness's post-tool-use hook to `runko agent event --from-hook` in one command (plain `agent hooks` prints the snippet for other harnesses) and the workspace page shows your reads/edits/commands LIVE. Observability only - it never gates anything; export RUNKO_RUNKOD_URL/RUNKO_TOKEN in the harness env and it just works. The server nudges a workspace's first change push that never streamed.",
		"Work under a TASK identity, never a shared credential: if you hold a human/admin credential, demote yourself first - `runko agent create --task <slug>` - and use the returned name:token for everything (git remote and --token alike). Attribution, policy, and workspace ownership then follow the task; the credential dies by TTL on its own.",
		"Stack small changes, never one big one: one reviewable step per change - a fresh `runko change create` per step stacks naturally (`jj split` is the surgical fix for one that grew too big); a single `runko change push` pushes the whole stack. Size caps are PER CHANGE - a big change is refused where the same work as a stack passes - and smaller changes scope required checks narrower, so they land faster.",
		"Stack only what DEPENDS: orthogonal changes go on PARALLEL workspace branches (`runko workspace branch <name>`), where they review and land independently - stacked, the upper one needlessly waits out the lower. The push output nudges you when a stacked step touches nothing its parent touches.",
		"Submit: `runko change create -m <msg>` then `runko change push`. Stacks land BOTTOM-UP; a child is not mergeable until its parent lands.",
		"Describe every change the moment it is pushed: `runko change describe --change <full-Change-Id> --description <what and why> --test-plan <how you verified>`. An agent-authored change without one is NOT mergeable - the push still succeeds, so the omission surfaces much later as a stuck gate.",
		"You cannot approve - not your own change, not anyone else's. Arm automerge, then TELL A HUMAN which Change-Ids need `runko change approve --owner <ref>`; that approval is the gate your work waits on, and no amount of re-pushing moves it.",
		"Trunk moved (land says revalidate): `runko change land` already runs the recovery loop itself (sync, re-push, wait, retry); `runko workspace sync` is the manual form. Never force.",
		"Do not poll for green: after pushing, arm `runko change automerge --change <id>` and MOVE ON - the server lands it the moment checks and approvals pass, under your name. Poll-and-land loops are the anti-pattern automerge exists to delete.",
		"Done or dead: land it or `runko change abandon`. An abandoned change stays visible only while something still stacks on it - rebase dependents off it or reopen it by re-pushing.",
		"Never claim a workspace you don't own or didn't work in - origin claims are validated and owner-bound, and they drive review views.",
	} {
		fmt.Fprintf(b, "- %s\n", line)
	}
	b.WriteString("\n")
}

// writeTransparency is the §12.7 rule that the materialization is ours and
// the NAME is yours. It gets its own section rather than a bullet because
// it is the one rule whose violation is invisible: a cd "works", so nothing
// refuses it, and the habit spreads by being copied out of transcripts.
// verbose adds the full flag inventory - worth the lines in the skill an
// agent loads before working, too many for the ambient file.
func writeTransparency(b *strings.Builder, verbose bool) {
	b.WriteString("## The worktree is transparent - address workspaces by NAME\n\n")
	b.WriteString("`workspace create` materializes a worktree somewhere on this machine;\n")
	b.WriteString("WHERE is an implementation detail. The workspace NAME is the\n")
	b.WriteString("handle: `-w <name[@branch]>` runs a checkout verb against that\n")
	b.WriteString("workspace's materialization from ANYWHERE - your repo root included.\n\n")
	b.WriteString("```\nrunko change create -w <ws> -m \"<what and why>\"\n")
	b.WriteString("runko change push -w <ws>\nrunko workspace watch -w <ws> &\n```\n\n")
	b.WriteString("**Never `cd` into a worktree, and never hand a human its path.** A `cd`\n")
	b.WriteString("silently rebinds the working directory for everything that follows, and\n")
	b.WriteString("a path passed onward teaches someone to depend on a layout that is ours\n")
	b.WriteString("to change. `runko workspace path <name>` is the escape hatch.\n\n")
	if !verbose {
		return
	}
	b.WriteString("The full `-w` set: `change`\n")
	b.WriteString("create/amend/push/requirements/land/describe/comment/comments/resolve/\n")
	b.WriteString("request-review, `workspace` snapshot/watch/branch/sync, and `agent\n")
	b.WriteString("hooks`. Two groups deliberately lack it: the server-side verbs\n")
	b.WriteString("(`automerge`, `approve`, `abandon`, `list`) key off the Change-Id and\n")
	b.WriteString("never touch a checkout at all, while `project create`, `doctor` and\n")
	b.WriteString("`agents-md` reach one only through `--repo` - for those,\n")
	b.WriteString("`--repo \"$(runko workspace path <ws>)\"` is the transparent form.\n")
	b.WriteString("Passing `-w` together with a non-`.` `--dir`/`--repo` is a\n")
	b.WriteString("contradiction and is refused.\n\n")
	b.WriteString("Editing files is the one thing that genuinely happens at a path: ask\n")
	b.WriteString("for it with `runko workspace path <ws>` instead of hardcoding it, and\n")
	b.WriteString("keep every `runko` invocation on `-w`.\n\n")
}

// writeTraps is the failure catalogue - each entry a refusal or silent trap
// that cost real time, with the fix that clears it.
func writeTraps(b *strings.Builder) {
	b.WriteString("## What bites agents (learned the expensive way)\n\n")
	b.WriteString("Each of these is a real refusal or silent trap, with the fix that clears it:\n\n")
	for _, line := range []string{
		"`runko change create` commits the WHOLE working tree, not what you staged. Read plain `git status --porcelain -uall` (no pathspec) before every one, and keep build output out of the tree - `$(git rev-parse --git-common-dir)/info/exclude` is the branch-independent place for it. A swept-in build directory does not fail as a content error; it fails as an opaque transport error at push time.",
		"`runko workspace snapshot` COMMITS the working tree onto your branch (only `workspace watch` is out-of-band). Never snapshot content you are about to `change create` - the content lands in a Change-Id-less commit and `change create` then reports no changes. Snapshot mid-edit for durability; `change push` snapshots for you anyway.",
		"A rejected SNAPSHOT push is not a rejected change: read past it to the `refs/for/...` line. Auto-snapshots carry the whole workspace delta, so they can trip a size cap or a stale-base policy diff while the change push itself is fine. Syncing onto the trunk tip clears the noise.",
		"Size caps count the FULL content of every file a change touches, not its diff. One line edited in a large file can exceed a per-change cap, and the refusal's advice is literal: split the work into a stack so the big files ride their own change.",
		"Ownership and manifests are the human lane BY DEFAULT: unless an operator has loosened this org's agent policy, an agent push that touches OWNERS or an existing PROJECT.yaml is refused - and it refuses the ENTIRE series, not just that commit. Where it still applies, carve those edits into their own change for a human to push, landing after the code that needs them; the same holds for other denylisted paths (e.g. CI workflows) your org's policy may restrict.",
		"Generated files are regenerated, never hand-edited, and the regeneration belongs in the SAME change as the edit that invalidated it. Split across a stack, the intermediate commit is one that never built.",
		"Every checkout authenticates as whoever its stored credential names. Never reuse another identity's checkout or credential: the push authenticates as THEM, and the funnel then rejects your workspace claim with an ownership error that reads like a server bug.",
	} {
		fmt.Fprintf(b, "- %s\n", line)
	}
	b.WriteString("\n")
}

// writeCommands renders the inventory table - the mechanical half, the one
// surface that must list every command verbatim.
func writeCommands(b *strings.Builder) {
	b.WriteString("## Commands\n\n")
	b.WriteString("| Command | Does | `--json` output |\n")
	b.WriteString("|---|---|---|\n")
	for _, c := range Commands {
		jsonOut := c.JSONOutput
		if jsonOut == "" {
			jsonOut = "(none)"
		}
		fmt.Fprintf(b, "| `%s %s` | %s | `%s` |\n", c.Binary, c.Usage, c.Description, jsonOut)
	}
	b.WriteString("\n")
}

// writeContract is the exit-code table and the §6.5 error shape: how to
// read a failure, wherever it came from.
func writeContract(b *strings.Builder) {
	b.WriteString("## Exit codes\n\n")
	b.WriteString("| Code | Meaning |\n|---|---|\n")
	b.WriteString("| `0` | Success |\n")
	b.WriteString("| `1` | Command understood but failed - a structured error is printed to stderr |\n")
	b.WriteString("| `2` | Usage error - unknown command, wrong subcommand, or unparseable flags |\n\n")

	b.WriteString("## Structured errors\n\n")
	b.WriteString("Errors with a known cause print as:\n\n")
	b.WriteString("```text\n<message>\n  -> <suggestion>\n  see <doc_url>\n```\n\n")
	b.WriteString("to stderr, exit code `1`. Read `<suggestion>` before retrying - it names\n")
	b.WriteString("the exact next command or fix, not a generic hint.\n\n")

	b.WriteString("Full contract, including per-command flag details: `docs/cli-contract.md`.\n")
}

// GenerateSkill renders the command-reference skill: what to type. It is
// deliberately NOT the whole of AGENTS.md - the workflow discipline lives
// in GenerateWorkspacesSkill, so asking "what are this command's flags"
// does not drag the entire model into context, and each skill has one
// unambiguous load trigger (§8.8's "reference prompts / skill files ...
// generated per monorepo").
func GenerateSkill() string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: runko\n")
	b.WriteString("description: The runko/runko-ci command inventory for this Runko-managed monorepo - every command's usage, what it does, its --json output shape, the exit codes, and the structured-error format. Load it when you need a command's exact flags or output; for the workspace/change/land workflow load the runko-workspaces skill.\n")
	b.WriteString("---\n\n")
	b.WriteString("# Runko CLI reference (generated by `runko agents-md`)\n\n")
	b.WriteString("Generated from the CLI's own command inventory - regenerate with\n")
	b.WriteString("`runko agents-md` after a CLI change, don't hand-edit it. The workflow\n")
	b.WriteString("these commands serve is the `runko-workspaces` skill; this is the\n")
	b.WriteString("reference.\n\n")
	writeOrientation(&b)
	writeCommands(&b)
	writeContract(&b)
	return b.String()
}

// GenerateWorkspacesSkill renders the discipline skill: how to work here.
// Everything in it is true of ANY Runko-managed monorepo - no repo's URL,
// build tool, check names or deploy path appears, because this file is
// scaffolded into every one of them. Repo-specific instructions belong in
// that repo's own CLAUDE.md/AGENTS.md, which sit alongside this.
func GenerateWorkspacesSkill() string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: runko-workspaces\n")
	b.WriteString("description: Use BEFORE making any change in this Runko-managed monorepo - the workspace/change/land workflow, one workspace per task, addressing workspaces by name instead of cd-ing into worktrees, stacking, and the refusals that block a push. Load it before creating a workspace, committing, pushing, or landing.\n")
	b.WriteString("---\n\n")
	b.WriteString("# Working in a Runko monorepo (generated by `runko agents-md`)\n\n")
	b.WriteString("Generated - regenerate with `runko agents-md`, don't hand-edit it. This\n")
	b.WriteString("is the workflow; `runko --help` and the `runko` skill are the command\n")
	b.WriteString("reference. Everything here holds in any Runko monorepo: this repo's own\n")
	b.WriteString("build, check and deploy commands live in its AGENTS.md/CLAUDE.md.\n\n")
	b.WriteString("The server ENFORCES most of what follows. Following it is the\n")
	b.WriteString("difference between one clean loop and an afternoon of structured\n")
	b.WriteString("rejections.\n\n")
	writeDiscipline(&b)
	writeTransparency(&b, true)
	writeTraps(&b)
	return b.String()
}
