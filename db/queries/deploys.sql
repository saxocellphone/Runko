-- Deploy records: the inverted CD trigger's server-of-record (§14.10). See
-- db/migrations/0021_deploy_records.up.sql. Opened on land, filled by
-- report-image, flipped to 'ready' when the expected image set is complete.

-- name: OpenDeployRecord :exec
-- Idempotent: a re-land or duplicate land event must not reset reported
-- digests, so an existing record is left untouched.
INSERT INTO deploy_records (monorepo_id, trunk_sha, change_key, expected, provenance)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (monorepo_id, trunk_sha) DO NOTHING;

-- name: GetDeployRecord :one
SELECT * FROM deploy_records WHERE monorepo_id = $1 AND trunk_sha = $2;

-- name: UpsertDeployImage :exec
INSERT INTO deploy_images (monorepo_id, trunk_sha, image, image_ref, digest, run_url)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (monorepo_id, trunk_sha, image)
DO UPDATE SET image_ref = EXCLUDED.image_ref, digest = EXCLUDED.digest,
             run_url = EXCLUDED.run_url, reported_at = now();

-- name: ListDeployImages :many
SELECT * FROM deploy_images WHERE monorepo_id = $1 AND trunk_sha = $2 ORDER BY image;

-- name: MarkDeployReadyIfComplete :one
-- Flip pending -> ready atomically, but ONLY when every expected image has a
-- reported row (evaluated against committed rows at UPDATE time, so it is
-- race-safe under concurrent final reports). Returns a row only on the
-- transition (state <> 'ready'), so the caller emits deploy.images_ready
-- exactly once.
UPDATE deploy_records dr
SET state = 'ready', ready_at = now()
WHERE dr.monorepo_id = $1 AND dr.trunk_sha = $2 AND dr.state <> 'ready'
  AND NOT EXISTS (
    SELECT 1 FROM unnest(dr.expected) AS e
    WHERE NOT EXISTS (
      SELECT 1 FROM deploy_images di
      WHERE di.monorepo_id = dr.monorepo_id AND di.trunk_sha = dr.trunk_sha AND di.image = e
    )
  )
RETURNING *;
