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
// Verified against a live Postgres (§28.3 stage 9d's CI, `make check-db`):
// the first real run caught a genuine bug sqlc's own schema analysis could
// never have found - projects.capabilities and .declared_dependencies are
// NOT NULL, but a project with neither in its PROJECT.yaml (the common
// case: both are optional per §6.2's layering rule) unmarshals to a nil Go
// slice, which pgx sends as SQL NULL, not an empty array. nonNilStrings
// below normalizes at this persistence boundary, the same fix
// checks.MergeRequirements' MarshalJSON already applies for JSON's
// analogous null-vs-[] distinction.
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
			Capabilities:         nonNilStrings(p.Capabilities),
			DeclaredDependencies: nonNilStrings(p.DeclaredDependencies),
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

// nonNilStrings replaces a nil slice with an empty one - projects.capabilities
// and .declared_dependencies are NOT NULL columns, but pgx encodes a nil Go
// []string as SQL NULL rather than an empty array.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
