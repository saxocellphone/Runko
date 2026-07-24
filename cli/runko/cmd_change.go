// `runko change` - the Change lifecycle (§7.4, §13.5): create/amend/push
// locally, then the control-plane verbs (requirements, approve, land,
// automerge, ...). Command wiring only; the mechanics live in
// changecreate.go, change.go, land.go, approve.go, changelist.go.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/land"
)

func newChangeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "change",
		Short:   "Create, submit, review, and land Changes",
		GroupID: "loop",
		Long: `A Change is one reviewable unit, identified by its Change-Id trailer
across amends. The loop: create commits the working tree, push submits
to refs/for/<trunk> (one push updates the WHOLE stack), land rebases
onto trunk once the merge gates are green.`,
		Example: `  runko change create -m "checkout: validate address up front"
  runko change push
  runko change requirements            # what still blocks HEAD's Change
  runko change automerge               # land HEAD's Change when the gates go green
  runko change land                    # land HEAD's Change once green`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		newChangeCreateCmd(), newChangeAmendCmd(), newChangePushCmd(),
		newChangeRequirementsCmd(a), newChangeLandCmd(a), newChangeApproveCmd(a),
		newChangeAckPolicyCmd(a),
		newChangeListCmd(a), newChangeAbandonCmd(a), newChangeDescribeCmd(a),
		newChangeAutomergeCmd(a), newChangeRerunCheckCmd(a),
		newChangeCommentCmd(a), newChangeCommentsCmd(a), newChangeResolveCmd(a),
		newChangeRequestReviewCmd(a),
	)
	return cmd
}

func newChangeCreateCmd() *cobra.Command {
	var (
		msg, dir            string
		allowLarge, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create -m <message>",
		Short: "Commit the working tree as one Change",
		Long: `Commits ALL working-tree changes as one commit carrying a fresh
Change-Id trailer. No auto-push - ` + "`change push`" + ` stays the
explicit submit step. Newly-added files that look like build artifacts
(executable+binary, or >=5 MiB) are refused with suspect_artifact;
--allow-large is the escape for an intentional binary asset.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			id, err := CreateChange(wd, msg, allowLarge)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"change_id": id})
			}
			// Nudge the two remaining steps of the submit loop. The description is a
			// SEPARATE control-plane field (§8.6, never derived from the commit
			// message) that RequireDescription gates agent lands on - surfacing it
			// here means an agent sets it up front instead of discovering the blocker
			// only at `change requirements` (FIX #6).
			fmt.Printf("created change %s\n  -> runko change describe --description \"WHAT changed and WHY\"   # agent changes must, before landing\n  -> runko change push                                       # submit it for review\n", id)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&msg, "message", "m", "", "change message (required)")
	fl.StringVar(&dir, "dir", ".", "repository directory")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&allowLarge, "allow-large", false, "commit large/executable untracked files anyway (they are refused by default as suspected build artifacts)")
	fl.BoolVar(&jsonOut, "json", false, "emit {change_id} as JSON")
	return cmd
}

// newChangeAmendCmd (FIX #6): fold the working tree into HEAD's existing
// Change with the Runko-identity fallback, so agents stop dropping to a
// raw `git commit --amend` that fails without a configured git author.
func newChangeAmendCmd() *cobra.Command {
	var (
		msg, dir string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "amend",
		Short: "Fold the working tree into HEAD's Change",
		Long: `The native git commit --amend: folds the working tree into HEAD's
existing Change, PRESERVING its Change-Id. -m rewords; the
default keeps HEAD's message. Refused in a jj colocated checkout
(jj squash / jj describe are the natives there).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			id, err := AmendChange(wd, msg)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"change_id": id})
			}
			fmt.Printf("amended change %s\n  -> runko change push   # re-submit the updated change\n", id)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&msg, "message", "m", "", "new change message (default: keep HEAD's, just fold in the working tree)")
	fl.StringVar(&dir, "dir", ".", "repository directory")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&jsonOut, "json", false, "emit {change_id} as JSON")
	return cmd
}

func newChangePushCmd() *cobra.Command {
	var (
		repoDir, remote, trunk      string
		noSync, noSnapshot, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push HEAD to refs/for/<trunk> for review",
		Long: `Submits for review: pushes HEAD (jj-aware) to refs/for/<trunk>
with the worktree's workspace-origin push options. Auto-syncs a stale
base onto the trunk tip first and, in a workspace-bound checkout,
auto-snapshots the working tree beforehand - both best-effort
opt-outs. One push updates EVERY Change in the stack (series receive).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), repoDir)
			if err != nil {
				return err
			}
			if err := checkPushIdentity(wd); err != nil {
				return err
			}
			changeID, err := pushChange(wd, remote, trunk, !noSync, !noSnapshot)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"change_id": changeID, "ref": "refs/for/" + trunk,
				})
			}
			fmt.Printf("pushed to refs/for/%s (Change-Id: %s)\n", trunk, changeID)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	addWorkspaceFlag(cmd)
	fl.StringVar(&remote, "remote", "origin", "git remote to push to")
	fl.StringVar(&trunk, "trunk", "main", "trunk ref name")
	fl.BoolVar(&noSync, "no-sync", false, "push as-is even when the base is stale (skip the automatic rebase onto the trunk tip)")
	fl.BoolVar(&noSnapshot, "no-snapshot", false, "skip the automatic workspace snapshot before pushing")
	fl.BoolVar(&jsonOut, "json", false, "emit {change_id, ref} as JSON instead of a human summary")
	return cmd
}

func newChangeRequirementsCmd(a *app) *cobra.Command {
	var (
		changeID, dir string
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "requirements",
		Short: "The merge gates for a Change: what still blocks",
		Long: `Reports the merge gates - owners, checks, mergeable, blockers - plus
the attention set (whose turn it is). --change defaults to HEAD's
Change-Id trailer.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			id := changeID
			if id == "" {
				if id, err = headChangeID(wd); err != nil {
					return err
				}
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if id, err = resolveChangeIDArg(context.Background(), http.DefaultClient, cred, id); err != nil {
				return err
			}
			reqs, err := ChangeRequirements(context.Background(), http.DefaultClient, cred, id)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(reqs)
			}
			printRequirements(id, reqs)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id or unique prefix (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&jsonOut, "json", false, "emit the merge requirements as JSON")
	return cmd
}

// newChangeLandCmd (§13.5, §28.3 stage 11b) - unlike push/project
// create/agents-md, this genuinely needs a live runkod instance to talk
// to (see LandChange's doc comment in land.go).
func newChangeLandCmd(a *app) *cobra.Command {
	var (
		changeID, repoDir, remote, trunk string
		force, sync, jsonOut             bool
		syncTimeout                      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "land [--change <Change-Id>]",
		Short: "Land a mergeable Change onto trunk",
		Long: `Rebase-lands a mergeable Change. --change defaults to HEAD's
Change-Id trailer. On requires_revalidation (trunk moved under it) the
default --sync recovery loop runs right here: sync onto trunk, re-push,
wait for checks, retry - bounded by --sync-timeout. --force is the admin
override: bypasses owner/check gates and revalidation (server-authorized;
never conflicts or stacking order), audited as landed_forced.`,
		Example: `  runko change land                              # land HEAD's Change
  runko change land --change I6a3f...
  runko change land -w my-workstream              # recovery checkout by name`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), repoDir)
			if err != nil {
				return err
			}
			id := changeID
			if id == "" {
				if id, err = headChangeID(wd); err != nil {
					return err
				}
				fmt.Fprintf(warnWriter, "landing HEAD's change %s\n", id)
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if id, err = resolveChangeIDArg(context.Background(), http.DefaultClient, cred, id); err != nil {
				return err
			}
			// The recovery loop needs a checkout to rebase; without one (or with
			// --force, which bypasses revalidation anyway) land is a single shot.
			var outcome land.Outcome
			if _, gitErr := runGit(wd, "rev-parse", "--git-dir"); sync && !force && gitErr == nil {
				outcome, err = LandWithSync(context.Background(), http.DefaultClient, cred, id, wd, remote, trunk, syncTimeout, os.Stderr)
			} else {
				outcome, err = LandChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, force)
			}
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(outcome)
			}
			if outcome.Landed {
				if force {
					fmt.Printf("FORCE-landed %s at %s (merge gates bypassed - audited as landed_forced)\n", id, outcome.LandedSHA)
				} else {
					fmt.Printf("landed %s at %s\n", id, outcome.LandedSHA)
				}
			} else {
				fmt.Printf("not landed: %+v\n", outcome)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id or unique prefix (default: HEAD's Change-Id trailer)")
	fl.BoolVar(&force, "force", false, "admin override: bypass owner/check gates and revalidation; audited as landed_forced")
	fl.StringVar(&repoDir, "repo", ".", "local checkout used by --sync to rebase and re-push on requires_revalidation (and for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.StringVar(&remote, "remote", "origin", "git remote --sync pushes to")
	fl.StringVar(&trunk, "trunk", "main", "trunk ref name")
	fl.BoolVar(&sync, "sync", true, "on requires_revalidation, run the 13.5 recovery loop here: sync onto trunk, re-push, wait for checks, retry")
	fl.DurationVar(&syncTimeout, "sync-timeout", 15*time.Minute, "wall-clock bound on the --sync recovery loop")
	fl.BoolVar(&jsonOut, "json", false, "emit the land.Outcome as JSON instead of a human summary")
	return cmd
}

// newChangeApproveCmd (§13.5, §28.3 stage 11c): record that a required
// owner approves this Change, and print the refreshed merge requirements -
// what the approval covered, what still blocks.
func newChangeApproveCmd(a *app) *cobra.Command {
	var (
		changeID, ownerRef, by string
		jsonOut                bool
	)
	cmd := &cobra.Command{
		Use:   "approve --change <Change-Id> --owner <ref>",
		Short: "Record an owner approval on a Change",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if changeID == "" || ownerRef == "" {
				return fmt.Errorf("change approve: --change and --owner are required\n  -> runko change approve --change <Id> --owner <ref>   # e.g. --owner group:eng")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			id, err := resolveChangeIDArg(context.Background(), http.DefaultClient, cred, changeID)
			if err != nil {
				return err
			}
			// --by stays optional for a signed-in principal: the server derives
			// the approver from the credential (and rejects a mismatch).
			reqs, err := ApproveChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, ownerRef, by)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(reqs)
			}
			fmt.Printf("approved %s on %s\n", ownerRef, id)
			if reqs.Mergeable {
				fmt.Println("mergeable: yes")
			} else {
				fmt.Println("mergeable: no")
				for _, b := range reqs.Blockers {
					fmt.Printf("  - %s\n", b)
				}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id or unique prefix to approve")
	fl.StringVar(&ownerRef, "owner", "", "owner requirement being satisfied, e.g. group:commerce-eng")
	fl.StringVar(&by, "by", "", "who is approving (default: the signed-in principal)")
	fl.BoolVar(&jsonOut, "json", false, "emit the merge requirements as JSON instead of a human summary")
	return cmd
}

// newChangeAckPolicyCmd (2026-07-24 enforcement split): the "extra button"
// - a human with approve rights acknowledges an agent change's policy
// findings, completing the reserved agent-policy check, and sees the
// refreshed merge requirements.
func newChangeAckPolicyCmd(a *app) *cobra.Command {
	var (
		changeID, by string
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:   "ack-policy --change <Change-Id>",
		Short: "Acknowledge an agent change's policy findings",
		Long: `An agent change that touched denylisted paths, blew a size cap, or
edited ownership is accepted at push but carries the agent-policy
check, red until a human with approve rights acknowledges the
findings after reading the diff. This is that acknowledgement.
An amend re-evaluates policy and resets it. Agents are refused.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if changeID == "" {
				return fmt.Errorf("change ack-policy: --change is required")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			reqs, err := AckPolicy(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), changeID, by)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(reqs)
			}
			fmt.Printf("acknowledged agent-policy findings on %s\n", changeID)
			if reqs.Mergeable {
				fmt.Println("mergeable: yes")
			} else {
				fmt.Println("mergeable: no")
				for _, b := range reqs.Blockers {
					fmt.Printf("  - %s\n", b)
				}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id whose findings to acknowledge")
	fl.StringVar(&by, "by", "", "who is acknowledging (default: the signed-in principal)")
	fl.BoolVar(&jsonOut, "json", false, "emit the merge requirements as JSON instead of a human summary")
	return cmd
}

func newChangeListCmd(a *app) *cobra.Command {
	var (
		state   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List changes, newest first",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			filter := state
			if filter == "all" {
				filter = ""
			}
			list, err := ListChanges(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), filter)
			if err != nil {
				return err
			}
			if jsonOut {
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
		},
	}
	cmd.Flags().StringVar(&state, "state", "open", "filter: open|landed|abandoned|all")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the change list as JSON")
	return cmd
}

func newChangeAbandonCmd(a *app) *cobra.Command {
	var (
		changeID string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "abandon --change <Change-Id>",
		Short: "Abandon an open change",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if changeID == "" {
				// Stay explicit: abandon is destructive and must not default to HEAD.
				return fmt.Errorf("change abandon: --change is required\n  -> pass --change <Id> explicitly (find it with `runko change list` or `runko status`)")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			id, err := resolveChangeIDArg(context.Background(), http.DefaultClient, cred, changeID)
			if err != nil {
				return err
			}
			change, err := AbandonChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(change)
			}
			fmt.Printf("abandoned %s (%s)\n", change.ChangeKey, change.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&changeID, "change", "", "Change-Id or unique prefix to abandon")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the abandoned change as JSON")
	return cmd
}

func newChangeDescribeCmd(a *app) *cobra.Command {
	var (
		changeID, dir, description, testPlan string
		jsonOut                              bool
	)
	cmd := &cobra.Command{
		Use:   "describe --description <text>",
		Short: "Set a Change's description and test plan",
		Long: `Sets the review summary on an open change: --description says what the
change does and why, --test-plan how it was verified - agents SHOULD
set both after push (RequireDescription gates agent lands). A separate
control-plane field, never derived from the commit message; an omitted
flag preserves the stored value, an explicit "" clears it.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A flag the caller never passed means "leave that field alone"; an
			// explicit --description "" clears it - distinguished by Changed.
			var descPtr, planPtr *string
			if cmd.Flags().Changed("description") {
				descPtr = &description
			}
			if cmd.Flags().Changed("test-plan") {
				planPtr = &testPlan
			}
			if descPtr == nil && planPtr == nil {
				return fmt.Errorf("change describe: provide --description and/or --test-plan")
			}
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			id := changeID
			if id == "" {
				if id, err = headChangeID(wd); err != nil {
					return err
				}
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if id, err = resolveChangeIDArg(context.Background(), http.DefaultClient, cred, id); err != nil {
				return err
			}
			change, err := DescribeChange(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, descPtr, planPtr)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(change)
			}
			fmt.Printf("described %s (%s)\n", change.ChangeKey, change.Title)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id or unique prefix (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.StringVar(&description, "description", "", "what the change does and why")
	fl.StringVar(&testPlan, "test-plan", "", "how the change was verified")
	fl.BoolVar(&jsonOut, "json", false, "emit the updated change as JSON")
	return cmd
}

func newChangeAutomergeCmd(a *app) *cobra.Command {
	var (
		changeID, dir string
		disable       bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "automerge [--change <Change-Id>]",
		Short: "Arm the when-ready land",
		Long: `Arms automerge: the server lands the change automatically the
moment its checks and approvals go green, attributed to the armer,
surviving amends (gates reset and re-gate). The alternative to
poll-and-land loops. --change defaults to HEAD's Change-Id trailer.
--disable disarms.`,
		Example: `  runko change automerge                      # arm HEAD's Change
  runko change automerge --change I6a3f...
  runko change automerge --disable`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := resolveChangeFlag(changeID, mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			if changeID == "" {
				verb := "arming"
				if disable {
					verb = "disarming"
				}
				fmt.Fprintf(warnWriter, "%s automerge for HEAD's change %s\n", verb, id)
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if id, err = resolveChangeIDArg(context.Background(), http.DefaultClient, cred, id); err != nil {
				return err
			}
			var change struct {
				ChangeKey   string
				Title       string
				Automerge   bool
				AutomergeBy string
			}
			err = apiJSON(context.Background(), http.DefaultClient, http.MethodPost,
				strings.TrimSuffix(cred.URL, "/")+"/api/changes/"+id+"/automerge", cred.AuthHeader(),
				map[string]bool{"enabled": !disable}, &change)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(change)
			}
			if change.Automerge {
				fmt.Printf("automerge armed on %s - it lands itself when the gates go green (armed by %s)\n", change.ChangeKey, change.AutomergeBy)
			} else {
				fmt.Printf("automerge disarmed on %s\n", change.ChangeKey)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id or unique prefix (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&disable, "disable", false, "disarm instead")
	fl.BoolVar(&jsonOut, "json", false, "emit the change as JSON")
	return cmd
}

func newChangeRerunCheckCmd(a *app) *cobra.Command {
	var (
		changeID, name string
		jsonOut        bool
	)
	cmd := &cobra.Command{
		Use:   "rerun-check --change <Change-Id> --name <check>",
		Short: "Request a required check re-run",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if changeID == "" || name == "" {
				return fmt.Errorf("change rerun-check: --change and --name are required\n  -> runko change rerun-check --change <Id> --name <check>")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			id, err := resolveChangeIDArg(context.Background(), http.DefaultClient, cred, changeID)
			if err != nil {
				return err
			}
			reqs, err := RerunCheck(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, name)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(reqs)
			}
			fmt.Printf("rerun requested for %s on %s\n", name, id)
			for _, b := range reqs.Blockers {
				fmt.Printf("  - %s\n", b)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&changeID, "change", "", "Change-Id or unique prefix whose check to rerun")
	cmd.Flags().StringVar(&name, "name", "", "required check name to rerun")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the refreshed merge requirements as JSON")
	return cmd
}

// mustWorkspaceFlag reads the -w/--workspace value a constructor
// registered via addWorkspaceFlag - by the time RunE runs, the flag
// always exists.
func mustWorkspaceFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("workspace")
	return v
}

// agentTokenSecret returns the password for `workspace create --as <name>
// --token X`. `runko agent create` prints the credential in Basic
// "name:token" form (its "--token name:token" line), so pasting that whole
// string as --token would send the wrong password (a bare 401). If --token
// is "<name>:<secret>" and the name matches --as, use just the secret.
func agentTokenSecret(as, token string) string {
	if name, secret, ok := strings.Cut(token, ":"); ok && name == as {
		return secret
	}
	return token
}

// checkPushIdentity pre-flights the workspace-owner <-> credential match
// before a push. A workspace-bound worktree authors as its stamped
// runko.owner (`runko workspace create`), and the server refuses a push
// authenticated as any OTHER named principal ("workspace belongs to
// <owner>", runkod/prereceive.go). Catch that here with the actionable fix
// instead of an opaque pre-receive rejection (dogfood papercut, 2026-07-22).
//
// It checks only the STORED-LOGIN path - the one that silently
// authenticates as the wrong principal. When an EXPLICIT override is in
// play (a URL-embedded credential, or the RUNKO_TOKEN env the agent flow
// documents), the push uses THAT instead (gitNetEnv's resolution order), so
// this skips rather than second-guess a deliberate choice - never a false
// block of a working setup.
func checkPushIdentity(repoDir string) error {
	owner, err := runGit(repoDir, "config", "runko.owner")
	if err != nil {
		return nil // not a workspace-bound worktree (or no git): nothing to check
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil
	}
	if os.Getenv("RUNKO_TOKEN") != "" {
		return nil // env token wins over the stored login; let it (and the server) decide
	}
	if remote, err := runGit(repoDir, "config", "remote.origin.url"); err == nil {
		if u, err := url.Parse(remote); err == nil && u.User != nil {
			return nil // URL-embedded credential answers the push; we can't name it
		}
	}
	cred, ok, err := loadCredential()
	// An anonymous (bearer/deploy) login has no Name and the server's owner
	// check bypasses it; only a NAMED login that isn't the owner is refused.
	if err != nil || !ok || cred.Name == "" || cred.Name == owner {
		return nil
	}
	return ownerCredentialMismatch(owner, cred.Name)
}

// ownerCredentialMismatch is the structured error checkPushIdentity raises.
func ownerCredentialMismatch(owner, credName string) *clierr.Error {
	return &clierr.Error{
		Code:  "workspace_owner_mismatch",
		Field: "auth",
		Message: fmt.Sprintf(
			"this worktree authors as %s, but your stored login is %s - the server refuses a push that claims another principal's workspace",
			owner, credName),
		Suggestion: fmt.Sprintf(
			"authenticate as the owner and retry from that shell: XDG_CONFIG_HOME=<dir> runko auth login --name %s --token <tok> (the token `runko agent create` printed), or set RUNKO_TOKEN",
			owner),
		DocURL: "docs/cli-contract.md",
	}
}
