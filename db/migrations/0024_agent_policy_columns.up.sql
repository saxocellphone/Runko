-- Complete the (0001_init) agent_policies table so a per-org policy can be
-- stored operator-settably (§8.7):
--   * require_description - receive.AgentPolicy grew this field AFTER 0001, so
--     the table lacked it; a stored policy would read it back as false, LOOSER
--     than DefaultAgentPolicy()'s true. Backfill true (the safe default).
--   * updated_by / updated_at - a config that can LOOSEN agent restrictions
--     (e.g. drop the .github/workflows denylist) must record who and when; the
--     table only had created_at.
ALTER TABLE agent_policies ADD COLUMN require_description BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE agent_policies ADD COLUMN updated_by TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_policies ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
