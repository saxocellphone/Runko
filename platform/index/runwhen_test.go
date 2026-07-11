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
