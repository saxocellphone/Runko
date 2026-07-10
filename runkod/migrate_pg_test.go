package runkod

import (
	"context"
	"os"
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
func TestPostgresApplyMigrationsFromEmptyDatabase(t *testing.T) {
	// dbtest.Connect both skips without a DSN and - critically - holds the
	// cross-process harness lock for this test's lifetime. This test drops
	// and recreates the whole schema on the SHARED test database; it was
	// the one live-Postgres test that took the DSN directly, so it could
	// run concurrently with another package's freshly-reset schema and
	// re-create tables under it (post-land CI 2026-07-10: platform/checks
	// failed its reset with "orgs already exists" - this test's
	// ApplyMigrations had landed between checks' teardown and its 0001).
	dbtest.Connect(t)
	dsn := os.Getenv("RUNKO_TEST_DATABASE_URL")
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
