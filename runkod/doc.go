// Package runkod assembles the write-path daemon (docs/design.md §28.3
// DAG stage 10, previously implicit in the old DAG): smart-HTTP hosting of
// one bare monorepo, real pre-receive wiring to receive.Decide() (closing
// the "an actual git pre-receive hook / server wiring" gap receive/doc.go
// flagged as out of scope), a gitleaks-backed SecretScanner (closing that
// package's other flagged gap), REST endpoints for changes/checks/affected/
// merge-requirements, and a webhook outbox delivery worker.
//
// Scope for this session: one daemon serves exactly one monorepo (§7.1's
// "exactly one primary monorepo per org in v1" - no multi-tenant routing
// yet), deploy-token bearer auth (not full OIDC, §15.1 - that's later
// hardening), and Store is an in-memory reference implementation rather
// than the internal/dbgen/Postgres wiring stages 2/4/6/8 already built.
// This is a deliberate choice, not a placeholder to feel bad about: §9.3
// lists "Eval / dev" as a first-class deployment profile, and an in-memory
// Store IS that profile - swapping in a Postgres-backed Store behind the
// same interface is additive work for a later session, not a rewrite.
//
// Out of scope, deferred to later stages: workspace snapshot-ref wiring
// (refs/workspaces/*, §12 - stage 12), multi-tenant org/monorepo routing,
// full OIDC AuthN (§15.1), TLS termination (assumed to sit behind a
// reverse proxy in any real deployment).
package runkod
