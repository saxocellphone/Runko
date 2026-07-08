// Command runko is the human/agent-facing CLI (docs/design.md §17.1).
//
// Implemented stage 9 (§28.3), operating purely on the local repo - no
// control plane required: doctor, project create, change push, agents-md.
// `change land` (stage 11b) is the one command in this file that DOES need
// a live control plane - and, unlike auth/workspace/mcp below, has one to
// talk to as of this session: runkod. Still stubbed because no live
// control plane is reachable in this sandbox to round-trip against: auth
// login, workspace create/attach, change create/requirements, mcp serve.
//
// Exit codes (docs/cli-contract.md, added in the §8.3 CLI-first audit):
// 0 success, 1 a recognized command failed (structured error printed to
// stderr), 2 usage error (unknown command, wrong subcommand, missing
// positional keyword) - flag-parsing errors already exit 2 via stdlib
// flag.ExitOnError, this file's usageError type extends the same code to
// this package's own pre-flag-parsing usage checks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/saxocellphone/runko/agentsmd"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/mcp"
	"github.com/saxocellphone/runko/project"
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
	case "workspace":
		err = cmdWorkspace(os.Args[2:])
	case "mcp":
		err = cmdMCP(os.Args[2:])
	case "auth":
		fmt.Fprintf(os.Stderr, "runko %s: requires a live control plane - not implemented yet (docs/design.md §17.1, §19.2)\n", os.Args[1])
		os.Exit(1)
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
  change push                     push HEAD to refs/for/<trunk> for review (§11.5) [--json]
  agents-md                       (re)generate AGENTS.md teaching this CLI to agents (§8.8) [--json]

commands (need a live runkod instance, §28.3 stages 11b/11c/12b):
  project list --runkod-url <url> --token <t>                 list projects indexed at trunk (§10.3) [--json]
  change land --change <id> --runkod-url <url> --token <t>   land a mergeable Change (§13.5) [--json]
  change approve --change <id> --owner <ref> --by <who> --runkod-url <url> --token <t>   record an owner approval (§13.5) [--json]
  change list [--state open] --runkod-url <url> --token <t>  list changes, newest first (§7.4) [--json]
  change abandon --change <id> --runkod-url <url> --token <t>   abandon an open change (§7.4) [--json]
  change rerun-check --change <id> --name <check> --runkod-url <url> --token <t>   request a check re-run (§14.4.2) [--json]
  workspace create --name <n> --project <p>... --by <who> --runkod-url <url> --token <t>   worktree + sparse cone + registry row (§12.3) [--json]
  workspace list --runkod-url <url> --token <t>              my workstreams, cones, base revisions [--json]
  workspace attach <id> --runkod-url <url> --token <t> [--branch <b>]   restore a workspace branch from its snapshot ref [--json]
  workspace snapshot [--dir .] [-m <msg>]                    WIP -> commit -> refs/workspaces/<id>/<branch> [--json]\n  workspace branch <name> [--dir .]                           fork a parallel line: snapshots now target refs/workspaces/<id>/<name> [--json]
  workspace update-base --runkod-url <url> --token <t> [--dir .]   fetch + rebase onto trunk tip [--json]
  mcp serve --runkod-url <url> --token <t>                    MCP stdio adapter: six read-only tools (§8.3, §17.4)

not yet implemented (need a live control plane, §19.2):
  auth login, change create/requirements

exit codes: 0 success, 1 command failed, 2 usage error (docs/cli-contract.md)`)
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	trunk := fs.String("trunk", "main", "trunk ref name")
	installHook := fs.Bool("install-hook", false, "install the commit-msg Change-Id hook")
	jsonOut := fs.Bool("json", false, "emit the doctor report as JSON instead of the cheat-sheet")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *installHook {
		if err := InstallChangeIDHook(*repoDir); err != nil {
			return err
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
	if len(args) < 1 || (args[0] != "create" && args[0] != "list") {
		return usageError("usage: runko project create --name <name> --type <type> [--owners a,b] [--path p] [--template t] [--capabilities c,d] | runko project list --runkod-url <url> --token <t>")
	}
	if args[0] == "list" {
		return cmdProjectList(args[1:])
	}
	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	name := fs.String("name", "", "project name")
	projectType := fs.String("type", "", "project type: library|service|app|job|other")
	owners := fs.String("owners", "", "comma-separated owner refs, e.g. group:commerce-eng")
	path := fs.String("path", "", "project path (default: derived from name)")
	template := fs.String("template", "", "template id (default: type's default template)")
	capabilities := fs.String("capabilities", "", "comma-separated capabilities, e.g. http,rpc")
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
		Path:         *path,
		TemplateID:   *template,
		Owners:       splitNonEmpty(*owners),
		Capabilities: splitNonEmpty(*capabilities),
	}

	rev, err := CreateProject(*repoDir, intent)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"name": intent.Name, "path": intent.Path, "rev": rev,
		})
	}
	fmt.Printf("created project %s at %s\n", intent.Name, rev)
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
	if *runkodURL == "" || *token == "" {
		return fmt.Errorf("project list: --runkod-url and --token are required")
	}
	var projects []index.IndexedProject
	if err := apiJSON(context.Background(), http.DefaultClient, http.MethodGet,
		strings.TrimRight(*runkodURL, "/")+"/api/projects", *token, nil, &projects); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(projects)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range projects {
		owners := make([]string, len(p.Owners))
		for i, o := range p.Owners {
			owners[i] = o.Ref
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Type, p.Path, strings.Join(owners, ","))
	}
	return tw.Flush()
}

func cmdChange(args []string) error {
	valid := map[string]bool{"push": true, "land": true, "approve": true, "list": true, "abandon": true, "rerun-check": true}
	if len(args) < 1 || !valid[args[0]] {
		return usageError("usage: runko change push|land|approve|list|abandon|rerun-check ... (see docs/cli-contract.md)")
	}
	switch args[0] {
	case "land":
		return cmdChangeLand(args[1:])
	case "approve":
		return cmdChangeApprove(args[1:])
	case "list":
		return cmdChangeList(args[1:])
	case "abandon":
		return cmdChangeAbandon(args[1:])
	case "rerun-check":
		return cmdChangeRerunCheck(args[1:])
	}

	fs := flag.NewFlagSet("change push", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	remote := fs.String("remote", "origin", "git remote to push to")
	trunk := fs.String("trunk", "main", "trunk ref name")
	jsonOut := fs.Bool("json", false, "emit {change_id, ref} as JSON instead of a human summary")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	changeID, err := PushChange(*repoDir, *remote, *trunk)
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
func cmdChangeLand(args []string) error {
	fs := flag.NewFlagSet("change land", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL, e.g. http://localhost:8080")
	token := fs.String("token", "", "deploy token")
	changeID := fs.String("change", "", "Change-Id to land")
	jsonOut := fs.Bool("json", false, "emit the land.Outcome as JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runkodURL == "" || *token == "" || *changeID == "" {
		return fmt.Errorf("change land: --runkod-url, --token, and --change are required")
	}

	outcome, err := LandChange(context.Background(), http.DefaultClient, *runkodURL, *token, *changeID)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(outcome)
	}
	if outcome.Landed {
		fmt.Printf("landed %s at %s\n", *changeID, outcome.LandedSHA)
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
	if *runkodURL == "" || *token == "" || *changeID == "" || *ownerRef == "" || *by == "" {
		return fmt.Errorf("change approve: --runkod-url, --token, --change, --owner, and --by are required")
	}

	reqs, err := ApproveChange(context.Background(), http.DefaultClient, *runkodURL, *token, *changeID, *ownerRef, *by)
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
	if *runkodURL == "" || *token == "" {
		return fmt.Errorf("change list: --runkod-url and --token are required")
	}
	filter := *state
	if filter == "all" {
		filter = ""
	}
	list, err := ListChanges(context.Background(), http.DefaultClient, *runkodURL, *token, filter)
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
	if *runkodURL == "" || *token == "" || *changeID == "" {
		return fmt.Errorf("change abandon: --runkod-url, --token, and --change are required")
	}
	change, err := AbandonChange(context.Background(), http.DefaultClient, *runkodURL, *token, *changeID)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(change)
	}
	fmt.Printf("abandoned %s (%s)\n", change.ChangeKey, change.Title)
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
	if *runkodURL == "" || *token == "" || *changeID == "" || *name == "" {
		return fmt.Errorf("change rerun-check: --runkod-url, --token, --change, and --name are required")
	}
	reqs, err := RerunCheck(context.Background(), http.DefaultClient, *runkodURL, *token, *changeID, *name)
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
		return usageError("usage: runko workspace create|list|attach|snapshot|branch|update-base ...")
	}
	sub, rest := args[0], args[1:]
	ctx := context.Background()
	switch sub {
	case "create":
		fs := flag.NewFlagSet("workspace create", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		name := fs.String("name", "", "workspace name (also the snapshot-ref segment)")
		by := fs.String("by", "", "who owns this workspace")
		cloneDir := fs.String("clone-dir", "", "shared blobless clone directory (created on first use)")
		dir := fs.String("dir", "", "worktree directory for this workspace")
		var projects stringSliceFlag
		fs.Var(&projects, "project", "project affinity (repeatable)")
		jsonOut := fs.Bool("json", false, "emit the workspace as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *runkodURL == "" || *token == "" || *name == "" || *by == "" || len(projects) == 0 {
			return fmt.Errorf("workspace create: --runkod-url, --token, --name, --by, and at least one --project are required")
		}
		if *cloneDir == "" {
			*cloneDir = "mono"
		}
		if *dir == "" {
			*dir = *name
		}
		info, err := WorkspaceCreate(ctx, http.DefaultClient, *runkodURL, *token, *name, *by, projects, *cloneDir, *dir)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(info)
		}
		fmt.Printf("workspace %s ready at %s (base %s, cone: %s)\n", info.ID, *dir, short(info.BaseRevision), strings.Join(info.SparsePatterns, ", "))
		return nil

	case "list":
		fs := flag.NewFlagSet("workspace list", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the list as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *runkodURL == "" || *token == "" {
			return fmt.Errorf("workspace list: --runkod-url and --token are required")
		}
		list, err := WorkspaceList(ctx, http.DefaultClient, *runkodURL, *token)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, ws := range list {
			branches := strings.Join(ws.Branches, ",")
			if branches == "" {
				branches = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\tbase %s\t%s\tbranches: %s\n", ws.ID, ws.Status, short(ws.BaseRevision), strings.Join(ws.ProjectAffinity, ","), branches)
		}
		return tw.Flush()

	case "attach":
		fs := flag.NewFlagSet("workspace attach", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		cloneDir := fs.String("clone-dir", "", "shared blobless clone directory")
		dir := fs.String("dir", "", "worktree directory for this workspace")
		branch := fs.String("branch", "head", "workspace branch to restore (parallel lines of work, §12.2)")
		jsonOut := fs.Bool("json", false, "emit the workspace as JSON")
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
		if *runkodURL == "" || *token == "" || id == "" {
			return fmt.Errorf("workspace attach: --runkod-url, --token, and a workspace id are required")
		}
		if *cloneDir == "" {
			*cloneDir = "mono"
		}
		if *dir == "" {
			// Two branches of one workspace are two worktrees - they can't
			// share the default directory.
			if *branch == "head" {
				*dir = id
			} else {
				*dir = id + "-" + *branch
			}
		}
		info, err := WorkspaceAttach(ctx, http.DefaultClient, *runkodURL, *token, id, *branch, *cloneDir, *dir)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(info)
		}
		fmt.Printf("workspace %s restored at %s\n", info.ID, *dir)
		return nil

	case "snapshot":
		fs := flag.NewFlagSet("workspace snapshot", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace worktree directory")
		msg := fs.String("m", "", "snapshot message")
		jsonOut := fs.Bool("json", false, "emit {ref} as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		ref, err := WorkspaceSnapshot(*dir, *msg)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref})
		}
		fmt.Printf("snapshot pushed to %s\n", ref)
		return nil

	case "branch":
		fs := flag.NewFlagSet("workspace branch", flag.ExitOnError)
		dir := fs.String("dir", ".", "workspace worktree directory")
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
			return usageError("usage: runko workspace branch <name> [--dir .]")
		}
		ref, err := WorkspaceBranch(*dir, name)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref})
		}
		fmt.Printf("branched: snapshots from here go to %s\n", ref)
		return nil

	case "update-base":
		fs := flag.NewFlagSet("workspace update-base", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		dir := fs.String("dir", ".", "workspace worktree directory")
		jsonOut := fs.Bool("json", false, "emit {base_revision} as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *runkodURL == "" || *token == "" {
			return fmt.Errorf("workspace update-base: --runkod-url and --token are required")
		}
		newBase, err := WorkspaceUpdateBase(ctx, http.DefaultClient, *runkodURL, *token, *dir)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"base_revision": newBase})
		}
		fmt.Printf("rebased onto trunk tip %s\n", short(newBase))
		return nil

	default:
		return usageError("usage: runko workspace create|list|attach|snapshot|branch|update-base ...")
	}
}

// cmdAgentsMD implements `runko agents-md`: (re)write AGENTS.md at the repo
// root from agentsmd.Generate() - §8.8's "reference prompts / skill files
// ... generated per monorepo", stage 11's (§28.3) done-when bar. Overwrites
// unconditionally, matching how sqlc/oapi-codegen generated files in this
// repo are treated: regenerate, don't hand-edit.
func cmdAgentsMD(args []string) error {
	fs := flag.NewFlagSet("agents-md", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	out := fs.String("out", "AGENTS.md", "output path, relative to --repo unless absolute")
	jsonOut := fs.Bool("json", false, "emit {path} as JSON instead of a human summary")
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
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"path": path})
	}
	fmt.Printf("generated %s\n", path)
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
	if *runkodURL == "" || *token == "" {
		return fmt.Errorf("mcp serve: --runkod-url and --token are required")
	}
	srv := &mcp.Server{Client: &mcp.Client{BaseURL: *runkodURL, Token: *token}}
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
