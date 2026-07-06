package checks

import (
	"fmt"
	"sort"
	"testing"
)

func TestComputeMergeRequirementsAllSatisfied(t *testing.T) {
	owners := []OwnerRequirement{{OwnerRef: "group:commerce-eng", Satisfied: true}}
	runs := []CheckRunView{{Name: "lint", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess}}

	req := ComputeMergeRequirements("chg_1", owners, []string{"lint"}, runs, nil, nil)
	if !req.Mergeable {
		t.Fatalf("expected mergeable, got blockers: %v", req.Blockers)
	}
	if len(req.PassingChecks) != 1 || req.PassingChecks[0] != "lint" {
		t.Fatalf("expected lint in passing checks, got %v", req.PassingChecks)
	}
}

func TestComputeMergeRequirementsOutstandingOwner(t *testing.T) {
	owners := []OwnerRequirement{{OwnerRef: "group:commerce-eng", Satisfied: false}}
	req := ComputeMergeRequirements("chg_1", owners, nil, nil, nil, nil)
	if req.Mergeable {
		t.Fatalf("expected not mergeable with an outstanding owner")
	}
	if len(req.OutstandingOwners) != 1 || req.OutstandingOwners[0] != "group:commerce-eng" {
		t.Fatalf("expected outstanding owner, got %v", req.OutstandingOwners)
	}
	if len(req.Blockers) == 0 {
		t.Fatalf("expected a plain-language blocker")
	}
}

func TestComputeMergeRequirementsFailingIndividualCheck(t *testing.T) {
	runs := []CheckRunView{{Name: "unit", Status: CheckStatusCompleted, Conclusion: ConclusionFailure}}
	req := ComputeMergeRequirements("chg_1", nil, []string{"unit"}, runs, nil, nil)
	if req.Mergeable {
		t.Fatalf("expected not mergeable with a failing required check")
	}
	if len(req.FailingChecks) != 1 || req.FailingChecks[0] != "unit" {
		t.Fatalf("expected unit in failing checks, got %v", req.FailingChecks)
	}
}

func TestComputeMergeRequirementsCheckSetMissingBlocksMerge(t *testing.T) {
	checkSets := []CheckSet{{Policy: CheckSetPolicy{Pattern: "unit:*", Scope: "affected"}, Projects: []string{"checkout-api", "billing-lib"}}}
	runs := []CheckRunView{{Name: "unit:checkout-api", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess}}

	req := ComputeMergeRequirements("chg_1", nil, nil, runs, checkSets, nil)
	if req.Mergeable {
		t.Fatalf("expected not mergeable with a missing check-set member, got %+v", req)
	}
	if len(req.Blockers) == 0 {
		t.Fatalf("expected at least one blocker message")
	}

	// A check-set member with no run posted at all must still show up in
	// both RequiredChecks and PendingChecks - not vanish from every array
	// leaving only a blocker string behind. Otherwise an agent reading
	// get_merge_requirements can't tell required == passing ∪ failing ∪
	// pending, precisely for the case where CI silently failed to fan out.
	if !containsString(req.RequiredChecks, "unit:billing-lib") {
		t.Fatalf("expected missing check-set member unit:billing-lib in RequiredChecks, got %v", req.RequiredChecks)
	}
	if !containsString(req.PendingChecks, "unit:billing-lib") {
		t.Fatalf("expected missing check-set member unit:billing-lib in PendingChecks, got %v", req.PendingChecks)
	}
	assertRequiredEqualsUnion(t, req)
}

func TestComputeMergeRequirementsCheckSetPendingCountMatchesLabel(t *testing.T) {
	// 3 projects, 1 passed, 2 still queued (not missing, not failed) - the
	// "still running" blocker's count must be the PENDING count, not
	// total-minus-pending (which silently reports the number that AREN'T
	// pending under a "still running" label).
	checkSets := []CheckSet{{
		Policy:   CheckSetPolicy{Pattern: "unit:*", Scope: "affected"},
		Projects: []string{"a", "b", "c"},
	}}
	runs := []CheckRunView{
		{Name: "unit:a", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
		{Name: "unit:b", Status: CheckStatusInProgress},
		{Name: "unit:c", Status: CheckStatusQueued},
	}

	req := ComputeMergeRequirements("chg_1", nil, nil, runs, checkSets, nil)
	want := fmt.Sprintf("%s — %d/%d still running", "unit:*", 2, 3)
	if !containsString(req.Blockers, want) {
		t.Fatalf("expected blocker %q, got %v", want, req.Blockers)
	}
	assertRequiredEqualsUnion(t, req)
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// assertRequiredEqualsUnion checks the invariant every caller of
// get_merge_requirements relies on: RequiredChecks is exactly the union of
// PassingChecks, FailingChecks, and PendingChecks.
func assertRequiredEqualsUnion(t *testing.T, req MergeRequirements) {
	t.Helper()
	union := append([]string{}, req.PassingChecks...)
	union = append(union, req.FailingChecks...)
	union = append(union, req.PendingChecks...)
	sort.Strings(union)
	required := append([]string{}, req.RequiredChecks...)
	sort.Strings(required)
	if fmt.Sprint(union) != fmt.Sprint(required) {
		t.Fatalf("RequiredChecks must equal PassingChecks ∪ FailingChecks ∪ PendingChecks:\nrequired: %v\nunion:    %v", required, union)
	}
}

func TestComputeMergeRequirementsCheckSetAllPassingIsMergeable(t *testing.T) {
	checkSets := []CheckSet{{Policy: CheckSetPolicy{Pattern: "unit:*", Scope: "affected"}, Projects: []string{"checkout-api"}}}
	runs := []CheckRunView{{Name: "unit:checkout-api", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess}}

	req := ComputeMergeRequirements("chg_1", nil, nil, runs, checkSets, nil)
	if !req.Mergeable {
		t.Fatalf("expected mergeable, got blockers: %v", req.Blockers)
	}
	if len(req.PassingChecks) != 1 || req.PassingChecks[0] != "unit:checkout-api" {
		t.Fatalf("expected check-set member expanded into passing checks, got %v", req.PassingChecks)
	}
}

func TestComputeMergeRequirementsStaleCheckBlocksMerge(t *testing.T) {
	req := ComputeMergeRequirements("chg_1", nil, nil, nil, nil, []string{"flaky-e2e"})
	if req.Mergeable {
		t.Fatalf("expected not mergeable with a stale check reporter")
	}
}

func TestComputeMergeRequirementsEmptyIsMergeable(t *testing.T) {
	req := ComputeMergeRequirements("chg_1", nil, nil, nil, nil, nil)
	if !req.Mergeable || len(req.Blockers) != 0 {
		t.Fatalf("expected a Change with no requirements to be trivially mergeable, got %+v", req)
	}
}
