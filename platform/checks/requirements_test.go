package checks

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func TestComputeMergeRequirementsAllSatisfied(t *testing.T) {
	owners := []OwnerRequirement{{OwnerRef: "group:commerce-eng", Satisfied: true}}
	runs := []CheckRunView{{Name: "lint", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess}}

	req := ComputeMergeRequirements("chg_1", owners, []string{"lint"}, runs, nil, nil, nil)
	if !req.Mergeable {
		t.Fatalf("expected mergeable, got blockers: %v", req.Blockers)
	}
	if len(req.PassingChecks) != 1 || req.PassingChecks[0] != "lint" {
		t.Fatalf("expected lint in passing checks, got %v", req.PassingChecks)
	}
}

func TestComputeMergeRequirementsOutstandingOwner(t *testing.T) {
	owners := []OwnerRequirement{{OwnerRef: "group:commerce-eng", Satisfied: false}}
	req := ComputeMergeRequirements("chg_1", owners, nil, nil, nil, nil, nil)
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

// TestMergeRequirementsJSONRoundTrip pins MarshalJSON/UnmarshalJSON as
// inverses - clients (runko change approve, the stage-12 MCP adapter)
// decode exactly what the daemon encodes, one wire contract (§8.3).
func TestMergeRequirementsJSONRoundTrip(t *testing.T) {
	in := MergeRequirements{
		ChangeID:          "chg_1",
		RequiredOwners:    []string{"group:commerce-eng"},
		OutstandingOwners: []string{"group:commerce-eng"},
		RequiredChecks:    []string{"unit"},
		PendingChecks:     []string{"unit"},
		CheckDetailsURLs:  map[string]string{"unit": "https://ci.example.com/runs/1"},
		Blockers:          []string{"unit has not reported yet", "waiting on approval from group:commerce-eng"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out MergeRequirements
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Marshal normalizes nil slices to empty ones (nonNilStrings), so
	// normalize the input the same way before comparing.
	in.SatisfiedOwners = []string{}
	in.PassingChecks = []string{}
	in.FailingChecks = []string{}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

func TestComputeMergeRequirementsFailingIndividualCheck(t *testing.T) {
	runs := []CheckRunView{{Name: "unit", Status: CheckStatusCompleted, Conclusion: ConclusionFailure}}
	req := ComputeMergeRequirements("chg_1", nil, []string{"unit"}, runs, nil, nil, nil)
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

	req := ComputeMergeRequirements("chg_1", nil, nil, runs, checkSets, nil, nil)
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

	req := ComputeMergeRequirements("chg_1", nil, nil, runs, checkSets, nil, nil)
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

	req := ComputeMergeRequirements("chg_1", nil, nil, runs, checkSets, nil, nil)
	if !req.Mergeable {
		t.Fatalf("expected mergeable, got blockers: %v", req.Blockers)
	}
	if len(req.PassingChecks) != 1 || req.PassingChecks[0] != "unit:checkout-api" {
		t.Fatalf("expected check-set member expanded into passing checks, got %v", req.PassingChecks)
	}
}

func TestComputeMergeRequirementsStaleCheckBlocksMerge(t *testing.T) {
	req := ComputeMergeRequirements("chg_1", nil, nil, nil, nil, []string{"flaky-e2e"}, nil)
	if req.Mergeable {
		t.Fatalf("expected not mergeable with a stale check reporter")
	}
}

func TestComputeMergeRequirementsEmptyIsMergeable(t *testing.T) {
	req := ComputeMergeRequirements("chg_1", nil, nil, nil, nil, nil, nil)
	if !req.Mergeable || len(req.Blockers) != 0 {
		t.Fatalf("expected a Change with no requirements to be trivially mergeable, got %+v", req)
	}
}

// TestComputeMergeRequirementsRequireBuildBindingBlocksMerge is the DAG
// stage 9c bar (docs/design.md §13.5, §14.5.4): with an org's
// require_build_binding gate on, a Change touching a project that lacks a
// build-graph binding must report a blocker - the caller (whoever resolves
// which affected projects have the "build" capability) is what decides
// unboundProjects is non-empty; ComputeMergeRequirements itself doesn't
// know or care whether the org opted in, matching how staleCheckNames works.
func TestComputeMergeRequirementsRequireBuildBindingBlocksMerge(t *testing.T) {
	req := ComputeMergeRequirements("chg_1", nil, nil, nil, nil, nil, []string{"legacy-lib"})
	if req.Mergeable {
		t.Fatalf("expected not mergeable when a project lacks a required build binding")
	}
	if !containsString(req.Blockers, "legacy-lib has no build-graph binding (org requires one: require_build_binding)") {
		t.Fatalf("expected a plain-language build-binding blocker, got %v", req.Blockers)
	}
}

func TestComputeMergeRequirementsWithoutRequireBuildBindingIsUnaffected(t *testing.T) {
	// Passing nil (the org hasn't opted in) must be indistinguishable from
	// the pre-9c behavior - no blocker, no change to Mergeable.
	req := ComputeMergeRequirements("chg_1", nil, nil, nil, nil, nil, nil)
	if !req.Mergeable || len(req.Blockers) != 0 {
		t.Fatalf("expected no build-binding blockers when unboundProjects is nil, got %+v", req)
	}
}
