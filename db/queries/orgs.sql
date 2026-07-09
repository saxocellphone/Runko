-- name: CreateOrg :one
INSERT INTO orgs (name) VALUES ($1) RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = $1;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = $1;

-- name: ListOrgs :many
SELECT * FROM orgs ORDER BY created_at;

-- name: SetOrgArchived :exec
UPDATE orgs SET archived_at = CASE WHEN sqlc.arg(archived)::boolean THEN now() ELSE NULL END
WHERE name = sqlc.arg(org_name)::text;

-- Org membership (db/migrations/0007): which store-backed principals may
-- reach an org's /o/<name>/ surface at all. Roles: 'admin' (may add
-- members) or 'member'. Operator principals and the deploy token are
-- daemon config and never appear here.

-- name: UpsertOrgMember :exec
INSERT INTO org_members (org_id, principal_name, role)
SELECT o.id, sqlc.arg(principal_name)::text, sqlc.arg(role)::text
FROM orgs o WHERE o.name = sqlc.arg(org_name)::text
ON CONFLICT (org_id, principal_name) DO UPDATE SET role = EXCLUDED.role;

-- name: GetOrgMemberRole :one
SELECT m.role FROM org_members m
JOIN orgs o ON o.id = m.org_id
WHERE o.name = sqlc.arg(org_name)::text AND m.principal_name = sqlc.arg(principal_name)::text;

-- name: ListOrgMembers :many
SELECT m.principal_name, m.role FROM org_members m
JOIN orgs o ON o.id = m.org_id
WHERE o.name = sqlc.arg(org_name)::text
ORDER BY m.principal_name;

-- name: DeleteOrgMember :exec
DELETE FROM org_members m
USING orgs o
WHERE o.id = m.org_id AND o.name = sqlc.arg(org_name)::text
  AND m.principal_name = sqlc.arg(principal_name)::text;

-- name: GetOrgSettings :one
SELECT settings FROM orgs WHERE name = sqlc.arg(org_name)::text;

-- name: UpdateOrgSettings :exec
UPDATE orgs SET settings = sqlc.arg(settings)::jsonb
WHERE name = sqlc.arg(org_name)::text;

-- name: ListOrgMembershipsForPrincipal :many
SELECT o.name AS org_name, m.role FROM org_members m
JOIN orgs o ON o.id = m.org_id
WHERE m.principal_name = sqlc.arg(principal_name)::text
  AND o.archived_at IS NULL
ORDER BY o.name;

-- name: CreateMonorepo :one
INSERT INTO monorepos (org_id, trunk_ref) VALUES ($1, $2) RETURNING *;

-- name: GetMonorepoByOrg :one
SELECT * FROM monorepos WHERE org_id = $1;

-- name: GetMonorepo :one
SELECT * FROM monorepos WHERE id = $1;
