-- Re-scope principals to the oldest org (the bootstrap org - the only
-- one that could have held accounts before 0007 went up).
ALTER TABLE principals ADD COLUMN org_id UUID REFERENCES orgs(id) ON DELETE CASCADE;
UPDATE principals SET org_id = (SELECT id FROM orgs ORDER BY created_at LIMIT 1);
ALTER TABLE principals ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE principals DROP CONSTRAINT principals_name_key;
ALTER TABLE principals ADD CONSTRAINT principals_org_id_name_key UNIQUE (org_id, name);

DROP TABLE org_members;
