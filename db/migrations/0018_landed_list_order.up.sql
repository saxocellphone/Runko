-- The landed listing reads in LANDING order, not creation order (finding
-- #45): change numbers are assigned at creation, so a later-created change
-- that lands first sorted ABOVE the change that landed after it, with an
-- earlier "landed" byline right there on the row. ListLandedChanges orders
-- by (landed_at DESC, number DESC); this index makes that read
-- index-ordered the same way 0010 did for the number-ordered lists.
-- Partial: only landed rows carry a landed_at worth sorting by, and the
-- query names state = 'landed' as a literal, so the planner can prove the
-- predicate.
CREATE INDEX idx_changes_landed_order ON changes (monorepo_id, landed_at DESC, number DESC) WHERE state = 'landed';
