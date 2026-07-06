package index

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/saxocellphone/runko/internal/dbgen"
)

// Sync replaces one monorepo's project index wholesale with the given scan
// results - a full rebuild, not an incremental diff, matching the
// rebuildable-index philosophy (§10.3): re-deriving from the tree is always
// cheap and always correct, so there is no drift to reconcile.
//
// Known limitation: because this deletes and recreates every project row,
// control-plane-only fields not present in the tree (template_id,
// created_at) do not survive a rebuild - a future session should switch to
// an upsert keyed on (monorepo_id, name) if that continuity turns out to
// matter before v1 ships.
//
// Untested against a live Postgres in this environment (no Docker/Postgres
// available here - see CLAUDE.md); the query shapes are sqlc-verified
// (internal/dbgen) but this wiring has not been run against a real database.
func Sync(ctx context.Context, db dbgen.DBTX, q *dbgen.Queries, monorepoID uuid.UUID, indexedAtSHA string, projects []IndexedProject) error {
	if err := q.DeleteProjectsForMonorepo(ctx, db, monorepoID); err != nil {
		return fmt.Errorf("index: clear existing projects: %w", err)
	}

	for _, p := range projects {
		row, err := q.CreateProject(ctx, db, dbgen.CreateProjectParams{
			MonorepoID:           monorepoID,
			Name:                 p.Name,
			Path:                 p.Path,
			ProjectType:          p.Type,
			Visibility:           p.Visibility,
			Capabilities:         p.Capabilities,
			DeclaredDependencies: p.DeclaredDependencies,
			IndexedAtSha:         indexedAtSHA,
		})
		if err != nil {
			return fmt.Errorf("index: create project %s: %w", p.Name, err)
		}
		for _, o := range p.Owners {
			if err := q.InsertProjectOwner(ctx, db, dbgen.InsertProjectOwnerParams{
				ProjectID: row.ID,
				OwnerRef:  o.Ref,
				Source:    o.Source,
			}); err != nil {
				return fmt.Errorf("index: insert owner %s for project %s: %w", o.Ref, p.Name, err)
			}
		}
	}
	return nil
}
