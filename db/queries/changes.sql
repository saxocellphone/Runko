-- name: CreateChange :one
INSERT INTO changes (
    monorepo_id, change_key, state, base_sha, head_sha, git_ref, title,
    description, test_plan, authored_by_actor_id, depends_on_change_id, mechanical
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
) RETURNING *;

-- name: GetChange :one
SELECT * FROM changes WHERE id = $1;

-- name: GetChangeByKey :one
SELECT * FROM changes WHERE monorepo_id = $1 AND change_key = $2;

-- name: UpdateChangeHead :one
-- authored_by_actor_id moves with the head: the last pusher owns the
-- current content, which is also who self-approval is checked against
-- (§8.7, stage 12c). Re-pushing an abandoned Change reopens it (§7.4 -
-- the change.reopened webhook event modeled this from day one); landed
-- stays landed, it is terminal.
UPDATE changes SET head_sha = $2, git_ref = $3, authored_by_actor_id = $4,
    state = CASE WHEN changes.state = 'abandoned' THEN 'open'::change_state ELSE changes.state END,
    updated_at = now()
WHERE id = $1 RETURNING *;

-- name: UpdateChangeDescription :one
UPDATE changes SET description = $2, test_plan = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: LandChange :one
UPDATE changes SET state = 'landed', landed_at = now(), landed_sha = $2, landed_by_actor_id = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ListOpenChanges :many
SELECT * FROM changes WHERE monorepo_id = $1 AND state = 'open' ORDER BY number DESC LIMIT $2 OFFSET $3;

-- name: ListChangesByState :many
SELECT * FROM changes WHERE monorepo_id = $1 AND state = $2 ORDER BY number DESC;

-- name: ListAllChanges :many
SELECT * FROM changes WHERE monorepo_id = $1 ORDER BY number DESC;

-- name: AbandonChange :one
UPDATE changes SET state = 'abandoned', updated_at = now() WHERE id = $1 RETURNING *;

-- name: AddChangeAssistedBy :exec
INSERT INTO change_assisted_by (change_id, actor_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListChangeAssistedBy :many
SELECT a.* FROM change_assisted_by cab
JOIN actors a ON a.id = cab.actor_id
WHERE cab.change_id = $1;

-- name: UpsertChangeAffected :one
INSERT INTO change_affected (change_id, head_sha, computation_id, run_everything, reason_codes)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (change_id, head_sha) DO UPDATE
    SET computation_id = EXCLUDED.computation_id,
        run_everything = EXCLUDED.run_everything,
        reason_codes = EXCLUDED.reason_codes
RETURNING *;

-- name: GetChangeAffected :one
SELECT * FROM change_affected WHERE change_id = $1 AND head_sha = $2;

-- name: AddChangeAffectedProject :exec
INSERT INTO change_affected_projects (change_affected_id, project_id)
VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListChangeAffectedProjects :many
SELECT p.* FROM change_affected_projects cap
JOIN projects p ON p.id = cap.project_id
WHERE cap.change_affected_id = $1;

-- name: CreateChangeComment :one
INSERT INTO change_comments (change_id, author_actor_id, body, path, line)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListChangeComments :many
SELECT * FROM change_comments WHERE change_id = $1 ORDER BY created_at LIMIT $2 OFFSET $3;

-- name: SetChangeOwnerRequirement :exec
INSERT INTO change_owner_requirements (change_id, owner_ref, satisfied)
VALUES ($1, $2, false)
ON CONFLICT (change_id, owner_ref) DO NOTHING;

-- name: SatisfyChangeOwnerRequirement :exec
UPDATE change_owner_requirements
SET satisfied = true, satisfied_by_actor_id = $3, satisfied_at = now(),
    satisfied_for_head_sha = $4
WHERE change_id = $1 AND owner_ref = $2;

-- name: ListChangeOwnerRequirements :many
SELECT * FROM change_owner_requirements WHERE change_id = $1;
