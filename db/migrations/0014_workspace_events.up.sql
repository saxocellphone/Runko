-- Workspace activity timeline (§12.6, DAG stage 18): stats-only rows
-- recorded at receive/land time - the observable history of a workspace
-- whose snapshot refs deliberately amend away their own past (§12.2).
-- NEVER file content (§12.1: Git is the only durable content store) -
-- numstat totals, shas, and actors only; §10.3's "cache and history,
-- not identity". workspace_id is the human ref segment as plain TEXT
-- with no FK (the migration-0005 origin_workspace precedent): deletion
-- is an explicit verb the workspace-delete core calls, and a registry
-- FK would couple row lifetimes the §12.2 metadata-only invariant keeps
-- separate (close keeps history; delete removes it).
CREATE TABLE workspace_events (
    id            BIGSERIAL PRIMARY KEY,
    org_id        UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    monorepo_id   UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    workspace_id  TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    event_type    TEXT NOT NULL CHECK (event_type IN
        ('snapshot_pushed','change_pushed','change_landed','change_abandoned','workspace_closed')),
    actor         TEXT NOT NULL DEFAULT '',
    sha           TEXT NOT NULL DEFAULT '',
    change_key    TEXT NOT NULL DEFAULT '',
    files_changed INT NOT NULL DEFAULT 0,
    additions     INT NOT NULL DEFAULT 0,
    deletions     INT NOT NULL DEFAULT 0,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspace_events_ws ON workspace_events (monorepo_id, workspace_id, id DESC);
