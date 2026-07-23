-- Per-org agent policy overrides (§8.7): the persisted, operator-tunable form
-- of receive.AgentPolicy, keyed by org NAME (like GetOrgSettings) + policy name
-- ('' = the org-wide policy; per-agent names are a future layer, and the schema
-- already carries actors.agent_policy_id for it). Absent -> DefaultAgentPolicy()
-- applies, so a fresh org stays locked down. Only operators write these.
-- The by-id / by-org_id queries in actors.sql predate this and stay for the
-- actor-binding path.

-- name: GetAgentPolicyForOrg :one
SELECT ap.* FROM agent_policies ap
JOIN orgs o ON o.id = ap.org_id
WHERE o.name = sqlc.arg(org_name)::text AND ap.name = sqlc.arg(name)::text;

-- name: UpsertAgentPolicyForOrg :exec
-- The CLI writes a COMPLETE policy (read-modify-write of the effective policy),
-- so every column is set; there is no partial-field merge here. A future
-- AgentPolicy field is a new column with a safe migration default, inherited by
-- existing rows - so stored policies never silently freeze to an unsafe zero.
INSERT INTO agent_policies (
    org_id, name, require_workspace_affinity, require_description, max_changed_files,
    max_diff_bytes, can_create_projects, can_land_changes, can_modify_owners,
    can_enable_capabilities, denylist_paths, updated_by, updated_at
)
SELECT o.id, sqlc.arg(name)::text, sqlc.arg(require_workspace_affinity)::boolean,
    sqlc.arg(require_description)::boolean, sqlc.arg(max_changed_files)::int,
    sqlc.arg(max_diff_bytes)::bigint, sqlc.arg(can_create_projects)::boolean,
    sqlc.arg(can_land_changes)::boolean, sqlc.arg(can_modify_owners)::boolean,
    sqlc.arg(can_enable_capabilities)::text[], sqlc.arg(denylist_paths)::text[],
    sqlc.arg(updated_by)::text, now()
FROM orgs o WHERE o.name = sqlc.arg(org_name)::text
ON CONFLICT (org_id, name) DO UPDATE SET
    require_workspace_affinity = EXCLUDED.require_workspace_affinity,
    require_description = EXCLUDED.require_description,
    max_changed_files = EXCLUDED.max_changed_files,
    max_diff_bytes = EXCLUDED.max_diff_bytes,
    can_create_projects = EXCLUDED.can_create_projects,
    can_land_changes = EXCLUDED.can_land_changes,
    can_modify_owners = EXCLUDED.can_modify_owners,
    can_enable_capabilities = EXCLUDED.can_enable_capabilities,
    denylist_paths = EXCLUDED.denylist_paths,
    updated_by = EXCLUDED.updated_by,
    updated_at = now();

-- name: DeleteAgentPolicyForOrg :exec
-- --reset: drop the override row so DefaultAgentPolicy() applies again.
DELETE FROM agent_policies ap USING orgs o
WHERE ap.org_id = o.id AND o.name = sqlc.arg(org_name)::text AND ap.name = sqlc.arg(name)::text;
