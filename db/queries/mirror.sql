-- name: UpsertMirrorCursor :one
INSERT INTO mirror_cursors (monorepo_id, remote_name, ref_name, last_synced_sha, writer, frozen)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (monorepo_id, remote_name, ref_name) DO UPDATE
    SET last_synced_sha = EXCLUDED.last_synced_sha,
        writer = EXCLUDED.writer,
        frozen = EXCLUDED.frozen,
        updated_at = now()
RETURNING *;

-- name: FreezeMirrorCursor :exec
UPDATE mirror_cursors SET frozen = true, updated_at = now() WHERE id = $1;

-- name: ListMirrorCursors :many
SELECT * FROM mirror_cursors WHERE monorepo_id = $1;
