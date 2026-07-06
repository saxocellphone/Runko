package checks

import "testing"

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
	found := false
	for _, b := range req.Blockers {
		if b != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected at least one blocker message")
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
