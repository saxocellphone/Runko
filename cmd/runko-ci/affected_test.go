package main

import (
	"errors"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

func manifest(name, projType string) string {
	return "schema: project/v1\nname: " + name + "\ntype: " + projType + "\n"
}

func TestAffectedComputesFromLocalRepo(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.WriteFile("libs/billing/PROJECT.yaml", manifest("billing-lib", "library"))
	repo.WriteFile("libs/billing/lib.go", "package billing\n")
	base := repo.Commit("two projects")

	repo.WriteFile("commerce/checkout/main.go", "package main\n// changed\n")
	head := repo.Commit("touch checkout only")

	result, err := Affected(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Affected: %v", err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "checkout-api" {
		t.Fatalf("expected only checkout-api affected, got %+v", result.Projects)
	}
	if result.RunEverything {
		t.Fatalf("did not expect RunEverything for a project-scoped change")
	}
}

func TestAffectedHonorsRootInvalidationPatterns(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	repo.WriteFile("go.mod", "module example\n")
	base := repo.Commit("initial")

	repo.WriteFile("go.mod", "module example\nrequire foo v1.0.0\n")
	head := repo.Commit("bump a dependency")

	result, err := Affected(repo.Dir, base, head, []string{"go.mod"})
	if err != nil {
		t.Fatalf("Affected: %v", err)
	}
	if !result.RunEverything {
		t.Fatalf("expected go.mod to trigger RunEverything, got %+v", result)
	}
}

func TestAffectedDefaultHeadIsHEAD(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	base := repo.Commit("initial")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go")

	result, err := Affected(repo.Dir, base, "HEAD", nil)
	if err != nil {
		t.Fatalf("Affected: %v", err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "checkout-api" {
		t.Fatalf("expected checkout-api affected via HEAD, got %+v", result.Projects)
	}
}

// TestAffectedBadBaseReturnsStructuredError exercises the resolve-or-explain
// requirement (§6.5, §28.3 stage 9a item 3): a typo'd --base must surface as
// a clierr.Error with guidance, not git's raw "ambiguous argument ...
// unknown revision" exit-128 text.
func TestAffectedBadBaseReturnsStructuredError(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	repo.Commit("initial")

	_, err := Affected(repo.Dir, "not-a-real-revision", "HEAD", nil)
	if err == nil {
		t.Fatalf("expected an error for an unresolvable --base")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Field != "--base" {
		t.Fatalf("expected the error to identify --base as the culprit, got %+v", ce)
	}
}
