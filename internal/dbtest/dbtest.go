// Package dbtest is a real-Postgres test harness for the persistence wiring
// that stages 2/4/6/8 left "unverified against a live Postgres in this
// environment" (see CLAUDE.md - no Docker/Postgres in the sandbox these
// packages were originally built in). It exists so that wherever a real
// Postgres IS available (a dev machine, CI with a database service), the
// same test files exercise index.Sync, receive.CreateOrUpdateChange, and
// checks.RerunCheck/EnqueueWebhook/RecordDeliveryResult against a real
// database - not sqlc's own schema/query analysis, which only proves the SQL
// parses, never that it behaves correctly under a real engine (constraints,
// defaults, enum coercion, JSONB round-tripping, etc).
//
// Tests using Connect skip (not fail) when RUNKO_TEST_DATABASE_URL is unset,
// so `go test ./...` stays green with no Postgres present; `make check-db`
// is the entry point for running them for real.
package dbtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saxocellphone/runko/internal/dbgen"
)

// envVar is the DSN tests read; matches db/README.md and the Makefile's
// check-db target.
const envVar = "RUNKO_TEST_DATABASE_URL"

// Connect returns a pool against RUNKO_TEST_DATABASE_URL with the schema
// reset to a clean, freshly-migrated state, or skips the test if the env
// var isn't set.
func Connect(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(envVar)
	if dsn == "" {
		t.Skipf("%s not set - skipping live-Postgres test (see db/README.md, `make check-db`)", envVar)
	}

	resetSchema(t, dsn)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// resetSchema applies every db/migrations/NNNN_*.{down,up}.sql via psql so
// each test starts from an empty, freshly-migrated schema - downs in
// reverse numeric order, then ups in numeric order, the same sequence a
// real migration runner would produce. Shelling out to psql (rather than
// adding a migration-runner dependency) keeps this a "real Postgres" test
// without a new mid-session dependency (CLAUDE.md rule).
func resetSchema(t *testing.T, dsn string) {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "db", "migrations")
	ups, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(ups) == 0 {
		t.Fatalf("dbtest: find migrations in %s: %v", dir, err)
	}
	downs, err := filepath.Glob(filepath.Join(dir, "*.down.sql"))
	if err != nil {
		t.Fatalf("dbtest: find down migrations in %s: %v", dir, err)
	}
	sort.Strings(ups)
	sort.Sort(sort.Reverse(sort.StringSlice(downs)))

	// Best-effort teardown first (ignore errors: first run against a fresh
	// database has nothing to tear down yet). The migration-tracking table
	// isn't any down file's concern (runkod.ApplyMigrations owns it), so
	// drop it here too.
	for _, down := range downs {
		exec.Command("psql", dsn, "-q", "-f", down).Run()
	}
	exec.Command("psql", dsn, "-q", "-c", "DROP TABLE IF EXISTS runko_schema_migrations").Run()

	versions := make([]string, 0, len(ups))
	for _, up := range ups {
		cmd := exec.Command("psql", dsn, "-v", "ON_ERROR_STOP=1", "-q", "-f", up)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dbtest: apply migration %s: %v: %s", up, err, out)
		}
		versions = append(versions, fmt.Sprintf("('%s')", strings.TrimSuffix(filepath.Base(up), ".up.sql")))
	}

	// Record what was applied in the same table runkod.ApplyMigrations
	// uses, so a daemon booting against a dbtest-prepared database (the
	// compiled-binary e2e tests) sees the schema as current instead of
	// re-running migration 0001 into existing tables.
	record := "CREATE TABLE IF NOT EXISTS runko_schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()); " +
		"INSERT INTO runko_schema_migrations (version) VALUES " + strings.Join(versions, ", ")
	if out, err := exec.Command("psql", dsn, "-v", "ON_ERROR_STOP=1", "-q", "-c", record).CombinedOutput(); err != nil {
		t.Fatalf("dbtest: record applied migrations: %v: %s", err, out)
	}
}

// Fixture is the common (org, monorepo, actor) triple nearly every
// persistence test needs as FK parents, seeded once per test via Seed.
type Fixture struct {
	OrgID      uuid.UUID
	MonorepoID uuid.UUID
	ActorID    uuid.UUID
}

// Seed creates one org, its primary monorepo, and one human actor - the
// minimal FK-satisfying fixture shared by index/receive/checks persistence
// tests. name should be unique per test (e.g. t.Name()) since orgs.name is
// UNIQUE.
func Seed(t *testing.T, ctx context.Context, db dbgen.DBTX, name string) Fixture {
	t.Helper()
	q := dbgen.New()

	org, err := q.CreateOrg(ctx, db, name)
	if err != nil {
		t.Fatalf("dbtest: seed org: %v", err)
	}
	repo, err := q.CreateMonorepo(ctx, db, dbgen.CreateMonorepoParams{OrgID: org.ID, TrunkRef: "main"})
	if err != nil {
		t.Fatalf("dbtest: seed monorepo: %v", err)
	}
	actor, err := q.UpsertActor(ctx, db, dbgen.UpsertActorParams{
		OrgID:       org.ID,
		Type:        dbgen.ActorTypeUser,
		ExternalRef: fmt.Sprintf("user:%s", name),
		Metadata:    []byte("{}"),
	})
	if err != nil {
		t.Fatalf("dbtest: seed actor: %v", err)
	}
	return Fixture{OrgID: org.ID, MonorepoID: repo.ID, ActorID: actor.ID}
}

// repoRoot walks up from this source file's directory to find the module
// root (the directory containing go.mod), so tests work regardless of the
// working directory `go test` is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("dbtest: could not determine source file location")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("dbtest: could not find go.mod above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}
