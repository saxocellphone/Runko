-- name: CreateCheckRun :one
INSERT INTO check_runs (
    change_id, head_sha, name, external_id, status, conclusion, started_at,
    details_url, output_title, output_summary, output_text, app_id, reporter,
    attempt, ttl_seconds
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
) RETURNING *;

-- name: UpdateCheckRun :one
UPDATE check_runs
SET status = $2, conclusion = $3, completed_at = $4, details_url = $5,
    output_title = $6, output_summary = $7, output_text = $8, last_seen_at = now()
WHERE id = $1
RETURNING *;

-- name: ListCheckRunsForChange :many
SELECT * FROM check_runs WHERE change_id = $1 AND head_sha = $2 ORDER BY name, attempt;

-- name: GetLatestCheckRunAttempt :one
SELECT * FROM check_runs
WHERE change_id = $1 AND head_sha = $2 AND name = $3
ORDER BY attempt DESC LIMIT 1;

-- name: ListStaleCheckRuns :many
SELECT * FROM check_runs
WHERE status IN ('queued', 'in_progress')
  AND last_seen_at < now() - make_interval(secs => ttl_seconds);

-- name: AddCheckAnnotation :one
INSERT INTO check_annotations (check_run_id, path, start_line, end_line, annotation_level, message, title)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: ListCheckAnnotations :many
SELECT * FROM check_annotations WHERE check_run_id = $1;
