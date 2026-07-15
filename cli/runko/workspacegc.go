// runko workspace gc (§12.7): reclamation and reuse of local workspace
// materializations. The predicate is the §12.2 durability contract cashed
// in - snapshot refs survive laptop loss, so a synced materialization is
// disposable BY CONSTRUCTION. Everything doubtful is a fail-closed skip
// with the reason printed; gc never deletes work it cannot prove is on
// the server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// errRecycleUnavailable marks a rebind that could not START (the worktree
// move was refused - locked, target exists, cross-device): the caller
// falls back to a fresh worktree and the candidate stays on disk. Errors
// AFTER the move are real failures and propagate.
var errRecycleUnavailable = errors.New("recycle unavailable")

// GCOptions configures one gc sweep.
type GCOptions struct {
	Apply bool          // execute the plan (default: plan only)
	Idle  time.Duration // >0: also sweep OPEN workspaces idle this long (clean+synced only)
	Scan  []string      // stores whose worktrees are adopted into the registry first
}

// GCCandidate is one row of the plan: a materialization and gc's verdict.
type GCCandidate struct {
	Materialization
	Reclaim   bool   // true: --apply removes it
	Reason    string // why it is reclaimable, or why it was skipped
	SizeBytes int64  // disk the reclaim frees (plan display; 0 for skips)
}

// autoGCScanCap bounds create/attach-time auto-gc: the sweep costs a few
// network round-trips per row (snapshot-ref probe), and materializing a
// workspace must never hang on lifecycle bookkeeping.
const autoGCScanCap = 8

// WorkspaceGC plans (and with opts.Apply executes) a reclamation sweep
// over this machine's materialization registry.
func WorkspaceGC(ctx context.Context, client *http.Client, runkodURL, token string, opts GCOptions) ([]GCCandidate, error) {
	for _, store := range opts.Scan {
		if err := adoptFromStore(store, runkodURL); err != nil {
			return nil, fmt.Errorf("scan %s: %w", store, err)
		}
	}
	rows, err := loadMaterializations()
	if err != nil {
		return nil, err
	}
	server, err := serverWorkspaces(ctx, client, runkodURL, token)
	if err != nil {
		return nil, err
	}
	var plan []GCCandidate
	for _, m := range rows {
		plan = append(plan, evaluateMaterialization(m, server, opts.Idle))
	}
	if opts.Apply {
		for i := range plan {
			if !plan[i].Reclaim {
				continue
			}
			if err := reclaimMaterialization(plan[i].Materialization); err != nil {
				plan[i].Reclaim = false
				plan[i].Reason = "reclaim failed: " + err.Error()
			}
		}
	}
	return plan, nil
}

func serverWorkspaces(ctx context.Context, client *http.Client, runkodURL, token string) (map[string]WorkspaceInfo, error) {
	list, err := WorkspaceList(ctx, client, runkodURL, token)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]WorkspaceInfo, len(list))
	for _, ws := range list {
		byID[ws.ID] = ws
	}
	return byID, nil
}

// adoptFromStore walks a store's worktrees and registers any that carry
// workspace bindings - the §12.7 adoption path for pre-registry layouts
// (in-tree worktrees, per-task clones), and the proof the registry is a
// rebuildable cache: truth is the worktrees' own runko.* config.
// Adoption NEUTRALIZES the store first (found live on the finding #49
// cleanup): a pre-§12.7 store embeds its creating agent's - long expired -
// token in the origin URL, and the legacy no-double-auth rule then blocks
// header injection, so the dead credential yields the anonymous
// advertisement, refs/workspaces stays hidden, and every synced worktree
// misdiagnoses as "no snapshot ref on the server". A store entering
// lifecycle management gets the same retrofit create/attach would give it.
func adoptFromStore(store, runkodURL string) error {
	if err := neutralizeStoreRemote(store); err != nil {
		return err
	}
	out, err := runGit(store, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	storeAbs, err := filepath.Abs(store)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok || path == storeAbs {
			continue // the store's own (checkout-less) entry, or metadata lines
		}
		id, _ := runGit(path, "config", "runko.workspace")
		if id == "" {
			continue // a hand-made worktree; not ours to manage
		}
		branch, _ := runGit(path, "config", "runko.branch")
		if branch == "" {
			branch = "head"
		}
		if err := recordMaterialization(Materialization{
			Workspace: id, Branch: branch, Path: path, Store: storeAbs, RunkodURL: runkodURL,
		}); err != nil {
			return err
		}
	}
	return nil
}

// evaluateMaterialization applies the §12.7 reclaim predicate: reclaimable
// iff the server says the workspace is CLOSED (or --idle covers an open
// one) AND the worktree's content is provably durable server-side -
// working tree == the snapshot ref's tree, HEAD under the snapshot ref
// (so watch-parked dirty worktrees, whose ref carries the tree while HEAD
// stays put, evaluate correctly). Anything else is a skip with a reason.
func evaluateMaterialization(m Materialization, server map[string]WorkspaceInfo, idle time.Duration) GCCandidate {
	c := GCCandidate{Materialization: m}
	if _, err := os.Stat(m.Path); err != nil {
		c.Reclaim, c.Reason = true, "directory already gone - dropping the stale registry row"
		return c
	}
	ws, known := server[m.Workspace]
	if !known {
		c.Reason = "not in the server registry (deleted?) - remove the directory by hand if you're sure"
		return c
	}
	switch {
	case ws.Status == "closed":
		c.Reason = "closed"
	case idle > 0 && time.Since(m.LastUsedAt) >= idle:
		c.Reason = fmt.Sprintf("idle %s (still open - re-attach recreates it)", time.Since(m.LastUsedAt).Round(time.Minute))
	default:
		c.Reason = "workspace is open (use --idle to sweep idle open workspaces)"
		return c
	}

	ref := "refs/workspaces/" + m.Workspace + "/" + m.Branch
	remoteSHA := ""
	if out, err := runGitNet(m.Path, "ls-remote", "origin", ref); err == nil {
		if fields := strings.Fields(out); len(fields) > 0 {
			remoteSHA = fields[0]
		}
	} else {
		c.Reason += "; server unreachable - kept (fail closed)"
		return c
	}

	if remoteSHA == "" {
		// No snapshot ref (never snapshotted, or retention already pruned
		// it): only a pristine never-touched worktree is provably durable.
		status, err := runGit(m.Path, "status", "--porcelain")
		head, _ := runGit(m.Path, "rev-parse", "HEAD")
		if err == nil && status == "" && head == ws.BaseRevision {
			c.Reclaim, c.Reason = true, c.Reason+"; never left its base revision"
			c.SizeBytes = dirSize(m.Path)
			return c
		}
		c.Reason += "; unpushed work (no snapshot ref on the server) - kept"
		return c
	}

	// Make the snapshot commit local, then prove both halves of durability.
	if _, err := runGitNet(m.Path, "fetch", "origin", ref); err != nil {
		c.Reason += "; snapshot ref fetch failed - kept (fail closed)"
		return c
	}
	wtTree, err := workingTreeHash(m.Path)
	if err != nil {
		c.Reason += "; " + err.Error() + " - kept (fail closed)"
		return c
	}
	remoteTree, err := runGit(m.Path, "rev-parse", remoteSHA+"^{tree}")
	if err != nil || wtTree != remoteTree {
		c.Reason += "; working tree differs from the last snapshot - kept"
		return c
	}
	if _, err := runGit(m.Path, "merge-base", "--is-ancestor", "HEAD", remoteSHA); err != nil {
		c.Reason += "; local commits not under the snapshot ref - kept"
		return c
	}
	c.Reclaim = true
	c.Reason += "; synced with " + ref
	c.SizeBytes = dirSize(m.Path)
	return c
}

// reclaimMaterialization removes the worktree directory, prunes the
// store's worktree metadata, and drops the registry row. The store itself
// is never deleted here (§12.7: object maintenance is git's job).
func reclaimMaterialization(m Materialization) error {
	if _, err := os.Stat(m.Path); err == nil {
		if err := os.RemoveAll(m.Path); err != nil {
			return err
		}
	}
	if _, err := os.Stat(m.Store); err == nil {
		if _, err := runGit(m.Store, "worktree", "prune"); err != nil {
			return err
		}
	}
	return dropMaterialization(m.Path)
}

// autoGCAndRecycle is the create/attach-time sweep (§12.7's "nobody has
// to remember a chore verb"): evaluate up to autoGCScanCap registry rows
// on THIS store, reclaim the unambiguous ones, and - when pickRecycle -
// hand one surviving candidate back for rebinding instead of deletion.
// Every failure is silent: lifecycle bookkeeping never blocks the verb.
func autoGCAndRecycle(ctx context.Context, client *http.Client, runkodURL, token, store string, pickRecycle bool) *Materialization {
	rows, err := loadMaterializations()
	if err != nil {
		return nil
	}
	storeAbs, err := filepath.Abs(store)
	if err != nil {
		return nil
	}
	var onStore []Materialization
	for _, m := range rows {
		if m.Store == storeAbs {
			onStore = append(onStore, m)
		}
	}
	if len(onStore) == 0 {
		return nil
	}
	if len(onStore) > autoGCScanCap {
		onStore = onStore[:autoGCScanCap]
	}
	server, err := serverWorkspaces(ctx, client, runkodURL, token)
	if err != nil {
		return nil
	}
	var recycle *Materialization
	reclaimed := 0
	for _, m := range onStore {
		c := evaluateMaterialization(m, server, 0)
		if !c.Reclaim {
			continue
		}
		if pickRecycle && recycle == nil {
			if _, err := os.Stat(m.Path); err == nil {
				keep := m
				recycle = &keep
				continue
			}
		}
		if err := reclaimMaterialization(m); err == nil {
			reclaimed++
		}
	}
	if reclaimed > 0 {
		fmt.Fprintf(os.Stderr, "reclaimed %d stale materialization(s) on %s (runko workspace gc to review the rest)\n", reclaimed, storeAbs)
	}
	return recycle
}

// rebindWorktree recycles a reclaimable worktree into a new workspace:
// move the directory, reset the branch to the new base, rewrite the
// sparse cone, restamp the runko.* binding, and `git clean -fd` WITHOUT
// -x - untracked residue from the dead task goes, ignored caches
// (node_modules, build dirs: the bytes that actually dominate a
// materialization) survive. Workspace N+1 costs a checkout delta, not
// clone + dependency install (§12.7).
func rebindWorktree(store string, old Materialization, dir string, info WorkspaceInfo, startPoint, wsBranch string, authEnv []string) error {
	if old.Path != dir {
		if _, err := runGit(store, "worktree", "move", old.Path, dir); err != nil {
			return fmt.Errorf("%w: %v", errRecycleUnavailable, err)
		}
	}
	if len(info.SparsePatterns) > 0 {
		args := append([]string{"sparse-checkout", "set", "--cone"}, info.SparsePatterns...)
		if _, err := runGitEnv(dir, authEnv, args...); err != nil {
			return fmt.Errorf("sparse-checkout set: %w", err)
		}
	}
	branch := "ws/" + info.ID + "/" + wsBranch
	if _, err := runGitEnv(dir, authEnv, "checkout", "-q", "-B", branch, startPoint); err != nil {
		return fmt.Errorf("checkout %s: %w", short(startPoint), err)
	}
	if oldBranch := "ws/" + old.Workspace + "/" + old.Branch; oldBranch != branch {
		_, _ = runGit(dir, "branch", "-D", oldBranch) // best-effort: the dead task's line
	}
	if _, err := runGit(dir, "clean", "-fdq"); err != nil {
		return fmt.Errorf("clean residue: %w", err)
	}
	for k, v := range map[string]string{
		"runko.workspace": info.ID,
		"runko.trunk":     info.TrunkRef,
		"runko.branch":    wsBranch,
	} {
		if _, err := runGit(dir, "config", "--worktree", k, v); err != nil {
			return err
		}
	}
	_ = dropMaterialization(old.Path)
	return nil
}

// dirSize sums a directory tree's file sizes (plan display only).
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// humanBytes renders a size for the gc plan (MiB granularity is plenty).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
