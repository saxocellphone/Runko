-- Releases (§14.10.3, stage 17b). Deliberately no UPDATE/DELETE: releases
-- are immutable by construction; the UNIQUE(monorepo_id, project_name,
-- version) constraint backstops concurrent same-version creates.

-- name: CreateRelease :one
INSERT INTO releases (monorepo_id, project_name, project_path, version, tag_ref, tag_sha, target_sha, head_change_key, changelog, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING *;

-- name: ListReleasesByProject :many
SELECT * FROM releases WHERE monorepo_id = $1 AND project_name = $2
ORDER BY created_at DESC LIMIT $3 OFFSET $4;

-- name: GetLatestReleaseByProject :one
SELECT * FROM releases WHERE monorepo_id = $1 AND project_name = $2
ORDER BY created_at DESC LIMIT 1;
