-- Push provenance (§12.2's workspace-branch ↔ stack mapping, decided
-- 2026-07-08): the workspace branch a Change was pushed from, stamped by
-- `runko change push` as git push options and validated against the
-- workspace registry at receive time. Advisory metadata for grouping and
-- display, never a merge gate - so plain text columns, no FK to any
-- workspace row (the registry row may be deleted/GC'd while landed Changes
-- keep their history). Empty string = no provenance (plain-git pushers,
-- the web create-project flow, bot lanes).
ALTER TABLE changes
    ADD COLUMN origin_workspace text NOT NULL DEFAULT '',
    ADD COLUMN origin_branch text NOT NULL DEFAULT '';
