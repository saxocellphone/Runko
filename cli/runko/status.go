// `runko status` - the orientation verb: where does this checkout stand.
// One look answers the questions an agent (or a human returning to a
// terminal) otherwise assembles from four commands: which workspace and
// branch this checkout is bound to (`doctor` reports wiring, not
// standing), who the credential authenticates as (`auth status`), whether
// the base has gone stale under trunk (`change push`'s auto-sync decides
// silently), and what the local stack looks like with each change's
// server-side gates (`change requirements`, one change at a time).
//
// Local facts always answer; server enrichment degrades to "unknown" with
// the reason named in ServerError, so the command works offline exactly
// as far as git does - a status verb that dies on a dropped connection
// answers the question backwards.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/receive"
)

// StackEntry is one commit above trunk in this checkout's line, bottom to
// top, with its server-side standing.
type StackEntry struct {
	ChangeID string // "" when the commit carries no Change-Id trailer yet
	SHA      string
	Title    string
	// Status: "ready" (open + mergeable), "blocked" (open, gates
	// outstanding - see Blockers), "landed"/"abandoned" (the server's own
	// state - landed entries mean this line still carries commits trunk
	// already has, e.g. a stale local trunk ref), "not_pushed" (the
	// control plane has no such change), "not_a_change" (no Change-Id
	// trailer yet - a described jj working-copy commit, or a raw git
	// commit in an unhooked checkout; there is nothing to ask the server
	// about), "unknown" (server state unavailable).
	Status   string
	Blockers []string
}

// StatusReport is what `runko status` reports: the checkout's standing,
// local facts first, server facts where a credential resolves.
type StatusReport struct {
	Dir           string
	IsJJWorkspace bool
	// WorkspaceID/Branch: the runko.workspace/runko.branch binding ("" /
	// "" outside a workspace-bound checkout; Branch defaults to "head"
	// when bound). WorkspaceStatus is the server's row ("" when unknown).
	WorkspaceID     string
	Branch          string
	WorkspaceStatus string
	Remote          string
	TrunkRef        string
	// DirtyPaths counts uncommitted paths - what the next `change create`
	// would sweep in. jj checkouts use `jj diff -r @ --summary` (not git
	// status: git over-counts paths outside jj's sparse set). If that jj
	// command fails we fall back to the git count rather than reporting a
	// confident zero - status is the orientation verb and a silent "clean"
	// on a dirty tree is worse than an overstated number.
	DirtyPaths int
	// StaleBase: the remote trunk tip is missing from this line's
	// ancestry, so a sync (or push's auto-sync) would rebase. Best-effort:
	// an unreachable remote reads as not-stale, matching staleBase.
	StaleBase bool
	// Principal/ControlPlane: who the resolved credential authenticates
	// as, and where. ServerError names why server facts are missing (no
	// credential, unreachable control plane); "" when live.
	Principal    string
	ControlPlane string
	ServerError  string
	// AuthorsAs is the checkout's bound authoring identity (runko.owner
	// with a credential to back it, principal.go) - who this checkout
	// pushes as regardless of the machine's login. "" when unbound.
	AuthorsAs string
	// TrunkSHA/TrunkTitle: the local remote-tracking trunk ref the stack
	// diffs against (the ◆ base node of the human graph). May lag the
	// real remote tip - that is what StaleBase reports.
	TrunkSHA   string
	TrunkTitle string
	// Stack is the local line above the remote trunk ref, bottom -> top.
	// Nil when no refs/remotes/<remote>/<trunk> exists locally to diff
	// against; empty when nothing is in flight (fully landed, or only
	// jj's undescribed working-copy commit which is reported via DirtyPaths).
	Stack []StackEntry
	// MirrorFrozenRefs are the outbound-mirror refs currently frozen on
	// divergence - DEPLOYMENT-WIDE state, not this checkout's. Reported
	// here because a frozen trunk ref is silent everywhere else: landing
	// keeps succeeding, but no landed commit reaches the mirror, so
	// post-land CI (which checks out the landed head THERE) stops running
	// and nothing deploys. Prod ran 19h in exactly that state on
	// 2026-07-24 without a single surface saying so. Empty when healthy,
	// when no mirror is configured, or when the read failed - this is a
	// warning channel, never a gate.
	MirrorFrozenRefs []string
}

// mirrorStatusView is the subset of GET /api/mirror/status this command
// reads. The daemon marshals that payload with Go field names and no tags,
// so these names must match its own (encoding/json matches case-
// insensitively, but not across renames).
type mirrorStatusView struct {
	Configured bool
	Cursors    []struct {
		Ref    string
		Frozen bool
	}
}

// RunStatus builds the report. cred is nil when no credential resolved -
// credErr then says why, and every server-side field stays zero.
func RunStatus(ctx context.Context, client *http.Client, cred *Credential, credErr, dir, remote, trunk string) (StatusReport, error) {
	if _, err := runGit(dir, "rev-parse", "--git-dir"); err != nil {
		return StatusReport{}, &clierr.Error{
			Code:       "not_a_repo",
			Field:      "repo",
			Message:    fmt.Sprintf("%s is not a git repository", dir),
			Suggestion: "run `runko status` inside a checkout, or name a workspace: `runko status -w <name>`",
		}
	}

	r := StatusReport{Dir: dir, Remote: remote, TrunkRef: trunk, ServerError: credErr}
	r.IsJJWorkspace = isJJWorkspace(dir)
	r.WorkspaceID, _ = runGit(dir, "config", "runko.workspace")
	if r.WorkspaceID != "" {
		r.Branch, _ = runGit(dir, "config", "runko.branch")
		if r.Branch == "" {
			r.Branch = "head"
		}
	}
	r.DirtyPaths = dirtyPaths(dir, r.IsJJWorkspace)
	if bound, ok := checkoutPrincipal(dir); ok {
		r.AuthorsAs = bound.Name
	}
	r.StaleBase = staleBase(dir, remote, trunk)
	if base, err := runGit(dir, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+trunk); err == nil {
		r.TrunkSHA = base
		r.TrunkTitle, _ = runGit(dir, "log", "-1", "--format=%s", base)
		r.Stack = statusStack(dir, base, r.IsJJWorkspace)
	}

	if cred == nil {
		return r, nil
	}
	r.ControlPlane = cred.URL
	name, anonymous, err := whoami(ctx, client, *cred)
	if err != nil {
		// One unreachable control plane explains every missing server
		// fact - report it once and return the local half intact.
		r.ServerError = firstNonEmptyLine(err.Error())
		return r, nil
	}
	if anonymous {
		r.Principal = "(anonymous bearer token)"
	} else {
		r.Principal = name
	}

	if r.WorkspaceID != "" {
		var info WorkspaceInfo
		if err := apiJSON(ctx, client, http.MethodGet,
			strings.TrimSuffix(cred.URL, "/")+"/api/workspaces/"+url.PathEscape(r.WorkspaceID),
			cred.AuthHeader(), nil, &info); err == nil {
			r.WorkspaceStatus = info.Status
		}
	}

	// Deployment health, best-effort: any read failure (older daemon
	// without the route, no permission, mirror unconfigured) leaves the
	// field empty and says nothing, exactly like the other server
	// enrichments here.
	var mirror mirrorStatusView
	if err := apiJSON(ctx, client, http.MethodGet,
		strings.TrimSuffix(cred.URL, "/")+"/api/mirror/status",
		cred.AuthHeader(), nil, &mirror); err == nil && mirror.Configured {
		for _, c := range mirror.Cursors {
			if c.Frozen {
				r.MirrorFrozenRefs = append(r.MirrorFrozenRefs, c.Ref)
			}
		}
	}

	for i := range r.Stack {
		e := &r.Stack[i]
		if e.ChangeID == "" {
			continue
		}
		var info ChangeInfo
		if err := apiJSON(ctx, client, http.MethodGet,
			strings.TrimSuffix(cred.URL, "/")+"/api/changes/"+url.PathEscape(e.ChangeID),
			cred.AuthHeader(), nil, &info); err != nil {
			var ce *clierr.Error
			if errors.As(err, &ce) && ce.Code == "not_found" {
				e.Status = "not_pushed"
			}
			continue
		}
		if info.State != "open" {
			// landed/abandoned: the gates no longer mean anything - the
			// server's own state is the answer.
			e.Status = info.State
			continue
		}
		reqs, err := ChangeRequirements(ctx, client, *cred, e.ChangeID)
		if err != nil {
			continue
		}
		if reqs.Mergeable {
			e.Status = "ready"
		} else {
			e.Status = "blocked"
			e.Blockers = reqs.Blockers
		}
	}
	return r, nil
}

// statusStack lists the commits above base, bottom -> top. jj checkouts
// resolve the tip from jj's working copy (git HEAD is detached in
// colocated repos). An empty undescribed @ is already skipped by
// jjTipCommit; a non-empty undescribed @ (dirty WC with no message) is
// still the tip for push purposes but is dropped here - its content is
// already on the working-tree line, and listing it double-reports.
func statusStack(dir, base string, jj bool) []StackEntry {
	tip := "HEAD"
	wcSHA := ""
	if jj {
		t, err := jjTipCommit(dir)
		if err != nil {
			return nil
		}
		tip = t
		// Resolve @ so we can drop the ephemeral undescribed WC commit
		// even when jjTipCommit keeps it (non-empty tree, empty description).
		if at, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "commit_id"); err == nil {
			wcSHA = strings.TrimSpace(at)
		}
	}
	out, err := runGit(dir, "log", "--reverse", "--format=%H%x1f%s%x1f%B%x1e", base+".."+tip)
	if err != nil {
		return nil
	}
	stack := []StackEntry{}
	for _, rec := range strings.Split(out, "\x1e") {
		parts := strings.SplitN(rec, "\x1f", 3)
		if len(parts) < 3 {
			continue
		}
		// A commit without a Change-Id trailer is not a Change at all -
		// "not_a_change" says so up front, instead of the "unknown" that
		// reads like a lookup failure (dogfood feedback, 2026-07-23: jj's
		// undescribed working-copy commit rendered as "? unknown").
		e := StackEntry{
			SHA:    strings.TrimSpace(parts[0]),
			Title:  strings.TrimSpace(parts[1]),
			Status: "not_a_change",
		}
		if id, ok := receive.ParseChangeID(parts[2]); ok {
			e.ChangeID = id
			e.Status = "unknown"
		}
		stack = append(stack, e)
	}
	// jj's undescribed working-copy commit: no Change-Id and no description
	// means the dirty content (if any) is already covered by DirtyPaths; a
	// described @ is genuine WIP and stays in the stack.
	if jj && wcSHA != "" && len(stack) > 0 {
		top := stack[len(stack)-1]
		if top.SHA == wcSHA && top.ChangeID == "" && top.Title == "" {
			stack = stack[:len(stack)-1]
		}
	}
	return stack
}

// dirtyPaths counts what the next `change create` would actually sweep in.
// In a jj checkout that is the working-copy commit's own diff, NOT `git
// status`: git additionally reports paths outside jj's sparse set, which jj
// cannot see and which `change create` refuses rather than commits - so
// counting them would overstate the change by exactly the files that will
// not be in it.
//
// Fail-open-to-zero is forbidden here: a jj error used to return 0 and
// PrintStatus said "working tree: clean" on a dirty tree (the non-root -R
// bug surfaced exactly that way). Fall back to the git porcelain count -
// still a real number, offline-safe, never aborts the verb. Git may
// overstate under sparse; overstatement is honest uncertainty, "clean" is a
// lie. Only if git also fails do we return 0 (no local tool answered).
func dirtyPaths(dir string, jj bool) int {
	if jj {
		out, err := runJJ(dir, "diff", "-r", "@", "--summary")
		if err == nil {
			return countLines(out)
		}
		// Fall through to git rather than invent a confident zero.
	}
	out, err := runGit(dir, "status", "--porcelain", "-uall")
	if err != nil {
		return 0
	}
	return countLines(out)
}

func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// PrintStatus renders the report in doctor's aligned-label style: the
// checkout's standing, then the line above trunk as a jj-style graph.
func PrintStatus(w io.Writer, r StatusReport) {
	fmt.Fprintln(w, "runko status")
	if r.WorkspaceID != "" {
		line := fmt.Sprintf("%s @ %s", r.WorkspaceID, r.Branch)
		if r.WorkspaceStatus != "" {
			line += " (" + r.WorkspaceStatus + ")"
		}
		fmt.Fprintf(w, "  workspace:    %s\n", line)
	} else {
		fmt.Fprintln(w, "  workspace:    none - this checkout is not workspace-bound (`runko workspace create` starts one)")
	}
	checkout := r.Dir
	if r.IsJJWorkspace {
		checkout += " (jj colocated)"
	}
	fmt.Fprintf(w, "  checkout:     %s\n", checkout)
	switch {
	case r.Principal != "":
		fmt.Fprintf(w, "  signed in:    %s @ %s\n", r.Principal, r.ControlPlane)
	case r.ServerError != "":
		fmt.Fprintf(w, "  signed in:    unknown - %s (server state omitted)\n", r.ServerError)
	}
	// Only worth a line when it differs from the ambient login: a checkout
	// that authors as the person already signed in is the unremarkable case.
	if r.AuthorsAs != "" && r.AuthorsAs != r.Principal {
		fmt.Fprintf(w, "  authors as:   %s - this checkout's own credential, used for its pushes\n", r.AuthorsAs)
	}
	if r.StaleBase {
		fmt.Fprintf(w, "  trunk:        %s/%s has new commits this line is missing - `runko workspace sync` (or let `change push` auto-sync)\n", r.Remote, r.TrunkRef)
	} else {
		fmt.Fprintf(w, "  trunk:        %s/%s - base is current\n", r.Remote, r.TrunkRef)
	}
	// Deployment-wide, and deliberately loud: a frozen mirror does not
	// block anything the person in front of this terminal is doing, which
	// is exactly why it can rot for hours unnoticed. Silent when healthy.
	if len(r.MirrorFrozenRefs) > 0 {
		fmt.Fprintf(w, "  MIRROR:       FROZEN on %s - landed changes are NOT reaching the mirror,\n", strings.Join(r.MirrorFrozenRefs, ", "))
		fmt.Fprintln(w, "                so post-land CI and deploys are stalled deployment-wide.")
		fmt.Fprintln(w, "                An operator unfreezes after reviewing the divergence.")
	}
	if r.DirtyPaths > 0 {
		fmt.Fprintf(w, "  working tree: %d uncommitted path(s) - `runko change create` commits ALL of them\n", r.DirtyPaths)
	} else {
		fmt.Fprintln(w, "  working tree: clean")
	}

	switch {
	case r.Stack == nil:
		fmt.Fprintf(w, "  stack:        unknown - no local %s/%s ref to compare against (fetch first)\n", r.Remote, r.TrunkRef)
	case len(r.Stack) == 0:
		// Empty (not nil): line is fully landed / nothing to push. Dirty
		// paths ride the working-tree line only - do not open an empty graph.
		fmt.Fprintln(w, "  stack:        nothing in flight - HEAD is on trunk")
	default:
		fmt.Fprintln(w)
		printStatusGraph(w, r)
	}
}

// printStatusGraph draws the line above trunk the way jj log draws it:
// newest on top, @ where the working copy sits (the uncommitted working
// tree when there is one, else the tip change), ○ each change under it,
// ◆ the immutable trunk base. A node's blockers ride its │ gutter, so
// they read as part of the node.
func printStatusGraph(w io.Writer, r StatusReport) {
	mark := "@"
	if r.DirtyPaths > 0 {
		fmt.Fprintf(w, "@  %d uncommitted path(s) - the next `runko change create` sweeps ALL of them\n", r.DirtyPaths)
		mark = "○"
	}
	for i := len(r.Stack) - 1; i >= 0; i-- {
		e := r.Stack[i]
		id := e.ChangeID
		if id == "" {
			// No Change-Id means no stable identity to print - the commit
			// SHA is the only true name it has.
			id = short(e.SHA)
		}
		title := e.Title
		if title == "" {
			title = "(no description set)"
		}
		fmt.Fprintf(w, "%s  %s  %s  (%s)\n", mark, id, title, statusMark(e.Status))
		for _, b := range e.Blockers {
			fmt.Fprintf(w, "│      -> %s\n", b)
		}
		mark = "○"
	}
	fmt.Fprintf(w, "◆  %.12s %s/%s  %s\n", r.TrunkSHA, r.Remote, r.TrunkRef, r.TrunkTitle)
}

func statusMark(status string) string {
	switch status {
	case "ready":
		return "✓ ready"
	case "blocked":
		return "✕ blocked"
	case "landed", "abandoned":
		return "· " + status
	case "not_pushed":
		return "○ not pushed"
	case "not_a_change":
		return "not a change yet - `runko change push` stamps its Change-Id"
	default:
		return "? unknown"
	}
}

func newStatusCmd(a *app) *cobra.Command {
	var (
		dir, remote, trunk string
		jsonOut            bool
	)
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Where this checkout stands: workspace, identity, stack, gates",
		GroupID: "loop",
		Long: `One look at this checkout's standing: the workspace binding and its
server-side state, who the stored credential signs you in as, whether
the base has gone stale under trunk, what the next change would sweep
in, and the local stack bottom -> top with each change's merge gates.

Local facts always answer; without a reachable control plane the
server-side fields read unknown and the reason is named.`,
		Example: `  runko status
  runko status -w my-workstream   # any workspace, from anywhere
  runko status --json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			var credp *Credential
			var credErr string
			if cred, err := a.credential(); err == nil {
				credp = &cred
			} else {
				credErr = firstNonEmptyLine(err.Error())
			}
			report, err := RunStatus(cmd.Context(), http.DefaultClient, credp, credErr, wd, remote, trunk)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(report)
			}
			PrintStatus(os.Stdout, report)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&dir, "dir", ".", "repository directory")
	addWorkspaceFlag(cmd)
	fl.StringVar(&remote, "remote", "origin", "git remote the trunk lives on")
	fl.StringVar(&trunk, "trunk", "main", "trunk ref name")
	fl.BoolVar(&jsonOut, "json", false, "emit the status report as JSON instead of the human summary")
	return cmd
}
