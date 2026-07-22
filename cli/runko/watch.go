// runko workspace watch (§12.6, DAG stage 18): the auto-snapshot loop
// that feeds the live workspace view. The load-bearing constraint is that
// a BACKGROUND snapshotter must never mutate the working checkout - the
// interactive snapshot verb (workspace.go) commits on the checked-out
// branch, which is exactly right for a human typing the command and
// exactly wrong beside a concurrently-running agent (index-lock races,
// `change create` finding a clean tree, commits behind jj's back in a
// colocated repo). Watch therefore builds its commit OUT-OF-BAND: a
// throwaway index file -> write-tree -> commit-tree parented on HEAD ->
// push the sha straight to refs/workspaces/<id>/<branch>. HEAD, the real
// index, and the worktree are untouched, and the ref keeps its
// amend-at-the-tip semantics (§12.2) because every push replaces the tip.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
)

// WorkspaceWatchSnapshot builds one out-of-band snapshot of dir's working
// tree and pushes it to the workspace's snapshot ref. skipTree short-
// circuits the loop's steady state: when the freshly-built tree matches
// it, nothing is pushed and sha comes back "". A clean tree snapshots as
// HEAD itself (the base is the snapshot, the workspace.go convention).
func WorkspaceWatchSnapshot(dir, message, skipTree string) (ref, sha, tree string, err error) {
	id, _ := runGit(dir, "config", "runko.workspace")
	if id == "" {
		return "", "", "", &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message:    fmt.Sprintf("%s is not bound to a runko workspace", dir),
			Suggestion: "name the workspace instead of standing in it: `runko workspace watch -w <name>` (§12.7); or run inside a `runko workspace create/attach` checkout (--jj for a jj colocated clone)",
		}
	}
	branch, _ := runGit(dir, "config", "runko.branch")
	if branch == "" {
		branch = "head"
	}
	ref = "refs/workspaces/" + id + "/" + branch

	head, err := runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", &clierr.Error{
			Code: "no_commits", Field: "repo",
			Message:    "HEAD has no commits - there is nothing to snapshot over",
			Suggestion: "make the first commit (or `runko workspace attach` a workspace with a base revision)",
		}
	}

	tree, err = workingTreeHash(dir)
	if err != nil {
		return "", "", "", err
	}
	if tree == skipTree {
		return ref, "", tree, nil // steady state: this exact tree already pushed
	}

	sha = head
	if headTree, _ := runGit(dir, "rev-parse", "HEAD^{tree}"); tree != headTree {
		// Same no-identity fallback as the interactive verb: snapshotting
		// must work on a fresh VM/agent container with no git identity.
		args := []string{}
		if email, _ := runGit(dir, "config", "user.email"); email == "" {
			args = append(args, "-c", "user.name=Runko", "-c", "user.email=runko@localhost")
		}
		args = append(args, "commit-tree", tree, "-p", head, "-m", fmt.Sprintf("%s: %s", snapshotSubjectPrefix, message))
		sha, err = runGit(dir, args...)
		if err != nil {
			return "", "", "", fmt.Errorf("commit snapshot tree: %w", err)
		}
	}
	if _, err := runGitNet(dir, "push", "origin", "+"+sha+":"+ref); err != nil {
		return "", "", "", fmt.Errorf("push snapshot: %w", err)
	}
	return ref, sha, tree, nil
}

// workingTreeHash stages the working tree into a throwaway index and
// returns its tree hash. The §12.6 out-of-band builder: GIT_INDEX_FILE
// redirects read-tree/add/write-tree away from .git/index, so HEAD, the
// real index, and the worktree are never touched - safe beside a working
// agent or jj. Shared with `workspace gc`'s durability check (§12.7),
// where "tree equals the snapshot ref's tree" is what makes a dirty but
// watch-parked worktree provably disposable.
func workingTreeHash(dir string) (string, error) {
	tmp, err := os.CreateTemp("", "runko-watch-index-")
	if err != nil {
		return "", fmt.Errorf("create temp index: %w", err)
	}
	tmpIndex := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpIndex)
	env := []string{"GIT_INDEX_FILE=" + tmpIndex}
	if _, err := runGitEnv(dir, env, "read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("seed temp index: %w", err)
	}
	if _, err := runGitEnv(dir, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage into temp index: %w", err)
	}
	tree, err := runGitEnv(dir, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write snapshot tree: %w", err)
	}
	return tree, nil
}

// WatchOptions configures WorkspaceWatch.
type WatchOptions struct {
	Dir      string
	Interval time.Duration // cadence of the check-and-push tick
	Once     bool          // one tick, then return (tests, CI, cron)
	JSON     bool          // NDJSON: one {"ref","sha"} line per push
	Out      io.Writer
}

// WorkspaceWatch ticks until interrupted: build the tree, push when it
// moved, sleep. Policy rejections (secret scan, size caps) warn once per
// failure state and keep watching - the cap refusing a bloated snapshot
// is the system working, not a reason to die; terminal states (not a
// workspace, closed workspace) exit with the error.
func WorkspaceWatch(opts WatchOptions) error {
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Second
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	lastTree := ""
	lastFailure := ""
	for {
		ref, sha, tree, err := WorkspaceWatchSnapshot(opts.Dir, "watch "+time.Now().UTC().Format(time.RFC3339), lastTree)
		switch {
		case err == nil:
			lastFailure = ""
			lastTree = tree
			if sha != "" {
				if opts.JSON {
					json.NewEncoder(opts.Out).Encode(map[string]string{"ref": ref, "sha": sha})
				} else {
					fmt.Fprintf(opts.Out, "snapshot %.12s -> %s\n", sha, ref)
				}
			}
		case opts.Once || isTerminalWatchError(err):
			return err
		default:
			// Log-once-per-state: a WIP stuck over the snapshot cap would
			// otherwise repeat the identical warning every tick.
			if err.Error() != lastFailure {
				fmt.Fprintf(warnWriter, "warning: snapshot failed (will keep watching): %v\n", err)
				lastFailure = err.Error()
			}
		}
		if opts.Once {
			return nil
		}
		time.Sleep(opts.Interval)
	}
}

// isTerminalWatchError classifies failures the loop cannot retry through:
// client-side misbinding (structured clierr) and the server's "this
// workspace will never accept your push again" rejections (§12.2's
// closed/unregistered states, matched on the funnel's own message text).
func isTerminalWatchError(err error) bool {
	if _, ok := err.(*clierr.Error); ok {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "is closed") || strings.Contains(msg, "is registered")
}
