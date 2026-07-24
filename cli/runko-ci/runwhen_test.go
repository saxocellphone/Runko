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
	out, err := Checks(repo.Dir, base, head, nil, "", "", 0, false)
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
	out2, err := Checks(repo.Dir, head, head2, nil, "", "", 0, false)
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
	out3, err := Checks(repo.Dir, head2, head3, nil, "", "", 0, false)
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

// TestChecksPostLandClass is the post_land class at the executor
// (2026-07-24): without --post-land the class never appears (pre-land CI
// and the merge gate see the same nothing - even under run_everything);
// with it, every project's post_land checks join the matrix regardless of
// the affected scope, because that class runs on every land.
func TestChecksPostLandClass(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml",
		"schema: project/v1\nname: repo\ntype: other\nci:\n  checks:\n    - name: docs-check\n      command: make check-docs\n    - name: compose-smoke\n      command: make check-compose\n      run_when: post_land\n")
	repo.WriteFile("lib/PROJECT.yaml", checksManifest("lib", "lib-test", "go test ./lib/..."))
	base := repo.Commit("seed")

	// A lib-only change: the root project is NOT affected. Pre-land never
	// sees compose-smoke; post-land runs it anyway - every land, unscoped.
	repo.WriteFile("lib/lib.go", "package lib\n")
	head := repo.Commit("touch lib")

	pre, err := Checks(repo.Dir, base, head, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	for _, c := range pre.Checks {
		if c.Name == "compose-smoke" {
			t.Fatalf("post_land check leaked into a pre-land resolve: %+v", pre.Checks)
		}
	}

	post, err := Checks(repo.Dir, base, head, nil, "", "", 0, true)
	if err != nil {
		t.Fatalf("Checks --post-land: %v", err)
	}
	names := map[string]string{}
	for _, c := range post.Checks {
		names[c.Name] = c.Project
	}
	if names["compose-smoke"] != "repo" || names["lib-test"] != "lib" {
		t.Fatalf("post-land resolve must carry both the scoped matrix and the unscoped post_land class, got %+v", post.Checks)
	}
	if _, ok := names["docs-check"]; ok {
		t.Fatalf("--post-land must not widen gate-class scoping (root is unaffected), got %+v", post.Checks)
	}

	// Even a DIRECT root change resolves no post_land check pre-land: the
	// class is not direct-vs-affected scoping, it is not-gate-material.
	repo.WriteFile("rootfile.txt", "root-owned\n")
	head2 := repo.Commit("touch the root project directly")
	pre2, err := Checks(repo.Dir, head, head2, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	names2 := map[string]bool{}
	for _, c := range pre2.Checks {
		names2[c.Name] = true
	}
	if !names2["docs-check"] || names2["compose-smoke"] {
		t.Fatalf("a direct root change owes its gate classes and never the post_land class, got %+v", pre2.Checks)
	}
}
