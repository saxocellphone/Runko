package contract

import (
	"strings"
	"testing"
)

func labelProjects() []Project {
	return []Project{
		{Name: "repo", Path: ""},
		{Name: "cli", Path: "cli", Dependencies: []string{"platform"}, TestDependencies: []string{"daemon"}},
		{Name: "platform", Path: "platform", Consumes: []string{"docs"}},
		{Name: "daemon", Path: "daemon"},
		{Name: "docs", Path: "docs"},
		{Name: "templates", Path: "templates"},
	}
}

// TestBuildLabelsAcceptEveryDeclaredEdgeKind: dependencies, test_dependencies
// and consumes all sanction a reference - the rule is "declared", not
// "declared as a dependency".
func TestBuildLabelsAcceptEveryDeclaredEdgeKind(t *testing.T) {
	files := []File{
		{Path: "cli/runko/BUILD.bazel", Content: []byte(`go_library(deps = ["//platform/index"])
go_test(deps = ["//daemon"], data = ["//daemon:runkod"])`)},
		{Path: "platform/checks/BUILD.bazel", Content: []byte(`go_test(data = ["//docs:contracts"])`)},
	}
	if v := CheckBuildLabels(labelProjects(), files); len(v) != 0 {
		t.Fatalf("declared edges must not violate: %+v", v)
	}
}

// TestBuildLabelsCatchTheUndeclaredEdge is the 2026-07-24 defect in
// miniature: cli embeds a package no manifest edge mentions, so its
// workspace cone never materializes it.
func TestBuildLabelsCatchTheUndeclaredEdge(t *testing.T) {
	files := []File{
		{Path: "cli/runko/BUILD.bazel", Content: []byte(`go_library(deps = ["//templates/ci"])`)},
	}
	v := CheckBuildLabels(labelProjects(), files)
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %+v", v)
	}
	if v[0].Code != "undeclared_project_dependency" || v[0].Path != "cli/runko/BUILD.bazel" {
		t.Fatalf("unexpected violation: %+v", v[0])
	}
	for _, want := range []string{"templates", "//templates/ci", "cli"} {
		if !strings.Contains(v[0].Message+v[0].Suggestion, want) {
			t.Errorf("violation should name %q: %+v", want, v[0])
		}
	}
}

// TestBuildLabelsIgnoreNonEdges: pseudo-packages, external repos, relative
// labels and same-project references are not cross-project edges. A BUILD
// file's own visibility declaration must never read as a dependency.
func TestBuildLabelsIgnoreNonEdges(t *testing.T) {
	files := []File{
		{Path: "cli/runko/BUILD.bazel", Content: []byte(`go_library(
    visibility = ["//visibility:public"],
    deps = ["//cli/runko/sub", ":helper", "@rules_go//go/tools", "//conditions:default"],
)`)},
		// Not a build file at all - the walker must skip it.
		{Path: "cli/runko/main.go", Content: []byte(`// "//templates/ci"`)},
	}
	if v := CheckBuildLabels(labelProjects(), files); len(v) != 0 {
		t.Fatalf("non-edges must not violate: %+v", v)
	}
}

// TestBuildLabelEdgesReportBothSides: the edge list is the readable graph,
// so it carries covered and uncovered references alike, deduped per label.
func TestBuildLabelEdgesReportBothSides(t *testing.T) {
	files := []File{
		{Path: "cli/runko/BUILD.bazel", Content: []byte(`go_library(deps = ["//platform/index", "//platform/land"])
go_test(deps = ["//templates/ci", "//templates/ci"])`)},
	}
	edges := BuildLabelEdges(labelProjects(), files)
	if len(edges) != 3 {
		t.Fatalf("want 3 deduped edges, got %+v", edges)
	}
	var undeclared int
	for _, e := range edges {
		if !e.Declared {
			undeclared++
			if e.To != "templates" {
				t.Errorf("only the templates edge is undeclared: %+v", e)
			}
		}
	}
	if undeclared != 1 {
		t.Fatalf("want 1 undeclared edge, got %d: %+v", undeclared, edges)
	}
}

// TestBuildLabelsQuoteStylesAndComments: Starlark takes either quote
// style, so a single-quoted label must be seen (missing one is a silent
// false negative - the direction that lets an unbuildable workspace ship);
// a commented-out dep must NOT be, or the rule demands a manifest edge for
// a dependency that does not exist.
func TestBuildLabelsQuoteStylesAndComments(t *testing.T) {
	single := []File{{Path: "cli/BUILD.bazel", Content: []byte(`go_library(deps = ['//templates/ci'])`)}}
	if v := CheckBuildLabels(labelProjects(), single); len(v) != 1 {
		t.Fatalf("single-quoted label must be scanned, got %+v", v)
	}

	commented := []File{{Path: "cli/BUILD.bazel", Content: []byte("# deps = [\"//templates/ci\"],\ngo_library(name = \"runko\")")}}
	if v := CheckBuildLabels(labelProjects(), commented); len(v) != 0 {
		t.Fatalf("a commented-out dep is not an edge, got %+v", v)
	}

	// A '#' inside a string is content: the label on the same line, and on
	// the next, must both survive.
	inString := []File{{Path: "cli/BUILD.bazel", Content: []byte(`genrule(cmd = "sed 's/#x/y/' $<", tools = ["//templates/ci"])
go_library(deps = ["//daemon"])`)}}
	edges := BuildLabelEdges(labelProjects(), inString)
	if len(edges) != 2 {
		t.Fatalf("want both edges past the in-string '#', got %+v", edges)
	}
}
