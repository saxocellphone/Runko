// `runko workspace` (§12.3 Phase A, §12.7, §28.3 stage 12b):
// create/list/attach/path/gc/snapshot/watch/branch/sync/delete. Command
// wiring only; the mechanics live in workspace.go, materializations.go,
// workspacegc.go, sync.go, watch.go.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
)

func newWorkspaceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Short:   "Materialize and manage workspaces",
		GroupID: "loop",
		Long: `A workspace is a durable workstream (§12.1-12.3): full-repo view,
materialized as a worktree holding only your slice (sparse cone), with
WIP made durable via snapshot refs pushed through the normal receive
path. One workspace = one task; parallel lines within it are branches.`,
		Example: `  runko workspace create --name checkout-fix --project checkout
  runko workspace snapshot -m "wip"     # durable WIP, secret-scanned
  runko workspace sync                  # rebase onto the trunk tip
  runko workspace list`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(
		newWorkspaceCreateCmd(a), newWorkspaceListCmd(a), newWorkspaceAttachCmd(a),
		newWorkspacePathCmd(), newWorkspaceGCCmd(a), newWorkspaceSnapshotCmd(),
		newWorkspaceWatchCmd(), newWorkspaceBranchCmd(), newWorkspaceSyncCmd(a),
		newWorkspaceDeleteCmd(a),
	)
	return cmd
}

func newWorkspaceCreateCmd(a *app) *cobra.Command {
	var (
		name, by, as, cloneDir, dir    string
		forceNested, jjClient, jsonOut bool
		projects, newPaths             []string
	)
	cmd := &cobra.Command{
		Use:   "create --name <name> --project <p> [--project <p2>...]",
		Short: "Register and materialize a new workspace",
		Long: `Registers the workspace (registry row + snapshot ref namespace) and
materializes it: worktree off a shared blobless clone, sparse cone
covering the --project affinities, hooks and credential helper wired
(§12.7). With no --dir/--clone-dir everything lands under
$RUNKO_WORKSPACE_HOME (default ~/runko-ws). --new-path grants affinity
for a project that does not exist at trunk yet (the greenfield
bootstrap). --jj materializes a standalone jj colocated checkout
instead (full clone; jj cannot lazy-fetch promisor blobs).`,
		Example: `  runko workspace create --name checkout-fix --project checkout
  runko workspace create --name new-svc --new-path services/new-svc
  runko workspace create --name fix --project checkout --by agent-x --as agent-x --token <tok>`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			// --as authenticates this one command as a named principal (typically
			// the agent) without storing or clobbering a credential (FIX #3): admin
			// mints the token, then `workspace create --by agent-x --as agent-x
			// --token <tok>` registers AND materializes as the agent. No
			// XDG_CONFIG_HOME, no separate `auth login`.
			if as != "" {
				if a.token == "" {
					return &clierr.Error{
						Code: "missing_token", Field: "token",
						Message:    "--as needs --token (the principal's password)",
						Suggestion: "pass the token `runko agent create --task <slug>` printed, e.g. --as agent-<slug>-xxxx --token <tok>",
					}
				}
				cred = Credential{URL: cred.URL, Name: as, Secret: agentTokenSecret(as, a.token)}
			}
			// The stored login already says who you are (§6.10): --by stays an
			// override, not a toll. Only the anonymous deploy token has no name
			// to default to.
			if by == "" {
				by = cred.Name
			}
			if name == "" || by == "" || len(projects)+len(newPaths) == 0 {
				return fmt.Errorf("workspace create: --name and at least one --project (or --new-path) are required (and --by, when signed in with a bare token)")
			}
			info, wsDir, err := WorkspaceCreate(cmd.Context(), http.DefaultClient, cred.URL, cred.AuthHeader(), name, by, projects, newPaths,
				MaterializeOptions{CloneDir: cloneDir, Dir: dir, ForceNested: forceNested, JJ: jjClient})
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(struct {
					WorkspaceInfo
					Dir string
				}{info, wsDir})
			}
			mode := ""
			if jjClient {
				mode = "jj colocated, "
			}
			fmt.Printf("workspace %s ready at %s (%sbase %s, cone: %s)\n", info.ID, wsDir, mode, short(info.BaseRevision), strings.Join(info.SparsePatterns, ", "))
			printWorkspaceStreamingGuidance(os.Stdout, info.ID)
			printWorkspaceLoop(os.Stdout, info.ID)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "workspace name (also the snapshot-ref segment)")
	fl.StringVar(&by, "by", "", "who owns this workspace (default: the stored login's principal)")
	fl.StringVar(&as, "as", "", "authenticate as this named principal using --token as its password (Basic, not stored) - the no-XDG agent form")
	fl.StringVar(&cloneDir, "clone-dir", "", "shared blobless clone directory (default: the managed home's .store, §12.7)")
	fl.StringVar(&dir, "dir", "", "worktree directory (default: under the managed home, ~/runko-ws)")
	fl.BoolVar(&forceNested, "force-nested", false, "materialize inside another git checkout anyway")
	fl.BoolVar(&jjClient, "jj", false, "standalone jj colocated checkout (jj + .git side by side, Change-Ids from jj change ids) instead of a worktree off the shared store")
	fl.StringArrayVar(&projects, "project", nil, "project affinity (repeatable)")
	fl.StringArrayVar(&newPaths, "new-path", nil, "path root for a project NOT on trunk yet (repeatable) - the greenfield bootstrap")
	fl.BoolVar(&jsonOut, "json", false, "emit the workspace (+ Dir) as JSON")
	return cmd
}

func newWorkspaceListCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "My workstreams, cones, base revisions",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			list, err := WorkspaceList(cmd.Context(), http.DefaultClient, cred.URL, cred.AuthHeader())
			if err != nil {
				return err
			}
			if jsonOut {
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
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the list as JSON")
	return cmd
}

func newWorkspaceAttachCmd(a *app) *cobra.Command {
	var (
		cloneDir, dir, branch          string
		forceNested, jjClient, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "attach <id>",
		Short: "Restore a workspace from its snapshot ref",
		Long: `Materializes an existing workspace's branch from its snapshot ref
(§12.2) - the restore path after a lost machine, or a second checkout
for a parallel branch (branch attaches land at <workspace>@<branch>
under the managed home).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireArg(cmd, args, "workspace id")
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			info, wsDir, err := WorkspaceAttach(cmd.Context(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, branch,
				MaterializeOptions{CloneDir: cloneDir, Dir: dir, ForceNested: forceNested, JJ: jjClient})
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(struct {
					WorkspaceInfo
					Dir string
				}{info, wsDir})
			}
			mode := ""
			if jjClient {
				mode = " (jj colocated)"
			}
			fmt.Printf("workspace %s restored at %s%s\n", info.ID, wsDir, mode)
			// A non-default branch attach materializes that branch's own row, so
			// the -w handle it teaches has to carry it (name@branch, §12.7).
			printWorkspaceStreamingGuidance(os.Stdout, workspaceHandle(info.ID, branch))
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&cloneDir, "clone-dir", "", "shared blobless clone directory (default: the managed home's .store, §12.7)")
	fl.StringVar(&dir, "dir", "", "worktree directory (default: under the managed home; branches land at <workspace>@<branch>)")
	fl.StringVar(&branch, "branch", "head", "workspace branch to restore (parallel lines of work, §12.2)")
	fl.BoolVar(&forceNested, "force-nested", false, "materialize inside another git checkout anyway")
	fl.BoolVar(&jjClient, "jj", false, "restore as a standalone jj colocated checkout instead of a worktree off the shared store")
	fl.BoolVar(&jsonOut, "json", false, "emit the workspace (+ Dir) as JSON")
	return cmd
}

func newWorkspacePathCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "path [<name[@branch]>]",
		Short: "Print a workspace's local directory",
		Long: `Scripting glue for the rare case -w cannot cover (§12.7):
cd $(runko workspace path <name>). With no name, the current checkout
answers for itself.`,
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			m, err := workspacePathLookup(name)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"workspace": m.Workspace, "branch": m.Branch, "path": m.Path,
				})
			}
			fmt.Println(m.Path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {workspace, branch, path} as JSON")
	return cmd
}

func newWorkspaceGCCmd(a *app) *cobra.Command {
	var (
		apply, jsonOut bool
		idle           time.Duration
		scans          []string
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Reclaim closed and synced materializations",
		Long: `Plans (default) or executes (--apply) reclamation of this machine's
materializations (§12.7): reclaimable means server-closed AND the
working tree provably preserved under its snapshot ref - everything
doubtful is a fail-closed keep with the reason named.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			plan, err := WorkspaceGC(cmd.Context(), http.DefaultClient, cred.URL, cred.AuthHeader(),
				GCOptions{Apply: apply, Idle: idle, Scan: scans})
			if err != nil {
				return err
			}
			if jsonOut {
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
			case apply:
				fmt.Printf("reclaimed %d materialization(s), %s\n", nReclaim, humanBytes(bytesReclaim))
			case nReclaim > 0:
				fmt.Printf("plan only - rerun with --apply to reclaim %d materialization(s), %s\n", nReclaim, humanBytes(bytesReclaim))
			default:
				fmt.Println("nothing reclaimable - every materialization is open, dirty, or not provably synced")
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&apply, "apply", false, "execute the plan (default: print it)")
	fl.DurationVar(&idle, "idle", 0, "also sweep OPEN workspaces idle this long (their durable state is server-side; re-attach recreates them)")
	fl.StringArrayVar(&scans, "scan", nil, "store directory whose worktrees are adopted into the registry first (repeatable; the pre-§12.7 migration path)")
	fl.BoolVar(&jsonOut, "json", false, "emit the plan as JSON")
	return cmd
}

func newWorkspaceSnapshotCmd() *cobra.Command {
	var (
		dir, msg string
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Commit WIP to the workspace's snapshot ref",
		Long: `Commits the working tree onto the workspace branch and pushes it to
refs/workspaces/<id>/<branch> (§12.2) - durable, secret-scanned WIP; a
killed session loses nothing. In a jj colocated checkout the snapshot
is built out-of-band, never a commit on the checked-out line.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			ref, err := WorkspaceSnapshot(wd, msg)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref})
			}
			fmt.Printf("snapshot pushed to %s\n", ref)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&dir, "dir", ".", "workspace worktree directory")
	addWorkspaceFlag(cmd)
	fl.StringVarP(&msg, "message", "m", "", "snapshot message")
	fl.BoolVar(&jsonOut, "json", false, "emit {ref} as JSON")
	return cmd
}

func newWorkspaceWatchCmd() *cobra.Command {
	var (
		dir      string
		interval time.Duration
		once     bool
		jsonOut  bool
	)
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Auto-snapshot the working tree while it changes",
		Long: `The §12.6 streaming loop: every --interval it builds the working
tree's snapshot OUT-OF-BAND (temp index + commit-tree - HEAD, the real
index, and the worktree are never touched, safe beside a working agent
or jj) and force-pushes when the tree moved. The workspace page shows
the WIP live.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			return WorkspaceWatch(WatchOptions{Dir: wd, Interval: interval, Once: once, JSON: jsonOut})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&dir, "dir", ".", "workspace worktree directory")
	addWorkspaceFlag(cmd)
	fl.DurationVar(&interval, "interval", 15*time.Second, "check-and-push cadence while dirty")
	fl.BoolVar(&once, "once", false, "one check-and-push tick, then exit (tests, CI)")
	fl.BoolVar(&jsonOut, "json", false, "NDJSON: one {ref, sha} line per pushed snapshot")
	return cmd
}

func newWorkspaceBranchCmd() *cobra.Command {
	var (
		dir     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "branch <name>",
		Short: "Fork a parallel line of work",
		Long: `Forks a parallel line at the current HEAD (§12.2): snapshots from
this worktree now target refs/workspaces/<id>/<name>. Each branch
reviews and lands on its own - the fix for unrelated work sharing a
stack.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(cmd, args, "branch name")
			if err != nil {
				return err
			}
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			ref, err := WorkspaceBranch(wd, name)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref})
			}
			fmt.Printf("branched: snapshots from here go to %s\n", ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "workspace worktree directory")
	addWorkspaceFlag(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {ref} as JSON")
	return cmd
}

func newWorkspaceSyncCmd(a *app) *cobra.Command {
	var (
		dir     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:     "sync",
		Aliases: []string{"update-base"}, // the original stage-12b name
		Short:   "Rebase the workspace onto the trunk tip",
		Long: `The CitC "sync to head" verb (§12.3): fetch trunk, rebase the
workspace line onto its tip (jj-aware: descendants follow), record the
new base in the registry. Conflicts abort and name the files.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			newBase, err := WorkspaceUpdateBase(cmd.Context(), http.DefaultClient, cred.URL, cred.AuthHeader(), wd)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"base_revision": newBase})
			}
			fmt.Printf("synced onto trunk tip %s\n", short(newBase))
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "workspace worktree directory")
	addWorkspaceFlag(cmd)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {base_revision} as JSON")
	return cmd
}

func newWorkspaceDeleteCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a workspace's registry row and snapshot refs",
		Long: `Removes the registry row and every refs/workspaces/<id>/* snapshot
ref. Refused while the workspace has open changes - land or abandon
first. Local checkouts are never touched (workspace gc reclaims them).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := requireArg(cmd, args, "workspace id")
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if err := WorkspaceDelete(cmd.Context(), http.DefaultClient, cred.URL, cred.AuthHeader(), id); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"deleted": id})
			}
			fmt.Printf("deleted workspace %s (registry row + snapshot refs; local checkouts are yours to remove)\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the result as JSON")
	return cmd
}
