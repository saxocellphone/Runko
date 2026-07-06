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

Not yet wired to a runnable stack (that's the compose eval loop, §16.4, session DAG stage 13). Once Postgres exists:

```bash
migrate -database "$DATABASE_URL" -path db/migrations up
```
