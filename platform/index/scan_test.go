package index

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
)

func manifest(name, projType string, extra string) string {
	return "schema: project/v1\nname: " + name + "\ntype: " + projType + "\n" + extra
}

func TestScanFindsNestedProjects(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout-api", "service", ""))
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.WriteFile("libs/auth/PROJECT.yaml", manifest("auth-lib", "library", ""))
	repo.WriteFile("libs/auth/lib.go", "package auth\n")
	head := repo.Commit("two projects")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("want 2 projects, got %d: %+v", len(projects), projects)
	}

	byName := map[string]IndexedProject{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if p, ok := byName["checkout-api"]; !ok || p.Path != "commerce/checkout" || p.Type != "service" {
		t.Fatalf("checkout-api: unexpected result %+v (ok=%v)", p, ok)
	}
	if p, ok := byName["auth-lib"]; !ok || p.Path != "libs/auth" || p.Type != "library" {
		t.Fatalf("auth-lib: unexpected result %+v (ok=%v)", p, ok)
	}
}

// TestOwnersResolutionPrecedence is the "owners resolution table-tested"
// done-when bar for stage 4 (§28.3): manifest owners > nearest ancestor
// OWNERS > org default, exercised as one table.
func TestOwnersResolutionPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(repo *gitfixture.Repo)
		orgDefault []string
		wantOwners []OwnerEntry
	}{
		{
			name: "manifest owners win outright",
			setup: func(repo *gitfixture.Repo) {
				repo.WriteFile("OWNERS", "group:root-team\n")
				repo.WriteFile("svc/PROJECT.yaml", manifest("svc", "service", "owners:\n  - group:svc-team\n"))
			},
			wantOwners: []OwnerEntry{{Ref: "group:svc-team", Source: "project_manifest"}},
		},
		{
			name: "nearest ancestor OWNERS wins over a farther one",
			setup: func(repo *gitfixture.Repo) {
				repo.WriteFile("OWNERS", "group:root-team\n")
				repo.WriteFile("commerce/OWNERS", "group:commerce-team\n")
				repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout", "service", ""))
			},
			wantOwners: []OwnerEntry{{Ref: "group:commerce-team", Source: "path_owners"}},
		},
		{
			name: "falls back to root OWNERS when no closer one exists",
			setup: func(repo *gitfixture.Repo) {
				repo.WriteFile("OWNERS", "group:root-team\n")
				repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout", "service", ""))
			},
			wantOwners: []OwnerEntry{{Ref: "group:root-team", Source: "path_owners"}},
		},
		{
			name: "empty OWNERS file is skipped in favor of a farther ancestor",
			setup: func(repo *gitfixture.Repo) {
				repo.WriteFile("OWNERS", "group:root-team\n")
				repo.WriteFile("commerce/OWNERS", "# no one yet\n")
				repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout", "service", ""))
			},
			wantOwners: []OwnerEntry{{Ref: "group:root-team", Source: "path_owners"}},
		},
		{
			name: "org default used when no manifest owners and no OWNERS file anywhere",
			setup: func(repo *gitfixture.Repo) {
				repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout", "service", ""))
			},
			orgDefault: []string{"group:org-default"},
			wantOwners: []OwnerEntry{{Ref: "group:org-default", Source: "org_default"}},
		},
		{
			name: "no owners anywhere and no org default yields nil, not an error",
			setup: func(repo *gitfixture.Repo) {
				repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest("checkout", "service", ""))
			},
			wantOwners: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitfixture.New(t)
			tc.setup(repo)
			head := repo.Commit(tc.name)

			store := gitstore.New(repo.Dir)
			projects, err := Scan(store, core.Revision(head), tc.orgDefault)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(projects) != 1 {
				t.Fatalf("want exactly 1 project, got %d: %+v", len(projects), projects)
			}

			got := projects[0].Owners
			if len(got) != len(tc.wantOwners) {
				t.Fatalf("owners: want %+v, got %+v", tc.wantOwners, got)
			}
			for i := range got {
				if got[i] != tc.wantOwners[i] {
					t.Fatalf("owners[%d]: want %+v, got %+v", i, tc.wantOwners[i], got[i])
				}
			}
		})
	}
}

func TestScanCapabilitiesAndDependenciesPassThrough(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", manifest("svc", "service",
		"capabilities:\n  - http\n  - rpc\ndependencies:\n  - auth-lib\nvisibility: restricted\n"))
	head := repo.Commit("with capabilities")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(projects))
	}
	p := projects[0]
	if len(p.Capabilities) != 2 || p.Capabilities[0] != "http" || p.Capabilities[1] != "rpc" {
		t.Fatalf("capabilities: want [http rpc], got %v", p.Capabilities)
	}
	if len(p.DeclaredDependencies) != 1 || p.DeclaredDependencies[0] != "auth-lib" {
		t.Fatalf("dependencies: want [auth-lib], got %v", p.DeclaredDependencies)
	}
	if p.Visibility != "restricted" {
		t.Fatalf("visibility: want restricted, got %q", p.Visibility)
	}
}

// TestScanExposesRequiredChecks guards a real gap found in review
// (§28.3 stage 11b's follow-up): PROJECT.yaml's ci.checks (§14.9) was
// parsed by project.Manifest but silently dropped by Scan, so nothing
// downstream (the merge-requirements gate) could ever see a project's
// declared required checks.
func TestScanExposesRequiredChecks(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", manifest("svc", "service",
		"ci:\n  checks:\n    - name: unit\n      command: go test ./...\n    - name: lint\n      command: golangci-lint run\n"))
	head := repo.Commit("with ci checks")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(projects))
	}
	got := projects[0].RequiredChecks
	if len(got) != 2 || got[0] != "unit" || got[1] != "lint" {
		t.Fatalf("RequiredChecks: want [unit lint], got %v", got)
	}
}

// TestScanNoCIBlockIsNoRequiredChecks confirms the anti-Boq default: a
// project with no ci block at all (the common case - most projects never
// set this L2/opt-in field) has zero required checks, not "everything
// required" or an error.
func TestScanNoCIBlockIsNoRequiredChecks(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", manifest("svc", "service", ""))
	head := repo.Commit("no ci block")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects[0].RequiredChecks) != 0 {
		t.Fatalf("expected no required checks, got %v", projects[0].RequiredChecks)
	}
}

func TestScanDefaultVisibility(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", manifest("svc", "service", ""))
	head := repo.Commit("no visibility set")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if projects[0].Visibility != "default" {
		t.Fatalf("visibility: want %q, got %q", "default", projects[0].Visibility)
	}
}

// root_invalidation is tree-borne policy (§9.4): Scan surfaces it and the
// helper concatenates in scan order - ORDER PRESERVED (§14.5.8: lists are
// first-match-wins with "!" exceptions, so the root manifest's exceptions
// must stay ahead of everything, including its own broad patterns and any
// deeper manifest's).
func TestScanRootInvalidation(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", "schema: project/v1\nname: repo\ntype: other\nroot_invalidation:\n  - \"!.github/workflows/ci.yml\"\n  - .github/**\n  - go.mod\n")
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\nroot_invalidation:\n  - go.mod\n")
	rev := repo.Commit("seed")

	indexed, err := Scan(gitstore.New(repo.Dir), core.Revision(rev), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := RootInvalidation(indexed)
	want := []string{"!.github/workflows/ci.yml", ".github/**", "go.mod", "go.mod"}
	if len(got) != len(want) {
		t.Fatalf("expected ordered concatenation %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected ordered concatenation %v, got %v", want, got)
		}
	}
}
