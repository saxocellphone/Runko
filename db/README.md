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
`receive/persist_pg_test.go`, `checks/persist_pg_test.go`) exercise the
persistence wiring stages 2/4/6/8 left "unverified against a live Postgres in
this environment" against a **real** database - not sqlc's schema analyzer.
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
