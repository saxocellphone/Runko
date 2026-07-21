-- Self-service principals (§15.1 sign-up; db/migrations/0004).
-- PER-ORG since 0017 (superseding 0007's global rows): an account is
-- (org, name, credential) - the same name in two orgs is two independent
-- accounts. org_members (orgs.sql) stays the access/role gate.

-- email is optional (0022): callers pass NULL for "not given", never an
-- empty string.
-- name: CreatePrincipal :one
INSERT INTO principals (org_id, name, credential_hash, email)
SELECT o.id, sqlc.arg(name)::text, sqlc.arg(credential_hash)::text, sqlc.narg(email)::text
FROM orgs o WHERE o.name = sqlc.arg(org_name)::text
RETURNING *;

-- name: GetPrincipalByOrgAndName :one
SELECT p.* FROM principals p
JOIN orgs o ON o.id = p.org_id
WHERE o.name = sqlc.arg(org_name)::text AND p.name = sqlc.arg(name)::text;

-- Every org holding an account with this name - the hub's cross-org
-- resolution (which orgs can this credential possibly sign into) and the
-- 403-vs-401 distinction ride on this.
-- name: ListPrincipalOrgsByName :many
SELECT o.name AS org_name, p.credential_hash, p.email FROM principals p
JOIN orgs o ON o.id = p.org_id
WHERE p.name = sqlc.arg(name)::text
ORDER BY o.name;
