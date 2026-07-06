-- name: CreateProject :one
INSERT INTO projects (
    monorepo_id, name, path, project_type, template_id, visibility,
    capabilities, declared_dependencies, indexed_at_sha
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING *;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE monorepo_id = $1 AND name = $2;

-- name: GetProjectByPath :one
SELECT * FROM projects WHERE monorepo_id = $1 AND path = $2;

-- name: ListProjects :many
SELECT * FROM projects
WHERE monorepo_id = $1
  AND ($2::text = '' OR name ILIKE '%' || $2::text || '%' OR path ILIKE '%' || $2::text || '%')
ORDER BY name
LIMIT $3 OFFSET $4;

-- name: DeleteProjectsForMonorepo :exec
DELETE FROM projects WHERE monorepo_id = $1;

-- name: DeleteProjectOwners :exec
DELETE FROM project_owners WHERE project_id = $1;

-- name: InsertProjectOwner :exec
INSERT INTO project_owners (project_id, owner_ref, source)
VALUES ($1, $2, $3)
ON CONFLICT (project_id, owner_ref) DO UPDATE SET source = EXCLUDED.source;

-- name: ListProjectOwners :many
SELECT * FROM project_owners WHERE project_id = $1;

-- name: UpsertInferredDependency :exec
INSERT INTO inferred_dependencies (project_id, depends_on_project_id, confidence, last_seen_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (project_id, depends_on_project_id) DO UPDATE
    SET confidence = EXCLUDED.confidence, last_seen_at = now();

-- name: ListInferredDependencies :many
SELECT * FROM inferred_dependencies WHERE project_id = $1;
