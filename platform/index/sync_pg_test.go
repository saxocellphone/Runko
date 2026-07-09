package index

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/saxocellphone/runko/internal/dbgen"
	"github.com/saxocellphone/runko/internal/dbtest"
)

// TestSyncAgainstLivePostgres exercises Sync (§10.3's "rebuildable index")
// against a real database: a first sync creates project + owner rows, and a
// second sync with a different project set proves the "wholesale replace"
// behavior sync.go documents - stale rows from the first sync must not
// survive, without relying on sqlc's schema analysis standing in for the
// real thing.
//
// Skips unless RUNKO_TEST_DATABASE_URL is set (see internal/dbtest, db/README.md).
func TestSyncAgainstLivePostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Connect(t)
	fx := dbtest.Seed(t, ctx, db, t.Name())
	q := dbgen.New()

	first := []IndexedProject{
		{
			Name: "checkout-api", Path: "commerce/checkout", Type: "service",
			Owners: []OwnerEntry{{Ref: "group:commerce-eng", Source: "project_manifest"}},
		},
		{
			Name: "billing-lib", Path: "libs/billing", Type: "library",
			DeclaredDependencies: []string{"checkout-api"},
			Owners:               []OwnerEntry{{Ref: "group:payments-eng", Source: "path_owners"}},
		},
	}
	if err := Sync(ctx, db, q, fx.MonorepoID, "sha1", first); err != nil {
		t.Fatalf("Sync (first): %v", err)
	}

	rows, err := q.ListProjects(ctx, db, listAllParams(fx.MonorepoID))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 projects after first sync, got %d", len(rows))
	}

	var checkoutID, billingID uuid.UUID
	for _, r := range rows {
		switch r.Name {
		case "checkout-api":
			checkoutID = r.ID
		case "billing-lib":
			billingID = r.ID
			if len(r.DeclaredDependencies) != 1 || r.DeclaredDependencies[0] != "checkout-api" {
				t.Fatalf("expected billing-lib to declare checkout-api as a dependency, got %v", r.DeclaredDependencies)
			}
		}
	}
	if checkoutID == uuid.Nil || billingID == uuid.Nil {
		t.Fatalf("expected both projects present, got %+v", rows)
	}

	owners, err := q.ListProjectOwners(ctx, db, checkoutID)
	if err != nil {
		t.Fatalf("ListProjectOwners: %v", err)
	}
	if len(owners) != 1 || owners[0].OwnerRef != "group:commerce-eng" {
		t.Fatalf("expected checkout-api owned by group:commerce-eng, got %+v", owners)
	}

	// Second sync drops billing-lib entirely and renames checkout-api's path -
	// a wholesale rebuild, not an incremental diff.
	second := []IndexedProject{
		{
			Name: "checkout-api", Path: "commerce/checkout-v2", Type: "service",
			Owners: []OwnerEntry{{Ref: "group:commerce-eng", Source: "project_manifest"}},
		},
	}
	if err := Sync(ctx, db, q, fx.MonorepoID, "sha2", second); err != nil {
		t.Fatalf("Sync (second): %v", err)
	}

	rows, err = q.ListProjects(ctx, db, listAllParams(fx.MonorepoID))
	if err != nil {
		t.Fatalf("ListProjects after second sync: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 project after the second sync dropped billing-lib, got %d: %+v", len(rows), rows)
	}
	if rows[0].Path != "commerce/checkout-v2" {
		t.Fatalf("expected the rebuilt row to reflect the new path, got %q", rows[0].Path)
	}
	if rows[0].IndexedAtSha != "sha2" {
		t.Fatalf("expected indexed_at_sha to reflect the second sync, got %q", rows[0].IndexedAtSha)
	}
}

func listAllParams(monorepoID uuid.UUID) dbgen.ListProjectsParams {
	return dbgen.ListProjectsParams{MonorepoID: monorepoID, Column2: "", Limit: 100, Offset: 0}
}
