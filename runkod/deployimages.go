package runkod

import (
	"context"
	"log"
	"time"

	"github.com/saxocellphone/runko/platform/deploy"
)

// deployRecordTTL bounds how long a 'pending' deploy record may sit before
// it's pruned as never-going-to-complete. An org that pins image digests in CI
// (not via report-image) never reports, so its records never flip to 'ready';
// this keeps them from accruing. Generous - well past any real post-land build
// - so a genuinely in-flight report is never dropped.
const deployRecordTTL = 24 * time.Hour

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
	// Opportunistically drop this org's stale pending records before opening a
	// new one: an org that pins digests in CI (not report-image) never
	// completes a record, so without a prune they accrue. Best-effort - a
	// failure here must never fail an already-durable land.
	if n, err := s.Store.PruneStalePendingDeployRecords(ctx, time.Now().Add(-deployRecordTTL)); err != nil {
		log.Printf("runkod: %s: prune stale deploy records: %v", change.ChangeKey, err)
	} else if n > 0 {
		log.Printf("runkod: pruned %d stale pending deploy record(s)", n)
	}
	if err := s.Store.OpenDeployRecord(ctx, landedSHA, change.ChangeKey, "runko change "+change.ChangeKey, images); err != nil {
		log.Printf("runkod: %s: open deploy record: %v", change.ChangeKey, err)
	}
}
