-- Self-service principals (§15.1 sign-up; db/migrations/0004).
-- Server-global since 0007: one account, many orgs - org access lives in
-- org_members (orgs.sql).

-- name: CreatePrincipal :one
INSERT INTO principals (name, credential_hash)
VALUES ($1, $2)
RETURNING *;

-- name: GetPrincipalByName :one
SELECT * FROM principals
WHERE name = $1;
