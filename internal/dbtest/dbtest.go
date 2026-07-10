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
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saxocellphone/runko/db"

	"github.com/saxocellphone/runko/internal/dbgen"
)

// envVar is the DSN tests read; matches db/README.md and the Makefile's
// check-db target.
const envVar = "RUNKO_TEST_DATABASE_URL"

// harnessLockKey serializes every Connect-using test across processes: they
// all share one database and resetSchema drops it flat, so two tests running
// concurrently (bazel runs test targets as parallel processes; go test runs
// packages in parallel) would clobber each other mid-flight. A session-level
// advisory lock held for the test's lifetime replaces the external runner
// flags (-p 1 / --local_test_jobs=1) that used to do this. Distinct from
// runkod's migrationLockKey ("RUNKO") - the daemon e2e tests boot a real
// daemon (which takes that lock during ApplyMigrations) WHILE the test holds
// this one.
const harnessLockKey = 0x52554e4b4f5f4442 // "RUNKO_DB"

// Connect returns a pool against RUNKO_TEST_DATABASE_URL with the schema
// reset to a clean, freshly-migrated state, or skips the test if the env
// var isn't set. It blocks until every other Connect-holding test (in any
// process) finishes; call it once per test (a second Connect in the same
// test would self-deadlock on the session lock).
func Connect(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(envVar)
	if dsn == "" {
		t.Skipf("%s not set - skipping live-Postgres test (see db/README.md, `make check-db`)", envVar)
	}

	lock, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("dbtest: connect for harness lock: %v", err)
	}
	if _, err := lock.Exec(context.Background(), "SELECT pg_advisory_lock($1)", int64(harnessLockKey)); err != nil {
		lock.Close(context.Background())
		t.Fatalf("dbtest: acquire harness lock: %v", err)
	}
	// Closing the session releases the lock; registered before resetSchema
	// so even a Fatalf below unblocks the next waiter.
	t.Cleanup(func() { lock.Close(context.Background()) })

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
	ups, downs, err := migrationNames()
	if err != nil || len(ups) == 0 {
		t.Fatalf("dbtest: enumerate embedded migrations: %v (ups: %d)", err, len(ups))
	}

	// Best-effort teardown first (ignore errors: first run against a fresh
	// database has nothing to tear down yet). The migration-tracking table
	// isn't any down file's concern (runkod.ApplyMigrations owns it), so
	// drop it here too.
	for _, down := range downs {
		runPsqlStdin(dsn, mustRead(t, down), false)
	}
	exec.Command("psql", dsn, "-q", "-c", "DROP TABLE IF EXISTS runko_schema_migrations").Run()

	versions := make([]string, 0, len(ups))
	for _, up := range ups {
		if out, err := runPsqlStdin(dsn, mustRead(t, up), true); err != nil {
			t.Fatalf("dbtest: apply migration %s: %v: %s", up, err, out)
		}
		versions = append(versions, fmt.Sprintf("('%s')", strings.TrimSuffix(path.Base(up), ".up.sql")))
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

// The migrations come from the SAME embedded FS the product ships
// (db.Migrations, //go:embed): no repo-root discovery, so this harness
// works identically under plain `go test` (source tree) and `bazel test`
// (sandbox runfiles) - §14.5.4's both-runners contract.

// migrationNames enumerates embedded migration paths: ups in ascending
// numeric order, downs descending - the sequence a real runner produces.
func migrationNames() (ups, downs []string, err error) {
	entries, err := fs.ReadDir(db.Migrations, "migrations")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		name := path.Join("migrations", e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".up.sql"):
			ups = append(ups, name)
		case strings.HasSuffix(e.Name(), ".down.sql"):
			downs = append(downs, name)
		}
	}
	sort.Strings(ups)
	sort.Sort(sort.Reverse(sort.StringSlice(downs)))
	return ups, downs, nil
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(db.Migrations, name)
	if err != nil {
		t.Fatalf("dbtest: read embedded %s: %v", name, err)
	}
	return b
}

// runPsqlStdin feeds sql to psql over stdin (embedded content has no file
// path to hand to -f). strict toggles ON_ERROR_STOP.
func runPsqlStdin(dsn string, sql []byte, strict bool) ([]byte, error) {
	args := []string{dsn, "-q"}
	if strict {
		args = append(args, "-v", "ON_ERROR_STOP=1")
	}
	cmd := exec.Command("psql", args...)
	cmd.Stdin = bytes.NewReader(sql)
	return cmd.CombinedOutput()
}
