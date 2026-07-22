package deploy

import (
	"reflect"
	"testing"

	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/index"
)

// runkoTopology is the image topology Track A's manifests declare: runkod,
// web, and webadmin each OWN an image; mailer and watchdog RIDE the runkod
// image (their binaries ship inside it). platform/internal/db/cli own no
// image - they reach an image only by being in the affected closure of an
// owner or rider.
func runkoTopology() []index.IndexedProject {
	return []index.IndexedProject{
		{Name: "runkod", DeployImage: &index.ImageDecl{Name: "runkod"}},
		{Name: "web", DeployImage: &index.ImageDecl{Name: "web"}},
		{Name: "webadmin", DeployImage: &index.ImageDecl{Name: "webadmin"}},
		{Name: "mailer", RidesImages: []string{"runkod"}},
		{Name: "watchdog", RidesImages: []string{"runkod"}},
		{Name: "platform"}, {Name: "internal"}, {Name: "db"}, {Name: "cli"},
	}
}

func affectedOf(names ...string) affected.Result {
	var refs []affected.ProjectRef
	for _, n := range names {
		refs = append(refs, affected.ProjectRef{Name: n})
	}
	return affected.Result{Projects: refs}
}

// TestImagesForAffected is the equivalence table from the Track A plan: every
// case the old hardcoded map decided, decided identically by the derivation.
// The affected closures here are what affected.Compute produces (dependents
// expansion + consumes contract-scoping); this function only READS them.
func TestImagesForAffected(t *testing.T) {
	topo := runkoTopology()
	cases := []struct {
		name   string
		result affected.Result
		want   []string
	}{
		// platform land -> the real closure is {platform, runkod, cli, watchdog}
		// (runkod/watchdog declare dependencies:[platform], cli rides in too);
		// runkod's image rebuilds because its owner + a rider are present.
		{"platform change", affectedOf("platform", "runkod", "cli", "watchdog"), []string{"runkod"}},
		// the rider edge is load-bearing: mailer is in no owner's dep graph.
		{"mailer change (rider)", affectedOf("mailer"), []string{"runkod"}},
		{"watchdog change (rider)", affectedOf("watchdog"), []string{"runkod"}},
		{"runkod-internals change", affectedOf("runkod"), []string{"runkod"}},
		// a runkod CONTRACT change pulls consumers (web, cli, mailer, watchdog)
		// into the closure; web owns an image, so it rebuilds too. This is the
		// case an earlier draft mis-analysed as a divergence - it is a MATCH.
		{"runkod contract change", affectedOf("runkod", "web", "cli", "mailer", "watchdog"), []string{"runkod", "web"}},
		{"web change", affectedOf("web"), []string{"web"}},
		{"webadmin change", affectedOf("webadmin"), []string{"webadmin"}},
		{"cli change (no image)", affectedOf("cli"), nil},
		{"prose/empty closure (no image)", affectedOf(), nil},
	}
	for _, tc := range cases {
		got := ImagesForAffected(tc.result, topo)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestImagesForAffectedRunEverything: a root build-sensitive change rebuilds
// every declared image (fail closed), regardless of the (informational)
// Projects list, in sorted order.
func TestImagesForAffectedRunEverything(t *testing.T) {
	got := ImagesForAffected(affected.Result{RunEverything: true}, runkoTopology())
	want := []string{"runkod", "web", "webadmin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunEverything: got %v, want %v", got, want)
	}
}

// TestImagesForAffectedDuplicateOwners: two projects declaring the SAME image
// name both count - a change to EITHER rebuilds the image. The derivation
// fails toward rebuilding (never silently drops a declarant), so this can
// never under-build the way a last-writer-wins map would.
func TestImagesForAffectedDuplicateOwners(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "alpha", DeployImage: &index.ImageDecl{Name: "shared"}},
		{Name: "beta", DeployImage: &index.ImageDecl{Name: "shared"}},
	}
	for _, who := range []string{"alpha", "beta"} {
		if got := ImagesForAffected(affectedOf(who), projects); !reflect.DeepEqual(got, []string{"shared"}) {
			t.Fatalf("%s change should rebuild shared, got %v", who, got)
		}
	}
}

// TestImagesForAffectedOwnerAlsoRider: a project may own image X and also run
// a workload on another project's image Y. A change to it rebuilds BOTH.
func TestImagesForAffectedOwnerAlsoRider(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "base", DeployImage: &index.ImageDecl{Name: "base"}},
		{Name: "combo", DeployImage: &index.ImageDecl{Name: "combo"}, RidesImages: []string{"base"}},
	}
	got := ImagesForAffected(affectedOf("combo"), projects)
	if want := []string{"base", "combo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("combo change: got %v, want %v", got, want)
	}
}

// TestImagesForAffectedDanglingRiderIgnored: a workload naming an image no
// project owns builds nothing (it has no Dockerfile). Track A drops it here;
// receive-time validation refuses it as a dangling reference (later step).
func TestImagesForAffectedDanglingRiderIgnored(t *testing.T) {
	projects := []index.IndexedProject{{Name: "ghost", RidesImages: []string{"nonesuch"}}}
	if got := ImagesForAffected(affectedOf("ghost"), projects); len(got) != 0 {
		t.Fatalf("dangling rider should build nothing, got %v", got)
	}
}

// TestImagesForAffectedEmptyIndex is the per-org safety property: an org that
// declares no deploy.image derives an empty set - even on RunEverything - so
// no deploy record opens (this is what closes the old map's phantom-record
// leak for orgs that happen to have a project named "web").
func TestImagesForAffectedEmptyIndex(t *testing.T) {
	if got := ImagesForAffected(affectedOf("anything"), nil); len(got) != 0 {
		t.Fatalf("no images declared -> empty, got %v", got)
	}
	if got := ImagesForAffected(affected.Result{RunEverything: true}, nil); len(got) != 0 {
		t.Fatalf("no images declared -> empty even on RunEverything, got %v", got)
	}
}
