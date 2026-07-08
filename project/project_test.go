package project

import (
	"strings"
	"testing"

	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
)

// TestCreateProjectRoundTrip is the session-3 "done when" bar (design.md
// §28.3 stage 3): create_project intent -> files -> commit round-trips in
// the gitfixture harness, using the real gitstore.Store (not a mock).
func TestCreateProjectRoundTrip(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "# monorepo\n")
	base := repo.Commit("initial")

	store := gitstore.New(repo.Dir)
	templates := DefaultTemplates()

	intent := Intent{
		Name:   "checkout-api",
		Type:   "service",
		Owners: []string{"group:commerce-eng"},
	}

	plan, errs := PlanCreate(intent, templates)
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	if plan.Path != "checkout-api" {
		t.Fatalf("PlanCreate: want path %q, got %q", "checkout-api", plan.Path)
	}

	newRev, err := Apply(store, core.Revision(base), plan, core.CommitMeta{
		AuthorName: "Test", AuthorEmail: "t@x.com",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Apply only creates the commit object; advancing trunk is the land
	// engine's job (out of scope here). Move refs/heads/main forward
	// ourselves so ListHistory below (which walks HEAD) sees it, the same way
	// a real land would.
	baseRev := core.Revision(base)
	if err := store.UpdateRef("refs/heads/main", newRev, &baseRev); err != nil {
		t.Fatalf("UpdateRef(main): %v", err)
	}

	entries, err := store.GetTree(newRev, "checkout-api")
	if err != nil {
		t.Fatalf("GetTree(checkout-api): %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Path] = true
	}
	for _, want := range []string{"PROJECT.yaml", "README.md", "main.go"} {
		if !names[want] {
			t.Fatalf("expected %s in checkout-api/, got entries: %+v", want, entries)
		}
	}

	// Root README from the base commit must survive the overlay commit -
	// CommitOverlay must build on top of base, not replace the tree.
	rootEntries, err := store.GetTree(newRev, "")
	if err != nil {
		t.Fatalf("GetTree(root): %v", err)
	}
	var sawRoot, sawProject bool
	for _, e := range rootEntries {
		if e.Path == "README.md" {
			sawRoot = true
		}
		if e.Path == "checkout-api" {
			sawProject = true
		}
	}
	if !sawRoot || !sawProject {
		t.Fatalf("expected root README.md and checkout-api/ to coexist, got: %+v", rootEntries)
	}

	manifestBlob, err := store.GetBlob(newRev, "checkout-api/PROJECT.yaml")
	if err != nil {
		t.Fatalf("GetBlob(PROJECT.yaml): %v", err)
	}
	gitfixture.Golden(t, "checkout_api_manifest", string(manifestBlob.Content))

	history, err := store.ListHistory("checkout-api", core.HistoryOptions{})
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 || !strings.Contains(history[0].Message, "checkout-api") {
		t.Fatalf("ListHistory(checkout-api): want one commit mentioning checkout-api, got %+v", history)
	}
}

func TestPlanCreateOmitsOwnersWhenNotGiven(t *testing.T) {
	plan, errs := PlanCreate(Intent{Name: "ownerless-lib", Type: "library"}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	if len(plan.EffectiveManifest.Owners) != 0 {
		t.Fatalf("expected no owners in manifest, got %v", plan.EffectiveManifest.Owners)
	}
	var manifestFile *FileWrite
	for i := range plan.Files {
		if plan.Files[i].Path == "PROJECT.yaml" {
			manifestFile = &plan.Files[i]
		}
	}
	if manifestFile == nil {
		t.Fatalf("expected PROJECT.yaml in plan files: %+v", plan.Files)
	}
	if strings.Contains(manifestFile.Content, "owners:") {
		t.Fatalf("expected no 'owners:' key in rendered manifest, got:\n%s", manifestFile.Content)
	}
}

func TestPlanCreateExplicitPathAndTemplate(t *testing.T) {
	plan, errs := PlanCreate(Intent{
		Name:       "widgets",
		Type:       "library",
		Path:       "commerce/widgets",
		TemplateID: "other-default",
	}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	if plan.Path != "commerce/widgets" {
		t.Fatalf("want explicit path honored, got %q", plan.Path)
	}
	for _, f := range plan.Files {
		if f.Path == "lib.go" {
			t.Fatalf("other-default template should not produce lib.go, got files: %+v", plan.Files)
		}
	}
}

// TestPlanCreateBuildCapabilityGeneratesBuildFileWithZeroHandAuthoredLines
// exercises the greenfield golden path (docs/design.md §14.5.4, DAG stage
// 9c): enabling the "build" capability must produce a BUILD.bazel file and
// a capability_config["build"] block purely from PlanCreate - the caller
// (a human or agent calling create_project) supplies nothing beyond the
// capability name itself.
func TestPlanCreateBuildCapabilityGeneratesBuildFileWithZeroHandAuthoredLines(t *testing.T) {
	plan, errs := PlanCreate(Intent{
		Name: "checkout-api", Type: "service", Path: "commerce/checkout",
		Capabilities: []string{"build"},
	}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}

	var buildFile *FileWrite
	for i := range plan.Files {
		if plan.Files[i].Path == "BUILD.bazel" {
			buildFile = &plan.Files[i]
		}
	}
	if buildFile == nil {
		t.Fatalf("expected a generated BUILD.bazel file, got files: %+v", plan.Files)
	}
	if !strings.Contains(buildFile.Content, "//commerce/checkout/...") {
		t.Fatalf("expected the generated BUILD.bazel to reference its own target pattern, got:\n%s", buildFile.Content)
	}

	buildCfg, ok := plan.EffectiveManifest.CapabilityConfig["build"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected capability_config.build to be set, got %+v", plan.EffectiveManifest.CapabilityConfig)
	}
	if buildCfg["engine"] != "bazel" {
		t.Fatalf("expected engine=bazel by default, got %+v", buildCfg)
	}
	patterns, ok := buildCfg["target_patterns"].([]string)
	if !ok || len(patterns) != 1 || patterns[0] != "//commerce/checkout/..." {
		t.Fatalf("expected target_patterns=[//commerce/checkout/...], got %+v", buildCfg["target_patterns"])
	}
}

// Bazel is the org default (2026-07-08): a bare intent gets the build
// capability from every template; an EXPLICIT capability list (even empty)
// replaces the defaults entirely - that's the opt-out.
func TestPlanCreateDefaultsToBuildCapability(t *testing.T) {
	plan, errs := PlanCreate(Intent{Name: "checkout-api", Type: "service"}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	hasBuildFile := false
	for _, f := range plan.Files {
		hasBuildFile = hasBuildFile || f.Path == "BUILD.bazel"
	}
	if !hasBuildFile || !hasCapability(plan.EffectiveManifest.Capabilities, "build") {
		t.Fatalf("bare intent should default to the build capability, got %+v", plan.EffectiveManifest)
	}
	if plan.EffectiveManifest.CapabilityConfig["build"] == nil {
		t.Fatalf("expected generated capability_config.build, got %+v", plan.EffectiveManifest.CapabilityConfig)
	}
}

func TestPlanCreateExplicitCapabilitiesOptOutOfBuild(t *testing.T) {
	plan, errs := PlanCreate(Intent{Name: "checkout-api", Type: "service", Capabilities: []string{}}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	for _, f := range plan.Files {
		if f.Path == "BUILD.bazel" {
			t.Fatalf("explicit empty capabilities must opt out of BUILD.bazel, got files: %+v", plan.Files)
		}
	}
	if plan.EffectiveManifest.CapabilityConfig != nil {
		t.Fatalf("expected no capability_config after opt-out, got %+v", plan.EffectiveManifest.CapabilityConfig)
	}
}

func TestPlanCreateValidationFailure(t *testing.T) {
	_, errs := PlanCreate(Intent{Name: "Bad Name!", Type: "service"}, DefaultTemplates())
	if len(errs) == 0 {
		t.Fatalf("expected validation errors for invalid name")
	}
	found := false
	for _, e := range errs {
		if e.Field == "name" && e.Code == "invalid_format" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an invalid_format error on field 'name', got %+v", errs)
	}
}

func TestValidateUnknownCapabilityAndTemplate(t *testing.T) {
	errs := Validate(Intent{
		Name: "checkout-api", Type: "service",
		Capabilities: []string{"not-a-real-capability"},
		TemplateID:   "does-not-exist",
	}, DefaultTemplates())

	var sawCapability, sawTemplate bool
	for _, e := range errs {
		if e.Field == "capabilities" {
			sawCapability = true
		}
		if e.Field == "template_id" {
			sawTemplate = true
		}
	}
	if !sawCapability || !sawTemplate {
		t.Fatalf("expected errors on both capabilities and template_id, got %+v", errs)
	}
}
