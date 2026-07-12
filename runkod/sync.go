// Server-side stack sync (§13.5's rebase machinery behind the Change
// page's Sync button): when trunk moves underneath a stack, the daemon
// rebases every member onto the new tip itself - no workspace checkout
// required, the remote-client equivalent of `runko workspace sync` +
// re-push. Compute-first, write-later: all members are rebased in memory
// (merge-tree creates objects, never moves refs), and refs/Store rows move
// only when the WHOLE stack rebases cleanly - one conflicted member
// reports its paths and leaves everything untouched, because a partially
// synced stack (child based on a parent head that no longer exists) is
// exactly the broken state the stack view can't render.
package runkod

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/land"
)

// syncDecision is syncChangeCore's outcome: exactly one of Synced,
// AlreadyInSync, or ConflictChange != "" holds (changes.proto
// SyncChangeResponse renders these as response fields, like land).
type syncDecision struct {
	Synced         bool
	AlreadyInSync  bool
	ConflictChange string
	ConflictPaths  []string
	Stack          []Change
}

// syncChangeCore rebases the whole stack containing key onto the current
// trunk tip. A rebased head is a new version of its Change, exactly as if
// the author had re-pushed: base/head move together, approvals bound to
// the old head go stale on their own (Approval.HeadSHA), and the
// change.updated webhook re-triggers required checks against the new head.
// Authorship and message (including the Change-Id trailer) are preserved
// from the old head; the committer is the machine, like a rebase-land.
func (s *Server) syncChangeCore(ctx context.Context, key string, change Change, principal *Principal) (syncDecision, *apiError) {
	if apiErr := requireOpenChange(key, change, "sync"); apiErr != nil {
		return syncDecision{}, apiErr
	}

	g := gitstore.New(s.RepoDir)
	trunkTip, err := g.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		// Unborn trunk: there is nothing to rebase onto - the stack is as
		// synced as it can be.
		return syncDecision{AlreadyInSync: true, Stack: []Change{change}}, nil
	}

	open, err := s.Store.ListChanges(ctx, "open")
	if err != nil {
		return syncDecision{}, internalErr(err)
	}
	chain, _ := stackForChange(open, change)

	// Compute phase: rebase each member bottom-up (stackForChange's
	// pre-order walk puts every parent before its children). A member's new
	// base is its parent's freshly minted head when the parent is in the
	// stack, and the trunk tip when its base is old trunk history - which
	// also covers the child of a REBASE-landed parent (its recorded base is
	// the parent's pre-land head, a commit trunk never contained; the 3-way
	// merge onto the tip that holds the parent's landed content is the
	// §13.5 recovery this endpoint exists for).
	type memberWrite struct {
		change  Change
		newBase string
		newHead string
	}
	newHeadByOldHead := make(map[string]string, len(chain))
	var writes []memberWrite
	for _, m := range chain {
		if m.State != "open" {
			continue // stackForChange keeps the requested change regardless; guarded above
		}
		if m.BaseSHA == "" {
			// A bootstrap-era Change (born on an unborn trunk) that never
			// re-pushed after trunk was born: there is no merge base to
			// rebase from.
			return syncDecision{}, typedErr(http.StatusConflict, clierr.Error{
				Code: "sync_unsupported", Field: "change",
				Message:    fmt.Sprintf("change %s has no recorded base to rebase from", m.ChangeKey),
				Suggestion: "rebase in your workspace and re-push: runko workspace sync && runko change push",
				DocURL:     "docs/design.md#135-landing",
			})
		}

		newBase := string(trunkTip)
		if nh, ok := newHeadByOldHead[m.BaseSHA]; ok {
			newBase = nh
		}
		if m.BaseSHA == newBase {
			// Already exactly where sync would put it; children chain off
			// the unchanged head.
			newHeadByOldHead[m.HeadSHA] = m.HeadSHA
			continue
		}

		rebased, err := land.Rebase(s.RepoDir, m.BaseSHA, newBase, m.HeadSHA)
		if err != nil {
			return syncDecision{}, internalErr(fmt.Errorf("sync %s: %w", m.ChangeKey, err))
		}
		if !rebased.Clean {
			// Nothing written yet - merge-tree and commit-tree only create
			// objects, and unreferenced objects are just GC fodder.
			return syncDecision{ConflictChange: m.ChangeKey, ConflictPaths: rebased.ConflictPaths}, nil
		}

		meta := core.CommitMeta{Message: m.Title + "\n\nChange-Id: " + m.ChangeKey + "\n"}
		if an, ae, msg, err := commitIdentity(s.RepoDir, m.HeadSHA); err == nil {
			if !strings.Contains(msg, "Change-Id: "+m.ChangeKey) {
				msg = strings.TrimRight(msg, "\n") + "\n\nChange-Id: " + m.ChangeKey + "\n"
			}
			meta = core.CommitMeta{AuthorName: an, AuthorEmail: ae, Message: msg}
		} else {
			log.Printf("runkod: sync %s: reading head identity (using fallback): %v", m.ChangeKey, err)
		}
		newHead, err := land.CommitTree(s.RepoDir, rebased.NewTreeSHA, newBase, meta)
		if err != nil {
			return syncDecision{}, internalErr(fmt.Errorf("sync %s: %w", m.ChangeKey, err))
		}
		newHeadByOldHead[m.HeadSHA] = newHead
		writes = append(writes, memberWrite{change: m, newBase: newBase, newHead: newHead})
	}

	if len(writes) == 0 {
		return syncDecision{AlreadyInSync: true, Stack: chain}, nil
	}

	// Write phase: move each member's stable ref with a CAS against the
	// head this sync computed from - a concurrent push to any member turns
	// the whole sync into a retryable 409 rather than silently overwriting
	// the newer head. Members already written stay written: they are valid
	// rebases of what their authors pushed, the same partial-progress
	// stance the land race loop takes (§13.5).
	for _, w := range writes {
		expected := core.Revision(w.change.HeadSHA)
		changeRef := "refs/changes/" + w.change.ChangeKey + "/head"
		if err := g.UpdateRef(changeRef, core.Revision(w.newHead), &expected); err != nil {
			return syncDecision{}, typedErr(http.StatusConflict, clierr.Error{
				Code: "sync_race", Field: "change",
				Message:    fmt.Sprintf("change %s was updated while syncing", w.change.ChangeKey),
				Suggestion: "reload the change and sync again",
			})
		}
		// Title/author pass through unchanged - a sync moves commits, not
		// ownership (CreateOrUpdateChange's last-pusher rule is for real
		// pushes). Empty origin preserves the stored workspace provenance.
		updated, err := s.Store.CreateOrUpdateChange(ctx, w.change.ChangeKey, w.newBase, w.newHead,
			changeRef, w.change.Title, w.change.AuthoredBy, "", "")
		if err != nil {
			return syncDecision{}, internalErr(fmt.Errorf("sync %s: record change: %w", w.change.ChangeKey, err))
		}
		if s.Processor != nil {
			paths, err := gitDiffNamesOnly(s.RepoDir, w.newBase, w.newHead)
			if err != nil {
				log.Printf("runkod: sync %s: diff rebased head: %v", w.change.ChangeKey, err)
				paths = nil
			}
			s.Processor.computeAffectedAndEnqueue(ctx, updated, paths, nil)
		}
	}
	if s.Processor != nil {
		s.Processor.Mirror.Trigger() // refs/changes/* are mirrored (§18.6)
	}

	who := "anonymous"
	if principal != nil {
		who = principal.Name
	}
	log.Printf("runkod: stack of %s synced onto %s by %q (%d member(s) rebased)", key, trunkTip, who, len(writes))

	refreshed := make([]Change, 0, len(chain))
	for _, m := range chain {
		if c, ok, err := s.Store.GetChange(ctx, m.ChangeKey); err == nil && ok {
			refreshed = append(refreshed, c)
		}
	}
	return syncDecision{Synced: true, Stack: refreshed}, nil
}
