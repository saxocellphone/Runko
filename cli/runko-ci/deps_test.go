package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

// seedDepsRepo writes the shape this verb exists for: a CLI project whose
// build file references a package owned by another project, with the
// manifest edge left to the caller.
func seedDepsRepo(t *testing.T, cliManifest string) string {
	t.Helper()
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", "schema: project/v1\nname: repo\ntype: other\n")
	repo.WriteFile("cli/PROJECT.yaml", cliManifest)
	repo.WriteFile("cli/BUILD.bazel", "go_binary(\n    name = \"cli\",\n    visibility = [\"//visibility:public\"],\n    deps = [\"//templates/ci\"],\n)\n")
	repo.WriteFile("templates/PROJECT.yaml", "schema: project/v1\nname: templates\ntype: library\n")
	repo.WriteFile("templates/ci/BUILD.bazel", "go_library(name = \"ci\", visibility = [\"//visibility:public\"])\n")
	repo.Commit("seed")
	return repo.Dir
}

// TestDepsRefusesTheUndeclaredEdge: the edge exists only in the build
// graph, so affected computation and the workspace cone are both blind to
// it - exactly the 2026-07-24 defect, and the verb must fail on it.
func TestDepsRefusesTheUndeclaredEdge(t *testing.T) {
	dir := seedDepsRepo(t, "schema: project/v1\nname: cli\ntype: app\n")

	res, err := Deps(dir)
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if len(res.Undeclared) != 1 {
		t.Fatalf("want 1 undeclared edge, got %+v", res.Edges)
	}
	if res.Undeclared[0].Code != "undeclared_project_dependency" {
		t.Fatalf("unexpected violation: %+v", res.Undeclared[0])
	}

	err = execCLI("deps", "--repo", dir)
	if err == nil {
		t.Fatal("the verb must exit non-zero on an undeclared edge")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a structured error, got %T: %v", err, err)
	}
	if !strings.Contains(ce.Suggestion, "test_dependencies") {
		t.Errorf("the suggestion should offer both edge kinds: %q", ce.Suggestion)
	}
}

// TestDepsAcceptsEitherDeclaration: a dependency or a test_dependency both
// close the hole - the verb polices declaredness, never which kind.
func TestDepsAcceptsEitherDeclaration(t *testing.T) {
	for _, field := range []string{"dependencies", "test_dependencies"} {
		dir := seedDepsRepo(t, "schema: project/v1\nname: cli\ntype: app\n"+field+":\n  - templates\n")
		res, err := Deps(dir)
		if err != nil {
			t.Fatalf("%s: Deps: %v", field, err)
		}
		if len(res.Undeclared) != 0 {
			t.Fatalf("%s should sanction the edge, got %+v", field, res.Undeclared)
		}
		if len(res.Edges) != 1 || !res.Edges[0].Declared {
			t.Fatalf("%s: want one declared edge, got %+v", field, res.Edges)
		}
		if err := execCLI("deps", "--repo", dir, "--json"); err != nil {
			t.Fatalf("%s: verb must succeed: %v", field, err)
		}
	}
}
