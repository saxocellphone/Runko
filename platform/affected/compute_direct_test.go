package affected

import "testing"

// TestDirectFlagSeparatesTouchedFromClosure pins §14.5.9's input: projects
// whose own paths were touched carry Direct=true; dependents pulled in via
// depends_on edges carry Direct=false.
func TestDirectFlagSeparatesTouchedFromClosure(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "platform", Path: "platform"},
		{Name: "runkod", Path: "runkod", DeclaredDependencies: []string{"platform"}},
	}
	res := Compute(projects, []string{"platform/checks/webhook.go"}, Options{})
	if res.RunEverything {
		t.Fatalf("owned path must not escalate: %+v", res)
	}
	direct := map[string]bool{}
	for _, ref := range res.Projects {
		direct[ref.Name] = ref.Direct
	}
	if !direct["platform"] {
		t.Fatalf("the touched project's owner must be Direct, got %+v", res.Projects)
	}
	if v, ok := direct["runkod"]; !ok || v {
		t.Fatalf("the dependency-closure member must be present and NOT Direct, got %+v", res.Projects)
	}
}

// TestCloseOverDependentNamesMarksSeedsDirect: §14.5.8's snapshot-diff
// union - graph-named impacted projects are the moral equivalent of
// touched (Direct), their dependents ride closure-shaped.
func TestCloseOverDependentNamesMarksSeedsDirect(t *testing.T) {
	projects := []ProjectInfo{
		{Name: "proto", Path: "proto"},
		{Name: "web", Path: "web", DeclaredDependencies: []string{"proto"}},
	}
	refs := CloseOverDependentNames(projects, []string{"proto"})
	direct := map[string]bool{}
	for _, r := range refs {
		direct[r.Name] = r.Direct
	}
	if !direct["proto"] || direct["web"] {
		t.Fatalf("expected proto Direct and web closure-shaped, got %+v", refs)
	}
}
