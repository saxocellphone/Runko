// Package checks implements the Checks API and merge-requirements aggregation
// (docs/design.md §14.4.2, §13.5): CheckRun ingestion, check-set policies
// ("unit:* over affected" fans out to one pass/fail row per §14.4.2), staleness
// on head_sha change, TTL expiry for dead reporters, and rerun-requests. The
// wire shapes are docs/spec/webhooks/checkrun.schema.json - generate types from
// that schema rather than redefining them here.
//
// contract_test.go validates the hand-written Go types against the real
// schemas in docs/spec/ (github.com/santhosh-tekuri/jsonschema), including
// the conditional if/then rules (completed CheckRuns require a conclusion;
// change.check_rerun_requested requires a rerun block) - not just structural
// shape. Webhook HTTP delivery (delivery.go) is genuinely tested against a
// local httptest.Server, not just DB-glue-caveated like persist.go, which is
// unverified against a live Postgres in this environment (see CLAUDE.md).
package checks
