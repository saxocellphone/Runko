package land

import (
	"fmt"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/core"
)

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
//  3. Otherwise, compute the trunk delta's affected projects and decide via
//     NeedsRevalidation whether checks must be re-run; if so, stop here
//     without landing.
//  4. Otherwise, rebase (§Rebase) onto the new tip; if clean, commit the
//     result and advance trunk with a compare-and-swap ref update.
//
// The ref update is always a CAS against the trunk tip this attempt observed
// - if it fails, another land won the race and the caller should retry from
// step 1 against the new tip.
func Land(
	store core.MonorepoStore,
	repoDir string,
	trunkRef string,
	oldBase string,
	changeHead string,
	scope RevalidationScope,
	changeAffectedProjects []string,
	projects []affected.ProjectInfo,
	meta core.CommitMeta,
) (Outcome, error) {
	trunkRefName := "refs/heads/" + trunkRef
	trunkTip, err := store.ResolveRef(trunkRefName)
	if err != nil {
		return Outcome{}, fmt.Errorf("land: resolve trunk %s: %w", trunkRefName, err)
	}

	if string(trunkTip) == oldBase {
		expected := trunkTip
		if err := store.UpdateRef(trunkRefName, core.Revision(changeHead), &expected); err != nil {
			return Outcome{RaceRetry: true}, nil
		}
		return Outcome{Landed: true, LandedSHA: changeHead}, nil
	}

	trunkDeltaPaths, err := diffPaths(repoDir, oldBase, string(trunkTip))
	if err != nil {
		return Outcome{}, err
	}
	trunkDelta := affected.Compute(projects, trunkDeltaPaths, affected.Options{})
	if NeedsRevalidation(scope, changeAffectedProjects, projectNames(trunkDelta.Projects)) {
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

func projectNames(refs []affected.ProjectRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Name
	}
	return out
}
