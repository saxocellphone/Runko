-- Invite requests (§15.1, decided 2026-07-13): "how do I get the invite
-- code?" submissions from the login gate. Deployment-wide - no org
-- column: a request precedes any account (0017 scoped principals per org
-- because accounts ARE per-org; a request isn't one). Lifecycle mirrors
-- webhook_deliveries (0001): the mailer service drains due rows over
-- REST and acks sent/failed; backoff/dead-letter state lives here, never
-- in the mailer.
CREATE TYPE invite_request_status AS ENUM ('pending', 'sent', 'failed', 'dead_letter');

CREATE TABLE invite_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    email           TEXT NOT NULL,
    message         TEXT NOT NULL DEFAULT '',
    status          invite_request_status NOT NULL DEFAULT 'pending',
    attempt         INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ
);

CREATE INDEX idx_invite_requests_due ON invite_requests (status, next_attempt_at)
    WHERE status IN ('pending', 'failed');

-- One live request per address (case-insensitive): the intake answers an
-- idempotent 202 on duplicates; this backstops the race.
CREATE UNIQUE INDEX idx_invite_requests_live_email ON invite_requests (lower(email))
    WHERE status IN ('pending', 'failed');
