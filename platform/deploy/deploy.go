// Package deploy computes which deployable images a landed change must
// rebuild, from the tree's `deploy` capability declarations (§14.10,
// docs/spec/deploy/README.md). It is the per-org, manifest-derived
// replacement for the hardcoded image->project map that lived in
// runkod/deployimages.go: same computation, but read from the indexed tree
// so it is correct for every org and stays in sync with the manifests.
package deploy

import (
	"sort"

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
