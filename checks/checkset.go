package checks

import "strings"

// CheckStatus and CheckConclusion mirror
// docs/spec/webhooks/checkrun.schema.json#/$defs/CheckStatus /
// #/$defs/CheckConclusion.
type CheckStatus string

const (
	CheckStatusQueued     CheckStatus = "queued"
	CheckStatusInProgress CheckStatus = "in_progress"
	CheckStatusCompleted  CheckStatus = "completed"
)

type CheckConclusion string

const (
	ConclusionSuccess        CheckConclusion = "success"
	ConclusionFailure        CheckConclusion = "failure"
	ConclusionCancelled      CheckConclusion = "cancelled"
	ConclusionSkipped        CheckConclusion = "skipped"
	ConclusionTimedOut       CheckConclusion = "timed_out"
	ConclusionActionRequired CheckConclusion = "action_required"
	ConclusionNeutral        CheckConclusion = "neutral"
)

// CheckRunView is the minimal shape check-set/merge-requirements logic needs
// about one posted CheckRun - deliberately independent of internal/dbgen so
// this package's pure logic never needs a database to test.
type CheckRunView struct {
	Name       string
	Status     CheckStatus
	Conclusion CheckConclusion // meaningful only when Status == CheckStatusCompleted
}

// CheckSetPolicy mirrors docs/spec/webhooks/checkrun.schema.json#/$defs/CheckSetPolicy.
// Pattern contains exactly one "*", substituted with a project name, e.g.
// "unit:*" + "checkout-api" -> "unit:checkout-api".
type CheckSetPolicy struct {
	Pattern string
	Scope   string // "affected" | "all" - resolving scope to a project list is the caller's job
}

// CheckSetStatus mirrors docs/spec/webhooks/checkrun.schema.json#/$defs/CheckSetStatus -
// the aggregated view a Change page renders as one collapsible row
// ("unit — 38/40 passed") instead of 40 required-check rows (§14.4.2).
type CheckSetStatus struct {
	Pattern string
	Scope   string
	Total   int
	Passed  int
	Failed  int
	Pending int
	Missing []string // project names with no run posted yet for this pattern
}

// EvaluateCheckSet expands policy over expectedProjectNames and tallies the
// result against runs. Only a completed run with conclusion "success" counts
// as passing; every other terminal conclusion counts as failed - the
// aggregate view is binary (satisfied the requirement or not), matching
// CheckSetStatus's passed/failed/pending/missing shape.
func EvaluateCheckSet(policy CheckSetPolicy, expectedProjectNames []string, runs []CheckRunView) CheckSetStatus {
	passing, failing, pending, missing := expandCheckSet(policy, expectedProjectNames, runs)
	return CheckSetStatus{
		Pattern: policy.Pattern,
		Scope:   policy.Scope,
		Total:   len(expectedProjectNames),
		Passed:  len(passing),
		Failed:  len(failing),
		Pending: len(pending),
		Missing: missing,
	}
}

// expandCheckSet is EvaluateCheckSet's expansion step, also reused directly
// by ComputeMergeRequirements to fold check-set members into the flat
// required/passing/failing/pending check-name lists that schema calls for
// (§14.4.2's "pre-expanded into concrete names").
func expandCheckSet(policy CheckSetPolicy, expectedProjectNames []string, runs []CheckRunView) (passingChecks, failingChecks, pendingChecks, missingProjects []string) {
	for _, project := range expectedProjectNames {
		checkName := strings.ReplaceAll(policy.Pattern, "*", project)
		run, ok := findRun(runs, checkName)
		switch {
		case !ok:
			missingProjects = append(missingProjects, project)
		case run.Status != CheckStatusCompleted:
			pendingChecks = append(pendingChecks, checkName)
		case run.Conclusion == ConclusionSuccess:
			passingChecks = append(passingChecks, checkName)
		default:
			failingChecks = append(failingChecks, checkName)
		}
	}
	return
}

func findRun(runs []CheckRunView, name string) (CheckRunView, bool) {
	for _, r := range runs {
		if r.Name == name {
			return r, true
		}
	}
	return CheckRunView{}, false
}
