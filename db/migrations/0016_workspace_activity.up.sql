-- Agent session activity (§12.6.1, DAG stage 19): harness-reported
-- "what is the agent doing" rows - the platform's first CLIENT-CLAIMED
-- data, observability-only by decision (never policy, gates, or affected
-- computation). Deliberately a separate table from workspace_events:
-- that table holds receive-time facts, and its 500-row timeline would be
-- evicted by an hour of tool-call chatter. detail is metadata (a path, a
-- command line) - truncated and secret-scanned at ingest, NEVER file
-- content (§12.1). workspace_id is the human ref segment as plain TEXT
-- with no FK (the workspace_events precedent): deletion is an explicit
-- verb the workspace-delete core calls; close keeps the rows.
CREATE TABLE workspace_activity (
    id           BIGSERIAL PRIMARY KEY,
    org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    monorepo_id  UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    actor        TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL CHECK (kind IN
        ('read','edit','command','search','note')),
    detail       TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspace_activity_ws ON workspace_activity (monorepo_id, workspace_id, id DESC);
