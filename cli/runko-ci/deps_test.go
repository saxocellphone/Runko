package main

import (
	"errors"
	"os"
	"path/filepath"
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

// TestDepsSeesUnmaterializedFiles: a sparse workspace is the normal way to
// work in a Runko monorepo, and a file outside the cone is tracked-but-
// absent from disk. Auditing only what happens to be materialized turns
// "0 undeclared" into a statement about the cone rather than the repo -
// the guard would have been blind to exactly the projects a narrow
// workspace never checks out.
func TestDepsSeesUnmaterializedFiles(t *testing.T) {
	dir := seedDepsRepo(t, "schema: project/v1\nname: cli\ntype: app\n")

	// Stand in for a sparse cone: git still tracks the file, disk does not
	// hold it.
	if err := os.Remove(filepath.Join(dir, "cli", "BUILD.bazel")); err != nil {
		t.Fatalf("unmaterialize: %v", err)
	}
	res, err := Deps(dir)
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if res.FromIndex != 1 {
		t.Fatalf("want 1 file read from HEAD, got %d", res.FromIndex)
	}
	if len(res.Undeclared) != 1 {
		t.Fatalf("the undeclared edge must survive unmaterialization, got %+v", res.Edges)
	}

	// A manifest outside the cone must still define its project, or every
	// label into it would resolve to the wrong owner (or to none).
	if err := os.Remove(filepath.Join(dir, "templates", "PROJECT.yaml")); err != nil {
		t.Fatalf("unmaterialize manifest: %v", err)
	}
	res, err = Deps(dir)
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if res.Projects != 3 || res.FromIndex != 2 {
		t.Fatalf("want 3 projects (2 from HEAD), got projects=%d from_index=%d", res.Projects, res.FromIndex)
	}
	if len(res.Undeclared) != 1 || res.Undeclared[0].Path != "cli/BUILD.bazel" {
		t.Fatalf("undeclared edge lost when the target's manifest was unmaterialized: %+v", res.Undeclared)
	}
}
