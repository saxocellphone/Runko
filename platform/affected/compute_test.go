package affected

import (
	"reflect"
	"sort"
	"testing"
	"testing/quick"
)

func TestDirectPathLongestPrefixMatch(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "commerce", Path: "commerce"},
		{Name: "checkout-api", Path: "commerce/checkout"},
	}
	r := Compute(projects, []string{"commerce/checkout/handler.go"}, Options{})

	if r.RunEverything {
		t.Fatalf("expected RunEverything=false, got true: %+v", r)
	}
	if len(r.Projects) != 1 || r.Projects[0].Name != "checkout-api" {
		t.Fatalf("expected only checkout-api (longest prefix), got %+v", r.Projects)
	}
	if !reasonSet(r).has(ReasonDirectPath) {
		t.Fatalf("expected direct_path reason, got %v", r.ReasonCodes)
	}
}

func TestTransitiveDependentsClosure(t *testing.T) {
	// gateway -> depends on checkout-api -> depends on payments-lib.
	// Changing payments-lib must affect checkout-api AND gateway.
	projects := []ProjectInfo{
		{Name: "payments-lib", Path: "libs/payments"},
		{Name: "checkout-api", Path: "commerce/checkout", DeclaredDependencies: []string{"payments-lib"}},
		{Name: "gateway", Path: "commerce/gateway", DeclaredDependencies: []string{"checkout-api"}},
		{Name: "unrelated", Path: "libs/unrelated"},
	}
	r := Compute(projects, []string{"libs/payments/charge.go"}, Options{})

	names := projectNames(r)
	sort.Strings(names)
	want := []string{"checkout-api", "gateway", "payments-lib"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("want %v, got %v", want, names)
	}
	if !reasonSet(r).has(ReasonDependsOn) {
		t.Fatalf("expected depends_on reason, got %v", r.ReasonCodes)
	}
	if r.RunEverything {
		t.Fatalf("expected RunEverything=false, got true")
	}
}

func TestDependencyCycleDoesNotHang(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "a", Path: "a", DeclaredDependencies: []string{"b"}},
		{Name: "b", Path: "b", DeclaredDependencies: []string{"a"}},
	}
	r := Compute(projects, []string{"a/x.go"}, Options{})
	names := projectNames(r)
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("want [a b], got %v", names)
	}
}

func TestRootInvalidationPattern(t *testing.T) {
	projects := []ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	opts := Options{RootInvalidationPatterns: []string{"go.mod", ".github/workflows/**"}}

	r := Compute(projects, []string{"go.mod"}, opts)
	if !r.RunEverything {
		t.Fatalf("expected RunEverything=true for go.mod, got false: %+v", r)
	}
	if !reasonSet(r).has(ReasonRootInvalidation) {
		t.Fatalf("expected root_invalidation reason, got %v", r.ReasonCodes)
	}

	r2 := Compute(projects, []string{".github/workflows/ci.yml"}, opts)
	if !r2.RunEverything {
		t.Fatalf("expected RunEverything=true for a workflows/** path, got false: %+v", r2)
	}
}

// §14.5.8: root-invalidation lists are ordered with "!" exceptions - a
// known post-land-only file is carved out of a broad ".github/**" and falls
// through to ordinary attribution instead of escalating.
func TestRootInvalidationExceptionDoesNotEscalate(t *testing.T) {
	projects := []ProjectInfo{{Name: "repo", Path: ""}}
	opts := Options{RootInvalidationPatterns: []string{"!.github/workflows/ci.yml", ".github/**"}}

	r := Compute(projects, []string{".github/workflows/ci.yml"}, opts)
	if r.RunEverything {
		t.Fatalf("excepted path must not escalate: %+v", r)
	}
	if len(r.Projects) != 1 || r.Projects[0].Name != "repo" {
		t.Fatalf("excepted path should fall through to its owner, got %v", r.Projects)
	}

	// A sibling not covered by the exception still escalates.
	r2 := Compute(projects, []string{".github/workflows/runko-checks.yml"}, opts)
	if !r2.RunEverything {
		t.Fatalf("non-excepted workflow path must still escalate: %+v", r2)
	}

	// Both in one change: the un-excepted path's escalation wins (an
	// exception never narrows what another path escalated).
	r3 := Compute(projects, []string{".github/workflows/ci.yml", ".github/workflows/runko-checks.yml"}, opts)
	if !r3.RunEverything {
		t.Fatalf("mixed change must still escalate: %+v", r3)
	}
}

// First-match-wins is load-bearing: an exception listed AFTER the broad
// pattern it means to carve is dead, and the path escalates - the safe
// direction to get ordering wrong.
func TestRootInvalidationExceptionAfterPatternIsDead(t *testing.T) {
	projects := []ProjectInfo{{Name: "repo", Path: ""}}
	opts := Options{RootInvalidationPatterns: []string{".github/**", "!.github/workflows/ci.yml"}}

	r := Compute(projects, []string{".github/workflows/ci.yml"}, opts)
	if !r.RunEverything {
		t.Fatalf("exception after its pattern must be inert (first match wins): %+v", r)
	}
}

// An excepted path is not exempt from prose/ownership rules - it re-enters
// the normal pipeline. With no owning project at all it keeps failing
// closed (§14.5.3): the exception removes ESCALATION, never gating.
func TestRootInvalidationExceptionStillFailsClosedWhenUnowned(t *testing.T) {
	projects := []ProjectInfo{{Name: "svc", Path: "svc"}} // no root project
	opts := Options{RootInvalidationPatterns: []string{"!.github/workflows/ci.yml", ".github/**"}}

	r := Compute(projects, []string{".github/workflows/ci.yml"}, opts)
	if !r.RunEverything {
		t.Fatalf("excepted but unowned path must fail closed to run_everything: %+v", r)
	}
}

func TestUnownedPathFailsClosedByDefault(t *testing.T) {
	projects := []ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	r := Compute(projects, []string{"some/random/unregistered/file.txt"}, Options{})
	if !r.RunEverything {
		t.Fatalf("conservative default: expected RunEverything=true for an unowned path, got false: %+v", r)
	}
}

func TestUnownedPathAggressiveModeDoesNotForceRunEverything(t *testing.T) {
	projects := []ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	r := Compute(projects, []string{"some/random/unregistered/file.txt"}, Options{Strictness: StrictnessAggressive})
	if r.RunEverything {
		t.Fatalf("aggressive mode: expected RunEverything=false for an unowned path, got true: %+v", r)
	}
	if len(r.Projects) != 0 {
		t.Fatalf("expected no affected projects, got %+v", r.Projects)
	}
}

func TestRootProjectMatchesEverythingAtLowestPriority(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "root-docs", Path: ""},
		{Name: "checkout-api", Path: "commerce/checkout"},
	}
	r := Compute(projects, []string{"commerce/checkout/handler.go", "top-level.md"}, Options{})
	names := projectNames(r)
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"checkout-api", "root-docs"}) {
		t.Fatalf("want [checkout-api root-docs], got %v", names)
	}
	if r.RunEverything {
		t.Fatalf("root project should absorb top-level.md without forcing RunEverything, got true")
	}
}

func TestPathsAreDeduplicatedAndSorted(t *testing.T) {
	r := Compute(nil, []string{"b.txt", "a.txt", "b.txt"}, Options{Strictness: StrictnessAggressive})
	if !reflect.DeepEqual(r.Paths, []string{"a.txt", "b.txt"}) {
		t.Fatalf("want deduplicated sorted paths, got %v", r.Paths)
	}
}

// --- property tests (design.md §28.3 stage 5: "property tests incl.
// run_everything root rules") ---

func TestComputeIsDeterministic(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "a", Path: "a", DeclaredDependencies: []string{"b"}},
		{Name: "b", Path: "b"},
	}
	f := func(paths []string) bool {
		r1 := Compute(projects, paths, Options{})
		r2 := Compute(projects, paths, Options{})
		return reflect.DeepEqual(r1, r2)
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func TestRunEverythingImpliesReasonCodes(t *testing.T) {
	projects := []ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	f := func(paths []string) bool {
		r := Compute(projects, paths, Options{})
		if r.RunEverything {
			return len(r.ReasonCodes) > 0
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func TestNoDuplicateProjectNamesInResult(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "a", Path: "a", DeclaredDependencies: []string{"c"}},
		{Name: "b", Path: "b", DeclaredDependencies: []string{"c"}},
		{Name: "c", Path: "c"},
	}
	f := func(paths []string) bool {
		r := Compute(projects, paths, Options{})
		seen := map[string]bool{}
		for _, p := range r.Projects {
			if seen[p.Name] {
				return false
			}
			seen[p.Name] = true
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

// TestAnyUnownedPathAlwaysFailsClosedConservatively is the explicit
// "run_everything root rules" property: with no projects and no root
// patterns registered, conservative strictness (the default) must force
// RunEverything=true for any non-empty path set, and never for an empty one.
func TestAnyUnownedPathAlwaysFailsClosedConservatively(t *testing.T) {
	f := func(paths []string) bool {
		r := Compute(nil, paths, Options{})
		if len(dedupSorted(paths)) == 0 {
			return !r.RunEverything
		}
		return r.RunEverything
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

func projectNames(r Result) []string {
	out := make([]string, len(r.Projects))
	for i, p := range r.Projects {
		out[i] = p.Name
	}
	return out
}

type reasons map[string]bool

func (r reasons) has(code string) bool { return r[code] }

func reasonSet(r Result) reasons {
	out := reasons{}
	for _, c := range r.ReasonCodes {
		out[c] = true
	}
	return out
}

// Root invalidation must win over ownership (§14.5.2): a root glue
// project at path "" owns every root file by longest-prefix, which
// previously made root-invalidation patterns unreachable for exactly the
// files they exist for (go.mod). Found by a live proof, not a test.
func TestRootInvalidationBeatsOwnership(t *testing.T) {
	projects := []ProjectInfo{{Name: "repo", Path: ""}, {Name: "svc", Path: "svc"}}
	res := Compute(projects, []string{"go.mod"}, Options{RootInvalidationPatterns: []string{"go.mod"}})
	if !res.RunEverything {
		t.Fatalf("go.mod matching a root-invalidation pattern must escalate even though the root project owns it, got %+v", res)
	}
	// A non-matching root file stays a plain direct hit on the owner.
	res = Compute(projects, []string{"README.md"}, Options{RootInvalidationPatterns: []string{"go.mod"}})
	if res.RunEverything || len(res.Projects) != 1 || res.Projects[0].Name != "repo" {
		t.Fatalf("README must stay a direct repo hit, got %+v", res)
	}
}
