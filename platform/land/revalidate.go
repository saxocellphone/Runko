package land

import "github.com/saxocellphone/runko/platform/affected"

// RevalidationScope controls when landing requires re-running checks (§13.5).
type RevalidationScope string

const (
	// RevalidationConflictOnly is the default (§13.5, rewritten 2026-07-15
	// - Gerrit's "Rebase If Necessary"): a Change green at its current head
	// whose rebase applies cleanly lands with zero re-runs, no matter how
	// far trunk moved. Conflicts still block - they are Outcome.Conflicts,
	// a separate gate this policy never touches - and post-land CI on the
	// landed tree is the accepted semantic-conflict net.
	RevalidationConflictOnly RevalidationScope = "conflict-only"
	// RevalidationAffectedIntersection is the org opt-in tier (the
	// pre-2026-07-15 default): land without re-running checks only when
	// the trunk delta since the Change's base doesn't intersect its own
	// affected project set.
	RevalidationAffectedIntersection RevalidationScope = "affected-intersection"
	// RevalidationAlways is an org opt-in that disables the optimization
	// entirely - every land re-runs checks.
	RevalidationAlways RevalidationScope = "always"
	// RevalidationNever skips the trunk-delta rule entirely - the admin
	// force-land override (§13.5, 2026-07-08). Never an org default (the
	// org-settings write path refuses it): it exists so an explicit,
	// audited override lands NOW; the everyday no-re-run path is
	// RevalidationConflictOnly, which still reports conflicts honestly
	// where force pairs with the gate bypass.
	RevalidationNever RevalidationScope = "never"
)

// NeedsRevalidation implements §13.5's land-time revalidation policy as a
// pure function over both sides' full affected.Result, not just project
// names. Under the default conflict-only tier the answer is always no -
// green checks at the current head plus a clean rebase IS the bar, and the
// trunk delta is never consulted. Under the affected-intersection tier the
// RunEverything rule matters: RunEverything on either side means that
// side's Projects list is an incomplete view BY CONSTRUCTION (§13.3 -
// "fail closed... never fail open to run nothing"), and comparing names
// alone against an incomplete list is exactly the silent fail-open that
// rule forbids, so RunEverything on either changeAffected or trunkDelta
// forces revalidation regardless of whether their (possibly-incomplete)
// project name sets happen to intersect. RevalidationAlways always
// requires it; the empty RevalidationScope behaves as
// RevalidationConflictOnly (the default).
func NeedsRevalidation(scope RevalidationScope, changeAffected, trunkDelta affected.Result) bool {
	switch scope {
	case RevalidationNever, RevalidationConflictOnly, "":
		return false
	case RevalidationAlways:
		return true
	}
	if changeAffected.RunEverything || trunkDelta.RunEverything {
		return true
	}
	return intersectsProjects(changeAffected.Projects, trunkDelta.Projects)
}

// NeedsTrunkDelta reports whether a scope's revalidation decision consults
// the trunk delta at all - callers skip the per-attempt diff + project scan
// under the tiers that never look at it.
func NeedsTrunkDelta(scope RevalidationScope) bool {
	return scope == RevalidationAffectedIntersection || scope == RevalidationAlways
}

func intersectsProjects(a, b []affected.ProjectRef) bool {
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x.Name] = true
	}
	for _, y := range b {
		if set[y.Name] {
			return true
		}
	}
	return false
}
