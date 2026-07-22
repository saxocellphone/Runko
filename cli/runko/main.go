// Command runko is the human/agent-facing CLI (docs/design.md §17.1).
//
// Implemented stage 9 (§28.3), operating purely on the local repo - no
// control plane required: doctor, project create, change push, agents-md.
// `change land` (stage 11b) is the one command in this file that DOES need
// a live control plane - and, unlike auth/workspace/mcp below, has one to
// talk to as of this session: runkod. Still stubbed because no live
// control plane is reachable in this sandbox to round-trip against: auth
// login, workspace create/attach, change create/requirements, mcp serve -
// all since implemented; auth login (2026-07-08) closed the list.
//
// Exit codes (docs/cli-contract.md, added in the §8.3 CLI-first audit):
// 0 success, 1 a recognized command failed (structured error printed to
// stderr), 2 usage error (unknown command, wrong subcommand, missing
// positional keyword) - flag-parsing errors already exit 2 via stdlib
// flag.ExitOnError, this file's usageError type extends the same code to
// this package's own pre-flag-parsing usage checks.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saxocellphone/runko/platform/land"
	"text/tabwriter"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/agentsmd"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/mcp"
	"github.com/saxocellphone/runko/platform/project"
)

// usageError marks an error as a usage problem (wrong/missing subcommand
// keyword) rather than a recognized command failing at runtime - main()
// maps it to exit code 2, everything else to exit code 1.
type usageError string

func (e usageError) Error() string { return string(e) }

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "project":
		err = cmdProject(os.Args[2:])
	case "change":
		err = cmdChange(os.Args[2:])
	case "agents-md":
		err = cmdAgentsMD(os.Args[2:])
	case "agent":
		err = cmdAgent(os.Args[2:])
	case "workspace":
		err = cmdWorkspace(os.Args[2:])
	case "mcp":
		err = cmdMCP(os.Args[2:])
	case "auth":
		err = cmdAuth(os.Args[2:])
	case "org":
		err = cmdOrg(os.Args[2:])
	case "github":
		err = cmdGithub(os.Args[2:])
	case "release":
		err = cmdRelease(os.Args[2:])
	case "self-update", "update": // update: the verb people type first
		err = cmdSelfUpdate(os.Args[2:])
	case "version", "-v", "--version":
		err = cmdVersion(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "runko: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "runko: %v\n", err)
		var ue usageError
		if errors.As(err, &ue) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: runko <command> [flags]

commands (operate on the local repo only):
  doctor                          check remotes/hooks, print a cheat-sheet (§6.9) [--json]
  project create --name <n> ...   create a project from an intent, on top of HEAD (§10.1) [--json]
  change push [-w <ws[@branch]>]  push HEAD to refs/for/<trunk> for review (§11.5) [--json]
  agents-md                       (re)generate AGENTS.md + the agent skill (.claude/skills/runko/) teaching this CLI to agents (§8.8) [--json]
  self-update [--check]           replace this binary with the rolling cli-latest GitHub release build, checksum-verified (§17.1) [--json]
  version                         which binary is this: vcs revision + build time + toolchain, from the Go build stamp [--json]

commands (need a live runkod instance, §28.3 stages 11b/11c/12b):
  project list --runkod-url <url> --token <t>                 list projects indexed at trunk (§10.3) [--json]
  change land --change <id> --runkod-url <url> --token <t>   land a mergeable Change (§13.5) [--json]
  change approve --change <id> --owner <ref> --by <who> --runkod-url <url> --token <t>   record an owner approval (§13.5) [--json]
  change list [--state open] --runkod-url <url> --token <t>  list changes, newest first (§7.4) [--json]
  change abandon --change <id> --runkod-url <url> --token <t>   abandon an open change (§7.4) [--json]
  change automerge --change <id> [--disable]                arm the when-ready land: the server lands it once checks+approvals go green [--json]
  change rerun-check --change <id> --name <check> --runkod-url <url> --token <t>   request a check re-run (§14.4.2) [--json]
  workspace create --name <n> --project <p>... [--new-path <dir>...] [--by <who>] [--jj]   worktree + sparse cone + registry row (§12.3; --new-path: affinity for a project not on trunk yet; --by defaults to the stored login; --jj: standalone jj colocated checkout) [--json]
  workspace list --runkod-url <url> --token <t>              my workstreams, cones, base revisions [--json]
  workspace attach <id> --runkod-url <url> --token <t> [--branch <b>] [--jj]   restore a workspace branch from its snapshot ref [--json]
  workspace delete <id> --runkod-url <url> --token <t>       delete the registry row + snapshot refs (refused while it has open changes) [--json]
  workspace path [<name>]                                     print a workspace's local directory - scripting glue for the rare case -w cannot cover (§12.7) [--json]
  workspace gc [--apply] [--idle <dur>] [--scan <store>]...   reclaim closed+synced materializations; plan-only by default (§12.7) [--json]
  agent create --task <slug> --runkod-url <url> --token <t> [--ttl 8h]   mint an ephemeral task identity (agent-<task>-<x>); token printed ONCE [--json]
  agent list --runkod-url <url> --token <t>                  live and expired agent identities [--json]
  agent revoke <name> --runkod-url <url> --token <t>         kill an agent credential immediately [--json]
  agent event --kind <k> --detail <text> [--from-hook] [--session <id>]   report what the agent is doing to the workspace's live feed (§12.6.1) [--json]
  agent hooks [--install [--dir . | -w <ws>]] [--json]        print the harness hooks snippet; --install merges it into the worktree's .claude/settings.local.json
  workspace snapshot [--dir . | -w <ws>] [-m <msg>]           WIP -> commit -> refs/workspaces/<id>/<branch> [--json]
  workspace branch <name> [--dir . | -w <ws>]                 fork a parallel line: snapshots now target refs/workspaces/<id>/<name> [--json]
  workspace sync [--dir . | -w <ws>]                          sync onto the trunk tip - fetch + rebase, jj-aware (update-base is an alias) [--json]

  -w/--workspace <name[@branch]> on checkout verbs (change create/amend/push/requirements/land/describe/comment/comments/resolve/request-review, workspace snapshot/watch/branch/sync, agent hooks):
  run against that workspace's registered materialization from ANYWHERE - no cd into the worktree (§12.7; workspace list shows what's local)
  mcp serve --runkod-url <url> --token <t>                    MCP stdio adapter: seven read-only tools (§8.3, §17.4)

  auth signup --runkod-url <host> --name <you> --org <org> --create|--join   first contact: register, create/join the org, store the credential
  auth login --runkod-url <url>/o/<org> [--name <you>]        sign in once (password prompted, hidden); every command below then needs no flags
  auth status | auth logout                                   who am I (against which control plane) / forget the credential
  org create --name <org>                                     new org owning its own repo at /o/<org>/, genesis-seeded and ready to work in (§6.10, §7.1) [--json]
  org list                                                    orgs you can reach (role + git URL) [--json]
  org add-member --org <org> --name <account> [--role member|admin|releaser]   grant an account access [--json]
  org bootstrap                                               ownerless org? open the self-landable root-OWNERS change naming you (§6.10 retrofit; unborn trunk seeds genesis directly) [--json]
  github connect --repo <owner/name>                          wire this org to a GitHub repo: verify the App, persist, arm the mirror - one command (2026-07-16) [--json]
  github status                                                the org's mirror state: target, cursors, freezes [--json]
  release create --project <p> [--version x.y.z]              cut an immutable release: server-minted tag + changelog from landed changes (§14.10.3) [--json]
  release list --project <p>                                  the project's releases, newest first [--json]
  change create -m <msg> [--dir . | -w <ws[@branch]>]         commit WIP as one Change (with its Change-Id) [--json]
  change requirements [--change <Id>] [--dir . | -w <ws>]     the §13.5 gates for a Change (default: HEAD's) [--json]
  change comment --change <id> -m <text> [--file <p> --line <n> --side head|base] [--reply-to <id>]   anchored review comment (§13.4.1) [--json]
  change comments [--change <Id>]                             list review threads - resolved/outdated marked (§13.4.1) [--json]
  change resolve <comment-id> [--undo] [--change <Id>]        resolve/reopen a review thread (§13.4.1) [--json]
  change request-review <who> [--change <Id>]                 ask a principal or group to review - enters the attention set (§13.4.2) [--json]

exit codes: 0 success, 1 command failed, 2 usage error (docs/cli-contract.md)`)
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	trunk := fs.String("trunk", "main", "trunk ref name")
	installHook := fs.Bool("install-hook", false, "wire this checkout: the commit-msg Change-Id hook, the advisory pre-commit verb nudge, and the agent skill")
	jsonOut := fs.Bool("json", false, "emit the doctor report as JSON instead of the cheat-sheet")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *installHook {
		if err := InstallChangeIDHook(*repoDir); err != nil {
			return err
		}
		// The verb nudge answers raw `git commit` with the native verbs
		// (jj-aware, advisory, never blocks - doctor.go). A foreign
		// pre-commit hook wins: say so instead of clobbering it.
		if installed, err := InstallVerbNudgeHook(*repoDir); err != nil {
			return err
		} else if !installed {
			fmt.Fprintln(os.Stderr, "note: a pre-commit hook already exists; leaving it alone (the verb nudge is optional)")
		}
		// A jj workspace gets its Change-Id identity from the trailer
		// template, not the hook (jj runs no git hooks) - one flag sets up
		// whichever worlds are present; colocated repos get both.
		if isJJWorkspace(*repoDir) {
			if err := SetupJJChangeIDs(*repoDir); err != nil {
				return err
			}
		}
		// Wiring a checkout means wiring it for its agents too (§8.8): a
		// harness finds the workflow at agentsmd.SkillPath or nowhere. The
		// tree's own skill is never touched - see ensureAgentSkill.
		if path, outcome, err := ensureAgentSkill(*repoDir); err != nil {
			return err
		} else if outcome == "local" {
			fmt.Fprintf(os.Stderr, "note: wrote the runko agent skill to %s (local to this checkout, excluded from changes)\n", path)
		}
	}

	report, err := RunDoctor(*repoDir, *trunk)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	PrintCheatSheet(os.Stdout, report)
	return nil
}

func cmdProject(args []string) error {
	if len(args) < 1 || (args[0] != "create" && args[0] != "list" && args[0] != "delete") {
		return usageError("usage: runko project create --name <name> --type <type> [--lang l] [--no-template] [--owners a,b] [--path p] [--template t] [--capabilities c,d] | runko project list --runkod-url <url> --token <t> | runko project delete --name <name>")
	}
	if args[0] == "list" {
		return cmdProjectList(args[1:])
	}
	if args[0] == "delete" {
		return cmdProjectDelete(args[1:])
	}
	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	name := fs.String("name", "", "project name")
	projectType := fs.String("type", "", "project type: library|service|app|job|other")
	lang := fs.String("lang", "", "project language: go|python|ts|rust|java|cpp (default go); other values need --no-template")
	noTemplate := fs.Bool("no-template", false, "skip template scaffolding: PROJECT.yaml + README only, --lang recorded verbatim")
	owners := fs.String("owners", "", "comma-separated owner refs, e.g. group:commerce-eng")
	path := fs.String("path", "", "project path (default: derived from name)")
	template := fs.String("template", "", "template id (default: type's default template)")
	capabilities := fs.String("capabilities", "", "comma-separated capabilities, e.g. http,rpc")
	buildEngine := fs.String("build-engine", "", "build scaffold: bazel|vite|none (default by language: ts -> vite, else bazel; docs/design.md §14.5.5)")
	api := fs.String("api", "", "contract surface: grpc|rest|none - required for --type service, optional for app, unavailable elsewhere (docs/design.md §13.3.1)")
	jsonOut := fs.Bool("json", false, "emit {name, path, rev} as JSON instead of a human summary")
	// All flags, deliberately no positional <name> argument: the stdlib flag
	// package stops parsing flags at the first positional token, so
	// `project create checkout-api --type service` would silently drop
	// --type. Keeping everything a flag sidesteps that ordering trap
	// entirely rather than requiring users to memorize a flags-first rule.
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("project create: --name is required")
	}

	intent := project.Intent{
		Name:         *name,
		Type:         *projectType,
		Language:     *lang,
		NoTemplate:   *noTemplate,
		Path:         *path,
		TemplateID:   *template,
		Owners:       splitNonEmpty(*owners),
		Capabilities: splitNonEmpty(*capabilities),
		BuildEngine:  *buildEngine,
		API:          *api,
	}

	rev, changeID, err := CreateProject(*repoDir, intent)
	if err != nil {
		return err
	}
	// The RESOLVED path (empty --path derives from the name, plan.go) -
	// reporting the raw flag printed "path": "" for the common case.
	outPath := intent.Path
	if outPath == "" {
		outPath = intent.Name
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"name": intent.Name, "path": outPath, "rev": rev, "change_id": changeID,
		})
	}
	fmt.Printf("created project %s at %s (Change-Id: %s)\n", intent.Name, rev, changeID)
	return nil
}

// cmdProjectDelete implements `runko project delete` - create's dual
// (§13.1): a server-calling verb, because the deletion plan needs the
// trunk-tip index for edge-stripping and a sparse local worktree may not
// even hold the project's files. The server authors an ordinary open
// Change; nothing reaches trunk until it lands through the normal gates.
func cmdProjectDelete(args []string) error {
	fs := flag.NewFlagSet("project delete", flag.ExitOnError)
	name := fs.String("name", "", "project to delete")
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	jsonOut := fs.Bool("json", false, "emit {change_id, title} as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("project delete: --name is required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	var out struct {
		ChangeID string `json:"change_id"`
		Title    string `json:"title"`
	}
	if err := apiJSON(context.Background(), http.DefaultClient, http.MethodPost,
		strings.TrimRight(cred.URL, "/")+"/api/projects/"+url.PathEscape(*name)+"/delete", cred.AuthHeader(), nil, &out); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	fmt.Printf("delete of %s opened as change %s - it lands through the normal gates\n", *name, out.ChangeID)
	return nil
}

// cmdProjectList implements `runko project list` against runkod's
// GET /api/projects (the trunk-tip project index, §10.3). Added in the
// stage-12 session so that server-side errors like unknown_project can
// suggest a CLI command instead of a raw API URL (§8.3's CLI-first
// decision: the CLI is the primary agent interface, so every suggested
// next step should be typeable, not curl-able).
func cmdProjectList(args []string) error {
	fs := flag.NewFlagSet("project list", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	jsonOut := fs.Bool("json", false, "emit the project list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	var projects []index.IndexedProject
	if err := apiJSON(context.Background(), http.DefaultClient, http.MethodGet,
		strings.TrimRight(cred.URL, "/")+"/api/projects", cred.AuthHeader(), nil, &projects); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(projects)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range rootProjectFirst(projects) {
		owners := make([]string, len(p.Owners))
		for i, o := range p.Owners {
			owners[i] = o.Ref
		}
		// The root project's path column is empty, which read as a
		// missing field rather than the fact it is: this project IS the
		// repo root and owns every path no deeper manifest claims. Name
		// it, and hoist the row - it is not a peer of the rest.
		path := p.Path
		if isRootProject(p) {
			path = "(root)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Type, path, strings.Join(owners, ","))
	}
	return tw.Flush()
}

// isRootProject / rootProjectFirst: the repo-root project (§10.3) exists
// so root glue - go.mod, Makefile, .github/, scripts/ - resolves a merge
// policy instead of falling to the fail-closed unowned-path default, and
// it carries the repo-wide root_invalidation/prose rules. Rootness is the
// PATH, never the name (`repo` is this repo's convention, not a reserved
// word); both spellings of the root path count, matching the daemon's own
// rule (runkod/deleteproject.go, runkod/bootstraporg.go). JSON output is
// deliberately untouched - this is presentation, and --json is a contract.
func isRootProject(p index.IndexedProject) bool {
	return p.Path == "" || p.Path == "."
}

func rootProjectFirst(projects []index.IndexedProject) []index.IndexedProject {
	out := make([]index.IndexedProject, 0, len(projects))
	for _, p := range projects {
		if isRootProject(p) {
			out = append(out, p)
		}
	}
	for _, p := range projects {
		if !isRootProject(p) {
			out = append(out, p)
		}
	}
	return out
}

// addWorkspaceFlag registers -w/--workspace on a verb that operates on a
// local checkout: the workspace's registered materialization becomes the
// working directory (resolveWorkspaceDir, materializations.go), so the
// verb runs from anywhere - no cd into the worktree.
func addWorkspaceFlag(fs *flag.FlagSet) *string {
	w := fs.String("workspace", "", "workspace name[@branch] - run against its local materialization instead of the current directory (§12.7)")
	fs.StringVar(w, "w", "", "shorthand for --workspace")
	return w
}

func cmdChange(args []string) error {
	valid := map[string]bool{"create": true, "amend": true, "push": true, "requirements": true, "land": true, "approve": true, "list": true, "abandon": true, "describe": true, "automerge": true, "rerun-check": true, "comment": true, "comments": true, "resolve": true, "request-review": true}
	if len(args) < 1 || !valid[args[0]] {
		return usageError("usage: runko change create|amend|push|requirements|land|approve|list|abandon|describe|automerge|rerun-check|comment|comments|resolve|request-review ... (see docs/cli-contract.md)")
	}
	switch args[0] {
	case "create":
		return cmdChangeCreate(args[1:])
	case "amend":
		return cmdChangeAmend(args[1:])
	case "requirements":
		return cmdChangeRequirements(args[1:])
	case "land":
		return cmdChangeLand(args[1:])
	case "approve":
		return cmdChangeApprove(args[1:])
	case "list":
		return cmdChangeList(args[1:])
	case "abandon":
		return cmdChangeAbandon(args[1:])
	case "describe":
		return cmdChangeDescribe(args[1:])
	case "automerge":
		return cmdChangeAutomerge(args[1:])
	case "rerun-check":
		return cmdChangeRerunCheck(args[1:])
	case "comment":
		return cmdChangeComment(args[1:])
	case "comments":
		return cmdChangeComments(args[1:])
	case "resolve":
		return cmdChangeResolve(args[1:])
	case "request-review":
		return cmdChangeRequestReview(args[1:])
	}

	fs := flag.NewFlagSet("change push", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	ws := addWorkspaceFlag(fs)
	remote := fs.String("remote", "origin", "git remote to push to")
	trunk := fs.String("trunk", "main", "trunk ref name")
	noSync := fs.Bool("no-sync", false, "push as-is even when the base is stale (skip the automatic rebase onto the trunk tip)")
	noSnapshot := fs.Bool("no-snapshot", false, "skip the automatic workspace snapshot before pushing (§12.6)")
	jsonOut := fs.Bool("json", false, "emit {change_id, ref} as JSON instead of a human summary")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	wd, err := resolveWorkspaceDir(*ws, *repoDir)
	if err != nil {
		return err
	}

	changeID, err := pushChange(wd, *remote, *trunk, !*noSync, !*noSnapshot)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"change_id": changeID, "ref": "refs/for/" + *trunk,
		})
	}
	fmt.Printf("pushed to refs/for/%s (Change-Id: %s)\n", *trunk, changeID)
	return nil
}

// cmdChangeLand implements `runko change land` (§13.5, §28.3 stage 11b) -
// unlike push/project create/agents-md, this genuinely needs a live runkod
// instance to talk to (see LandChange's doc comment in land.go).
func cmdChangeCreate(args []string) error {
	fs := flag.NewFlagSet("change create", flag.ExitOnError)
	msg := fs.String("m", "", "change message (required)")
	dir := fs.String("dir", ".", "repository directory")
	ws := addWorkspaceFlag(fs)
	allowLarge := fs.Bool("allow-large", false, "commit large/executable untracked files anyway (they are refused by default as suspected build artifacts, §12.2)")
	jsonOut := fs.Bool("json", false, "emit {change_id} as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, err := resolveWorkspaceDir(*ws, *dir)
	if err != nil {
		return err
	}
	id, err := CreateChange(wd, *msg, *allowLarge)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"change_id": id})
	}
	// Nudge the two remaining steps of the submit loop. The description is a
	// SEPARATE control-plane field (§8.6, never derived from the commit
	// message) that RequireDescription gates agent lands on - surfacing it
	// here means an agent sets it up front instead of discovering the blocker
	// only at `change requirements` (FIX #6).
	fmt.Printf("created change %s\n  -> runko change describe --description \"WHAT changed and WHY\"   # agent changes must, before landing (§8.7)\n  -> runko change push                                       # submit it for review\n", id)
	return nil
}

// cmdChangeAmend implements `runko change amend` (FIX #6): fold the working
// tree into HEAD's existing Change with the Runko-identity fallback, so
// agents stop dropping to a raw `git commit --amend` that fails without a
// configured git author.
func cmdChangeAmend(args []string) error {
	fs := flag.NewFlagSet("change amend", flag.ExitOnError)
	msg := fs.String("m", "", "new change message (default: keep HEAD's, just fold in the working tree)")
	dir := fs.String("dir", ".", "repository directory")
	ws := addWorkspaceFlag(fs)
	jsonOut := fs.Bool("json", false, "emit {change_id} as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, err := resolveWorkspaceDir(*ws, *dir)
	if err != nil {
		return err
	}
	id, err := AmendChange(wd, *msg)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"change_id": id})
	}
	fmt.Printf("amended change %s\n  -> runko change push   # re-submit the updated change\n", id)
	return nil
}

func cmdChangeRequirements(args []string) error {
	fs := flag.NewFlagSet("change requirements", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id (default: HEAD's Change-Id trailer)")
	dir := fs.String("dir", ".", "repository directory (for the HEAD default)")
	ws := addWorkspaceFlag(fs)
	jsonOut := fs.Bool("json", false, "emit the merge requirements as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, err := resolveWorkspaceDir(*ws, *dir)
	if err != nil {
		return err
	}
	id := *changeID
	if id == "" {
		if id, err = headChangeID(wd); err != nil {
			return err
		}
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	reqs, err := ChangeRequirements(context.Background(), http.DefaultClient, cred, id)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(reqs)
	}
	printRequirements(id, reqs)
	return nil
}

func cmdAuth(args []string) error {
	if len(args) < 1 {
		return usageError("usage: runko auth signup|login|status|logout ...")
	}
	ctx := context.Background()
	switch args[0] {
	case "signup":
		// First contact, CLI-first (§6.10): one command registers the
		// account, creates or joins the org, and stores the credential -
		// signup IS login, so nothing downstream needs auth flags.
		fs := flag.NewFlagSet("auth signup", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "the control plane HOST root, e.g. https://runko.example.com (signup is served by the hub, not an /o/<org> mount)")
		name := fs.String("name", "", "the account name to register")
		password := fs.String("password", "", "password (min 8 chars); omit to be prompted securely (input hidden)")
		org := fs.String("org", "", "the org to create or join - every account belongs to one")
		create := fs.Bool("create", false, "create --org as a new org; you become its admin")
		join := fs.Bool("join", false, "join --org, an existing org, as a member")
		code := fs.String("invite-code", "", "invite code, if this control plane requires one to sign up")
		email := fs.String("email", "", "your email address - OPTIONAL, so nothing prompts for it when omitted")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *runkodURL == "" || *name == "" || *org == "" || *create == *join {
			return &clierr.Error{
				Code: "missing_field", Field: "signup",
				Message:    "auth signup needs --runkod-url, --name, --org, and exactly one of --create/--join",
				Suggestion: "runko auth signup --runkod-url https://<host> --name <you> --org <org> --create",
			}
		}
		orgMode := "join"
		if *create {
			orgMode = "create"
		}
		_, err := AuthSignup(ctx, http.DefaultClient, *runkodURL, *name, *password, *org, orgMode, *code, *email, bufio.NewReader(os.Stdin), os.Stdout)
		return err

	case "login":
		fs := flag.NewFlagSet("auth login", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod API base: the /o/<org> mount, NOT the web path - e.g. https://runko.victornazzaro.com/o/acme (required)")
		name := fs.String("name", "", "your principal name, e.g. alice; omit to store a bare deploy token (anonymous bearer)")
		token := fs.String("token", "", "token or password; omit to be prompted securely (input hidden)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *runkodURL == "" {
			return &clierr.Error{
				Code: "missing_url", Field: "runkod-url",
				Message:    "auth login needs --runkod-url (the /o/<org> API mount, not the web path)",
				Suggestion: "runko auth login --runkod-url https://<host>/o/<org> --name <you>",
			}
		}
		_, err := AuthLogin(ctx, http.DefaultClient, *runkodURL, *name, *token, bufio.NewReader(os.Stdin), os.Stdout)
		return err

	case "status":
		cred, found, err := loadCredential()
		if err != nil {
			return err
		}
		if !found {
			fmt.Println("not logged in (runko auth login --runkod-url <url>)")
			return nil
		}
		who, anonymous, err := whoami(ctx, http.DefaultClient, cred)
		if err != nil {
			return err
		}
		if anonymous {
			fmt.Printf("%s: logged in anonymously (deploy token)\n", cred.URL)
		} else {
			fmt.Printf("%s: logged in as %s\n", cred.URL, who)
		}
		return nil

	case "logout":
		path, err := credentialPath()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("logged out")
		return nil

	case "git-credential":
		// git's credential-helper protocol (§12.7): workspace stores stamp
		// `credential.helper = !runko auth git-credential`, so raw git in
		// any worktree resolves the INVOKING principal's stored login.
		// Called by git, not humans; get/store/erase on argv, attributes
		// on stdin.
		if len(args) < 2 {
			return usageError("usage: runko auth git-credential <get|store|erase> (called by git)")
		}
		return AuthGitCredential(args[1], os.Stdin, os.Stdout)

	default:
		return usageError("usage: runko auth signup|login|status|logout|git-credential ...")
	}
}

func cmdChangeLand(args []string) error {
	fs := flag.NewFlagSet("change land", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL, e.g. http://localhost:8080")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id to land")
	force := fs.Bool("force", false, "admin override (docs/design.md 13.5): bypass owner/check gates and revalidation; audited as landed_forced")
	repoDir := fs.String("repo", ".", "local checkout used by --sync to rebase and re-push on requires_revalidation")
	ws := addWorkspaceFlag(fs)
	remote := fs.String("remote", "origin", "git remote --sync pushes to")
	trunk := fs.String("trunk", "main", "trunk ref name")
	sync := fs.Bool("sync", true, "on requires_revalidation, run the 13.5 recovery loop here: sync onto trunk, re-push, wait for checks, retry")
	syncTimeout := fs.Duration("sync-timeout", 15*time.Minute, "wall-clock bound on the --sync recovery loop")
	jsonOut := fs.Bool("json", false, "emit the land.Outcome as JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changeID == "" {
		return fmt.Errorf("change land: --change is required")
	}
	wd, err := resolveWorkspaceDir(*ws, *repoDir)
	if err != nil {
		return err
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	// The recovery loop needs a checkout to rebase; without one (or with
	// --force, which bypasses revalidation anyway) land is a single shot.
	var outcome land.Outcome
	if _, gitErr := runGit(wd, "rev-parse", "--git-dir"); *sync && !*force && gitErr == nil {
		outcome, err = LandWithSync(context.Background(), http.DefaultClient, cred, *changeID, wd, *remote, *trunk, *syncTimeout, os.Stderr)
	} else {
		outcome, err = LandChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), *changeID, *force)
	}
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(outcome)
	}
	if outcome.Landed {
		if *force {
			fmt.Printf("FORCE-landed %s at %s (merge gates bypassed - audited as landed_forced)\n", *changeID, outcome.LandedSHA)
		} else {
			fmt.Printf("landed %s at %s\n", *changeID, outcome.LandedSHA)
		}
	} else {
		fmt.Printf("not landed: %+v\n", outcome)
	}
	return nil
}

// cmdChangeApprove implements `runko change approve` (§13.5, §28.3 stage
// 11c): record that a required owner approves this Change, and print the
// refreshed merge requirements - what the approval covered, what still
// blocks.
func cmdChangeApprove(args []string) error {
	fs := flag.NewFlagSet("change approve", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL, e.g. http://localhost:8080")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id to approve")
	ownerRef := fs.String("owner", "", "owner requirement being satisfied, e.g. group:commerce-eng")
	by := fs.String("by", "", "who is approving")
	jsonOut := fs.Bool("json", false, "emit the merge requirements as JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changeID == "" || *ownerRef == "" {
		return fmt.Errorf("change approve: --change and --owner are required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	// --by stays optional for a signed-in principal: the server derives
	// the approver from the credential (and rejects a mismatch).
	reqs, err := ApproveChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), *changeID, *ownerRef, *by)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(reqs)
	}
	fmt.Printf("approved %s on %s\n", *ownerRef, *changeID)
	if reqs.Mergeable {
		fmt.Println("mergeable: yes")
	} else {
		fmt.Println("mergeable: no")
		for _, b := range reqs.Blockers {
			fmt.Printf("  - %s\n", b)
		}
	}
	return nil
}

func cmdChangeList(args []string) error {
	fs := flag.NewFlagSet("change list", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	state := fs.String("state", "open", "filter: open|landed|abandoned|all")
	jsonOut := fs.Bool("json", false, "emit the change list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	filter := *state
	if filter == "all" {
		filter = ""
	}
	list, err := ListChanges(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), filter)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(list)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, c := range list {
		author := c.AuthoredBy
		if author == "" {
			author = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.ChangeKey, c.State, author, c.Title)
	}
	return tw.Flush()
}

func cmdChangeAbandon(args []string) error {
	fs := flag.NewFlagSet("change abandon", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id to abandon")
	jsonOut := fs.Bool("json", false, "emit the abandoned change as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changeID == "" {
		return fmt.Errorf("change abandon: --change is required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	change, err := AbandonChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), *changeID)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(change)
	}
	fmt.Printf("abandoned %s (%s)\n", change.ChangeKey, change.Title)
	return nil
}

func cmdChangeDescribe(args []string) error {
	fs := flag.NewFlagSet("change describe", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id (default: HEAD's Change-Id trailer)")
	dir := fs.String("dir", ".", "repository directory (for the HEAD default)")
	ws := addWorkspaceFlag(fs)
	description := fs.String("description", "", "what the change does and why (§8.6)")
	testPlan := fs.String("test-plan", "", "how the change was verified (§8.6)")
	jsonOut := fs.Bool("json", false, "emit the updated change as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// A flag the caller never passed means "leave that field alone"; an
	// explicit --description "" clears it. flag can't tell those apart
	// from values, so distinguish by visitation.
	var descPtr, planPtr *string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "description":
			descPtr = description
		case "test-plan":
			planPtr = testPlan
		}
	})
	if descPtr == nil && planPtr == nil {
		return fmt.Errorf("change describe: provide --description and/or --test-plan")
	}
	wd, err := resolveWorkspaceDir(*ws, *dir)
	if err != nil {
		return err
	}
	id := *changeID
	if id == "" {
		if id, err = headChangeID(wd); err != nil {
			return err
		}
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	change, err := DescribeChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, descPtr, planPtr)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(change)
	}
	fmt.Printf("described %s (%s)\n", change.ChangeKey, change.Title)
	return nil
}

func cmdChangeAutomerge(args []string) error {
	fs := flag.NewFlagSet("change automerge", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id to arm")
	disable := fs.Bool("disable", false, "disarm instead")
	jsonOut := fs.Bool("json", false, "emit the change as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changeID == "" {
		return fmt.Errorf("change automerge: --change is required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	var change struct {
		ChangeKey   string
		Title       string
		Automerge   bool
		AutomergeBy string
	}
	err = apiJSON(context.Background(), http.DefaultClient, http.MethodPost,
		strings.TrimSuffix(cred.URL, "/")+"/api/changes/"+*changeID+"/automerge", cred.AuthHeader(),
		map[string]bool{"enabled": !*disable}, &change)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(change)
	}
	if change.Automerge {
		fmt.Printf("automerge armed on %s - it lands itself when the gates go green (armed by %s)\n", change.ChangeKey, change.AutomergeBy)
	} else {
		fmt.Printf("automerge disarmed on %s\n", change.ChangeKey)
	}
	return nil
}

func cmdChangeRerunCheck(args []string) error {
	fs := flag.NewFlagSet("change rerun-check", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id whose check to rerun")
	name := fs.String("name", "", "required check name to rerun")
	jsonOut := fs.Bool("json", false, "emit the refreshed merge requirements as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *changeID == "" || *name == "" {
		return fmt.Errorf("change rerun-check: --change and --name are required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	reqs, err := RerunCheck(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), *changeID, *name)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(reqs)
	}
	fmt.Printf("rerun requested for %s on %s\n", *name, *changeID)
	for _, b := range reqs.Blockers {
		fmt.Printf("  - %s\n", b)
	}
	return nil
}

// stringSliceFlag collects a repeatable string flag (--project a --project b).
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// cmdWorkspace implements `runko workspace` (§12.3 Phase A, §28.3 stage
// 12b): create/list/attach/snapshot/update-base. See workspace.go for the
// mechanics; this is flag parsing and output shaping only.
func cmdWorkspace(args []string) error {
	if len(args) < 1 {
		return usageError("usage: runko workspace create|list|attach|path|gc|snapshot|watch|branch|sync|delete ...")
	}
	sub, rest := args[0], args[1:]
	ctx := context.Background()
	switch sub {
	case "create":
		fs := flag.NewFlagSet("workspace create", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		name := fs.String("name", "", "workspace name (also the snapshot-ref segment)")
		by := fs.String("by", "", "who owns this workspace (default: the stored login's principal)")
		as := fs.String("as", "", "authenticate as this named principal using --token as its password (Basic, not stored) - the no-XDG agent form: mint a token with `runko agent create`, then --by agent-x --as agent-x --token <tok>")
		cloneDir := fs.String("clone-dir", "", "shared blobless clone directory (default: the managed home's .store, §12.7)")
		dir := fs.String("dir", "", "worktree directory (default: under the managed home, ~/runko-ws)")
		forceNested := fs.Bool("force-nested", false, "materialize inside another git checkout anyway")
		jjClient := fs.Bool("jj", false, "standalone jj colocated checkout (jj + .git side by side, Change-Ids from jj change ids) instead of a worktree off the shared store")
		var projects stringSliceFlag
		fs.Var(&projects, "project", "project affinity (repeatable)")
		var newPaths stringSliceFlag
		fs.Var(&newPaths, "new-path", "path root for a project NOT on trunk yet (repeatable) - the greenfield bootstrap: the cone + write affinity cover it so the change that creates the project can be pushed from here")
		jsonOut := fs.Bool("json", false, "emit the workspace (+ Dir) as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		// --as authenticates this one command as a named principal (typically
		// the agent) without storing or clobbering a credential (FIX #3): admin
		// mints the token, then `workspace create --by agent-x --as agent-x
		// --token <tok>` registers AND materializes as the agent. No
		// XDG_CONFIG_HOME, no separate `auth login`.
		if *as != "" {
			if *token == "" {
				return &clierr.Error{
					Code: "missing_token", Field: "token",
					Message:    "--as needs --token (the principal's password)",
					Suggestion: "pass the token `runko agent create --task <slug>` printed, e.g. --as agent-<slug>-xxxx --token <tok>",
				}
			}
			cred = Credential{URL: cred.URL, Name: *as, Secret: *token}
		}
		// The stored login already says who you are (§6.10): --by stays an
		// override, not a toll. Only the anonymous deploy token has no name
		// to default to.
		if *by == "" {
			*by = cred.Name
		}
		if *name == "" || *by == "" || len(projects)+len(newPaths) == 0 {
			return fmt.Errorf("workspace create: --name and at least one --project (or --new-path) are required (and --by, when signed in with a bare token)")
		}
		info, wsDir, err := WorkspaceCreate(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(), *name, *by, projects, newPaths,
			MaterializeOptions{CloneDir: *cloneDir, Dir: *dir, ForceNested: *forceNested, JJ: *jjClient})
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(struct {
				WorkspaceInfo
				Dir string
			}{info, wsDir})
		}
		mode := ""
		if *jjClient {
			mode = "jj colocated, "
		}
		fmt.Printf("workspace %s ready at %s (%sbase %s, cone: %s)\n", info.ID, wsDir, mode, short(info.BaseRevision), strings.Join(info.SparsePatterns, ", "))
		printWorkspaceStreamingGuidance(os.Stdout, info.ID)
		printWorkspaceLoop(os.Stdout, info.ID)
		return nil

	case "delete":
		fs := flag.NewFlagSet("workspace delete", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the result as JSON")
		// id-first documented form: pop the positional before flag parsing
		// (stdlib flag stops at the first positional - the workspace attach
		// live-test lesson).
		var id string
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			id, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if id == "" {
			return usageError("usage: runko workspace delete <id> [--runkod-url <url> --token <t>]")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		if err := WorkspaceDelete(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(), id); err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"deleted": id})
		}
		fmt.Printf("deleted workspace %s (registry row + snapshot refs; local checkouts are yours to remove)\n", id)
		return nil

	case "list":
		fs := flag.NewFlagSet("workspace list", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the list as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		list, err := WorkspaceList(ctx, http.DefaultClient, cred.URL, cred.AuthHeader())
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(list)
		}
		local := localPathsByWorkspace()
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, ws := range list {
			branches := strings.Join(ws.Branches, ",")
			if branches == "" {
				branches = "-"
			}
			here := "-"
			if paths := local[ws.ID]; len(paths) > 0 {
				here = paths[0]
				if len(paths) > 1 {
					here += fmt.Sprintf(" (+%d)", len(paths)-1)
				}
			}
			fmt.Fprintf(tw, "%s\t%s\tbase %s\t%s\tbranches: %s\tlocal: %s\n", ws.ID, ws.Status, short(ws.BaseRevision), strings.Join(ws.ProjectAffinity, ","), branches, here)
		}
		return tw.Flush()

	case "attach":
		fs := flag.NewFlagSet("workspace attach", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		cloneDir := fs.String("clone-dir", "", "shared blobless clone directory (default: the managed home's .store, §12.7)")
		dir := fs.String("dir", "", "worktree directory (default: under the managed home; branches land at <workspace>@<branch>)")
		branch := fs.String("branch", "head", "workspace branch to restore (parallel lines of work, §12.2)")
		forceNested := fs.Bool("force-nested", false, "materialize inside another git checkout anyway")
		jjClient := fs.Bool("jj", false, "restore as a standalone jj colocated checkout instead of a worktree off the shared store")
		jsonOut := fs.Bool("json", false, "emit the workspace (+ Dir) as JSON")
		// The documented form is id-first (`workspace attach <id> --runkod-url
		// ...`), but stdlib flag stops parsing at the first positional - pop
		// the id off the front before parsing so the printed help is actually
		// copy-pasteable (§6.9); flags-first with a trailing id still works.
		var id string
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			id, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if id == "" {
			id = fs.Arg(0)
		}
		if id == "" {
			return fmt.Errorf("workspace attach: a workspace id is required")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		info, wsDir, err := WorkspaceAttach(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(), id, *branch,
			MaterializeOptions{CloneDir: *cloneDir, Dir: *dir, ForceNested: *forceNested, JJ: *jjClient})
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(struct {
				WorkspaceInfo
				Dir string
			}{info, wsDir})
		}
		mode := ""
		if *jjClient {
			mode = " (jj colocated)"
		}
		fmt.Printf("workspace %s restored at %s%s\n", info.ID, wsDir, mode)
		// A non-default branch attach materializes that branch's own row, so
		// the -w handle it teaches has to carry it (name@branch, §12.7).
		printWorkspaceStreamingGuidance(os.Stdout, workspaceHandle(info.ID, *branch))
		return nil

	case "gc":
		fs := flag.NewFlagSet("workspace gc", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		apply := fs.Bool("apply", false, "execute the plan (default: print it)")
		idle := fs.Duration("idle", 0, "also sweep OPEN workspaces idle this long (their durable state is server-side; re-attach recreates them)")
		var scans stringSliceFlag
		fs.Var(&scans, "scan", "store directory whose worktrees are adopted into the registry first (repeatable; the pre-§12.7 migration path)")
		jsonOut := fs.Bool("json", false, "emit the plan as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		plan, err := WorkspaceGC(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(),
			GCOptions{Apply: *apply, Idle: *idle, Scan: scans})
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(plan)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		var nReclaim int
		var bytesReclaim int64
		for _, c := range plan {
			verdict := "keep"
			size := "-"
			if c.Reclaim {
				verdict = "reclaim"
				nReclaim++
				bytesReclaim += c.SizeBytes
				size = humanBytes(c.SizeBytes)
			}
			fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%s\t%s\n", verdict, c.Workspace, c.Branch, c.Path, size, c.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		switch {
		case len(plan) == 0:
			fmt.Println("no materializations tracked on this machine (adopt legacy stores with --scan <store>)")
		case *apply:
			fmt.Printf("reclaimed %d materialization(s), %s\n", nReclaim, humanBytes(bytesReclaim))
		case nReclaim > 0:
			fmt.Printf("plan only - rerun with --apply to reclaim %d materialization(s), %s\n", nReclaim, humanBytes(bytesReclaim))
		default:
			fmt.Println("nothing reclaimable - every materialization is open, dirty, or not provably synced")
		}
		return nil

	case "path":
		fs := flag.NewFlagSet("workspace path", flag.ExitOnError)
		jsonOut := fs.Bool("json", false, "emit {workspace, branch, path} as JSON")
		var name string
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		m, err := workspacePathLookup(name)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{
				"workspace": m.Workspace, "branch": m.Branch, "path": m.Path,
			})
		}
		fmt.Println(m.Path)
		return nil

	case "snapshot":
		fs := flag.NewFlagSet("workspace snapshot", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace worktree directory")
		ws := addWorkspaceFlag(fs)
		msg := fs.String("m", "", "snapshot message")
		jsonOut := fs.Bool("json", false, "emit {ref} as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		wd, err := resolveWorkspaceDir(*ws, *dir)
		if err != nil {
			return err
		}
		ref, err := WorkspaceSnapshot(wd, *msg)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref})
		}
		fmt.Printf("snapshot pushed to %s\n", ref)
		return nil

	case "watch":
		fs := flag.NewFlagSet("workspace watch", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace worktree directory")
		ws := addWorkspaceFlag(fs)
		interval := fs.Duration("interval", 15*time.Second, "check-and-push cadence while dirty")
		once := fs.Bool("once", false, "one check-and-push tick, then exit (tests, CI)")
		jsonOut := fs.Bool("json", false, "NDJSON: one {ref, sha} line per pushed snapshot")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		wd, err := resolveWorkspaceDir(*ws, *dir)
		if err != nil {
			return err
		}
		return WorkspaceWatch(WatchOptions{Dir: wd, Interval: *interval, Once: *once, JSON: *jsonOut})

	case "branch":
		fs := flag.NewFlagSet("workspace branch", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace worktree directory")
		ws := addWorkspaceFlag(fs)
		jsonOut := fs.Bool("json", false, "emit {ref} as JSON")
		// id-first parsing, same trap and same fix as attach above.
		var name string
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if name == "" {
			name = fs.Arg(0)
		}
		if name == "" {
			return usageError("usage: runko workspace branch <name> [--dir . | -w <ws>]")
		}
		wd, err := resolveWorkspaceDir(*ws, *dir)
		if err != nil {
			return err
		}
		ref, err := WorkspaceBranch(wd, name)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref})
		}
		fmt.Printf("branched: snapshots from here go to %s\n", ref)
		return nil

	case "sync", "update-base": // sync is the verb (12.3, the CitC "sync to head"); update-base is the original 12b name
		fs := flag.NewFlagSet("workspace sync", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		dir := fs.String("dir", ".", "workspace worktree directory")
		ws := addWorkspaceFlag(fs)
		jsonOut := fs.Bool("json", false, "emit {base_revision} as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		wd, err := resolveWorkspaceDir(*ws, *dir)
		if err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		newBase, err := WorkspaceUpdateBase(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(), wd)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"base_revision": newBase})
		}
		fmt.Printf("synced onto trunk tip %s\n", short(newBase))
		return nil

	default:
		return usageError("usage: runko workspace create|list|attach|path|gc|snapshot|branch|sync|delete ...")
	}
}

// cmdAgentsMD implements `runko agents-md`: (re)write both generated agent
// teaching surfaces - AGENTS.md at the repo root and the loadable skill at
// agentsmd.SkillPath - from the same command inventory (§8.8's "reference
// prompts / skill files ... generated per monorepo", stage 11's (§28.3)
// done-when bar; org genesis seeds the identical pair). Overwrites
// unconditionally, matching how sqlc/oapi-codegen generated files in this
// repo are treated: regenerate, don't hand-edit.
func cmdAgentsMD(args []string) error {
	fs := flag.NewFlagSet("agents-md", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	out := fs.String("out", "AGENTS.md", "output path, relative to --repo unless absolute")
	jsonOut := fs.Bool("json", false, "emit {path,skill_path} as JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *out
	if !filepath.IsAbs(path) {
		path = filepath.Join(*repoDir, path)
	}
	if err := os.WriteFile(path, []byte(agentsmd.Generate()), 0o644); err != nil {
		return fmt.Errorf("agents-md: write %s: %w", path, err)
	}
	skillPath := filepath.Join(*repoDir, filepath.FromSlash(agentsmd.SkillPath))
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		return fmt.Errorf("agents-md: mkdir %s: %w", filepath.Dir(skillPath), err)
	}
	if err := os.WriteFile(skillPath, []byte(agentsmd.GenerateSkill()), 0o644); err != nil {
		return fmt.Errorf("agents-md: write %s: %w", skillPath, err)
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"path": path, "skill_path": skillPath})
	}
	fmt.Printf("generated %s and %s\n", path, skillPath)
	return nil
}

// cmdMCP implements `runko mcp serve` (§8.3, §17.4, §28.3 stage 12): the
// MCP stdio adapter for clients that can't shell out to this CLI. It
// serves newline-delimited JSON-RPC on stdin/stdout until EOF - run it
// from an MCP client's server config, not interactively. Log output (none
// today) would go to stderr; stdout is exclusively protocol.
func cmdMCP(args []string) error {
	if len(args) < 1 || args[0] != "serve" {
		return usageError("usage: runko mcp serve --runkod-url <url> --token <t>")
	}
	fs := flag.NewFlagSet("mcp serve", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	srv := &mcp.Server{Client: &mcp.Client{BaseURL: cred.URL, Token: cred.AuthHeader()}}
	return srv.Serve(context.Background(), os.Stdin, os.Stdout)
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
