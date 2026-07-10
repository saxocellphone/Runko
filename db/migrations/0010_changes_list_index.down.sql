CREATE INDEX idx_changes_state ON changes (monorepo_id, state);
DROP INDEX idx_changes_state_number;
