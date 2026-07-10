-- name: UpsertActor :one
INSERT INTO actors (org_id, type, external_ref, display_name, agent_policy_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (org_id, type, external_ref) DO UPDATE
    SET display_name = EXCLUDED.display_name,
        agent_policy_id = EXCLUDED.agent_policy_id,
        metadata = EXCLUDED.metadata
RETURNING *;

-- name: GetActor :one
SELECT * FROM actors WHERE id = $1;

-- name: GetActorsByIDs :many
-- Batch form of GetActor for list hydration: ListChanges used to resolve
-- authored_by/landed_by with one GetActor round-trip per row (N+1 - the
-- landed tab's 300ms at 44 changes), this fetches every distinct actor a
-- page of changes references in one query.
SELECT * FROM actors WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: GetActorByExternalRef :one
SELECT * FROM actors WHERE org_id = $1 AND type = $2 AND external_ref = $3;

-- name: CreateAgentPolicy :one
INSERT INTO agent_policies (
    org_id, name, require_workspace_affinity, max_changed_files, max_diff_bytes,
    can_create_projects, can_land_changes, can_modify_owners, can_enable_capabilities, denylist_paths
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: GetAgentPolicy :one
SELECT * FROM agent_policies WHERE id = $1;

-- name: GetAgentPolicyByName :one
SELECT * FROM agent_policies WHERE org_id = $1 AND name = $2;
