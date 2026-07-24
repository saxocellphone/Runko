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

// TestScanDeployImageAndRiders pins the §14.10 deploy-capability extraction:
// deploy.image -> DeployImage (owner), deploy.workloads[].image -> RidesImages
// (rider edge), with context defaulting to the project dir and the neither-
// sub-block case staying zero-valued.
func TestScanDeployImageAndRiders(t *testing.T) {
	repo := gitfixture.New(t)
	// owner: declares deploy.image with an explicit context + build_args.
	repo.WriteFile("api/PROJECT.yaml", manifest("api", "service",
		"capabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: api\n      context: .\n      dockerfile: Dockerfile\n      build_args:\n        FOO: bar\n"))
	// owner omitting context: it must default to the project dir.
	repo.WriteFile("svc2/PROJECT.yaml", manifest("svc2", "service",
		"capabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: svc2\n      dockerfile: svc2/Dockerfile\n"))
	// rider: no image of its own, a workload running from api's image.
	repo.WriteFile("side/PROJECT.yaml", manifest("side", "service",
		"capabilities:\n  - deploy\ncapability_config:\n  deploy:\n    workloads:\n      - name: side-worker\n        image: api\n"))
	// deploy capability but neither sub-block: both fields stay zero-valued.
	repo.WriteFile("bare/PROJECT.yaml", manifest("bare", "library",
		"capabilities:\n  - deploy\n"))
	// image sub-block with no name declares nothing rebuildable -> nil.
	repo.WriteFile("noname/PROJECT.yaml", manifest("noname", "service",
		"capabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      context: .\n      dockerfile: Dockerfile\n"))
	// deploy config present but the capability is NOT in `capabilities`: the
	// hasCapability gate drops it (same posture as rpc/http) - both fields nil.
	repo.WriteFile("nocap/PROJECT.yaml", manifest("nocap", "service",
		"capability_config:\n  deploy:\n    image:\n      name: nocap\n      dockerfile: Dockerfile\n"))
	head := repo.Commit("deploy manifests")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byName := map[string]IndexedProject{}
	for _, p := range projects {
		byName[p.Name] = p
	}

	api := byName["api"]
	if api.DeployImage == nil {
		t.Fatalf("api: DeployImage nil, want owner")
	}
	if api.DeployImage.Name != "api" || api.DeployImage.Context != "." || api.DeployImage.Dockerfile != "Dockerfile" {
		t.Fatalf("api image: got %+v", *api.DeployImage)
	}
	if api.DeployImage.BuildArgs["FOO"] != "bar" {
		t.Fatalf("api build_args: want FOO=bar, got %v", api.DeployImage.BuildArgs)
	}
	if len(api.RidesImages) != 0 {
		t.Fatalf("api rides: want none, got %v", api.RidesImages)
	}

	if got := byName["svc2"].DeployImage; got == nil || got.Context != "svc2" {
		t.Fatalf("svc2 image context should default to the project dir, got %+v", got)
	}

	side := byName["side"]
	if side.DeployImage != nil {
		t.Fatalf("side: DeployImage %+v, want nil (no image sub-block)", *side.DeployImage)
	}
	if len(side.RidesImages) != 1 || side.RidesImages[0] != "api" {
		t.Fatalf("side rides: want [api], got %v", side.RidesImages)
	}

	bare := byName["bare"]
	if bare.DeployImage != nil || len(bare.RidesImages) != 0 {
		t.Fatalf("bare: want zero-valued deploy, got image=%v rides=%v", bare.DeployImage, bare.RidesImages)
	}

	if got := byName["noname"].DeployImage; got != nil {
		t.Fatalf("noname: image with no name should yield nil DeployImage, got %+v", got)
	}

	nocap := byName["nocap"]
	if nocap.DeployImage != nil || len(nocap.RidesImages) != 0 {
		t.Fatalf("nocap: deploy config without the capability must be dropped, got image=%v rides=%v", nocap.DeployImage, nocap.RidesImages)
	}
}

// TestScanDeployRegistry: the top-level deploy_registry surfaces into
// IndexedProject.DeployRegistry (the root-oriented §14.10 registry base).
func TestScanDeployRegistry(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", manifest("repo", "other", "deploy_registry: ghcr.io/acme/monorepo\n"))
	repo.WriteFile("svc/PROJECT.yaml", manifest("svc", "service", ""))
	head := repo.Commit("root registry")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byName := map[string]IndexedProject{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if got := byName["repo"].DeployRegistry; got != "ghcr.io/acme/monorepo" {
		t.Fatalf("root deploy_registry: got %q", got)
	}
	if got := byName["svc"].DeployRegistry; got != "" {
		t.Fatalf("non-declaring project should have empty registry, got %q", got)
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

// TestScanContractSurfaces pins §13.3.1's manifest-derived fields: the rpc
// capability yields ContractGenDir (config path or the proto default; "."
// keeps the legacy standalone-proto layout), and the http capability yields
// the OpenAPI document path plus its presence at rev.
func TestScanContractSurfaces(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("runkod/PROJECT.yaml", manifest("runkod", "service",
		"capabilities:\n  - rpc\ncapability_config:\n  rpc:\n    path: proto\n"))
	repo.WriteFile("proto/PROJECT.yaml", manifest("proto", "library",
		"capabilities:\n  - rpc\ncapability_config:\n  rpc:\n    path: .\n"))
	repo.WriteFile("billing/PROJECT.yaml", manifest("billing", "service",
		"capabilities:\n  - http\n"))
	repo.WriteFile("shop/PROJECT.yaml", manifest("shop", "service",
		"capabilities:\n  - http\ncapability_config:\n  http:\n    openapi: api/openapi.yaml\n"))
	repo.WriteFile("shop/api/openapi.yaml", "openapi: 3.1.0\n")
	repo.WriteFile("plain/PROJECT.yaml", manifest("plain", "library", ""))
	repo.WriteFile("mailer/PROJECT.yaml", manifest("mailer", "service",
		"consumes:\n  - runkod\n"))
	head := repo.Commit("contract surfaces")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byName := map[string]IndexedProject{}
	for _, p := range projects {
		byName[p.Name] = p
	}
	if p := byName["runkod"]; p.ContractDir != "runkod/proto" || p.ContractGenDir != "runkod/proto/gen" {
		t.Fatalf("runkod contract surface = %q/%q, want runkod/proto + runkod/proto/gen", p.ContractDir, p.ContractGenDir)
	}
	if p := byName["proto"]; p.ContractDir != "proto" || p.ContractGenDir != "proto/gen" {
		t.Fatalf("proto contract surface = %q/%q, want proto + proto/gen", p.ContractDir, p.ContractGenDir)
	}
	if got := byName["mailer"].Consumes; len(got) != 1 || got[0] != "runkod" {
		t.Fatalf("mailer Consumes = %v, want [runkod]", got)
	}
	if p := byName["billing"]; p.OpenAPIPath != "billing/openapi.yaml" || p.OpenAPIPresent {
		t.Fatalf("billing = %+v, want default path, absent", p)
	}
	if p := byName["shop"]; p.OpenAPIPath != "shop/api/openapi.yaml" || !p.OpenAPIPresent {
		t.Fatalf("shop = %+v, want configured path, present", p)
	}
	if p := byName["plain"]; p.ContractGenDir != "" || p.OpenAPIPath != "" {
		t.Fatalf("plain must have no contract surface: %+v", p)
	}
}

// TestScanSchemasCapability pins §13.3.1's third contract shape: the
// schemas capability's paths surface repo-relative, and
// AffectedProjectInfos folds them (plus the manifest, fail closed) into
// the consumes closure's ContractPaths.
func TestScanSchemasCapability(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("docs/PROJECT.yaml", manifest("docs", "other",
		"capabilities:\n  - schemas\ncapability_config:\n  schemas:\n    paths:\n      - spec\n      - cli-contract.md\n"))
	repo.WriteFile("docs/spec/x.schema.json", "{}\n")
	head := repo.Commit("schemas capability")

	store := gitstore.New(repo.Dir)
	projects, err := Scan(store, core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("want 1 project, got %v", projects)
	}
	p := projects[0]
	if len(p.SchemaPaths) != 2 || p.SchemaPaths[0] != "docs/spec" || p.SchemaPaths[1] != "docs/cli-contract.md" {
		t.Fatalf("SchemaPaths = %v", p.SchemaPaths)
	}

	infos := AffectedProjectInfos(projects)
	want := []string{"docs/PROJECT.yaml", "docs/spec", "docs/cli-contract.md"}
	if len(infos[0].ContractPaths) != len(want) {
		t.Fatalf("ContractPaths = %v, want %v", infos[0].ContractPaths, want)
	}
	for i, w := range want {
		if infos[0].ContractPaths[i] != w {
			t.Fatalf("ContractPaths = %v, want %v", infos[0].ContractPaths, want)
		}
	}
}

// TestScanDeployGitOpsAndBinaries pins the 2026-07-24 additions to the
// deploy surface: the root's deploy_gitops target (both fields required -
// half a target is dropped, not emitted for a pin job to fail against) and
// the deploy.binaries sub-block (tag required; item paths default to
// <project dir>/<name>).
func TestScanDeployGitOpsAndBinaries(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", manifest("repo", "other",
		"deploy_gitops:\n  repository: acme/k8s-cluster\n  kustomization: apps/mono/kustomization.yaml\n"))
	repo.WriteFile("cli/PROJECT.yaml", manifest("cli", "app",
		"capabilities:\n  - deploy\ncapability_config:\n  deploy:\n    binaries:\n      tag: cli-latest\n      items:\n        - name: runko\n        - name: runko-ci\n          path: cli/custom-ci\n"))
	// A binaries block with no tag declares nothing (read-time normalization).
	repo.WriteFile("untagged/PROJECT.yaml", manifest("untagged", "app",
		"capabilities:\n  - deploy\ncapability_config:\n  deploy:\n    binaries:\n      items:\n        - name: stray\n"))
	// Half a gitops target (no kustomization) is dropped, not surfaced.
	repo.WriteFile("half/PROJECT.yaml", manifest("half", "other",
		"deploy_gitops:\n  repository: acme/other\n"))
	head := repo.Commit("gitops + binaries manifests")

	projects, err := Scan(gitstore.New(repo.Dir), core.Revision(head), nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byName := map[string]IndexedProject{}
	for _, p := range projects {
		byName[p.Name] = p
	}

	root := byName["repo"]
	if root.DeployGitOps == nil || root.DeployGitOps.Repository != "acme/k8s-cluster" || root.DeployGitOps.Kustomization != "apps/mono/kustomization.yaml" {
		t.Fatalf("root gitops: got %+v", root.DeployGitOps)
	}

	cli := byName["cli"]
	if cli.DeployBinaryTag != "cli-latest" {
		t.Fatalf("cli binary tag: got %q", cli.DeployBinaryTag)
	}
	want := []BinaryDecl{{Name: "runko", Path: "cli/runko"}, {Name: "runko-ci", Path: "cli/custom-ci"}}
	if len(cli.DeployBinaries) != 2 || cli.DeployBinaries[0] != want[0] || cli.DeployBinaries[1] != want[1] {
		t.Fatalf("cli binaries: default path must be <dir>/<name>, explicit path kept; got %+v", cli.DeployBinaries)
	}

	if u := byName["untagged"]; u.DeployBinaryTag != "" || len(u.DeployBinaries) != 0 {
		t.Fatalf("untagged binaries block must declare nothing, got tag=%q items=%v", u.DeployBinaryTag, u.DeployBinaries)
	}
	if h := byName["half"]; h.DeployGitOps != nil {
		t.Fatalf("partial deploy_gitops must be dropped, got %+v", h.DeployGitOps)
	}
}
