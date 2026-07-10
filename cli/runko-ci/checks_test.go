package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

func checksManifest(name, checkName, command string) string {
	return "schema: project/v1\nname: " + name + "\ntype: service\nci:\n  checks:\n    - name: " + checkName + "\n      command: " + command + "\n"
}

// The encapsulation contract (§14.9): the matrix comes from the affected
// closure's manifest-declared checks - scoped changes yield scoped checks,
// shared names dedupe, run_everything yields every project's checks.
func TestChecksResolvesScopedManifestChecks(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", checksManifest("svc", "svc-test", "bazel test //svc/..."))
	repo.WriteFile("lib/PROJECT.yaml", checksManifest("lib", "lib-test", "bazel test //lib/..."))
	base := repo.Commit("seed")
	repo.WriteFile("svc/main.go", "package main\n")
	head := repo.Commit("touch svc")

	out, err := Checks(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if out.RunEverything || len(out.Checks) != 1 {
		t.Fatalf("svc-only change must resolve exactly svc's check, got %+v", out)
	}
	c := out.Checks[0]
	if c.Project != "svc" || c.Name != "svc-test" || c.Command != "bazel test //svc/..." {
		t.Fatalf("unexpected check: %+v", c)
	}
}

func TestChecksFollowsDependencyClosure(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml", checksManifest("lib", "lib-test", "go test ./lib/..."))
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\ndependencies:\n  - lib\nci:\n  checks:\n    - name: svc-test\n      command: go test ./svc/...\n")
	base := repo.Commit("seed")
	repo.WriteFile("lib/lib.go", "package lib\n")
	head := repo.Commit("touch lib")

	out, err := Checks(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	names := []string{}
	for _, c := range out.Checks {
		names = append(names, c.Name)
	}
	if len(names) != 2 || names[0] != "lib-test" || names[1] != "svc-test" {
		t.Fatalf("a lib change must pull svc's check via the closure, got %v", names)
	}
}

func TestChecksRunEverythingResolvesAll(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", checksManifest("svc", "svc-test", "t"))
	repo.WriteFile("lib/PROJECT.yaml", checksManifest("lib", "lib-test", "t"))
	base := repo.Commit("seed")
	// No root project owns stray root files -> conservative fail-closed
	// run_everything (§14.5.3).
	repo.WriteFile("orphan.txt", "x\n")
	head := repo.Commit("orphan")

	out, err := Checks(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if !out.RunEverything || len(out.Checks) != 2 {
		t.Fatalf("run_everything must resolve every project's checks, got %+v", out)
	}
}

func TestChecksSharedNameDedupesAndConflictErrors(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a/PROJECT.yaml", checksManifest("a", "shared", "make shared"))
	repo.WriteFile("b/PROJECT.yaml", checksManifest("b", "shared", "make shared"))
	base := repo.Commit("seed")
	repo.WriteFile("a/x.go", "package a\n")
	repo.WriteFile("b/x.go", "package b\n")
	head := repo.Commit("touch both")

	out, err := Checks(repo.Dir, base, head, nil)
	if err != nil || len(out.Checks) != 1 {
		t.Fatalf("identical shared checks must dedupe to one run, got %+v / %v", out, err)
	}

	// Same name, different command: loud structured refusal.
	repo.WriteFile("b/PROJECT.yaml", checksManifest("b", "shared", "make other"))
	head2 := repo.Commit("conflict")
	_, err = Checks(repo.Dir, base, head2, nil)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "ambiguous_check" || !strings.Contains(ce.Message, "shared") {
		t.Fatalf("expected ambiguous_check naming the check, got %v", err)
	}
}
