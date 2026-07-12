-- Automerge (§13.5 grown a "when ready" bit, 2026-07-12, migration renumbered 0014->0015 after colliding with workspace_events): a Change can be
-- ARMED to land itself the moment its merge requirements go green - the
-- verb every client has been simulating with poll loops. The bit survives
-- amends on purpose (arm once, iterate until green, it lands itself);
-- §13.5's amend semantics already reset approvals and checks, so nothing
-- lands that the gates didn't re-approve. automerge_by records the arming
-- principal and becomes landed_by on the automatic land.
ALTER TABLE changes ADD COLUMN automerge BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE changes ADD COLUMN automerge_by TEXT NOT NULL DEFAULT '';
