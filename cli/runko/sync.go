// SyncToTrunk - the CitC "sync to head" verb (§12.3), and the engine
// behind `runko workspace sync`, `change push`'s auto-sync, and `change
// land`'s revalidation loop. jj-colocated repos rebase via jj so
// descendants follow (never a raw rebase behind jj's back); plain-git
// worktrees rebase with the §6.6 conflict UX (abort + name the files).
package main

import (
	"fmt"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// SyncToTrunk fetches the remote trunk and rebases the local line onto
// its tip. Returns the trunk tip synced onto. A no-op sync (already
// based on the tip) is fine and returns the tip.
func SyncToTrunk(dir, remote, trunk string) (string, error) {
	return syncToTrunk(dir, remote, trunk, false)
}

// syncToTrunk with restoreOnConflict=true guarantees a conflicting sync
// leaves the checkout exactly as it found it, so the caller can carry on
// with the stale base (`change push`'s push-anyway path): the plain-git
// rebase is always aborted (§6.6), and the jj path - which otherwise
// keeps conflicts recorded in-tree for jj-native resolution - rolls back
// to the pre-sync operation. A sync_conflict error is only returned once
// that rollback has succeeded; a failed rollback is a plain error so no
// caller mistakes the tree for clean.
func syncToTrunk(dir, remote, trunk string, restoreOnConflict bool) (string, error) {
	if _, err := runGitNet(dir, "fetch", remote, trunk); err != nil {
		return "", fmt.Errorf("sync: fetch %s %s: %w", remote, trunk, err)
	}
	tip, err := runGit(dir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}

	if isJJWorkspace(dir) {
		var preSyncOp string
		if restoreOnConflict {
			op, err := runJJ(dir, "op", "log", "-n", "1", "--no-graph", "-T", "id")
			if err != nil {
				return "", fmt.Errorf("sync: record the pre-sync jj operation: %w", err)
			}
			preSyncOp = strings.TrimSpace(op)
		}
		// git did the transport above (jj's own fetch fails silently on
		// URL-embedded basic auth); any jj command auto-imports the refs.
		// jj rebase moves the whole line containing the working copy and
		// keeps descendants attached - the evolve semantics (§21).
		if _, err := runJJ(dir, "rebase", "-d", tip); err != nil {
			return "", fmt.Errorf("sync: jj rebase onto %s: %w", short(tip), err)
		}
		// jj records conflicts in-tree instead of stopping the rebase;
		// surface them structurally rather than letting a later push ship
		// conflict markers.
		out, _ := runJJ(dir, "log", "--no-graph", "-r", "conflicts() & mutable()", "-T", `change_id.short() ++ " "`)
		if ids := strings.TrimSpace(out); ids != "" {
			suggestion := "resolve in the working copy (`jj status` names the paths), then sync again"
			if restoreOnConflict {
				if _, err := runJJ(dir, "op", "restore", preSyncOp); err != nil {
					return "", fmt.Errorf("sync: rebase onto %s left conflicts in %s and rolling back to the pre-sync operation failed: %w", short(tip), ids, err)
				}
				suggestion = "rerun `jj rebase -d " + short(tip) + "`, resolve the conflicts it records, then push again"
			}
			return "", &clierr.Error{
				Code: "sync_conflict", Field: "workspace",
				Message:    fmt.Sprintf("syncing onto trunk tip %s left conflicts in: %s", short(tip), ids),
				Suggestion: suggestion,
			}
		}
		return tip, nil
	}

	// Plain git: already based on the tip is a no-op.
	if _, err := runGit(dir, "merge-base", "--is-ancestor", tip, "HEAD"); err == nil {
		return tip, nil
	}
	// Rebase re-commits, so it needs a committer even on an unconfigured
	// machine (the WorkspaceSnapshot identity fallback - one placeholder
	// shared with `change create`; the daemon re-stamps the canonical
	// landing identity at land time anyway, §7.5).
	rebaseArgs := []string{"rebase", tip}
	if email, _ := runGit(dir, "config", "user.email"); email == "" {
		rebaseArgs = append([]string{"-c", "user.name=Runko", "-c", "user.email=runko@localhost"}, rebaseArgs...)
	}
	if _, rebaseErr := runGit(dir, rebaseArgs...); rebaseErr != nil {
		conflicts, _ := runGit(dir, "diff", "--name-only", "--diff-filter=U")
		_, abortErr := runGit(dir, "rebase", "--abort")
		if conflicts == "" {
			// Not a content conflict - surface the real failure, never a
			// misleading "conflicts in:" with an empty list.
			return "", fmt.Errorf("sync: rebase onto %s: %w", short(tip), rebaseErr)
		}
		suggestion := "resolve by hand: git rebase " + short(tip) + ", fix conflicts, then sync again"
		if restoreOnConflict {
			if abortErr != nil {
				return "", fmt.Errorf("sync: rebase onto %s conflicted and the abort failed: %w", short(tip), abortErr)
			}
			suggestion = "resolve by hand: git rebase " + short(tip) + ", fix conflicts, then push again"
		}
		return "", &clierr.Error{
			Code: "sync_conflict", Field: "workspace",
			Message:    fmt.Sprintf("syncing onto trunk tip %s conflicts in: %s", short(tip), strings.ReplaceAll(conflicts, "\n", ", ")),
			Suggestion: suggestion,
		}
	}
	return tip, nil
}

// staleBase reports whether HEAD (or the jj tip) is missing the remote
// trunk tip from its ancestry - i.e. a sync would change something. An
// unreachable remote answers false: the caller's push will surface the
// real transport error.
func staleBase(dir, remote, trunk string) bool {
	tip, err := lsRemoteTrunk(dir, remote, trunk)
	if err != nil || tip == "" {
		return false
	}
	head := "HEAD"
	if isJJWorkspace(dir) {
		if t, err := jjTipCommit(dir); err == nil {
			head = t
		}
	}
	// The tip object may not exist locally yet - then the base is stale
	// by definition (we've never even fetched that trunk state).
	if _, err := runGit(dir, "cat-file", "-e", tip); err != nil {
		return true
	}
	_, err = runGit(dir, "merge-base", "--is-ancestor", tip, head)
	return err != nil
}
