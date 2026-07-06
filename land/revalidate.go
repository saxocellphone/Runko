package land

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
)

// NeedsRevalidation implements §13.5's optimistic-land rule as a pure
// function: given the Change's own affected project set and the set of
// projects touched by the trunk delta since the Change's base, decide
// whether checks must be re-run before landing. RevalidationAlways always
// requires it; the empty RevalidationScope behaves as
// RevalidationAffectedIntersection (the default).
func NeedsRevalidation(scope RevalidationScope, changeAffectedProjects, trunkDeltaAffectedProjects []string) bool {
	if scope == RevalidationAlways {
		return true
	}
	return intersects(changeAffectedProjects, trunkDeltaAffectedProjects)
}

func intersects(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if set[y] {
			return true
		}
	}
	return false
}
