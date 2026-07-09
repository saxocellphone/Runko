package land

import (
	"testing"

	"github.com/saxocellphone/runko/platform/affected"
)

func projRefs(names ...string) []affected.ProjectRef {
	out := make([]affected.ProjectRef, len(names))
	for i, n := range names {
		out[i] = affected.ProjectRef{Name: n}
	}
	return out
}

func TestNeedsRevalidationNoIntersection(t *testing.T) {
	change := affected.Result{Projects: projRefs("checkout-api")}
	trunk := affected.Result{Projects: projRefs("billing-lib")}
	if NeedsRevalidation(RevalidationAffectedIntersection, change, trunk) {
		t.Fatalf("expected no revalidation needed when project sets don't intersect")
	}
}

func TestNeedsRevalidationIntersects(t *testing.T) {
	change := affected.Result{Projects: projRefs("checkout-api", "billing-lib")}
	trunk := affected.Result{Projects: projRefs("billing-lib")}
	if !NeedsRevalidation(RevalidationAffectedIntersection, change, trunk) {
		t.Fatalf("expected revalidation needed when project sets intersect")
	}
}

func TestNeedsRevalidationAlwaysForcesTrue(t *testing.T) {
	change := affected.Result{Projects: projRefs("checkout-api")}
	if !NeedsRevalidation(RevalidationAlways, change, affected.Result{}) {
		t.Fatalf("expected RevalidationAlways to force revalidation even with no trunk delta")
	}
}

func TestNeedsRevalidationEmptyScopeDefaultsToIntersection(t *testing.T) {
	change := affected.Result{Projects: projRefs("checkout-api")}
	trunk := affected.Result{Projects: projRefs("billing-lib")}
	if NeedsRevalidation("", change, trunk) {
		t.Fatalf("expected the zero-value scope to behave like affected-intersection")
	}
}

func TestNeedsRevalidationChangeRunEverything(t *testing.T) {
	change := affected.Result{RunEverything: true, Projects: projRefs("checkout-api")}
	trunk := affected.Result{Projects: projRefs("billing-lib")} // no intersection
	if !NeedsRevalidation(RevalidationAffectedIntersection, change, trunk) {
		t.Fatalf("expected the change's own RunEverything to force revalidation despite no intersection")
	}
}

func TestNeedsRevalidationTrunkRunEverything(t *testing.T) {
	change := affected.Result{Projects: projRefs("checkout-api")}
	trunk := affected.Result{RunEverything: true, Projects: projRefs("billing-lib")} // no intersection
	if !NeedsRevalidation(RevalidationAffectedIntersection, change, trunk) {
		t.Fatalf("expected the trunk delta's own RunEverything to force revalidation despite no intersection")
	}
}
