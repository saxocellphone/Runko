package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

// withFakeBazelOnPath puts a scripted "bazel" ahead of PATH for the duration
// of the test, so Affected's --engine=bazel path can be exercised end-to-end
// without a real Bazel install (unavailable in this sandbox - see
// CLAUDE.md).
func withFakeBazelOnPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "bazel")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake bazel: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

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

	result, err := Affected(repo.Dir, base, head, nil, "", "", 0)
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

	result, err := Affected(repo.Dir, base, head, []string{"go.mod"}, "", "", 0)
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

	result, err := Affected(repo.Dir, base, "HEAD", nil, "", "", 0)
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

	_, err := Affected(repo.Dir, "not-a-real-revision", "HEAD", nil, "", "", 0)
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

func TestAffectedWithEngineAddsBuildRefinement(t *testing.T) {
	withFakeBazelOnPath(t, `echo "//commerce/checkout:go_default_test"`)

	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	// The changed file must live in a bazel PACKAGE (BUILD in its dir):
	// the adapter skips non-package paths (migration-findings #6), so an
	// engine fixture without one never reaches the engine at all.
	repo.WriteFile("commerce/checkout/BUILD.bazel", "filegroup(name = \"x\")\n")
	base := repo.Commit("initial")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	head := repo.Commit("add main.go")

	out, err := Affected(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Affected: %v", err)
	}
	if out.BuildRefinement == nil {
		t.Fatalf("expected a BuildRefinement when --engine is set")
	}
	if out.BuildRefinement.RunEverything {
		t.Fatalf("did not expect the engine to fail, got %+v", out.BuildRefinement)
	}
	if len(out.BuildRefinement.Targets) != 1 {
		t.Fatalf("expected the fake engine's target to pass through, got %+v", out.BuildRefinement.Targets)
	}
	if out.Result.RunEverything {
		t.Fatalf("a successful engine refinement must not force RunEverything on the floor result")
	}
}

// TestAffectedEngineFailureEscalatesRunEverything is the fail-closed
// contract from docs/spec/build-adapter/README.md §1: an engine failure
// must escalate the WHOLE AffectedOutput, not just BuildRefinement's own
// field - a caller reading only the top-level RunEverything (as every
// existing caller does today) must not be fooled into thinking a narrow,
// project-scoped result is safe to trust.
func TestAffectedEngineFailureEscalatesRunEverything(t *testing.T) {
	withFakeBazelOnPath(t, `echo "boom" >&2; exit 1`)

	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	// The changed file must live in a bazel PACKAGE (BUILD in its dir):
	// the adapter skips non-package paths (migration-findings #6), so an
	// engine fixture without one never reaches the engine at all.
	repo.WriteFile("commerce/checkout/BUILD.bazel", "filegroup(name = \"x\")\n")
	base := repo.Commit("initial")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	head := repo.Commit("add main.go")

	out, err := Affected(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Affected: %v", err)
	}
	if !out.BuildRefinement.RunEverything {
		t.Fatalf("expected the engine failure to be reflected in BuildRefinement")
	}
	if !out.Result.RunEverything {
		t.Fatalf("expected the engine failure to escalate the top-level RunEverything too, got %+v", out.Result)
	}
}

func TestAffectedUnknownEngineReturnsStructuredError(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service"))
	repo.Commit("initial")

	_, err := Affected(repo.Dir, "HEAD", "HEAD", nil, "makefile", "", 0)
	if err == nil {
		t.Fatalf("expected an error for an unknown engine")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Field != "--engine" {
		t.Fatalf("expected the error to identify --engine, got %+v", ce)
	}
}
