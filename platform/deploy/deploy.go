// Package deploy computes which deployable images a landed change must
// rebuild, from the tree's `deploy` capability declarations (§14.10,
// docs/spec/deploy/README.md). It is the per-org, manifest-derived
// replacement for the hardcoded image->project map that lived in
// runkod/deployimages.go: same computation, but read from the indexed tree
// so it is correct for every org and stays in sync with the manifests.
package deploy

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/index"
)

// ImagesForAffected returns the deployable image names a change with this
// affected result must rebuild, given the full project index at the change's
// head (the same []IndexedProject a live index.Scan produces). The rule is a
// POST-FILTER over the affected result, never a new closure:
//
//	rebuild(image I) iff result.Projects ∩ ({owner(I)} ∪ riders(I)) ≠ ∅
//
// where owner(I) is the project declaring deploy.image.name == I and riders(I)
// are the projects whose deploy.workloads run from I. RunEverything rebuilds
// every declared image (fail closed, §14.5.3). The dependency closure is not
// enumerated: a change to a project the owner depends on already places the
// owner in result.Projects via the ordinary dependents expansion, so it
// intersects {owner(I)} for free.
//
// This READS result.Projects and never mutates it - riders are not added to
// the affected set, so the merge gate (which shares result.Projects via
// index.ChecksFor) is never widened by image-rebuild computation.
//
// Only OWNED images (those with a declaring project) are returned; a workload
// referencing an image no project owns is ignored here (it builds nothing -
// caught as a dangling reference by receive-time validation, a later step).
// The result is sorted for determinism.
func ImagesForAffected(result affected.Result, projects []index.IndexedProject) []string {
	// members[image] = every project whose change must rebuild that image -
	// the image's owner(s) AND every rider. owned records which image names
	// have at least one declaring owner (only those are rebuildable; a
	// workload riding an unowned image builds nothing). Multiple projects
	// declaring the same image name ALL count: we fail toward rebuilding, so
	// a duplicate never silently drops a declarant's changes from CD.
	members := map[string][]string{}
	owned := map[string]bool{}
	for _, p := range projects {
		if p.DeployImage != nil {
			members[p.DeployImage.Name] = append(members[p.DeployImage.Name], p.Name)
			owned[p.DeployImage.Name] = true
		}
		for _, img := range p.RidesImages {
			members[img] = append(members[img], p.Name)
		}
	}

	imageNames := make([]string, 0, len(owned))
	for img := range owned {
		imageNames = append(imageNames, img)
	}
	sort.Strings(imageNames)

	if len(imageNames) == 0 {
		return nil
	}
	if result.RunEverything {
		return imageNames
	}

	touched := make(map[string]bool, len(result.Projects))
	for _, pr := range result.Projects {
		touched[pr.Name] = true
	}

	var out []string
	for _, img := range imageNames {
		for _, proj := range members[img] {
			if touched[proj] {
				out = append(out, img)
				break
			}
		}
	}
	return out
}

// ImageBuild is one deployable image the CI executor must build: its logical
// name, the full registry-qualified ref to tag/push/report, plus everything
// needed to build it, read from the OWNER's deploy.image (§14.9.1 executor
// contract - the workflow gets build config from the tree, never hardcoded).
// JSON-tagged for `runko-ci images`.
type ImageBuild struct {
	Name string `json:"name"`
	// ImageRef is <deploy_registry>/<name> when the root manifest sets
	// deploy_registry, else the bare name (local/dev). The generic image-build
	// workflow tags/pushes/reports this, hardcoding no registry.
	ImageRef   string            `json:"image_ref"`
	Context    string            `json:"context"`
	Dockerfile string            `json:"dockerfile"`
	BuildArgs  map[string]string `json:"build_args,omitempty"`
}

// ImageBuildsForAffected returns an ImageBuild for each image
// ImagesForAffected names, resolving the build config from that image's
// owning project's deploy.image. In owned-image (sorted) order; nil when no
// image rebuilds.
//
// Two projects declaring the SAME image name with DIFFERENT build config is a
// structured `ambiguous_image` error - the executor cannot know which context/
// dockerfile/build_args to build, and silently picking one would BUILD one
// project's config for a rebuild the other project's change triggered, then
// deploy that wrong artifact invisibly. This mirrors cli/runko-ci's
// `ambiguous_check` (a red, visible refusal beats a wrong deploy); identical
// duplicate declarations dedupe, exactly as ambiguous_check tolerates
// same-name-same-command. Only images in the rebuild set are checked, so an
// unrelated conflict elsewhere in the tree never fails an unaffected build.
func ImageBuildsForAffected(result affected.Result, projects []index.IndexedProject) ([]ImageBuild, error) {
	names := ImagesForAffected(result, projects)
	if len(names) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	registry := rootRegistry(projects)
	type owned struct {
		build ImageBuild
		owner string
	}
	byName := map[string]owned{}
	for _, p := range projects {
		if p.DeployImage == nil || !want[p.DeployImage.Name] {
			continue
		}
		b := ImageBuild{Name: p.DeployImage.Name, ImageRef: imageRef(registry, p.DeployImage.Name), Context: p.DeployImage.Context, Dockerfile: p.DeployImage.Dockerfile, BuildArgs: p.DeployImage.BuildArgs}
		if prev, ok := byName[b.Name]; ok {
			if !sameBuild(prev.build, b) {
				return nil, &clierr.Error{
					Code:       "ambiguous_image",
					Field:      "capability_config.deploy.image",
					Message:    fmt.Sprintf("image %q is declared with different build config by projects %q and %q", b.Name, prev.owner, p.Name),
					Suggestion: "give the images distinct names, or align their context/dockerfile/build_args - the executor must know which to build",
					DocURL:     "docs/spec/deploy/README.md",
				}
			}
			continue
		}
		byName[b.Name] = owned{build: b, owner: p.Name}
	}
	out := make([]ImageBuild, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n].build) // present: ImagesForAffected returns only owned images
	}
	return out, nil
}

func sameBuild(a, b ImageBuild) bool {
	return a.Context == b.Context && a.Dockerfile == b.Dockerfile && reflect.DeepEqual(a.BuildArgs, b.BuildArgs)
}

// rootRegistry returns the repo-wide deploy_registry declared on the root
// project (path "" or "."), or "" when unset or no root is indexed.
func rootRegistry(projects []index.IndexedProject) string {
	for _, p := range projects {
		if p.Path == "" || p.Path == "." {
			return p.DeployRegistry
		}
	}
	return ""
}

// imageRef forms an image's full ref: <registry>/<name>, or the bare name
// when no registry is declared (local/dev; a real registry push needs it set).
func imageRef(registry, name string) string {
	if registry == "" {
		return name
	}
	return strings.TrimRight(registry, "/") + "/" + name
}
