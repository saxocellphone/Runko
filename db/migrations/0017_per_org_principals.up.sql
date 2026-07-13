-- Per-org accounts (2026-07-13, user direction: "user should be per org
-- - you can have the same username for different orgs"). Supersedes the
-- identity half of 0007: an account row is (org, name, credential), the
-- Slack-workspace model - two orgs can each have their own "victor",
-- different humans, different passwords, no interaction. org_members
-- stays the access/role gate (0007's other half is unchanged); signup
-- creates account + membership together, so every account row has a
-- matching membership in its own org.

ALTER TABLE principals ADD COLUMN org_id UUID REFERENCES orgs(id) ON DELETE CASCADE;

-- Existing global accounts split into one per-org account per membership
-- (same credential hash - it was one human with reach into those orgs).
UPDATE principals p SET org_id = (
    SELECT m.org_id FROM org_members m
    WHERE m.principal_name = p.name
    ORDER BY m.created_at LIMIT 1
);

INSERT INTO principals (org_id, name, credential_hash)
SELECT m.org_id, m.principal_name, p.credential_hash
FROM org_members m
JOIN principals p ON p.name = m.principal_name
WHERE p.org_id IS NOT NULL AND m.org_id <> p.org_id;

-- Accounts that belonged to no org could sign in nowhere (the pre-#44
-- stranding); with per-org identity they have no home to bind to.
DELETE FROM principals WHERE org_id IS NULL;

ALTER TABLE principals ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE principals DROP CONSTRAINT principals_name_key;
ALTER TABLE principals ADD CONSTRAINT principals_org_id_name_key UNIQUE (org_id, name);
