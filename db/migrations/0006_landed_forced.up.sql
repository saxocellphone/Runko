-- Admin force-land audit bit (§13.5, 2026-07-08): true when the change
-- landed via the force override that bypasses owner/check gates. Durable
-- and immutable once set - an override without an audit trail is a hole,
-- not a feature.
ALTER TABLE changes ADD COLUMN landed_forced boolean NOT NULL DEFAULT false;
