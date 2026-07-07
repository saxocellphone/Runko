-- Stage 12c (docs/design.md §13.5, decided 2026-07-07): approvals bind to
-- the head_sha they were granted for. An amend moves the Change's head, so
-- a satisfied requirement whose satisfied_for_head_sha no longer matches
-- the current head stops counting toward the merge gate - exactly as check
-- runs (keyed by (change_id, head_sha) since 0001) already do. The row is
-- retained for audit, not deleted (§13.5: "stale approvals are kept, they
-- just stop counting").
--
-- NULLable by design: rows satisfied before this column existed have an
-- unknown approval head, which must read as stale (fail closed), never as
-- currently-valid.
ALTER TABLE change_owner_requirements
    ADD COLUMN satisfied_for_head_sha TEXT;
