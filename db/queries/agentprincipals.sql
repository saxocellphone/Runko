-- name: MintAgentPrincipal :one
INSERT INTO agent_principals (org_id, name, task, token_hash, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: GetAgentPrincipalByName :one
SELECT * FROM agent_principals WHERE org_id = $1 AND name = $2;

-- name: GetAgentPrincipalByTokenHash :one
SELECT * FROM agent_principals WHERE org_id = $1 AND token_hash = $2;

-- name: ListAgentPrincipals :many
SELECT * FROM agent_principals WHERE org_id = $1 ORDER BY created_at DESC;

-- name: RevokeAgentPrincipal :exec
UPDATE agent_principals SET revoked = true WHERE org_id = $1 AND name = $2;

-- name: DeleteExpiredAgentPrincipals :exec
-- Opportunistic hygiene (run at mint time): credentials dead for over the
-- grace window carry no auth value - attribution lives in actors rows, not
-- here - so the table never grows with history.
DELETE FROM agent_principals WHERE org_id = $1 AND expires_at < $2;
