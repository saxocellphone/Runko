-- name: CreateTemplate :one
INSERT INTO templates (org_id, template_key, name, project_type, description, default_capabilities, version)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListTemplates :many
SELECT * FROM templates WHERE org_id = $1 ORDER BY name;

-- name: GetTemplateByKey :one
SELECT * FROM templates WHERE org_id = $1 AND template_key = $2;
