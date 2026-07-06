-- name: CreateOrg :one
INSERT INTO orgs (name) VALUES ($1) RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = $1;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = $1;

-- name: ListOrgs :many
SELECT * FROM orgs ORDER BY created_at;

-- name: CreateMonorepo :one
INSERT INTO monorepos (org_id, trunk_ref) VALUES ($1, $2) RETURNING *;

-- name: GetMonorepoByOrg :one
SELECT * FROM monorepos WHERE org_id = $1;

-- name: GetMonorepo :one
SELECT * FROM monorepos WHERE id = $1;
