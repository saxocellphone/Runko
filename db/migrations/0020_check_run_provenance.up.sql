-- §13.5 trivial-rebase carry-forward (2026-07-15): a copied check run is a
-- fresh row at the new head stamped with the head its result was earned at.
ALTER TABLE check_runs ADD COLUMN copied_from_head_sha TEXT;
