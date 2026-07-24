package deploy

import (
	"reflect"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
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

// TestImageBuildsForAffected: the image NAMES from ImagesForAffected, each
// resolved to its owner's build config (context/dockerfile/build_args).
func TestImageBuildsForAffected(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "runkod", DeployImage: &index.ImageDecl{Name: "runkod", Context: ".", Dockerfile: "Dockerfile"}},
		{Name: "web", DeployImage: &index.ImageDecl{Name: "web", Context: "web", Dockerfile: "web/Dockerfile", BuildArgs: map[string]string{"VITE_RUNKO_URL": "/"}}},
		{Name: "mailer", RidesImages: []string{"runkod"}},
	}
	// a rider change carries the OWNER's build config, not the rider's. No
	// root deploy_registry here, so image_ref is the bare name.
	got, err := ImageBuildsForAffected(affectedOf("mailer"), projects)
	if err != nil {
		t.Fatalf("mailer change: unexpected err %v", err)
	}
	want := []ImageBuild{{Name: "runkod", ImageRef: "runkod", Context: ".", Dockerfile: "Dockerfile"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mailer change: got %+v, want %+v", got, want)
	}
	// RunEverything -> every owned image, sorted, each with its config.
	all, err := ImageBuildsForAffected(affected.Result{RunEverything: true}, projects)
	if err != nil {
		t.Fatalf("RunEverything: unexpected err %v", err)
	}
	if len(all) != 2 || all[0].Name != "runkod" || all[1].Name != "web" || all[1].BuildArgs["VITE_RUNKO_URL"] != "/" {
		t.Fatalf("RunEverything build set: got %+v", all)
	}
	// nothing deployable affected -> nil.
	if got, err := ImageBuildsForAffected(affectedOf("unrelated"), projects); err != nil || got != nil {
		t.Fatalf("no image -> nil, got %+v err %v", got, err)
	}
}

// TestImageBuildsForAffectedRegistry: a root deploy_registry prefixes every
// image's ref (<registry>/<name>); a trailing slash on the registry is
// trimmed; the image's own name is unchanged.
func TestImageBuildsForAffectedRegistry(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "root", Path: "", DeployRegistry: "ghcr.io/acme/monorepo/"},
		{Name: "api", DeployImage: &index.ImageDecl{Name: "api", Context: ".", Dockerfile: "Dockerfile"}},
	}
	got, err := ImageBuildsForAffected(affectedOf("api"), projects)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" || got[0].ImageRef != "ghcr.io/acme/monorepo/api" {
		t.Fatalf("image_ref should be <registry>/<name>, got %+v", got)
	}

	// RunEverything shares the registry resolution path.
	all, err := ImageBuildsForAffected(affected.Result{RunEverything: true}, projects)
	if err != nil || len(all) != 1 || all[0].ImageRef != "ghcr.io/acme/monorepo/api" {
		t.Fatalf("RunEverything must carry the registry ref, got %+v err %v", all, err)
	}

	// deploy_registry is ROOT-ONLY: set on a non-root project it is silently
	// unread, so image_ref falls back to the bare name.
	nonRoot := []index.IndexedProject{
		{Name: "sub", Path: "sub", DeployRegistry: "ghcr.io/ignored"},
		{Name: "api", DeployImage: &index.ImageDecl{Name: "api", Context: ".", Dockerfile: "Dockerfile"}},
	}
	got, err = ImageBuildsForAffected(affectedOf("api"), nonRoot)
	if err != nil || len(got) != 1 || got[0].ImageRef != "api" {
		t.Fatalf("a non-root deploy_registry must be ignored (bare name), got %+v err %v", got, err)
	}
}

// TestImageBuildsForAffectedDuplicateOwners: two projects declaring the same
// image name with IDENTICAL config dedupe silently; with DIFFERENT config it
// is a structured ambiguous_image error - only when that image is in the
// rebuild set (an unrelated conflict never fails an unaffected build).
func TestImageBuildsForAffectedDuplicateOwners(t *testing.T) {
	same := []index.IndexedProject{
		{Name: "alpha", DeployImage: &index.ImageDecl{Name: "shared", Context: "x", Dockerfile: "x/Dockerfile"}},
		{Name: "beta", DeployImage: &index.ImageDecl{Name: "shared", Context: "x", Dockerfile: "x/Dockerfile"}},
	}
	got, err := ImageBuildsForAffected(affectedOf("alpha"), same)
	if err != nil || len(got) != 1 || got[0].Name != "shared" {
		t.Fatalf("identical duplicate owners should dedupe, got %+v err %v", got, err)
	}

	conflict := []index.IndexedProject{
		{Name: "alpha", DeployImage: &index.ImageDecl{Name: "shared", Context: "a", Dockerfile: "a/Dockerfile"}},
		{Name: "beta", DeployImage: &index.ImageDecl{Name: "shared", Context: "b", Dockerfile: "b/Dockerfile"}},
	}
	// the conflicting image is in the rebuild set -> error
	if _, err := ImageBuildsForAffected(affectedOf("alpha"), conflict); err == nil {
		t.Fatalf("conflicting duplicate owners must be an ambiguous_image error")
	} else if ce, ok := err.(*clierr.Error); !ok || ce.Code != "ambiguous_image" {
		t.Fatalf("want ambiguous_image clierr, got %T %v", err, err)
	}
	// the conflict is NOT in the rebuild set (nothing affected) -> no error
	if _, err := ImageBuildsForAffected(affectedOf("unrelated"), conflict); err != nil {
		t.Fatalf("an unaffected conflict must not fail the build, got %v", err)
	}
}

// TestBinaryReleasesForAffected pins the standalone-release rule
// (2026-07-24): a project's binaries republish iff the project itself is
// affected - the dependency closure already carries "anything it builds
// from changed", which is exactly the hand-maintained project list this
// retires from the binary-release workflow.
func TestBinaryReleasesForAffected(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "cli", DeployBinaryTag: "cli-latest", DeployBinaries: []index.BinaryDecl{
			{Name: "runko", Path: "cli/runko"}, {Name: "runko-ci", Path: "cli/runko-ci"}}},
		{Name: "platform"},
	}

	for _, tc := range []struct {
		name string
		res  affected.Result
		want int // releases
	}{
		{"cli affected directly", affectedOf("cli"), 1},
		{"cli in closure only", affectedOf("platform", "cli"), 1},
		{"unrelated change", affectedOf("platform"), 0},
		{"run_everything fail closed", affected.Result{RunEverything: true}, 1},
	} {
		got, err := BinaryReleasesForAffected(tc.res, projects)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != tc.want {
			t.Fatalf("%s: want %d releases, got %+v", tc.name, tc.want, got)
		}
		if tc.want == 1 {
			rel := got[0]
			if rel.Tag != "cli-latest" || len(rel.Binaries) != 2 || rel.Binaries[0].Name != "runko" || rel.Binaries[1].Name != "runko-ci" {
				t.Fatalf("%s: release wrong: %+v", tc.name, rel)
			}
		}
	}
}

// TestBinaryReleasesMergeAndAmbiguity: two projects under one tag merge into
// one release; the same name with different paths is ambiguous_binary
// (identical duplicates dedupe - the ambiguous_image posture).
func TestBinaryReleasesMergeAndAmbiguity(t *testing.T) {
	merge := []index.IndexedProject{
		{Name: "a", DeployBinaryTag: "tools-latest", DeployBinaries: []index.BinaryDecl{{Name: "one", Path: "a/one"}}},
		{Name: "b", DeployBinaryTag: "tools-latest", DeployBinaries: []index.BinaryDecl{{Name: "two", Path: "b/two"}, {Name: "one", Path: "a/one"}}},
	}
	got, err := BinaryReleasesForAffected(affectedOf("a", "b"), merge)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(got) != 1 || len(got[0].Binaries) != 2 {
		t.Fatalf("merge: want one release with two binaries, got %+v", got)
	}

	conflict := []index.IndexedProject{
		{Name: "a", DeployBinaryTag: "tools-latest", DeployBinaries: []index.BinaryDecl{{Name: "one", Path: "a/one"}}},
		{Name: "b", DeployBinaryTag: "tools-latest", DeployBinaries: []index.BinaryDecl{{Name: "one", Path: "b/one"}}},
	}
	_, err = BinaryReleasesForAffected(affectedOf("a", "b"), conflict)
	if err == nil {
		t.Fatal("conflicting duplicate binaries must be an ambiguous_binary error")
	} else if ce, ok := err.(*clierr.Error); !ok || ce.Code != "ambiguous_binary" {
		t.Fatalf("want ambiguous_binary clierr, got %T %v", err, err)
	}
	// An unaffected conflict never fails an unrelated release.
	if _, err := BinaryReleasesForAffected(affectedOf("a"), conflict); err != nil {
		t.Fatalf("unaffected conflict must not fail: %v", err)
	}
}

// TestRootGitOps: the deploy_gitops target resolves from the root project
// only (the rootRegistry rule applied to the write-back half).
func TestRootGitOps(t *testing.T) {
	withRoot := []index.IndexedProject{
		{Name: "svc", Path: "svc", DeployGitOps: &index.GitOpsDecl{Repository: "wrong/place", Kustomization: "x.yaml"}},
		{Name: "repo", Path: "", DeployGitOps: &index.GitOpsDecl{Repository: "acme/k8s-cluster", Kustomization: "apps/mono/kustomization.yaml"}},
	}
	got := RootGitOps(withRoot)
	if got == nil || got.Repository != "acme/k8s-cluster" || got.Kustomization != "apps/mono/kustomization.yaml" {
		t.Fatalf("RootGitOps: got %+v", got)
	}
	if RootGitOps([]index.IndexedProject{{Name: "repo", Path: ""}}) != nil {
		t.Fatal("root without deploy_gitops must resolve nil")
	}
	if RootGitOps(withRoot[:1]) != nil {
		t.Fatal("no root indexed must resolve nil - a non-root declaration never leaks")
	}
}
