package land

import "testing"

func TestNeedsRevalidationNoIntersection(t *testing.T) {
	if NeedsRevalidation(RevalidationAffectedIntersection, []string{"checkout-api"}, []string{"billing-lib"}) {
		t.Fatalf("expected no revalidation needed when project sets don't intersect")
	}
}

func TestNeedsRevalidationIntersects(t *testing.T) {
	if !NeedsRevalidation(RevalidationAffectedIntersection, []string{"checkout-api", "billing-lib"}, []string{"billing-lib"}) {
		t.Fatalf("expected revalidation needed when project sets intersect")
	}
}

func TestNeedsRevalidationAlwaysForcesTrue(t *testing.T) {
	if !NeedsRevalidation(RevalidationAlways, []string{"checkout-api"}, nil) {
		t.Fatalf("expected RevalidationAlways to force revalidation even with no trunk delta")
	}
}

func TestNeedsRevalidationEmptyScopeDefaultsToIntersection(t *testing.T) {
	if NeedsRevalidation("", []string{"checkout-api"}, []string{"billing-lib"}) {
		t.Fatalf("expected the zero-value scope to behave like affected-intersection")
	}
}
