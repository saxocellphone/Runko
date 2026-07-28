package index

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
)

// TestAutoApproveInheritance is the whole contract of the zone rule as one
// table: the nearest DECLARING ancestor decides, so a root declaration covers
// the repo, a nested false carves back out of it, and a nested true opens one
// subtree inside an otherwise governed repo.
func TestAutoApproveInheritance(t *testing.T) {
	projects := []IndexedProject{
		{Name: "root", Path: "", AutoApprove: boolPtr(true)},
		{Name: "governed", Path: "billing", AutoApprove: boolPtr(false)},
		{Name: "silent", Path: "billing/api"}, // undeclared: inherits billing's false
		{Name: "web", Path: "web"},            // undeclared: inherits root's true
	}
	cases := []struct {
		path string
		want bool
	}{
		{"README.md", true},               // root project, declared true
		{"web/src/App.tsx", true},         // undeclared project inherits the root
		{"billing/ledger.go", false},      // nested false beats the root's true
		{"billing/api/handler.go", false}, // inherited through the undeclared child
		{"billing", false},                // the project dir itself
	}
	for _, c := range cases {
		if got := AutoApproved(projects, c.path); got != c.want {
			t.Errorf("AutoApproved(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// An undeclared tree is a governed tree: every repo that predates the field
// keeps the approval gate it had, with no migration and no opt-out.
func TestAutoApproveUndeclaredIsGoverned(t *testing.T) {
	projects := []IndexedProject{{Name: "root", Path: ""}, {Name: "svc", Path: "svc"}}
	if AutoApproved(projects, "svc/main.go") {
		t.Fatal("an undeclared tree must not auto-approve")
	}
	if AutoApproved(nil, "anything") {
		t.Fatal("an unindexed tree must not auto-approve")
	}
}

// The change-wide question is ALL-or-nothing: one governed path in the diff
// keeps the human in the loop, and an empty diff decides nothing (fail closed).
func TestAutoApprovedAllNeedsEveryPath(t *testing.T) {
	projects := []IndexedProject{
		{Name: "sandbox", Path: "sandbox", AutoApprove: boolPtr(true)},
		{Name: "root", Path: ""},
	}
	if !AutoApprovedAll(projects, []string{"sandbox/a.go", "sandbox/b/c.go"}) {
		t.Fatal("a diff wholly inside the zone must be auto-approved")
	}
	if AutoApprovedAll(projects, []string{"sandbox/a.go", "platform/land/land.go"}) {
		t.Fatal("one governed path must keep the whole change governed")
	}
	if AutoApprovedAll(projects, nil) {
		t.Fatal("an empty path set must fail closed")
	}
}

// Scan carries the manifest's tri-state through unflattened: false must stay
// distinguishable from unset, or a carve-out reads as an inheriting child.
func TestScanCarriesAutoApproveTriState(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", manifest("root", "library", "auto_approve: true\n"))
	repo.WriteFile("billing/PROJECT.yaml", manifest("billing", "service", "auto_approve: false\n"))
	repo.WriteFile("web/PROJECT.yaml", manifest("web", "service", ""))
	head := repo.Commit("three postures")

	projects, err := Scan(gitstore.New(repo.Dir), core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byName := map[string]IndexedProject{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if got := byName["root"].AutoApprove; got == nil || !*got {
		t.Fatalf("root: want declared true, got %v", got)
	}
	if got := byName["billing"].AutoApprove; got == nil || *got {
		t.Fatalf("billing: want declared false, got %v", got)
	}
	if got := byName["web"].AutoApprove; got != nil {
		t.Fatalf("web: want undeclared, got %v", *got)
	}
	if !AutoApproved(projects, "web/src/App.tsx") || AutoApproved(projects, "billing/x.go") {
		t.Fatal("scanned tree does not resolve the way the manifests declare it")
	}
}

func boolPtr(v bool) *bool { return &v }
