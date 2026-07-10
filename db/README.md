# Persistence

Postgres holds workflow state and a **rebuildable index** of tree-resident structure (docs/design.md §9.2, §10.3) - never an independent source of truth. See `0001_init.up.sql` for the annotated schema.

## Layout

- `migrations/` - plain numbered SQL, `golang-migrate` naming convention (`NNNN_name.up.sql` / `.down.sql`).
- `queries/` - sqlc named queries (`-- name: X :one|:many|:exec`), one file per domain.
- `../sqlc.yaml` - generates `internal/dbgen` (pgx/v5). **Never hand-edit `internal/dbgen`** - rerun `sqlc generate`.

## Regenerating

```bash
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"   # go + sqlc, if not already on PATH
sqlc generate
```

sqlc parses the schema and queries with its own SQL analyzer - `sqlc generate` succeeding does **not** require a live Postgres connection. The real correctness check is `make check-db` (below), which CI runs against a live Postgres on every change that affects this layer.

## Running migrations against a real Postgres

The product applies them itself: `runkod` embeds `db/migrations` (package
`db`'s `//go:embed`) and `runkod.ApplyMigrations` runs any unapplied files
in order at boot — advisory-locked so concurrent replicas don't race, each
recorded in `runko_schema_migrations`. A fresh `docker compose up` (§16.4)
therefore needs no migration tooling at all. To apply them outside the
daemon, any runner over the same files works, e.g.:

```bash
migrate -database "$DATABASE_URL" -path db/migrations up
```

## Live-Postgres integration tests (`make check-db`)

`internal/dbtest` plus the `*_pg_test.go` files (`platform/index`,
`platform/receive`, `platform/checks`, `runkod/pgstore_pg_test.go`,
`runkod/cmd/runkod`'s restart test) exercise the persistence wiring
against a **real** database - not sqlc's schema analyzer.
`go test ./...` / `make check` skip them (no Postgres in this sandbox); to
run them for real:

```bash
export RUNKO_TEST_DATABASE_URL="postgres://user:pass@localhost:5432/runko_test?sslmode=disable"
make check-db
```

Each test wipes and re-applies every `db/migrations/*.{down,up}.sql` (downs
in reverse numeric order, ups in order) via `psql` before
running (see `internal/dbtest.Connect`), so point this at a database that's
safe to discard - never a real environment's data. `psql` must be on `PATH`;
no other tooling (Docker, testcontainers) is required, so this also works
against a Postgres started any other way (a local install, a cloud dev
instance, etc).

**Serialization is the harness's job, not a runner flag's:** each
package's reset wipes the *entire* shared schema, not just its own tables,
while go/bazel both run different packages' test binaries concurrently -
two resets interleaving mid-test was a real CI failure ("relation already
exists" / "relation does not exist"). `dbtest.Connect` therefore holds a
session-level Postgres **advisory lock** for the test's lifetime, so
Connect-holding tests serialize across processes with no `-p 1` /
`--local_test_jobs=1` anywhere (that's what lets pg tests ride each
project's ordinary scoped check in CI, §14.9.1). Consequences when writing
new live-DB tests: call `Connect` **once per test** (a second call in the
same test self-deadlocks on the session lock), and expect wall-clock
serialization - keep them short.
