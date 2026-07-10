package project

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// §14.5.8: root_invalidation entries parse from both YAML forms - bare
// pattern strings (blunt, the fail-closed default) and
// {pattern, refinable} objects marking graph-visible patterns.
func TestRootInvalidationEntryTwoForms(t *testing.T) {
	src := `schema: project/v1
name: repo
type: other
root_invalidation:
  - "!.github/workflows/ci.yml"
  - .github/**
  - pattern: MODULE.bazel
    refinable: true
  - pattern: Makefile
`
	var m Manifest
	if err := yaml.Unmarshal([]byte(src), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []RootInvalidationEntry{
		{Pattern: "!.github/workflows/ci.yml"},
		{Pattern: ".github/**"},
		{Pattern: "MODULE.bazel", Refinable: true},
		{Pattern: "Makefile"},
	}
	if len(m.RootInvalidation) != len(want) {
		t.Fatalf("want %d entries, got %+v", len(want), m.RootInvalidation)
	}
	for i, w := range want {
		if m.RootInvalidation[i] != w {
			t.Fatalf("entry %d: want %+v, got %+v", i, w, m.RootInvalidation[i])
		}
	}
}

// A "!" exception removes escalation - there is nothing for a graph diff
// to refine, so marking one refinable is a manifest error, not a silent
// no-op someone later trips over.
func TestRootInvalidationEntryRejectsRefinableException(t *testing.T) {
	src := "root_invalidation:\n  - pattern: \"!.github/workflows/ci.yml\"\n    refinable: true\n"
	var m Manifest
	err := yaml.Unmarshal([]byte(src), &m)
	if err == nil || !strings.Contains(err.Error(), "cannot be refinable") {
		t.Fatalf("expected a refinable-exception error, got %v", err)
	}
}

func TestRootInvalidationEntryRejectsEmptyPattern(t *testing.T) {
	src := "root_invalidation:\n  - refinable: true\n"
	var m Manifest
	err := yaml.Unmarshal([]byte(src), &m)
	if err == nil || !strings.Contains(err.Error(), "non-empty pattern") {
		t.Fatalf("expected an empty-pattern error, got %v", err)
	}
}

// Marshal round-trips the compact form: blunt entries stay bare strings,
// refinable ones keep the object form.
func TestRootInvalidationEntryMarshalCompact(t *testing.T) {
	m := Manifest{
		Schema: "project/v1", Name: "repo", Type: "other",
		RootInvalidation: []RootInvalidationEntry{
			{Pattern: "go.mod", Refinable: true},
			{Pattern: "Makefile"},
		},
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "- Makefile") {
		t.Fatalf("blunt entry should marshal as a bare string, got:\n%s", s)
	}
	if !strings.Contains(s, "pattern: go.mod") || !strings.Contains(s, "refinable: true") {
		t.Fatalf("refinable entry should marshal as an object, got:\n%s", s)
	}

	var back Manifest
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(back.RootInvalidation) != 2 || back.RootInvalidation[0] != m.RootInvalidation[0] || back.RootInvalidation[1] != m.RootInvalidation[1] {
		t.Fatalf("round trip mismatch: %+v", back.RootInvalidation)
	}
}
