package buildadapter

import (
	"context"
	"errors"
	"testing"
)

type fakeEngine struct {
	result QueryResult
	err    error
}

func (f fakeEngine) Query(ctx context.Context, req QueryRequest) (QueryResult, error) {
	return f.result, f.err
}

func TestRefineFailsClosedOnEngineError(t *testing.T) {
	engine := fakeEngine{err: errors.New("bazel query: exit status 1: ERROR: boom")}
	ref := Refine(context.Background(), engine, "bazel", QueryRequest{}, nil)

	if !ref.RunEverything {
		t.Fatalf("expected RunEverything=true on any engine error, got %+v", ref)
	}
	if ref.FailureReason == "" {
		t.Fatalf("expected a failure reason to be recorded")
	}
	if ref.Targets != nil {
		t.Fatalf("expected no targets on a failed query, got %v", ref.Targets)
	}
}

func TestRefineSuccessDefaultsUniverse(t *testing.T) {
	engine := fakeEngine{result: QueryResult{Targets: []string{"//commerce/checkout:go_default_test"}}}
	ref := Refine(context.Background(), engine, "bazel", QueryRequest{}, nil)

	if ref.RunEverything {
		t.Fatalf("did not expect RunEverything on a clean query")
	}
	if ref.UniversePattern != "//..." {
		t.Fatalf("expected the default universe //..., got %q", ref.UniversePattern)
	}
	if len(ref.Targets) != 1 || ref.Targets[0] != "//commerce/checkout:go_default_test" {
		t.Fatalf("expected the engine's targets to pass through, got %v", ref.Targets)
	}
}

func TestRefineHonorsExplicitUniverse(t *testing.T) {
	engine := fakeEngine{result: QueryResult{}}
	ref := Refine(context.Background(), engine, "bazel", QueryRequest{UniversePattern: "//commerce/..."}, nil)
	if ref.UniversePattern != "//commerce/..." {
		t.Fatalf("expected the explicit universe to be preserved, got %q", ref.UniversePattern)
	}
}

func TestRefineMapsTargetsToProjectsByLongestPrefix(t *testing.T) {
	engine := fakeEngine{result: QueryResult{Targets: []string{
		"//commerce/checkout:go_default_test",
		"//commerce/checkout/internal:helpers_test",
		"//libs/billing:go_default_test",
		"//:root_tool", // no owning project
	}}}
	projects := []ProjectInfo{
		{Name: "checkout-api", Path: "commerce/checkout"},
		{Name: "billing-lib", Path: "libs/billing"},
	}

	ref := Refine(context.Background(), engine, "bazel", QueryRequest{}, projects)

	want := map[string]string{
		"//commerce/checkout:go_default_test":       "checkout-api",
		"//commerce/checkout/internal:helpers_test": "checkout-api",
		"//libs/billing:go_default_test":            "billing-lib",
	}
	for target, wantProject := range want {
		if got := ref.TargetProjects[target]; got != wantProject {
			t.Fatalf("target %s: expected project %q, got %q (all: %+v)", target, wantProject, got, ref.TargetProjects)
		}
	}
	if _, ok := ref.TargetProjects["//:root_tool"]; ok {
		t.Fatalf("expected an unowned target to be absent from the mapping, got %+v", ref.TargetProjects)
	}
}

func TestRefineDoesNotConfusePathPrefixSiblings(t *testing.T) {
	// "commerce/checkout-v2" must never match project path "commerce/checkout" -
	// a plain strings.HasPrefix without a path-component boundary would.
	engine := fakeEngine{result: QueryResult{Targets: []string{"//commerce/checkout-v2:test"}}}
	projects := []ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}

	ref := Refine(context.Background(), engine, "bazel", QueryRequest{}, projects)
	if _, ok := ref.TargetProjects["//commerce/checkout-v2:test"]; ok {
		t.Fatalf("expected commerce/checkout-v2 to NOT match project path commerce/checkout, got %+v", ref.TargetProjects)
	}
}
