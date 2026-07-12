-- Workspace activity timeline (§12.6): stats-only, never file content.
-- History is bounded per workspace: the store's RecordWorkspaceEvent
-- prunes to the retention cap after every insert; DeleteWorkspaceEvents
-- rides workspace delete (close keeps the timeline).

-- name: InsertWorkspaceEvent :one
INSERT INTO workspace_events (org_id, monorepo_id, workspace_id, branch, event_type, actor, sha, change_key, files_changed, additions, deletions)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING *;

-- name: ListWorkspaceEvents :many
SELECT * FROM workspace_events WHERE monorepo_id = $1 AND workspace_id = $2
ORDER BY id DESC LIMIT $3 OFFSET $4;

-- name: DeleteWorkspaceEvents :exec
DELETE FROM workspace_events WHERE monorepo_id = $1 AND workspace_id = $2;

-- name: PruneWorkspaceEvents :exec
DELETE FROM workspace_events
WHERE workspace_events.monorepo_id = $1 AND workspace_events.workspace_id = $2
  AND workspace_events.id NOT IN (
    SELECT we.id FROM workspace_events we
    WHERE we.monorepo_id = $1 AND we.workspace_id = $2
    ORDER BY we.id DESC LIMIT $3
);
