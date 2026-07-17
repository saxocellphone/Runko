package runkod

import (
	"context"
	"log"

	"github.com/saxocellphone/runko/platform/affected"
)

// deployImages is the ordered set of deployable images (§14.10 inverted CD
// trigger). Keeping the affected->image map here (one tree-of-record place,
// not inline in the CI workflow) makes the build workflow a generic executor:
// it builds exactly the images runkod's deploy record names.
var deployImages = []string{"runkod", "web", "webadmin"}

// imageProjects[image] is the set of projects whose changes require rebuilding
// image (a project plus the ones it embeds). Mirrors the mapping the
// release-images workflow used inline.
var imageProjects = map[string][]string{
	"runkod":   {"runkod", "platform", "internal", "db", "proto", "watchdog", "mailer"},
	"web":      {"web", "proto"},
	"webadmin": {"webadmin"},
}

// imagesForAffected maps an affected result to the deployable images it
// requires rebuilding, in deployImages order. RunEverything (a root
// build-sensitive change) rebuilds every image - fail closed to a broader run
// (§14.5.3); a docs-only / non-deployable land returns none, so no deploy
// record opens and no rollout fires.
func imagesForAffected(result affected.Result) []string {
	if result.RunEverything {
		return append([]string(nil), deployImages...)
	}
	touched := make(map[string]bool, len(result.Projects))
	for _, p := range result.Projects {
		touched[p.Name] = true
	}
	var out []string
	for _, img := range deployImages {
		for _, proj := range imageProjects[img] {
			if touched[proj] {
				out = append(out, img)
				break
			}
		}
	}
	return out
}

// openDeployRecordForLand opens the deploy record for a just-landed commit
// (§14.10): it computes the images the landed range affects and records them
// as the set the post-land build must report digests for before the rollout
// fires. Best-effort - a failure logs and returns, never failing an
// already-durable land. Requires a Processor (the index scanner); a daemon
// without one (some tests) simply opens no record.
func (s *Server) openDeployRecordForLand(ctx context.Context, change Change, landedSHA string) {
	if s.Processor == nil {
		return
	}
	// The landed range is prev-trunk..landedSHA. Trunk is linear (rebase
	// landing, no merges), so landedSHA's first parent IS the prior trunk tip
	// and this diff is exactly the change's own footprint.
	changedPaths, err := gitDiffNamesOnly(s.RepoDir, landedSHA+"^", landedSHA)
	if err != nil {
		log.Printf("runkod: %s: deploy record: diff landed range: %v", change.ChangeKey, err)
		return
	}
	result, _, err := s.Processor.computeAffectedForChange(Change{HeadSHA: landedSHA}, changedPaths, nil)
	if err != nil {
		log.Printf("runkod: %s: deploy record: affected: %v", change.ChangeKey, err)
		return
	}
	images := imagesForAffected(result)
	if len(images) == 0 {
		return // docs-only / non-deployable land: no images, no rollout
	}
	if err := s.Store.OpenDeployRecord(ctx, landedSHA, change.ChangeKey, "runko change "+change.ChangeKey, images); err != nil {
		log.Printf("runkod: %s: open deploy record: %v", change.ChangeKey, err)
	}
}
