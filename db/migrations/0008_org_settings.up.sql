-- Per-org settings (org settings page, 2026-07-08): a small JSONB blob
-- on the org row - {description, global_required_checks} in v1. Kept
-- deliberately thin: real merge policy lives in the tree (§9.4 "the tree
-- owns policy"); this holds only the org-level knobs that were daemon
-- flags before multi-org made "the org" a first-class row.
ALTER TABLE orgs ADD COLUMN settings JSONB NOT NULL DEFAULT '{}'::jsonb;
