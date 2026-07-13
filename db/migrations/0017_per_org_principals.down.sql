-- Reverse of per-org accounts: collapse back to one global row per name
-- (keeping the oldest row's credential - the pre-split account), restore
-- the global uniqueness. Same-name accounts in other orgs are dropped;
-- their reach was per-org and cannot be represented globally.

DELETE FROM principals p WHERE EXISTS (
    SELECT 1 FROM principals q
    WHERE q.name = p.name AND q.created_at < p.created_at
);
-- Two same-name rows created in the same instant: break the tie by id.
DELETE FROM principals p WHERE EXISTS (
    SELECT 1 FROM principals q
    WHERE q.name = p.name AND q.created_at = p.created_at AND q.id < p.id
);

ALTER TABLE principals DROP CONSTRAINT principals_org_id_name_key;
ALTER TABLE principals ADD CONSTRAINT principals_name_key UNIQUE (name);
ALTER TABLE principals DROP COLUMN org_id;
