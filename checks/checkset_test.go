package checks

import (
	"reflect"
	"sort"
	"testing"
)

// TestEvaluateCheckSetTable is the DAG's "check-set aggregation table-tested"
// bar (design.md §28.3 stage 8, §14.4.2's "unit:* over affected" example).
func TestEvaluateCheckSetTable(t *testing.T) {
	policy := CheckSetPolicy{Pattern: "unit:*", Scope: "affected"}
	projects := []string{"checkout-api", "billing-lib", "gateway"}

	cases := []struct {
		name       string
		runs       []CheckRunView
		wantTotal  int
		wantPassed int
		wantFailed int
		wantPend   int
		wantMissed []string
	}{
		{
			name: "all passing",
			runs: []CheckRunView{
				{Name: "unit:checkout-api", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
				{Name: "unit:billing-lib", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
				{Name: "unit:gateway", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
			},
			wantTotal: 3, wantPassed: 3,
		},
		{
			name: "one failing",
			runs: []CheckRunView{
				{Name: "unit:checkout-api", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
				{Name: "unit:billing-lib", Status: CheckStatusCompleted, Conclusion: ConclusionFailure},
				{Name: "unit:gateway", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
			},
			wantTotal: 3, wantPassed: 2, wantFailed: 1,
		},
		{
			name: "one pending, one missing",
			runs: []CheckRunView{
				{Name: "unit:checkout-api", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess},
				{Name: "unit:billing-lib", Status: CheckStatusInProgress},
			},
			wantTotal: 3, wantPassed: 1, wantPend: 1, wantMissed: []string{"gateway"},
		},
		{
			name:       "none reported",
			runs:       nil,
			wantTotal:  3,
			wantMissed: []string{"checkout-api", "billing-lib", "gateway"},
		},
		{
			name: "non-success terminal conclusions count as failed",
			runs: []CheckRunView{
				{Name: "unit:checkout-api", Status: CheckStatusCompleted, Conclusion: ConclusionCancelled},
				{Name: "unit:billing-lib", Status: CheckStatusCompleted, Conclusion: ConclusionTimedOut},
				{Name: "unit:gateway", Status: CheckStatusCompleted, Conclusion: ConclusionActionRequired},
			},
			wantTotal: 3, wantFailed: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := EvaluateCheckSet(policy, projects, tc.runs)
			if status.Total != tc.wantTotal || status.Passed != tc.wantPassed ||
				status.Failed != tc.wantFailed || status.Pending != tc.wantPend {
				t.Fatalf("status = %+v, want total=%d passed=%d failed=%d pending=%d",
					status, tc.wantTotal, tc.wantPassed, tc.wantFailed, tc.wantPend)
			}
			gotMissing := append([]string(nil), status.Missing...)
			sort.Strings(gotMissing)
			wantMissing := append([]string(nil), tc.wantMissed...)
			sort.Strings(wantMissing)
			if !reflect.DeepEqual(gotMissing, wantMissing) {
				t.Fatalf("Missing = %v, want %v", gotMissing, wantMissing)
			}
		})
	}
}

func TestEvaluateCheckSetPatternExpansion(t *testing.T) {
	policy := CheckSetPolicy{Pattern: "lint:*", Scope: "all"}
	runs := []CheckRunView{{Name: "lint:my-project", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess}}
	status := EvaluateCheckSet(policy, []string{"my-project"}, runs)
	if status.Passed != 1 || status.Total != 1 {
		t.Fatalf("expected pattern 'lint:*' to expand to 'lint:my-project' and match, got %+v", status)
	}
}
