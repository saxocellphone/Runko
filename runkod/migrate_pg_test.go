package runkod

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saxocellphone/runko/internal/dbtest"
)

// TestPostgresApplyMigrationsFromEmptyDatabase is stage 14's schema-upgrade
// path (§16.4) against a real Postgres: from a completely empty schema (the
// `docker compose up` starting state the smoke test found broken),
// ApplyMigrations brings everything up, a second call is a no-op, and
// BootstrapPostgresStore works end to end on the result. Named to match
// check-db's -run Postgres filter, like every live-Postgres test here.
//
// dbtest.Lock, not Connect: this test wants an EMPTY database, not a
// migrated one, but it must still hold the harness lock - its DROP SCHEMA
// CASCADE against the shared database is precisely the operation that must
// never interleave with another package's reset (it did, for four
// consecutive post-land check-db runs, once -p 1 stopped serializing
// externally: "relation orgs already exists" in whichever package lost).
func TestPostgresApplyMigrationsFromEmptyDatabase(t *testing.T) {
	dsn := dbtest.Lock(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// The compose starting state: nothing, not even the tracking table.
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA public"); err != nil {
		t.Fatalf("recreate schema: %v", err)
	}

	ran, err := ApplyMigrations(ctx, pool)
	if err != nil {
		t.Fatalf("ApplyMigrations (fresh): %v", err)
	}
	if len(ran) < 3 || ran[0] != "0001_init" {
		t.Fatalf("expected all migrations applied in order starting at 0001_init, got %v", ran)
	}

	again, err := ApplyMigrations(ctx, pool)
	if err != nil {
		t.Fatalf("ApplyMigrations (repeat): %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected the second run to be a no-op, got %v", again)
	}

	// The migrated schema actually works: the full bootstrap path the
	// daemon takes at startup (which itself re-runs ApplyMigrations).
	store, err := BootstrapPostgresStore(ctx, dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore: %v", err)
	}
	defer store.Pool.Close()
	if _, err := store.CreateOrUpdateChange(ctx, "Imig", "b", "h", "r", "t", "alice", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange on migrated schema: %v", err)
	}
}
