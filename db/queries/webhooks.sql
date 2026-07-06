-- name: EnqueueWebhookDelivery :one
INSERT INTO webhook_deliveries (org_id, event_type, payload) VALUES ($1, $2, $3) RETURNING *;

-- name: ListDueWebhookDeliveries :many
SELECT * FROM webhook_deliveries
WHERE status IN ('pending', 'failed') AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT $1;

-- name: MarkWebhookDelivered :exec
UPDATE webhook_deliveries SET status = 'delivered', delivered_at = now() WHERE id = $1;

-- name: MarkWebhookFailed :exec
UPDATE webhook_deliveries
SET status = $2, attempt = attempt + 1, next_attempt_at = $3, last_error = $4
WHERE id = $1;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries WHERE id = $1;
