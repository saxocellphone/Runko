package main

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// svcManifest declares one dependent project with a split check suite:
// an affected-class integration lane and a direct-only unit lane
// (§14.5.9's test classes).
const splitSvcManifest = "schema: project/v1\nname: svc\ntype: service\ndependencies:\n  - lib\nci:\n  checks:\n    - name: svc-integration\n      command: go test ./svc/e2e/...\n    - name: svc-unit\n      command: go test ./svc/...\n      run_when: direct\n"

// TestChecksRunWhenDirectSkipsClosureDependents is §14.5.9 at the
// executor: an upstream change runs the dependent's integration lane but
// NOT its direct-only unit lane; the dependent's own change runs both;
// run_everything runs both (fail closed).
func TestChecksRunWhenDirectSkipsClosureDependents(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml", checksManifest("lib", "lib-test", "go test ./lib/..."))
	repo.WriteFile("svc/PROJECT.yaml", splitSvcManifest)
	base := repo.Commit("seed")

	// Upstream (lib) change: svc rides the closure - integration only.
	repo.WriteFile("lib/lib.go", "package lib\n")
	head := repo.Commit("touch lib")
	out, err := Checks(repo.Dir, base, head, nil, "", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	names := map[string]bool{}
	for _, c := range out.Checks {
		names[c.Name] = true
	}
	if !names["lib-test"] || !names["svc-integration"] {
		t.Fatalf("upstream change must run lib's checks and svc's integration lane, got %+v", out.Checks)
	}
	if names["svc-unit"] {
		t.Fatalf("a closure-affected dependent's direct-only unit lane must not run, got %+v", out.Checks)
	}

	// svc's own change: both classes.
	repo.WriteFile("svc/main.go", "package main\n")
	head2 := repo.Commit("touch svc")
	out2, err := Checks(repo.Dir, head, head2, nil, "", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	names2 := map[string]bool{}
	for _, c := range out2.Checks {
		names2[c.Name] = true
	}
	if !names2["svc-unit"] || !names2["svc-integration"] {
		t.Fatalf("a direct change must run both classes, got %+v", out2.Checks)
	}

	// run_everything (an unowned path): every class of every project.
	repo.WriteFile("orphan.txt", "unowned\n")
	head3 := repo.Commit("unowned path")
	out3, err := Checks(repo.Dir, head2, head3, nil, "", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if !out3.RunEverything {
		t.Fatalf("unowned path must escalate, got %+v", out3)
	}
	names3 := map[string]bool{}
	for _, c := range out3.Checks {
		names3[c.Name] = true
	}
	if !names3["svc-unit"] || !names3["svc-integration"] || !names3["lib-test"] {
		t.Fatalf("run_everything must run both classes everywhere (fail closed), got %+v", out3.Checks)
	}
}
