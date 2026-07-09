-- Multi-org (§7.1's org->monorepo model finally reaching the daemon,
-- 2026-07-08): accounts become SERVER-GLOBAL - one account, many orgs -
-- and access to an org becomes an explicit membership row. Before this,
-- principals were scoped to the single bootstrap org and every account
-- shared its one repo; now each org owns a repo and store-backed
-- principals only reach orgs they are members of. (Operator principals
-- and the deploy token stay daemon config and server-wide.)

-- Memberships first, while principals still carry org_id, so existing
-- accounts keep access to the org they signed up under.
CREATE TABLE org_members (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    principal_name TEXT NOT NULL,
    role           TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, principal_name)
);

INSERT INTO org_members (org_id, principal_name, role)
SELECT org_id, name, 'member' FROM principals;

-- Dropping org_id drops the old UNIQUE (org_id, name) with it. The new
-- global uniqueness fails loudly if two orgs somehow held the same
-- account name - impossible before this migration, since only the single
-- bootstrap org ever had principal rows.
ALTER TABLE principals DROP COLUMN org_id;
ALTER TABLE principals ADD CONSTRAINT principals_name_key UNIQUE (name);
