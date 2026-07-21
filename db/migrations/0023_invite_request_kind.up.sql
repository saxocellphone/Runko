-- Contact messages ride the invite-request outbox (2026-07-20): the
-- landing page's contact form and the invite intake share validation,
-- rate limiting, backlog caps, and the mailer drain - the only real
-- difference is what the operator's email says, so a kind column is the
-- whole schema change. The live-email dedupe index becomes per-kind: a
-- contact message and an invite request from the same address must
-- coexist, while double-submits of either kind still collapse.
CREATE TYPE invite_request_kind AS ENUM ('invite', 'contact');

ALTER TABLE invite_requests
    ADD COLUMN kind invite_request_kind NOT NULL DEFAULT 'invite';

DROP INDEX idx_invite_requests_live_email;
CREATE UNIQUE INDEX idx_invite_requests_live_email
    ON invite_requests (kind, lower(email))
    WHERE status IN ('pending', 'failed');
