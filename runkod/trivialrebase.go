// §13.5 trivial-rebase carry-forward (decided 2026-07-15; Gerrit's
// copyCondition changekind:TRIVIAL_REBASE). A head move that provably
// carries the identical base..head diff - same commit message, and the old
// delta replays cleanly onto the new base yielding exactly the new tree -
// copies owner approvals (every revalidation tier) and passing check runs
// (conflict-only tier) to the new head instead of resetting them. Detection
// lives on the Processor because the push path runs inside git's receive
// quarantine: the pushed objects are only readable through p.runGit's
// GIT_OBJECT_DIRECTORY env plumbing, never a plain exec.
package runkod

import (
	"context"
	"log"
	"strings"

	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/land"
)

// trivialRebaseOf reports whether newHead is a trivial rebase of the open
// change old: message byte-identical, and replaying old.BaseSHA..old.HeadSHA
// onto newBase with the land engine's own merge (git merge-tree) yields
// exactly newHead's tree - Gerrit's rebase-replay definition, which also
// covers an identical re-commit (NO_CHANGE) and is robust where textual
// diff comparison false-negatives on shifted context lines. Anything the
// oracle can't prove - conflicts, read errors, a same-head re-push (the
// documented CI re-trigger), a bootstrap base - reads as NOT trivial: the
// failure mode is a spurious CI run, never a carried stale result.
func (p *Processor) trivialRebaseOf(old Change, newBase, newHead string, extraEnv []string) bool {
	if old.State != "open" || old.BaseSHA == "" || old.HeadSHA == "" ||
		old.HeadSHA == newHead || newBase == "" {
		return false
	}
	oldMsg, err := p.runGit(extraEnv, "log", "-1", "--format=%B", old.HeadSHA)
	if err != nil {
		return false
	}
	newMsg, err := p.runGit(extraEnv, "log", "-1", "--format=%B", newHead)
	if err != nil || oldMsg != newMsg {
		return false
	}
	// Replay: a conflicting merge exits non-zero, which runGit surfaces as
	// an error - the right answer anyway.
	replayed, err := p.runGit(extraEnv, "merge-tree", "--write-tree", "--merge-base="+old.BaseSHA, newBase, old.HeadSHA)
	if err != nil {
		return false
	}
	replayedTree := strings.TrimSpace(replayed)
	if i := strings.IndexByte(replayedTree, '\n'); i >= 0 {
		replayedTree = replayedTree[:i]
	}
	newTree, err := p.runGit(extraEnv, "rev-parse", newHead+"^{tree}")
	if err != nil {
		return false
	}
	return replayedTree == strings.TrimSpace(newTree)
}

// effectiveRevalidation is the Processor half of the §13.5 tier resolution
// (org setting > daemon flag > conflict-only), the tags.go Directory
// pattern - funnel and land gate must never disagree about the policy.
func (p *Processor) effectiveRevalidation(ctx context.Context) land.RevalidationScope {
	var dir Directory
	switch {
	case p.Directory != nil:
		dir = p.Directory
	case p.Store != nil:
		if d, ok := p.Store.(Directory); ok {
			dir = d
		}
	}
	return resolveRevalidation(ctx, dir, p.OrgName, p.Revalidation)
}

// carryForwardTrivialRebase copies old's results to the updated head:
// approvals under every tier (the amend-reset rule exists to stop
// approve-v1-amend-v2 bypass, and a trivial rebase provably carries the
// identical approved diff), passing check runs only under conflict-only
// (under stricter tiers a carried check would launder an intersecting
// trunk delta past exactly the re-run the org opted into). Returns whether
// the new head is FULLY COVERED - every required check green after the
// copy - in which case the caller suppresses the change.updated webhook
// and CI never re-runs. Every error path reads as not-covered: fail closed
// to a CI run, never open to a silently ungated head.
func (p *Processor) carryForwardTrivialRebase(ctx context.Context, old, updated Change, result affected.Result, indexed []index.IndexedProject) (covered bool) {
	for _, a := range mustListApprovals(ctx, p.Store, updated.ChangeKey) {
		if a.HeadSHA != old.HeadSHA {
			continue
		}
		if err := p.Store.RecordApproval(ctx, updated.ChangeKey, a.OwnerRef, a.ApprovedBy, updated.HeadSHA); err != nil {
			log.Printf("runkod: %s: carry approval %s: %v", updated.ChangeKey, a.OwnerRef, err)
		} else {
			log.Printf("runkod: %s: carried approval %s by %s from %.12s to %.12s (trivial rebase)", updated.ChangeKey, a.OwnerRef, a.ApprovedBy, old.HeadSHA, updated.HeadSHA)
		}
	}

	if p.effectiveRevalidation(ctx) != land.RevalidationConflictOnly {
		return false
	}
	copied, err := p.Store.CopyPassingCheckRuns(ctx, updated.ChangeKey, old.HeadSHA, updated.HeadSHA)
	if err != nil {
		log.Printf("runkod: %s: carry check runs: %v", updated.ChangeKey, err)
		return false
	}
	if len(copied) > 0 {
		log.Printf("runkod: %s: carried %d passing check(s) from %.12s to %.12s (trivial rebase)", updated.ChangeKey, len(copied), old.HeadSHA, updated.HeadSHA)
	}

	required := requiredCheckNames(result, indexed)
	required = append(required, p.GlobalRequiredChecks...)
	required = append(required, p.orgGlobalRequiredChecks(ctx)...)
	if len(required) == 0 {
		// Nothing gates this change - a re-run wouldn't either, so the
		// carried head counts as covered.
		return true
	}
	runs, err := p.Store.ListCheckRuns(ctx, updated.ChangeKey, updated.HeadSHA)
	if err != nil {
		return false
	}
	green := make(map[string]bool, len(runs))
	for _, r := range runs {
		green[r.Name] = r.Status == checks.CheckStatusCompleted && r.Conclusion == checks.ConclusionSuccess
	}
	for _, name := range required {
		if !green[name] {
			return false
		}
	}
	return true
}

// orgGlobalRequiredChecks mirrors Server.effectiveGlobalChecks for the
// funnel side: org-stored names join the flag-level ones so coverage never
// under-counts what the merge gate will demand.
func (p *Processor) orgGlobalRequiredChecks(ctx context.Context) []string {
	var dir Directory
	switch {
	case p.Directory != nil:
		dir = p.Directory
	case p.Store != nil:
		if d, ok := p.Store.(Directory); ok {
			dir = d
		}
	}
	if dir == nil || p.OrgName == "" {
		return nil
	}
	settings, err := dir.GetOrgSettings(ctx, p.OrgName)
	if err != nil {
		return nil
	}
	return settings.GlobalRequiredChecks
}

func mustListApprovals(ctx context.Context, store Store, key string) []Approval {
	approvals, err := store.ListApprovals(ctx, key)
	if err != nil {
		log.Printf("runkod: %s: list approvals for carry-forward: %v", key, err)
		return nil
	}
	return approvals
}
