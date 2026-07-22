package runkod

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/deploy"
	"github.com/saxocellphone/runko/platform/index"
)

// --- Frozen reference: the RETIRED hardcoded image->project map. -----------
// deployimages.go's static map/imagesForAffected were deleted when
// openDeployRecordForLand moved to the manifest-derived
// platform/deploy.ImagesForAffected. This frozen copy is the migration
// contract: the derivation over the real topology must reproduce the retired
// map's decisions, asserted by TestDeployDerivationMatchesRetiredMap.
var retiredImageProjects = map[string][]string{
	"runkod":   {"runkod", "platform", "internal", "db", "proto", "watchdog", "mailer"},
	"web":      {"web", "proto"},
	"webadmin": {"webadmin"},
}

var retiredDeployImages = []string{"runkod", "web", "webadmin"}

func retiredImagesForAffected(result affected.Result) []string {
	if result.RunEverything {
		return append([]string(nil), retiredDeployImages...)
	}
	touched := map[string]bool{}
	for _, p := range result.Projects {
		touched[p.Name] = true
	}
	var out []string
	for _, img := range retiredDeployImages {
		for _, proj := range retiredImageProjects[img] {
			if touched[proj] {
				out = append(out, img)
				break
			}
		}
	}
	return out
}

// realTopology mirrors A3's landed manifests: runkod/web/webadmin OWN images;
// mailer/watchdog RIDE runkod. Kept in sync with the PROJECT.yaml files by the
// same review that would change the deployment topology.
func realTopology() []index.IndexedProject {
	return []index.IndexedProject{
		{Name: "runkod", DeployImage: &index.ImageDecl{Name: "runkod", Context: ".", Dockerfile: "Dockerfile"}},
		{Name: "web", DeployImage: &index.ImageDecl{Name: "web", Context: "web", Dockerfile: "web/Dockerfile"}},
		{Name: "webadmin", DeployImage: &index.ImageDecl{Name: "webadmin", Context: "webadmin", Dockerfile: "webadmin/Dockerfile"}},
		{Name: "mailer", RidesImages: []string{"runkod"}},
		{Name: "watchdog", RidesImages: []string{"runkod"}},
		{Name: "platform"}, {Name: "internal"}, {Name: "db"}, {Name: "cli"},
	}
}

func sortedEqual(a, b []string) bool {
	ac, bc := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	if len(ac) == 0 && len(bc) == 0 {
		return true
	}
	return reflect.DeepEqual(ac, bc)
}

// TestDeployDerivationMatchesRetiredMap is the migration invariant, asserted
// EXACTLY: over the realistic affected closures affected.Compute produces
// (dependents expansion + consumes contract-scoping), the manifest-derived
// image set equals the retired hardcoded map's, decision-for-decision. The
// closures below are what the real dependency/consumes edges yield; any
// divergence between the two computations fails the test.
func TestDeployDerivationMatchesRetiredMap(t *testing.T) {
	topo := realTopology()
	closures := [][]string{
		{"platform", "runkod", "cli", "watchdog"}, // platform land: deps pull runkod+watchdog
		{"internal", "runkod"},                    // runkod deps internal
		{"db", "runkod"},                          // runkod deps db
		{"mailer"},                                // rider-only: rider edge carries it
		{"watchdog"},                              // rider-only
		{"runkod"},                                // owner internals (non-contract)
		{"runkod", "web", "cli", "mailer", "watchdog"}, // runkod CONTRACT: consumers join
		{"web"},
		{"webadmin"},
		{"cli"}, // owns/rides no image
		{},      // prose/docs-only
	}
	for _, names := range closures {
		var res affected.Result
		for _, n := range names {
			res.Projects = append(res.Projects, affected.ProjectRef{Name: n})
		}
		got := deploy.ImagesForAffected(res, topo)
		want := retiredImagesForAffected(res)
		if !sortedEqual(got, want) {
			t.Errorf("closure %v: derived %v != retired map %v", names, got, want)
		}
	}
	// RunEverything: both rebuild every image (fail closed, §14.5.3).
	if got, want := deploy.ImagesForAffected(affected.Result{RunEverything: true}, topo), retiredImagesForAffected(affected.Result{RunEverything: true}); !sortedEqual(got, want) {
		t.Errorf("RunEverything: derived %v != retired %v", got, want)
	}
}

// TestOpenDeployRecordOnLand: landing a change that touches a deployable
// project opens a deploy record naming the derived images (§14.10) - the
// server-of-record report-image later completes to fire the rollout. The
// fixture declares runkod's deploy.image and platform as its dependency, so a
// platform change reaches the runkod image through the SAME path production
// uses: land -> computeAffectedForChange -> deploy.ImagesForAffected.
func TestOpenDeployRecordOnLand(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("platform/PROJECT.yaml", "schema: project/v1\nname: platform\ntype: library\n")
	repo.WriteFile("runkod/PROJECT.yaml", "schema: project/v1\nname: runkod\ntype: service\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: runkod\n      context: .\n      dockerfile: Dockerfile\ndependencies:\n  - platform\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("platform/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push rejected: %+v", res)
	}

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, AllowUnpolicedLand: true}
	chA, _, _ := store.GetChange(ctx, stackIDA)
	dec, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, &Principal{Name: "val", Stored: true}, false)
	if apiErr != nil || !dec.Landed {
		t.Fatalf("land: dec=%+v err=%+v", dec, apiErr)
	}

	rec, ok, err := store.GetDeployRecord(ctx, dec.LandedSHA)
	if err != nil || !ok {
		t.Fatalf("a deployable land must open a deploy record: ok=%v err=%v", ok, err)
	}
	if len(rec.Expected) != 1 || rec.Expected[0] != "runkod" {
		t.Fatalf("a platform change (runkod deps platform) expects the runkod image, got %v", rec.Expected)
	}
	if rec.State != "pending" || rec.ChangeKey != stackIDA {
		t.Fatalf("record: %+v", rec)
	}
}

// TestNoDeployRecordForNonDeployableLand: a land that touches only a project
// declaring no image (and feeding no image's owner/riders) opens NO record -
// the per-org emptiness that also means an org declaring no deploy.image
// never opens a phantom record.
func TestNoDeployRecordForNonDeployableLand(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml", "schema: project/v1\nname: lib\ntype: library\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("lib/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push rejected: %+v", res)
	}

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, AllowUnpolicedLand: true}
	chA, _, _ := store.GetChange(ctx, stackIDA)
	dec, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, &Principal{Name: "val", Stored: true}, false)
	if apiErr != nil || !dec.Landed {
		t.Fatalf("land: dec=%+v err=%+v", dec, apiErr)
	}
	if _, ok, _ := store.GetDeployRecord(ctx, dec.LandedSHA); ok {
		t.Fatalf("a non-deployable land must open NO deploy record")
	}
}
