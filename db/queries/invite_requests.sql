-- Invite requests (§15.1, decided 2026-07-13): deployment-wide rows (no
-- org column - requests precede accounts) with the webhook-outbox
-- lifecycle. The mailer service drains the due feed over REST; backoff
-- and dead-lettering are computed server-side (runkod/invite.go).

-- name: CreateInviteRequest :one
INSERT INTO invite_requests (name, email, message) VALUES ($1, $2, $3)
ON CONFLICT (lower(email)) WHERE status IN ('pending', 'failed') DO NOTHING
RETURNING *;

-- name: ListDueInviteRequests :many
SELECT * FROM invite_requests
WHERE status IN ('pending', 'failed') AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT $1;

-- name: GetInviteRequest :one
SELECT * FROM invite_requests WHERE id = $1;

-- name: MarkInviteRequestSent :one
UPDATE invite_requests SET status = 'sent', sent_at = now() WHERE id = $1 RETURNING *;

-- name: MarkInviteRequestFailed :one
UPDATE invite_requests
SET status = $2, attempt = attempt + 1, next_attempt_at = $3, last_error = $4
WHERE id = $1 RETURNING *;

-- name: CountLiveInviteRequests :one
SELECT count(*) FROM invite_requests WHERE status IN ('pending', 'failed');
