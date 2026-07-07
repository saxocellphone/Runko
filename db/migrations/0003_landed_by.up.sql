-- Stage 12c (docs/design.md §7.5, §15.1): landed_by attribution. 0001
-- already records who AUTHORED a change (authored_by_actor_id) but not who
-- landed it - unattributable before the interim named-token principal
-- registry existed, since every caller was the same anonymous deploy
-- token. Nullable: anonymous (deploy-token) lands stay anonymous.
ALTER TABLE changes
    ADD COLUMN landed_by_actor_id UUID REFERENCES actors(id);
