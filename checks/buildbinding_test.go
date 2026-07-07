package checks

import "testing"

func TestDefaultBuildCheckSetPolicy(t *testing.T) {
	p := DefaultBuildCheckSetPolicy()
	if p.Scope != "affected" {
		t.Fatalf("expected the default build check-set to scope to affected, got %q", p.Scope)
	}
	if got := ExpandCheckName(p.Pattern, "checkout-api"); got != "bazel_test:checkout-api" {
		t.Fatalf("expected pattern to expand to bazel_test:checkout-api, got %q", got)
	}
}

func TestRequireBuildBindingBlockersEmptyForNilOrEmpty(t *testing.T) {
	if b := RequireBuildBindingBlockers(nil); b != nil {
		t.Fatalf("expected nil blockers for nil input, got %v", b)
	}
	if b := RequireBuildBindingBlockers([]string{}); b != nil {
		t.Fatalf("expected nil blockers for empty input, got %v", b)
	}
}

func TestRequireBuildBindingBlockersOnePerProject(t *testing.T) {
	b := RequireBuildBindingBlockers([]string{"legacy-lib", "billing-lib"})
	if len(b) != 2 {
		t.Fatalf("expected one blocker per unbound project, got %v", b)
	}
}
