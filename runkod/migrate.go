package runkod

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saxocellphone/runko/db"
)

// migrationLockKey serializes concurrent migrators (two daemon replicas
// racing at startup) on a Postgres advisory lock. Arbitrary constant;
// only collisions with OTHER advisory-lock users of the same database
// would matter, and runkod is this database's only tenant.
const migrationLockKey = 0x52554e4b4f // "RUNKO"

// ApplyMigrations brings the database up to the embedded schema
// (db/migrations, §16.4 "schema upgrades"): every not-yet-recorded
// NNNN_name.up.sql runs in filename order, each in one implicit
// transaction together with the row that records it - so a failed
// migration leaves neither half-applied DDL nor a lying record. Found
// by stage 14's compose smoke on its first real run: BootstrapPostgresStore
// assumed a migrated schema, but only the test harnesses ever applied one -
// a fresh `docker compose up` died on `relation "orgs" does not exist`.
//
// Returns the versions applied by THIS call ([] when current). Uses the
// simple query protocol (PgConn.Exec) because migration files are
// multi-statement SQL, which the extended protocol refuses.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	entries, err := fs.ReadDir(db.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("runkod: read embedded migrations: %w", err)
	}
	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("runkod: acquire migration connection: %w", err)
	}
	defer conn.Release()
	pg := conn.Conn().PgConn()

	exec := func(sql string) error {
		_, err := pg.Exec(ctx, sql).ReadAll()
		return err
	}
	if err := exec(fmt.Sprintf("SELECT pg_advisory_lock(%d)", migrationLockKey)); err != nil {
		return nil, fmt.Errorf("runkod: take migration lock: %w", err)
	}
	defer exec(fmt.Sprintf("SELECT pg_advisory_unlock(%d)", migrationLockKey))

	if err := exec("CREATE TABLE IF NOT EXISTS runko_schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"); err != nil {
		return nil, fmt.Errorf("runkod: create migration table: %w", err)
	}
	rows, err := pool.Query(ctx, "SELECT version FROM runko_schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("runkod: read applied migrations: %w", err)
	}
	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	rows.Close()

	var ran []string
	for _, name := range ups {
		version := strings.TrimSuffix(name, ".up.sql")
		if applied[version] {
			continue
		}
		content, err := fs.ReadFile(db.Migrations, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("runkod: read migration %s: %w", name, err)
		}
		// One simple-protocol batch = one implicit transaction: the
		// migration and its record commit or roll back together.
		batch := string(content) + fmt.Sprintf(";\nINSERT INTO runko_schema_migrations (version) VALUES ('%s');", version)
		if err := exec(batch); err != nil {
			return ran, fmt.Errorf("runkod: apply migration %s: %w", name, err)
		}
		ran = append(ran, version)
	}
	return ran, nil
}
