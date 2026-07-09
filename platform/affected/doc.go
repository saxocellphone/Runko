// Package affected computes affected projects for a set of changed paths
// (docs/design.md §13.3): paths -> projects by longest-prefix match, plus
// DECLARED dependency edges (transitive) and root-invalidation rules that force
// run_everything. Import-based inferred dependencies are advisory-only in v1 and
// must never feed this computation - gate on facts, suggest from inference.
//
// This should be a pure function of (tree state, declared deps, changed paths)
// with property tests, per the session DAG (§28.3 stage 5) and budget table
// (§28.1: "transcription", ~1 session). Callers (webhooks, CLI, MCP) must
// propagate run_everything rather than reinterpreting it (§14.5.3).
package affected
