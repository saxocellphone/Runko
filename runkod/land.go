package runkod

import (
	"context"
	"fmt"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/land"
)

// maxLandRaceRetries bounds the caller-side retry loop land.Land's own doc
// comment calls for ("this function does not loop internally; the caller
// decides retry policy - a bounded loop today, a merge queue in v1.x").
// Land races are the norm under concurrent landers (§13.5); a handful of
// attempts rides that out. If trunk is still moving after this many
// attempts, that's a signal worth surfacing to the client as
// race_retry_exhausted, not a reason to loop forever.
const maxLandRaceRetries = 5

// attemptLand runs land.Land against the current trunk tip, retrying on
// RaceRetry: each attempt re-reads the trunk tip and recomputes the trunk
// delta from scratch, since a losing attempt's view of trunk is stale by
// construction the moment it loses the compare-and-swap.
//
// base is passed to land.Land AS-IS (possibly "") - land.Land's own
// bootstrap handling (§28.3 stage 11b: landing onto an unborn trunk, the
// only way a brand-new monorepo's first Change can ever reach trunk, given
// §6.9's closed-trunk policy) specifically compares the unresolved trunk
// tip against an empty string, not emptyTreeOID. A separate diffBase
// (emptyTreeOID when base is "") is used only for gitDiffNamesOnly, which
// needs a real git object to diff against.
func (s *Server) attemptLand(ctx context.Context, change Change) (land.Outcome, error) {
	gstore := gitstore.New(s.RepoDir)

	base := change.BaseSHA
	diffBase := base
	if diffBase == "" {
		diffBase = emptyTreeOID
	}

	var rootInvalidation []string
	if s.Processor != nil {
		rootInvalidation = s.Processor.RootInvalidationPatterns
	}
	opts := affected.Options{RootInvalidationPatterns: rootInvalidation}

	for attempt := 0; attempt < maxLandRaceRetries; attempt++ {
		var projects []affected.ProjectInfo
		if _, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef); err == nil {
			indexed, err := index.Scan(gstore, core.Revision("refs/heads/"+s.TrunkRef), nil)
			if err != nil {
				return land.Outcome{}, fmt.Errorf("land: scan projects: %w", err)
			}
			projects = make([]affected.ProjectInfo, len(indexed))
			for i, p := range indexed {
				projects[i] = affected.ProjectInfo{Name: p.Name, Path: p.Path, DeclaredDependencies: p.DeclaredDependencies}
			}
		}
		// else: trunk has no commits yet - no projects exist to scan,
		// matching land.Land's own unborn-trunk bootstrap path below.

		changedPaths, err := gitDiffNamesOnly(s.RepoDir, diffBase, change.HeadSHA)
		if err != nil {
			return land.Outcome{}, fmt.Errorf("land: diff change: %w", err)
		}
		changeAffected := affected.Compute(projects, changedPaths, opts)

		meta := core.CommitMeta{Message: change.Title + "\n\nChange-Id: " + change.ChangeKey + "\n"}

		outcome, err := land.Land(gstore, s.RepoDir, s.TrunkRef, base, change.HeadSHA,
			land.RevalidationAffectedIntersection, changeAffected, projects, opts, meta)
		if err != nil {
			return land.Outcome{}, err
		}
		if !outcome.RaceRetry {
			return outcome, nil
		}
	}
	return land.Outcome{RaceRetry: true}, nil
}
