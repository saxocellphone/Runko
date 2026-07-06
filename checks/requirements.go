package checks

import (
	"encoding/json"
	"fmt"
)

// OwnerRequirement is one required owner and whether their approval has been
// recorded, mirroring the change_owner_requirements table.
type OwnerRequirement struct {
	OwnerRef  string
	Satisfied bool
}

// MergeRequirements mirrors docs/spec/mcp-tools/common.schema.json#/$defs/MergeRequirements -
// the same structure surfaced to humans (Change page) and agents
// (get_merge_requirements), §8.3, §13.4. Check-set policies are pre-expanded
// into concrete check names here, per that schema.
type MergeRequirements struct {
	ChangeID string

	RequiredOwners    []string
	SatisfiedOwners   []string
	OutstandingOwners []string

	RequiredChecks []string
	PassingChecks  []string
	FailingChecks  []string
	PendingChecks  []string

	Mergeable bool
	// Blockers are plain-language, per §6.6 ("Your workspace is 12 commits
	// behind trunk; 2 of your files conflict" is the model to follow).
	Blockers []string
}

// CheckSet pairs a policy with the project list its scope resolves to -
// resolving "affected" vs "all" to a concrete project list is the caller's
// job (e.g. using affected.Compute's result for "affected").
type CheckSet struct {
	Policy   CheckSetPolicy
	Projects []string
}

// ComputeMergeRequirements assembles the aggregate view from owner
// requirements, individually-required check names, check-set policies, and
// currently-stale check names.
func ComputeMergeRequirements(
	changeID string,
	owners []OwnerRequirement,
	requiredCheckNames []string,
	runs []CheckRunView,
	checkSets []CheckSet,
	staleCheckNames []string,
) MergeRequirements {
	var reqOwners, satOwners, outOwners []string
	for _, o := range owners {
		reqOwners = append(reqOwners, o.OwnerRef)
		if o.Satisfied {
			satOwners = append(satOwners, o.OwnerRef)
		} else {
			outOwners = append(outOwners, o.OwnerRef)
		}
	}

	var reqChecks, passChecks, failChecks, pendChecks []string
	var blockers []string

	for _, name := range requiredCheckNames {
		reqChecks = append(reqChecks, name)
		run, ok := findRun(runs, name)
		switch {
		case !ok:
			pendChecks = append(pendChecks, name)
			blockers = append(blockers, fmt.Sprintf("%s has not reported yet", name))
		case run.Status != CheckStatusCompleted:
			pendChecks = append(pendChecks, name)
			blockers = append(blockers, fmt.Sprintf("%s is still running", name))
		case run.Conclusion == ConclusionSuccess:
			passChecks = append(passChecks, name)
		default:
			failChecks = append(failChecks, name)
			blockers = append(blockers, fmt.Sprintf("%s failed", name))
		}
	}

	for _, cs := range checkSets {
		passing, failing, pending, missing := expandCheckSet(cs.Policy, cs.Projects, runs)
		reqChecks = append(reqChecks, passing...)
		reqChecks = append(reqChecks, failing...)
		reqChecks = append(reqChecks, pending...)
		passChecks = append(passChecks, passing...)
		failChecks = append(failChecks, failing...)
		pendChecks = append(pendChecks, pending...)

		total := len(cs.Projects)
		switch {
		case len(failing) > 0:
			blockers = append(blockers, fmt.Sprintf("%s — %d/%d failed", cs.Policy.Pattern, len(failing), total))
		case len(missing) > 0:
			blockers = append(blockers, fmt.Sprintf("%s — missing runs for %d project(s)", cs.Policy.Pattern, len(missing)))
		case len(pending) > 0:
			blockers = append(blockers, fmt.Sprintf("%s — %d/%d still running", cs.Policy.Pattern, total-len(pending), total))
		}
	}

	for _, o := range outOwners {
		blockers = append(blockers, fmt.Sprintf("waiting on approval from %s", o))
	}
	for _, name := range staleCheckNames {
		blockers = append(blockers, fmt.Sprintf("%s has a stale reporter - no update received within its TTL", name))
	}

	return MergeRequirements{
		ChangeID:          changeID,
		RequiredOwners:    reqOwners,
		SatisfiedOwners:   satOwners,
		OutstandingOwners: outOwners,
		RequiredChecks:    reqChecks,
		PassingChecks:     passChecks,
		FailingChecks:     failChecks,
		PendingChecks:     pendChecks,
		Mergeable:         len(blockers) == 0,
		Blockers:          blockers,
	}
}

// mergeRequirementsWire is the nested JSON shape defined by
// docs/spec/mcp-tools/common.schema.json#/$defs/MergeRequirements - distinct
// from MergeRequirements' flat Go fields, which are more convenient for the
// aggregation logic above.
type mergeRequirementsWire struct {
	ChangeID string `json:"change_id"`
	Owners   struct {
		Required    []string `json:"required"`
		Satisfied   []string `json:"satisfied"`
		Outstanding []string `json:"outstanding"`
	} `json:"owners"`
	Checks struct {
		Required []string `json:"required"`
		Passing  []string `json:"passing"`
		Failing  []string `json:"failing"`
		Pending  []string `json:"pending"`
	} `json:"checks"`
	Mergeable bool     `json:"mergeable"`
	Blockers  []string `json:"blockers"`
}

// MarshalJSON renders MergeRequirements in the schema's nested shape (see
// mergeRequirementsWire) rather than Go's flat field names.
func (m MergeRequirements) MarshalJSON() ([]byte, error) {
	var w mergeRequirementsWire
	w.ChangeID = m.ChangeID
	w.Owners.Required = nonNilStrings(m.RequiredOwners)
	w.Owners.Satisfied = nonNilStrings(m.SatisfiedOwners)
	w.Owners.Outstanding = nonNilStrings(m.OutstandingOwners)
	w.Checks.Required = nonNilStrings(m.RequiredChecks)
	w.Checks.Passing = nonNilStrings(m.PassingChecks)
	w.Checks.Failing = nonNilStrings(m.FailingChecks)
	w.Checks.Pending = nonNilStrings(m.PendingChecks)
	w.Mergeable = m.Mergeable
	w.Blockers = nonNilStrings(m.Blockers)
	return json.Marshal(w)
}

// nonNilStrings replaces a nil slice with an empty one, so JSON Schema's
// "array" type (which rejects null) is always satisfied.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
