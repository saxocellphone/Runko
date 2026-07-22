// The cobra command tree (docs/cli-contract.md; clig.dev redesign,
// 2026-07-22). Every command keeps its pre-cobra name, aliases, --json
// shape, and the 0/1/2 exit-code contract - what changed is the frame:
// grouped help, per-command --help pages, did-you-mean suggestions,
// generated shell completions, and POSIX flag parsing (pflag), which
// retires the stdlib-flag "flags stop at the first positional" trap and
// the id-first popping hacks it forced.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// app carries the global connection flags (--runkod-url/--token,
// registered once as persistent flags on the root) every control-plane
// verb resolves a credential through: flags > RUNKO_RUNKOD_URL/RUNKO_TOKEN
// env > the stored login (§12.7). Constructors close over one app so a
// fresh newRootCmd() is fully self-contained - tests execute isolated
// trees, main executes one.
type app struct {
	runkodURL string
	token     string
	version   bool
	// versionJSON backs the hidden root --json so `runko -v --json` keeps
	// emitting BuildIdentity JSON, exactly like the pre-cobra alias did.
	versionJSON bool
}

// credential resolves the global connection flags to a usable credential -
// the uniform flags > env > stored-login rule (previously agent event's
// verb-local fallback, §12.6.1, made global in the clig.dev redesign).
func (a *app) credential() (Credential, error) {
	return resolveCredentialEnv(a.runkodURL, a.token)
}

// errUsageShown is the sentinel for "help was already printed to stderr,
// just exit 2" - main prints nothing for an empty usageError.
var errUsageShown = usageError("")

func newRootCmd() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:   "runko",
		Short: "The monorepo operating system CLI",
		Long: `Runko is a monorepo operating system layered on Git (docs/design.md):
Projects and CitC-class Workspaces over one repo, change-centric review,
and CI/CD contracts. This CLI is the primary interface for humans and
agents alike (§8.3); every data-producing command takes --json.

The loop (§6.9):
  runko change create -m "<what and why>"   # commit the working tree as one Change
  runko change push                         # submit the stack for review (refs/for/<trunk>)
  runko change land --change <Change-Id>    # land it once the gates are green`,
		Example: `  # First contact with a control plane (signup IS login):
  runko auth signup --runkod-url https://<host> --name <you> --org <org> --create

  # A checkout to work in, then the three-command loop:
  runko workspace create --name <workstream> --project <p>
  runko change create -m "fix: ..." && runko change push`,
		SilenceErrors: true,
		SilenceUsage:  true,
		// Unknown top-level keywords fall through to RunE (the root has
		// subcommands, so cobra only calls this for no args, flags only,
		// or an unrecognized command word).
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.version {
				id := buildIdentity()
				if a.versionJSON {
					return json.NewEncoder(os.Stdout).Encode(id)
				}
				fmt.Printf("runko %s\n", id)
				return nil
			}
			return groupRunE(cmd, args)
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&a.runkodURL, "runkod-url", "", "control-plane base URL, e.g. https://<host>/o/<org> (default: the stored login; env RUNKO_RUNKOD_URL, honored alongside RUNKO_TOKEN)")
	pf.StringVar(&a.token, "token", "", "bearer token, <name>:<token>, or a named principal's password (env RUNKO_TOKEN; default: the stored login)")
	root.Flags().BoolVarP(&a.version, "version", "v", false, "print this binary's build identity")
	root.Flags().BoolVar(&a.versionJSON, "json", false, "with -v/--version: emit the build identity as JSON")
	_ = root.Flags().MarkHidden("json")

	// Flag-parse failures are usage errors (exit 2, docs/cli-contract.md) -
	// one line + a help pointer, never a full usage dump (clig.dev).
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError(fmt.Sprintf("%v\nRun '%s --help' for usage.", err, cmd.CommandPath()))
	})

	root.AddGroup(
		&cobra.Group{ID: "start", Title: "Getting started:"},
		&cobra.Group{ID: "loop", Title: "The daily loop:"},
		&cobra.Group{ID: "repo", Title: "Repo & org management:"},
		&cobra.Group{ID: "agents", Title: "Agents & integrations:"},
	)

	root.AddCommand(
		// Getting started.
		newDoctorCmd(),
		newAuthCmd(a),
		newVersionCmd(),
		newSelfUpdateCmd(),
		// The daily loop.
		newChangeCmd(a),
		newWorkspaceCmd(a),
		// Repo & org management.
		newProjectCmd(a),
		newOrgCmd(a),
		newReleaseCmd(a),
		newGithubCmd(a),
		// Agents & integrations.
		newAgentCmd(a),
		newAgentsMDCmd(),
		newMCPCmd(a),
		newCICmd(),
	)
	return root
}

// groupRunE is the RunE for noun commands run bare (`runko change`) or
// with an unknown subcommand keyword (`runko change pull`): help to
// stderr, exit 2 - the pre-cobra usage-error contract, now with
// did-you-mean suggestions.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return unknownCommand(cmd, args[0])
	}
	cmd.SetOut(cmd.ErrOrStderr())
	_ = cmd.Help()
	return errUsageShown
}

// unknownCommand is the exit-2 refusal for a command keyword the tree
// doesn't know, with cobra's suggestions when one is close enough. main
// prefixes "runko: ", so the message itself carries no binary name.
func unknownCommand(cmd *cobra.Command, name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown command %q for %q", name, cmd.CommandPath())
	// SuggestionsFor reads the threshold straight off the command;
	// zero-valued means "never suggest", so set the cobra default.
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	if s := cmd.SuggestionsFor(name); len(s) > 0 {
		b.WriteString("\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t"))
	}
	fmt.Fprintf(&b, "\n\nRun '%s --help' for usage.", cmd.CommandPath())
	return usageError(b.String())
}

// addWorkspaceFlag registers -w/--workspace on a verb that operates on a
// local checkout: the workspace's registered materialization becomes the
// working directory (resolveWorkspaceDir, materializations.go), so the
// verb runs from anywhere - no cd into the worktree (§12.7).
func addWorkspaceFlag(cmd *cobra.Command) *string {
	return cmd.Flags().StringP("workspace", "w", "", "workspace name[@branch]: run against its registered materialization instead of the current directory (§12.7)")
}

// noArgs is cobra.NoArgs mapped into the exit-2 usage class
// (docs/cli-contract.md): a stray positional on a flags-only command
// refuses with the command's own usage line, not a generic error. The
// pre-cobra CLI silently ignored trailing positionals; refusing beats
// silently doing something other than what was typed (fable review,
// 2026-07-22).
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageError(fmt.Sprintf("%s: unexpected argument %q\nusage: %s", cmd.CommandPath(), args[0], cmd.UseLine()))
	}
	return nil
}

// maxOneArg is noArgs' sibling for the zero-or-one positional forms
// (`workspace path [<name>]`).
func maxOneArg(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return usageError(fmt.Sprintf("%s: expected at most one argument, got %d\nusage: %s", cmd.CommandPath(), len(args), cmd.UseLine()))
	}
	return nil
}

// requireArg enforces exactly one positional (the id-first documented
// forms: `workspace attach <id>`, `change resolve <comment-id>`, ...),
// with the command's own usage line as the exit-2 explanation.
func requireArg(cmd *cobra.Command, args []string, what string) (string, error) {
	switch len(args) {
	case 1:
		return args[0], nil
	case 0:
		return "", usageError(fmt.Sprintf("%s: a %s is required\nusage: %s", cmd.CommandPath(), what, cmd.UseLine()))
	default:
		return "", usageError(fmt.Sprintf("%s: expected one %s, got %d arguments\nusage: %s", cmd.CommandPath(), what, len(args), cmd.UseLine()))
	}
}
