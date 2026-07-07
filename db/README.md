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

sqlc parses the schema and queries with its own SQL analyzer - `sqlc generate` succeeding does **not** require a live Postgres connection, and is treated as the correctness check for this layer in this environment (no Docker/Postgres available here; see CLAUDE.md).

## Running migrations against a real Postgres

Not yet wired to a runnable stack (that's the compose eval loop, §16.4, session DAG stage 14). Once Postgres exists:

```bash
migrate -database "$DATABASE_URL" -path db/migrations up
```

## Live-Postgres integration tests (`make check-db`)

`internal/dbtest` plus `*_pg_test.go` files (`index/sync_pg_test.go`,
`receive/persist_pg_test.go`, `checks/persist_pg_test.go`,
`runkod/pgstore_pg_test.go`, `cmd/runkod`'s restart test) exercise the
persistence wiring against a **real** database - not sqlc's schema analyzer.
`go test ./...` / `make check` skip them (no Postgres in this sandbox); to
run them for real:

```bash
export RUNKO_TEST_DATABASE_URL="postgres://user:pass@localhost:5432/runko_test?sslmode=disable"
make check-db
```

Each test wipes and re-applies `0001_init.{down,up}.sql` via `psql` before
running (see `internal/dbtest.Connect`), so point this at a database that's
safe to discard - never a real environment's data. `psql` must be on `PATH`;
no other tooling (Docker, testcontainers) is required, so this also works
against a Postgres started any other way (a local install, a cloud dev
instance, etc).

**Why `make check-db` passes `-p 1`:** each package's reset wipes the
*entire* shared schema, not just its own tables - fine when only one
package's live-DB tests run at a time, but `go test ./...` normally runs
different packages' test binaries concurrently, so two packages' resets can
interleave and race (caught in CI once a 4th/5th package gained
`*_pg_test.go` files: "relation already exists" / "relation does not
exist" errors from a reset landing mid-test). `-p 1` forces one package at a
time - correct for tests sharing external stateful infrastructure instead of
being hermetic. Don't drop it when adding new live-Postgres tests.
