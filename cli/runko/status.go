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
	// control plane has no such change), "unknown" (server state
	// unavailable, or no Change-Id to ask about).
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
	// DirtyPaths counts uncommitted paths (`git status --porcelain
	// -uall`) - what the next `change create` would sweep in.
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
	// Stack is the local line above the remote trunk ref, bottom -> top.
	// Nil when no refs/remotes/<remote>/<trunk> exists locally to diff
	// against; empty when the line is fully landed.
	Stack []StackEntry
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
	if out, err := runGit(dir, "status", "--porcelain", "-uall"); err == nil {
		r.DirtyPaths = countLines(out)
	}
	r.StaleBase = staleBase(dir, remote, trunk)
	r.Stack = statusStack(dir, remote, trunk, r.IsJJWorkspace)

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

// statusStack lists the commits above the local remote-tracking trunk
// ref, bottom -> top. jj checkouts resolve the tip from jj's working copy
// (git HEAD is detached in colocated repos); an empty undescribed @ is
// already skipped by jjTipCommit. Nil when the trunk ref isn't resolvable
// locally - a diff against nothing would misreport the whole history as
// a stack.
func statusStack(dir, remote, trunk string, jj bool) []StackEntry {
	tip := "HEAD"
	if jj {
		t, err := jjTipCommit(dir)
		if err != nil {
			return nil
		}
		tip = t
	}
	base, err := runGit(dir, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+trunk)
	if err != nil {
		return nil
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
		e := StackEntry{
			SHA:    strings.TrimSpace(parts[0]),
			Title:  strings.TrimSpace(parts[1]),
			Status: "unknown",
		}
		if id, ok := receive.ParseChangeID(parts[2]); ok {
			e.ChangeID = id
		}
		stack = append(stack, e)
	}
	return stack
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
// checkout's standing, then the stack with one mark per change.
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
	if r.StaleBase {
		fmt.Fprintf(w, "  trunk:        %s/%s has new commits this line is missing - `runko workspace sync` (or let `change push` auto-sync)\n", r.Remote, r.TrunkRef)
	} else {
		fmt.Fprintf(w, "  trunk:        %s/%s - base is current\n", r.Remote, r.TrunkRef)
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
		fmt.Fprintln(w, "  stack:        empty - nothing above trunk")
	default:
		fmt.Fprintf(w, "\nstack (bottom -> top, %d change(s)):\n", len(r.Stack))
		for _, e := range r.Stack {
			id := e.ChangeID
			if id == "" {
				id = "(no Change-Id yet - `runko change push` stamps one)"
			}
			fmt.Fprintf(w, "  %-12s %s  %s\n", statusMark(e.Status), id, e.Title)
			for _, b := range e.Blockers {
				fmt.Fprintf(w, "      -> %s\n", b)
			}
		}
	}
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
