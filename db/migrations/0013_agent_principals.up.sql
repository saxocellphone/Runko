-- Ephemeral agent identity (§15.1 grown a third principal kind, 2026-07-11):
-- agents come and go - often ten at once - so their credentials are MINTED
-- per task over the API and die by TTL, never edited into operator config.
-- The name embeds the task (agent-<task>-<suffix>): attribution everywhere
-- (authored_by, workspace owner, the §8.7 badge) answers "what was this
-- agent doing" by construction. token_hash is sha256 of a random 256-bit
-- bearer token - high-entropy, so no KDF; lookup is one indexed read.
-- Rows are org-scoped: agents work inside an org.
CREATE TABLE agent_principals (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    task       TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked    BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (org_id, name)
);
CREATE INDEX idx_agent_principals_token ON agent_principals (token_hash);
