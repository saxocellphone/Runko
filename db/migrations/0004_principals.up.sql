-- Self-service principals (§15.1 sign-up flow, 2026-07-08). Operator
-- principals stay daemon config (--principal / RUNKO_PRINCIPALS - they
-- carry agent policy and bot-lane semantics); rows here are HUMAN
-- principals created through POST /api/signup, holding a PBKDF2
-- credential hash, never a plaintext token. Deliberately still not an
-- auth system (no rotation, no sessions, no federation - OIDC stays the
-- real answer, §15.1); this exists so joining an org doesn't require an
-- operator editing daemon flags.
CREATE TABLE principals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    credential_hash TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);
