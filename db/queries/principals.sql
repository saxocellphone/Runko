-- Self-service principals (§15.1 sign-up; db/migrations/0004).

-- name: CreatePrincipal :one
INSERT INTO principals (org_id, name, credential_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetPrincipalByName :one
SELECT * FROM principals
WHERE org_id = $1 AND name = $2;
