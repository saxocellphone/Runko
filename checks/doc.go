// Package checks implements the Checks API and merge-requirements aggregation
// (docs/design.md §14.4.2, §13.5): CheckRun ingestion, check-set policies
// ("unit:* over affected" fans out to one pass/fail row per §14.4.2), staleness
// on head_sha change, TTL expiry for dead reporters, and rerun-requests. The
// wire shapes are docs/spec/webhooks/checkrun.schema.json - generate types from
// that schema rather than redefining them here.
package checks
