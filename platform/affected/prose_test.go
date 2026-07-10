package affected

import (
	"reflect"
	"sort"
	"testing"
)

// §14.5.7: a prose path inside a project's folder re-attributes to the
// repo-root project for check derivation - it must NOT affect its
// folder-owner, and therefore must NOT drag the owner's dependents in
// through the closure. Tests for a README was the failure mode.
func TestProsePathAttributesToRootNotFolderOwner(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "repo", Path: ""},
		{Name: "platform", Path: "platform"},
		{Name: "runkod", Path: "runkod", DeclaredDependencies: []string{"platform"}},
	}
	r := Compute(projects, []string{"platform/README.md"}, Options{ProsePatterns: []string{"**/*.md"}})
	if r.RunEverything {
		t.Fatalf("prose must not escalate: %+v", r)
	}
	if got := projectNames(r); !reflect.DeepEqual(got, []string{"repo"}) {
		t.Fatalf("want [repo], got %v", got)
	}
}

// Ordered first-match-wins with ! exceptions: a load-bearing doc excepted
// from a broad "**/*.md" stays with its folder owner (and its dependents).
func TestProseNegationKeepsLoadBearingDocsWithTheirOwner(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "repo", Path: ""},
		{Name: "docs", Path: "docs"},
	}
	opts := Options{ProsePatterns: []string{"!docs/cli-contract.md", "**/*.md"}}

	r := Compute(projects, []string{"docs/cli-contract.md"}, opts)
	if got := projectNames(r); !reflect.DeepEqual(got, []string{"docs"}) {
		t.Fatalf("excepted path: want [docs], got %v", got)
	}

	r = Compute(projects, []string{"docs/design.md"}, opts)
	if got := projectNames(r); !reflect.DeepEqual(got, []string{"repo"}) {
		t.Fatalf("prose path: want [repo], got %v", got)
	}
}

// Root invalidation is checked before prose: a collision escalates,
// never de-escalates.
func TestRootInvalidationBeatsProse(t *testing.T) {
	projects := []ProjectInfo{{Name: "repo", Path: ""}}
	r := Compute(projects, []string{"docs/x.md"}, Options{
		RootInvalidationPatterns: []string{"docs/x.md"},
		ProsePatterns:            []string{"**/*.md"},
	})
	if !r.RunEverything {
		t.Fatalf("invalidation must win over prose: %+v", r)
	}
}

// Without a root project there is nothing to de-escalate to: an owned
// prose path keeps its folder owner, an unowned one keeps failing closed.
func TestProseWithoutRootProjectFallsThrough(t *testing.T) {
	projects := []ProjectInfo{{Name: "platform", Path: "platform"}}
	opts := Options{ProsePatterns: []string{"**/*.md"}}

	r := Compute(projects, []string{"platform/README.md"}, opts)
	if got := projectNames(r); !reflect.DeepEqual(got, []string{"platform"}) {
		t.Fatalf("owned prose path without a root project: want [platform], got %v", got)
	}

	r = Compute(projects, []string{"elsewhere/NOTES.md"}, opts)
	if !r.RunEverything {
		t.Fatalf("unowned prose path without a root project must stay fail-closed: %+v", r)
	}
}

// A mixed change: code paths drive their projects (and closure) as usual,
// the prose path adds only the root project.
func TestProseMixedChangeKeepsCodeAttribution(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "repo", Path: ""},
		{Name: "platform", Path: "platform"},
		{Name: "runkod", Path: "runkod", DeclaredDependencies: []string{"platform"}},
	}
	r := Compute(projects, []string{"platform/receive/funnel.go", "README.md"}, Options{ProsePatterns: []string{"**/*.md"}})
	got := projectNames(r)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"platform", "repo", "runkod"}) {
		t.Fatalf("want [platform repo runkod], got %v", got)
	}
}

func TestMatchOrderedFirstMatchWins(t *testing.T) {
	patterns := []string{"!docs/spec/**", "!docs/cli-contract.md", "**/*.md", "LICENSE", "docs/images/**"}
	cases := map[string]bool{
		"README.md":                         true,
		"platform/README.md":                true,
		"docs/design.md":                    true,
		"a/b/c/deep.md":                     true,
		"LICENSE":                           false, // path.Match exact
		"docs/images/change-review.png":     true,
		"docs/spec/project.schema.json":     false,
		"docs/spec/build-adapter/README.md": false, // exception beats **/*.md by order
		"docs/cli-contract.md":              false,
		"platform/receive/funnel.go":        false,
	}
	cases["LICENSE"] = true // exact-name pattern matches the root LICENSE
	for path, want := range cases {
		if got := MatchOrdered(patterns, path); got != want {
			t.Fatalf("MatchOrdered(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestMatchPathAnyDepthForm(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/design.md", true},
		{"**/*.md", "a/b/c.md", true},
		{"**/*.md", "a/b/c.go", false},
		{"**/OWNERS", "platform/receive/OWNERS", true},
		{"**/OWNERS", "OWNERS", true},
		{"**/spec/**", "docs/spec/x/y.json", true},
		{"**/spec/**", "docs/design.md", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.path); got != c.want {
			t.Fatalf("MatchPath(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
