package index

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
)

// TestChecksForIsTheSharedRunWhenRule pins §14.5.9's one decision point:
// the merge gate and the CI executor both resolve through ChecksFor, and
// this table is the semantics they share.
func TestChecksForIsTheSharedRunWhenRule(t *testing.T) {
	p := IndexedProject{
		Name: "runkod",
		Checks: []CheckDef{
			{Name: "runkod-test", Command: "bazel test //runkod/...", RunWhen: RunWhenAffected},
			{Name: "runkod-race", Command: "bazel test --race //runkod/...", RunWhen: RunWhenDirect},
		},
	}

	names := func(defs []CheckDef) []string {
		var out []string
		for _, d := range defs {
			out = append(out, d.Name)
		}
		return out
	}

	direct := names(ChecksFor(p, true))
	if len(direct) != 2 {
		t.Fatalf("a directly-touched project owes both classes, got %v", direct)
	}
	closure := names(ChecksFor(p, false))
	if len(closure) != 1 || closure[0] != "runkod-test" {
		t.Fatalf("a closure-affected dependent owes only affected-class checks, got %v", closure)
	}
}

// TestScanNormalizesRunWhen: unknown/absent run_when reads as the affected
// default - running a check too often is safe, silently dropping one is
// not (the scanner feeds the merge gate; the schema polices authoring).
func TestScanNormalizesRunWhen(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml",
		"schema: project/v1\nname: svc\ntype: service\nci:\n  checks:\n    - name: unit\n      command: make test\n      run_when: direct\n    - name: integration\n      command: make itest\n    - name: weird\n      command: make weird\n      run_when: sometimes\n")
	head := repo.Commit("svc")
	projects, err := Scan(gitstore.New(repo.Dir), core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected one project, got %d", len(projects))
	}
	byName := map[string]string{}
	for _, c := range projects[0].Checks {
		byName[c.Name] = c.RunWhen
	}
	if byName["unit"] != RunWhenDirect || byName["integration"] != RunWhenAffected || byName["weird"] != RunWhenAffected {
		t.Fatalf("run_when normalization wrong: %v", byName)
	}
}

// TestPostLandClassIsInvisibleToTheGate pins the post_land contract
// (2026-07-24): ChecksFor - the one rule the merge gate and pre-land
// executor share - never returns a post_land check, not even for a
// directly-touched project (run_everything's fail-closed posture must not
// leak a check the gate cannot credit); PostLandChecks is its only
// selection path.
func TestPostLandClassIsInvisibleToTheGate(t *testing.T) {
	p := IndexedProject{
		Name: "repo",
		Checks: []CheckDef{
			{Name: "docs-check", Command: "make check-docs", RunWhen: RunWhenAffected},
			{Name: "compose-smoke", Command: "make check-compose", RunWhen: RunWhenPostLand},
		},
	}
	for _, direct := range []bool{true, false} {
		for _, c := range ChecksFor(p, direct) {
			if c.Name == "compose-smoke" {
				t.Fatalf("ChecksFor(direct=%v) returned the post_land check - the gate would require a check pre-land CI never runs", direct)
			}
		}
	}
	post := PostLandChecks(p)
	if len(post) != 1 || post[0].Name != "compose-smoke" {
		t.Fatalf("PostLandChecks: want exactly the post_land check, got %v", post)
	}
}

// TestScanPostLandRunWhen: scan recognizes run_when: post_land (it must NOT
// normalize to affected - that would hand it to the gate) and keeps
// post_land names out of RequiredChecks, the names-only gate view.
func TestScanPostLandRunWhen(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml",
		"schema: project/v1\nname: repo\ntype: other\nci:\n  checks:\n    - name: docs-check\n      command: make check-docs\n    - name: compose-smoke\n      command: make check-compose\n      run_when: post_land\n")
	head := repo.Commit("root with post_land check")
	projects, err := Scan(gitstore.New(repo.Dir), core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected one project, got %d", len(projects))
	}
	p := projects[0]
	byName := map[string]string{}
	for _, c := range p.Checks {
		byName[c.Name] = c.RunWhen
	}
	if byName["compose-smoke"] != RunWhenPostLand {
		t.Fatalf("run_when: post_land must survive scan, got %q", byName["compose-smoke"])
	}
	if len(p.RequiredChecks) != 1 || p.RequiredChecks[0] != "docs-check" {
		t.Fatalf("RequiredChecks must exclude post_land names, got %v", p.RequiredChecks)
	}
}
