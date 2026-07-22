package runkod

import (
	"context"
	"log"

	"github.com/saxocellphone/runko/platform/deploy"
)

// openDeployRecordForLand opens the deploy record for a just-landed commit
// (§14.10): it computes the deployable images the landed range affects -
// per-org, from the tree's `deploy` capability declarations
// (platform/deploy.ImagesForAffected over the landed head's index, replacing
// the retired hardcoded image->project map; docs/spec/deploy/README.md) - and
// records them as the set the post-land build must report digests for before
// the rollout fires. Best-effort - a failure logs and returns, never failing
// an already-durable land. Requires a Processor (the index scanner); a daemon
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
	result, indexed, err := s.Processor.computeAffectedForChange(Change{HeadSHA: landedSHA}, changedPaths, nil)
	if err != nil {
		log.Printf("runkod: %s: deploy record: affected: %v", change.ChangeKey, err)
		return
	}
	// The index is the landing ORG's own tree, so images are derived per-org:
	// an org that declares no deploy.image derives none, and no phantom record
	// opens (the retired name-matching map opened one for any org with a
	// project merely named "web"/"runkod").
	images := deploy.ImagesForAffected(result, indexed)
	if len(images) == 0 {
		return // docs-only / non-deployable land: no images, no rollout
	}
	if err := s.Store.OpenDeployRecord(ctx, landedSHA, change.ChangeKey, "runko change "+change.ChangeKey, images); err != nil {
		log.Printf("runkod: %s: open deploy record: %v", change.ChangeKey, err)
	}
}
