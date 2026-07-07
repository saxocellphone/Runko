// Command runko is the human/agent-facing CLI (docs/design.md §17.1).
//
// Implemented this session (§28.3 stage 9), operating purely on the local
// repo - no control plane required: doctor, project create, change push.
// Stubbed because they require a live control plane not built in this
// environment (no compose/Postgres/network to round-trip against, see
// CLAUDE.md): auth login, workspace create/attach, change create/
// requirements, mcp serve.
//
// Exit codes (docs/cli-contract.md, added in the §8.3 CLI-first audit):
// 0 success, 1 a recognized command failed (structured error printed to
// stderr), 2 usage error (unknown command, wrong subcommand, missing
// positional keyword) - flag-parsing errors already exit 2 via stdlib
// flag.ExitOnError, this file's usageError type extends the same code to
// this package's own pre-flag-parsing usage checks.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

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
	case "auth", "workspace", "mcp":
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

not yet implemented (need a live control plane, §19.2):
  auth login, workspace create/attach, change create/requirements, mcp serve

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
	if len(args) < 1 || args[0] != "create" {
		return usageError("usage: runko project create --name <name> --type <type> [--owners a,b] [--path p] [--template t] [--capabilities c,d]")
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

func cmdChange(args []string) error {
	if len(args) < 1 || args[0] != "push" {
		return usageError("usage: runko change push [--remote origin] [--trunk main]")
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
