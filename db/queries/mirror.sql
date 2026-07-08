-- Mirror cursors (§18.6): per-(remote, ref) sync state for the outbound
-- mirror. writer names which side may write the ref (always 'runko' in the
-- outbound M1 - the single-writer lease's bookkeeping half); frozen is
-- invariant 4's loud stop. State is rebuildable from the two git histories
-- (invariant 5) - these rows are cache and audit, never truth.

-- name: GetMirrorCursor :one
SELECT * FROM mirror_cursors WHERE monorepo_id = $1 AND remote_name = $2 AND ref_name = $3;

-- name: ListMirrorCursors :many
SELECT * FROM mirror_cursors WHERE monorepo_id = $1 AND remote_name = $2 ORDER BY ref_name;

-- name: UpsertMirrorCursor :one
INSERT INTO mirror_cursors (monorepo_id, remote_name, ref_name, last_synced_sha, writer, frozen)
VALUES ($1, $2, $3, $4, $5, false)
ON CONFLICT (monorepo_id, remote_name, ref_name) DO UPDATE
    SET last_synced_sha = EXCLUDED.last_synced_sha, frozen = false, updated_at = now()
RETURNING *;

-- name: FreezeMirrorCursor :one
INSERT INTO mirror_cursors (monorepo_id, remote_name, ref_name, last_synced_sha, writer, frozen)
VALUES ($1, $2, $3, NULL, $4, true)
ON CONFLICT (monorepo_id, remote_name, ref_name) DO UPDATE
    SET frozen = true, updated_at = now()
RETURNING *;
