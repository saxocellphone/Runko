-- name: CreateChange :one
INSERT INTO changes (
    monorepo_id, change_key, state, base_sha, head_sha, git_ref, title,
    description, test_plan, authored_by_actor_id, depends_on_change_id, mechanical,
    origin_workspace, origin_branch
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
) RETURNING *;

-- name: GetChange :one
SELECT * FROM changes WHERE id = $1;

-- name: GetChangeByKey :one
SELECT * FROM changes WHERE monorepo_id = $1 AND change_key = $2;

-- name: UpdateChangeHead :one
-- authored_by_actor_id moves with the head: the last pusher owns the
-- current content, which is also who self-approval is checked against
-- (§8.7, stage 12c). base_sha moves with it too (compose edge case E7):
-- it is merge-base(head, trunk) at push time, and freezing the creation-
-- time value made §13.5's requires_revalidation a permanent dead end.
-- Re-pushing an abandoned Change reopens it (§7.4 - the change.reopened
-- webhook event modeled this from day one); landed stays landed, it is
-- terminal. Origin provenance (§12.2 branch ↔ stack) moves with the head
-- when the push carries it, and is PRESERVED when it doesn't - a plain-git
-- amend of a workspace Change must not erase where the Change lives.
-- Title moves with the head too: an amend that rewords the commit subject
-- is the pusher renaming the Change.
UPDATE changes SET head_sha = $2, git_ref = $3, authored_by_actor_id = $4, base_sha = $5,
    title = sqlc.arg(title)::text,
    origin_workspace = CASE WHEN sqlc.arg(origin_workspace)::text = '' THEN changes.origin_workspace ELSE sqlc.arg(origin_workspace)::text END,
    origin_branch = CASE WHEN sqlc.arg(origin_workspace)::text = '' THEN changes.origin_branch ELSE sqlc.arg(origin_branch)::text END,
    state = CASE WHEN changes.state = 'abandoned' THEN 'open'::change_state ELSE changes.state END,
    updated_at = now()
WHERE id = $1 RETURNING *;

-- name: UpdateChangeDescription :one
UPDATE changes SET description = $2, test_plan = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: LandChange :one
UPDATE changes SET state = 'landed', landed_at = now(), landed_sha = $2, landed_by_actor_id = $3,
    landed_forced = sqlc.arg(landed_forced)::boolean, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: ListOpenChanges :many
SELECT * FROM changes WHERE monorepo_id = $1 AND state = 'open' ORDER BY number DESC LIMIT $2 OFFSET $3;

-- name: ListChangesByState :many
SELECT * FROM changes WHERE monorepo_id = $1 AND state = $2 ORDER BY number DESC;

-- name: ListLandedChanges :many
-- Landed listings read in LANDING order, not creation order (finding #45):
-- number is assigned at creation, so a later-created change that landed
-- first would sort above the change that landed after it. landed_at is the
-- one clock that matches trunk order (finding #43); number breaks the
-- (sub-microsecond) ties deterministically. Rides idx_changes_landed_order
-- (migration 0018) - the state literal is what lets the planner use the
-- partial index.
SELECT * FROM changes WHERE monorepo_id = $1 AND state = 'landed'
ORDER BY landed_at DESC, number DESC;

-- name: ListAllChanges :many
SELECT * FROM changes WHERE monorepo_id = $1 ORDER BY number DESC;

-- name: ListChangesByStatePage :many
-- One page of ListChangesByState, newest first - LIMIT/OFFSET ride
-- idx_changes_state_number (migration 0010). page_limit 0 means no limit
-- (NULLIF turns it into LIMIT NULL), matching Store.ListChangesPage's
-- contract.
SELECT * FROM changes WHERE monorepo_id = $1 AND state = $2
ORDER BY number DESC
LIMIT NULLIF(sqlc.arg(page_limit)::int, 0) OFFSET sqlc.arg(page_offset)::int;

-- name: ListLandedChangesPage :many
-- One page of ListLandedChanges - same LIMIT NULLIF(x, 0) contract as
-- ListChangesByStatePage.
SELECT * FROM changes WHERE monorepo_id = $1 AND state = 'landed'
ORDER BY landed_at DESC, number DESC
LIMIT NULLIF(sqlc.arg(page_limit)::int, 0) OFFSET sqlc.arg(page_offset)::int;

-- name: ListAllChangesPage :many
SELECT * FROM changes WHERE monorepo_id = $1
ORDER BY number DESC
LIMIT NULLIF(sqlc.arg(page_limit)::int, 0) OFFSET sqlc.arg(page_offset)::int;

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
INSERT INTO change_comments (change_id, author_actor_id, body, path, line, side, head_sha, parent_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *;

-- name: ListChangeComments :many
SELECT * FROM change_comments WHERE change_id = $1 ORDER BY created_at LIMIT $2 OFFSET $3;

-- name: GetChangeComment :one
SELECT * FROM change_comments WHERE id = $1 AND change_id = $2;

-- name: ResolveChangeComment :execrows
UPDATE change_comments SET resolved = $3 WHERE id = $1 AND change_id = $2;

-- name: UpsertChangeReviewRequest :exec
INSERT INTO change_review_requests (change_id, reviewer, requested_by)
VALUES ($1, $2, $3)
ON CONFLICT (change_id, reviewer) DO UPDATE SET requested_by = EXCLUDED.requested_by;

-- name: ListChangeReviewRequests :many
SELECT * FROM change_review_requests WHERE change_id = $1 ORDER BY reviewer;

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

-- name: SetChangeAutomerge :one
-- Arm/disarm the when-ready land (§13.5). Disarming clears the armer.
UPDATE changes SET automerge = $2,
    automerge_by = CASE WHEN $2 THEN sqlc.arg(automerge_by)::text ELSE '' END,
    updated_at = now()
WHERE id = $1 RETURNING *;
