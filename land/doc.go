// Package land implements rebase-based landing with optimistic revalidation
// (docs/design.md §13.5, §7.4): land = rebase the Change onto trunk tip; if the
// trunk delta since the Change's checked head_sha does not intersect its affected
// set, land without re-running checks, otherwise re-run required checks first.
// Land races are the norm at this scale, not the edge case - handle concurrent
// landing attempts explicitly rather than assuming a quiet trunk.
//
// Per §28.1 this is a "discovery" component, not transcription - budget test
// tokens 1:1 with product tokens here. A v1.x merge queue (§19.4) batches and
// pipelines this same rule; it is not a new semantic and does not belong in v1.
//
// Rebase is implemented via `git merge-tree --write-tree` (a 3-way merge of
// old-base/new-trunk-tip/change-head, with old-base passed explicitly as the
// merge base) rather than a bespoke merge algorithm - see rebase.go. Land's
// ref update is a compare-and-swap (core.MonorepoStore.UpdateRef) so a lost
// race surfaces as Outcome.RaceRetry, never a silent overwrite; this
// function does not retry internally (see land.go's Land doc comment) - a
// bounded retry loop or the future merge queue owns that policy.
package land
