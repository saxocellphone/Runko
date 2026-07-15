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

-- name: UpsertCheckRunByName :one
-- Report-check posts a STATUS TRANSITION for the same logical run (queued ->
-- in_progress -> completed) - a different flow from
-- CreateCheckRun/GetLatestCheckRunAttempt's explicit new-attempt re-run
-- semantics (§14.4.2). The caller passes the attempt to target: the LATEST
-- one (runkod's PostgresStore resolves it first), so a result posted after
-- a rerun completes the rerun's attempt rather than resurrecting attempt 1
-- (stage 12c-③ - previously hardcoded to attempt 1, which stranded every
-- rerun attempt as forever-queued the moment reruns became requestable).
INSERT INTO check_runs (
    change_id, head_sha, name, external_id, status, conclusion, reporter, ttl_seconds, attempt, details_url
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) ON CONFLICT (change_id, head_sha, name, attempt) DO UPDATE
    SET external_id = EXCLUDED.external_id,
        status = EXCLUDED.status,
        conclusion = EXCLUDED.conclusion,
        reporter = EXCLUDED.reporter,
        -- A report without a link must not erase the link an earlier
        -- transition carried (queued often has it, completed may not).
        details_url = COALESCE(EXCLUDED.details_url, check_runs.details_url),
        completed_at = CASE WHEN EXCLUDED.status = 'completed' THEN now() ELSE check_runs.completed_at END,
        last_seen_at = now()
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

-- name: CopyPassingCheckRunsToHead :many
-- §13.5 trivial-rebase carry-forward (2026-07-15): clone the LATEST passing
-- completed attempt of each check name from one head to another as a fresh
-- attempt-1 row stamped with its provenance. ON CONFLICT DO NOTHING: a run
-- already reported at the new head (racing CI) is fresher truth than a copy.
WITH latest AS (
    SELECT DISTINCT ON (source.name) *
    FROM check_runs source
    WHERE source.change_id = sqlc.arg(change_id) AND source.head_sha = sqlc.arg(from_head)
      AND source.status = 'completed' AND source.conclusion = 'success'
    ORDER BY source.name, source.attempt DESC
)
INSERT INTO check_runs (change_id, head_sha, name, external_id, status, conclusion,
    started_at, completed_at, details_url, output_title, output_summary, output_text,
    app_id, reporter, attempt, ttl_seconds, copied_from_head_sha)
SELECT latest.change_id, sqlc.arg(to_head)::text, latest.name, latest.external_id, latest.status,
    latest.conclusion, latest.started_at, latest.completed_at, latest.details_url,
    latest.output_title, latest.output_summary, latest.output_text, latest.app_id,
    latest.reporter, 1, latest.ttl_seconds, sqlc.arg(from_head)::text
FROM latest
ON CONFLICT (change_id, head_sha, name, attempt) DO NOTHING
RETURNING *;
