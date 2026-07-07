// Package db embeds the SQL migrations so the daemon can apply them itself
// at startup (§16.4's self-host definition of done includes "schema
// upgrades" - an evaluator's `docker compose up` must not require a
// separate migration tool; see runkod.ApplyMigrations). The files remain
// plain golang-migrate-style numbered pairs on disk: psql-based harnesses
// (internal/dbtest, db/README.md) keep using them directly.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
