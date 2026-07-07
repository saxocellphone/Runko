package land

import (
	"fmt"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/core"
)

// zeroOID asserts "this ref must not currently exist" as UpdateRef's
// expected value (`git update-ref <ref> <new> <all-zeros>` - the same
// convention real git pre-receive hooks use for a brand-new ref). Landing
// the very first Change onto a monorepo that has never had a commit needs
// this: trunk is closed to direct push (§6.9), so the org's first commit
// can ONLY arrive via this same land path - Land must support that
// bootstrap case as a real compare-and-swap, not an unconditional
// force-write that could silently let two concurrent "first ever" lands
// clobber each other.
const zeroOID = "0000000000000000000000000000000000000000"

// Outcome is the result of one attempt to land a Change. Exactly one of
// Landed, RequiresRevalidation, len(Conflicts)>0, or RaceRetry describes what
// happened.
type Outcome struct {
	Landed    bool
	LandedSHA string

	// RequiresRevalidation is true when the trunk delta intersects the
	// Change's affected set (or RevalidationAlways is set): checks must be
	// re-run on the rebased head before landing can be retried.
	RequiresRevalidation bool

	// Conflicts is non-empty when the rebase itself could not be computed
	// cleanly - a real merge conflict, not a revalidation question.
	Conflicts []string

	// RaceRetry is true when trunk moved again between this attempt's
	// rebase computation and its ref update. Land races are the norm, not
	// the edge case (§13.5) - this function does not loop internally; the
	// caller decides retry policy (a bounded loop today, a merge queue in
	// v1.x per §19.4, batching/pipelining this same rule rather than
	// replacing it).
	RaceRetry bool
}

// Land attempts to land a Change onto trunkRef (§13.5):
//
//  1. Resolve the current trunk tip.
//  2. If trunk hasn't moved since the Change's base, fast-forward trunk to
//     changeHead directly (no rebase needed).
//  3. Otherwise, compute the trunk delta's affected projects (using the same
//     affectedOpts - root-invalidation patterns, strictness - the org
//     configures for regular affected computation) and decide via
//     NeedsRevalidation whether checks must be re-run; if so, stop here
//     without landing.
//  4. Otherwise, rebase (§Rebase) onto the new tip; if clean, commit the
//     result and advance trunk with a compare-and-swap ref update.
//
// changeAffected is the Change's own affected computation, recorded when its
// checks last ran - not recomputed here. The ref update is always a CAS
// against the trunk tip this attempt observed - if it fails, another land
// won the race and the caller should retry from step 1 against the new tip.
func Land(
	store core.MonorepoStore,
	repoDir string,
	trunkRef string,
	oldBase string,
	changeHead string,
	scope RevalidationScope,
	changeAffected affected.Result,
	projects []affected.ProjectInfo,
	affectedOpts affected.Options,
	meta core.CommitMeta,
) (Outcome, error) {
	trunkRefName := "refs/heads/" + trunkRef
	trunkTip, resolveErr := store.ResolveRef(trunkRefName)
	trunkIsUnborn := resolveErr != nil
	if trunkIsUnborn {
		trunkTip = ""
	}

	if string(trunkTip) == oldBase {
		expected := trunkTip
		if trunkIsUnborn {
			expected = zeroOID
		}
		if err := store.UpdateRef(trunkRefName, core.Revision(changeHead), &expected); err != nil {
			return Outcome{RaceRetry: true}, nil
		}
		return Outcome{Landed: true, LandedSHA: changeHead}, nil
	}

	if trunkIsUnborn {
		// oldBase didn't match "" (the only value that could agree with an
		// unborn trunk above), and there is no real trunk tip to diff
		// against - a genuinely inconsistent Change record, not a normal
		// race/conflict outcome.
		return Outcome{}, fmt.Errorf("land: trunk %s has no commits yet, but the change's base %q is neither empty nor resolvable", trunkRefName, oldBase)
	}

	trunkDeltaPaths, err := diffPaths(repoDir, oldBase, string(trunkTip))
	if err != nil {
		return Outcome{}, err
	}
	trunkDelta := affected.Compute(projects, trunkDeltaPaths, affectedOpts)
	if NeedsRevalidation(scope, changeAffected, trunkDelta) {
		return Outcome{RequiresRevalidation: true}, nil
	}

	rebased, err := Rebase(repoDir, oldBase, string(trunkTip), changeHead)
	if err != nil {
		return Outcome{}, err
	}
	if !rebased.Clean {
		return Outcome{Conflicts: rebased.ConflictPaths}, nil
	}

	newSHA, err := commitTree(repoDir, rebased.NewTreeSHA, string(trunkTip), meta)
	if err != nil {
		return Outcome{}, err
	}

	expected := trunkTip
	if err := store.UpdateRef(trunkRefName, core.Revision(newSHA), &expected); err != nil {
		return Outcome{RaceRetry: true}, nil
	}
	return Outcome{Landed: true, LandedSHA: newSHA}, nil
}
