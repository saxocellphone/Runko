-- name: CreateWorkspace :one
INSERT INTO workspaces (
    org_id, monorepo_id, principal_actor_id, coding_session_id, base_revision,
    project_affinity, write_allowlist, snapshot_ref, mode, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;

-- name: UpdateWorkspaceStatus :one
UPDATE workspaces SET status = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: UpdateWorkspaceBase :one
UPDATE workspaces SET base_revision = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ListWorkspacesByPrincipal :many
SELECT * FROM workspaces WHERE principal_actor_id = $1 AND status = 'active' ORDER BY created_at DESC;

-- name: GetWorkspaceBySnapshotRef :one
SELECT * FROM workspaces WHERE monorepo_id = $1 AND snapshot_ref = $2;

-- name: ListWorkspacesByMonorepo :many
SELECT * FROM workspaces WHERE monorepo_id = $1 ORDER BY created_at;

-- name: DeleteWorkspace :exec
-- Hard delete (workspace deletion, stage 15+): the registry row is
-- metadata only (§12.2) - content durability is the snapshot refs, which
-- the caller deletes alongside. A deleted id becomes reusable.
DELETE FROM workspaces WHERE id = $1;
