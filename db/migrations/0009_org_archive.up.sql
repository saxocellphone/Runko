-- Org archive (admin panel, 2026-07-09; migration-findings #19's "org
-- lifecycle needs at least archive"): an archived org drops out of
-- routing and every listing, but its row and repo stay - recovery is an
-- unarchive, never a restore-from-backup. Deliberately NOT a delete.
ALTER TABLE orgs ADD COLUMN archived_at TIMESTAMPTZ;
