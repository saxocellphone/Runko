package land

import "github.com/saxocellphone/runko/platform/affected"

// RevalidationScope controls when landing requires re-running checks (§13.5).
type RevalidationScope string

const (
	// RevalidationAffectedIntersection is the v1 default: land without
	// re-running checks when the trunk delta since the Change's base
	// doesn't intersect its own affected project set.
	RevalidationAffectedIntersection RevalidationScope = "affected-intersection"
	// RevalidationAlways is an org opt-in that disables the optimization
	// entirely - every land re-runs checks.
	RevalidationAlways RevalidationScope = "always"
	// RevalidationNever skips the trunk-delta rule entirely - the admin
	// force-land override (§13.5, 2026-07-08). Never an org default: it
	// exists so an explicit, audited override lands NOW; everything else
	// uses the intersection rule.
	RevalidationNever RevalidationScope = "never"
)

// NeedsRevalidation implements §13.5's optimistic-land rule as a pure
// function over both sides' full affected.Result, not just project names.
// This matters: RunEverything on either side means that side's Projects list
// is an incomplete view BY CONSTRUCTION (§13.3 - "fail closed... never fail
// open to run nothing"). Comparing names alone against an incomplete list is
// exactly the silent fail-open that rule forbids, so RunEverything on either
// changeAffected or trunkDelta always forces revalidation, regardless of
// whether their (possibly-incomplete) project name sets happen to intersect.
// RevalidationAlways always requires it; the empty RevalidationScope behaves
// as RevalidationAffectedIntersection (the default).
func NeedsRevalidation(scope RevalidationScope, changeAffected, trunkDelta affected.Result) bool {
	if scope == RevalidationNever {
		return false
	}
	if scope == RevalidationAlways {
		return true
	}
	if changeAffected.RunEverything || trunkDelta.RunEverything {
		return true
	}
	return intersectsProjects(changeAffected.Projects, trunkDelta.Projects)
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
