-- Agent session activity (§12.6.1): client-claimed, observability-only.
-- The store's RecordWorkspaceActivity prunes to the retention cap after
-- every batch; DeleteWorkspaceActivity rides workspace delete (close
-- keeps the feed).

-- name: InsertWorkspaceActivity :one
INSERT INTO workspace_activity (org_id, monorepo_id, workspace_id, actor, kind, detail, session_id)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: ListWorkspaceActivity :many
SELECT * FROM workspace_activity WHERE monorepo_id = $1 AND workspace_id = $2
ORDER BY id DESC LIMIT $3 OFFSET $4;

-- name: LatestWorkspaceActivity :many
SELECT DISTINCT ON (workspace_id) * FROM workspace_activity
WHERE monorepo_id = $1 AND workspace_id = ANY(sqlc.arg(workspace_ids)::text[])
ORDER BY workspace_id, id DESC;

-- name: DeleteWorkspaceActivity :exec
DELETE FROM workspace_activity WHERE monorepo_id = $1 AND workspace_id = $2;

-- name: PruneWorkspaceActivity :exec
DELETE FROM workspace_activity
WHERE workspace_activity.monorepo_id = $1 AND workspace_activity.workspace_id = $2
  AND workspace_activity.id NOT IN (
    SELECT wa.id FROM workspace_activity wa
    WHERE wa.monorepo_id = $1 AND wa.workspace_id = $2
    ORDER BY wa.id DESC LIMIT $3
);
