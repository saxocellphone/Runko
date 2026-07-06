// Command runko is the human/agent-facing CLI (docs/design.md §17.1).
//
// Implemented this session (§28.3 stage 9), operating purely on the local
// repo - no control plane required: doctor, project create, change push.
// Stubbed because they require a live control plane not built in this
// environment (no compose/Postgres/network to round-trip against, see
// CLAUDE.md): auth login, workspace create/attach, change create/
// requirements, mcp serve.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/saxocellphone/runko/project"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
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
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "runko: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: runko <command> [flags]

commands (operate on the local repo only):
  doctor                          check remotes/hooks, print a cheat-sheet (§6.9)
  project create --name <n> ...   create a project from an intent, on top of HEAD (§10.1)
  change push                     push HEAD to refs/for/<trunk> for review (§11.5)

not yet implemented (need a live control plane, §19.2):
  auth login, workspace create/attach, change create/requirements, mcp serve`)
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	trunk := fs.String("trunk", "main", "trunk ref name")
	installHook := fs.Bool("install-hook", false, "install the commit-msg Change-Id hook")
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
	PrintCheatSheet(os.Stdout, report)
	return nil
}

func cmdProject(args []string) error {
	if len(args) < 1 || args[0] != "create" {
		return fmt.Errorf("usage: runko project create --name <name> --type <type> [--owners a,b] [--path p] [--template t] [--capabilities c,d]")
	}
	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	name := fs.String("name", "", "project name")
	projectType := fs.String("type", "", "project type: library|service|app|job|other")
	owners := fs.String("owners", "", "comma-separated owner refs, e.g. group:commerce-eng")
	path := fs.String("path", "", "project path (default: derived from name)")
	template := fs.String("template", "", "template id (default: type's default template)")
	capabilities := fs.String("capabilities", "", "comma-separated capabilities, e.g. http,rpc")
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
	fmt.Printf("created project %s at %s\n", intent.Name, rev)
	return nil
}

func cmdChange(args []string) error {
	if len(args) < 1 || args[0] != "push" {
		return fmt.Errorf("usage: runko change push [--remote origin] [--trunk main]")
	}
	fs := flag.NewFlagSet("change push", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	remote := fs.String("remote", "origin", "git remote to push to")
	trunk := fs.String("trunk", "main", "trunk ref name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	changeID, err := PushChange(*repoDir, *remote, *trunk)
	if err != nil {
		return err
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
