package affected

import (
	"reflect"
	"testing"
)

// §14.5.8: EscalationRefinableOnly is the snapshot-diff precondition -
// true only when EVERY escalation event was a refinable-pattern match.
func TestEscalationRefinableOnly(t *testing.T) {
	projects := []ProjectInfo{{Name: "repo", Path: ""}, {Name: "svc", Path: "svc"}}
	opts := Options{
		RootInvalidationPatterns: []string{"MODULE.bazel", "Makefile"},
		RefinablePatterns:        []string{"MODULE.bazel"},
	}

	r := Compute(projects, []string{"MODULE.bazel"}, opts)
	if !r.RunEverything || !r.EscalationRefinableOnly {
		t.Fatalf("refinable-only escalation: want (true, true), got (%v, %v)", r.RunEverything, r.EscalationRefinableOnly)
	}

	// A blunt pattern in the same change poisons refinability.
	r2 := Compute(projects, []string{"MODULE.bazel", "Makefile"}, opts)
	if !r2.RunEverything || r2.EscalationRefinableOnly {
		t.Fatalf("mixed escalation: want (true, false), got (%v, %v)", r2.RunEverything, r2.EscalationRefinableOnly)
	}

	// An unowned-path conservative escalation is never refinable - no
	// manifest vouched for the path.
	noRoot := []ProjectInfo{{Name: "svc", Path: "svc"}}
	r3 := Compute(noRoot, []string{"MODULE.bazel", "stray/unowned.txt"}, opts)
	if !r3.RunEverything || r3.EscalationRefinableOnly {
		t.Fatalf("unowned+refinable: want (true, false), got (%v, %v)", r3.RunEverything, r3.EscalationRefinableOnly)
	}

	// No escalation at all: the flag is meaningless and stays false.
	r4 := Compute(projects, []string{"svc/main.go"}, opts)
	if r4.RunEverything || r4.EscalationRefinableOnly {
		t.Fatalf("no escalation: want (false, false), got (%v, %v)", r4.RunEverything, r4.EscalationRefinableOnly)
	}
}

// RefinableHandled is the post-diff-success mode: refinable paths re-enter
// prose/ownership attribution exactly like "!" exceptions; blunt patterns
// keep escalating regardless.
func TestRefinableHandledMode(t *testing.T) {
	projects := []ProjectInfo{{Name: "repo", Path: ""}, {Name: "svc", Path: "svc"}}
	opts := Options{
		RootInvalidationPatterns: []string{"MODULE.bazel", "Makefile"},
		RefinablePatterns:        []string{"MODULE.bazel"},
		RefinableHandled:         true,
	}

	r := Compute(projects, []string{"MODULE.bazel"}, opts)
	if r.RunEverything {
		t.Fatalf("handled refinable path must not escalate: %+v", r)
	}
	if len(r.Projects) != 1 || r.Projects[0].Name != "repo" {
		t.Fatalf("handled path should attribute to its owner, got %v", r.Projects)
	}

	r2 := Compute(projects, []string{"Makefile"}, opts)
	if !r2.RunEverything {
		t.Fatalf("blunt pattern must escalate even in handled mode: %+v", r2)
	}

	// A handled path nobody owns keeps failing closed (§14.5.3): the mode
	// removes escalation, never gating.
	noRoot := []ProjectInfo{{Name: "svc", Path: "svc"}}
	r3 := Compute(noRoot, []string{"MODULE.bazel"}, opts)
	if !r3.RunEverything {
		t.Fatalf("handled-but-unowned path must fail closed: %+v", r3)
	}
}

func TestCloseOverDependentNames(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "proto", Path: "proto"},
		{Name: "platform", Path: "platform", DeclaredDependencies: []string{"proto"}},
		{Name: "runkod", Path: "runkod", DeclaredDependencies: []string{"platform"}},
		{Name: "web", Path: "web", DeclaredDependencies: []string{"proto"}},
	}

	refs := CloseOverDependentNames(projects, []string{"proto"})
	var names []string
	for _, r := range refs {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"platform", "proto", "runkod", "web"}) {
		t.Fatalf("want full dependents closure, got %v", names)
	}

	// Unknown names are dropped, never invented.
	refs2 := CloseOverDependentNames(projects, []string{"no-such-project"})
	if len(refs2) != 0 {
		t.Fatalf("unknown seed must yield nothing, got %v", refs2)
	}
}

// The refinable check keys on the deciding pattern's exact spelling - a
// negated decider comes back with its "!" prefix intact and matched=false.
func TestMatchOrderedWhichReportsDecider(t *testing.T) {
	patterns := []string{"!.github/workflows/ci.yml", ".github/**", "go.mod"}

	pat, matched := MatchOrderedWhich(patterns, ".github/workflows/ci.yml")
	if matched || pat != "!.github/workflows/ci.yml" {
		t.Fatalf("want (!..., false), got (%q, %v)", pat, matched)
	}
	pat2, matched2 := MatchOrderedWhich(patterns, ".github/workflows/runko-checks.yml")
	if !matched2 || pat2 != ".github/**" {
		t.Fatalf("want (.github/**, true), got (%q, %v)", pat2, matched2)
	}
	pat3, matched3 := MatchOrderedWhich(patterns, "cmd/main.go")
	if matched3 || pat3 != "" {
		t.Fatalf("no-match must report empty decider, got (%q, %v)", pat3, matched3)
	}
}
