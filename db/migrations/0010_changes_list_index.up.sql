-- ListChangesByState/ListOpenChanges filter on (monorepo_id, state) and sort
-- by number DESC; the 0001 index covered only the filter, so every list read
-- sorted the matched rows. Open stays small but landed grows without bound -
-- extend the index with the sort key so the query is index-ordered for good.
CREATE INDEX idx_changes_state_number ON changes (monorepo_id, state, number DESC);
DROP INDEX idx_changes_state;
