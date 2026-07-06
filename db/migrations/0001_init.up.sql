-- Initial control-plane schema (docs/design.md §9.2, §7.1).
--
-- Postgres holds workflow state (changes, review, workspaces, agents,
-- policies, templates) plus a REBUILDABLE INDEX of tree-resident structure
-- (projects, owners) - never an independent source of truth (§10.3). Any row
-- here derived from the tree (projects, project_owners) must be reconstructible
-- by re-reading trunk; this schema does not become truth just because it's
-- convenient to query.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Organization: tenant boundary for auth, policy, templates, agent policy (§7.1).
CREATE TABLE orgs (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Monorepo: single Git repository; trunk ref is source of truth (§7.1).
-- Exactly one primary monorepo per org in v1 (§7.1 "exactly one primary per org").
CREATE TABLE monorepos (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    trunk_ref  TEXT NOT NULL DEFAULT 'main',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id)
);

-- Agent policy: org-level defaults, overridable per agent identity (§8.7).
CREATE TABLE agent_policies (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                    UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name                      TEXT NOT NULL,
    require_workspace_affinity BOOLEAN NOT NULL DEFAULT true,
    max_changed_files         INT NOT NULL DEFAULT 40,
    max_diff_bytes            BIGINT NOT NULL DEFAULT 512000,
    can_create_projects       BOOLEAN NOT NULL DEFAULT true,
    can_land_changes          BOOLEAN NOT NULL DEFAULT false,
    can_modify_owners         BOOLEAN NOT NULL DEFAULT false,
    can_enable_capabilities   TEXT[] NOT NULL DEFAULT '{}',
    denylist_paths            TEXT[] NOT NULL DEFAULT '{}',
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

-- Actor: users, groups, and agent identities (§7.5). Agent-type actors
-- optionally bind to an AgentPolicy; a null policy means "org default".
CREATE TYPE actor_type AS ENUM ('user', 'group', 'agent');

CREATE TABLE actors (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    type            actor_type NOT NULL,
    external_ref    TEXT NOT NULL, -- OIDC subject | group name | agent token/install id
    display_name    TEXT,
    agent_policy_id UUID REFERENCES agent_policies(id),
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, type, external_ref),
    CHECK (agent_policy_id IS NULL OR type = 'agent')
);

-- Template: versioned scaffold used by create; org-customizable (§7.1, §10.4).
CREATE TABLE templates (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    template_key         TEXT NOT NULL, -- e.g. 'go-service-v3'
    name                 TEXT NOT NULL,
    project_type         TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    default_capabilities TEXT[] NOT NULL DEFAULT '{}',
    version              INT NOT NULL DEFAULT 1,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, template_key)
);

-- Project: rebuildable index of tree-resident PROJECT.yaml (§10.3). id/created_at
-- here are control-plane-derived per §10.2 and never written into the tree.
-- indexed_at_sha records which trunk commit this row reflects, so drift is
-- detectable and reindexing is always "re-read the tree", never "trust this row".
CREATE TABLE projects (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    monorepo_id           UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL,
    path                  TEXT NOT NULL,
    project_type          TEXT NOT NULL,
    template_id           UUID REFERENCES templates(id),
    visibility            TEXT NOT NULL DEFAULT 'default',
    capabilities          TEXT[] NOT NULL DEFAULT '{}',
    declared_dependencies TEXT[] NOT NULL DEFAULT '{}',
    indexed_at_sha        TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (monorepo_id, name),
    UNIQUE (monorepo_id, path)
);

-- Effective owners resolved for a project - indexed from tree OWNERS +
-- PROJECT.yaml, not hand-maintained (§7.3, §10.2).
CREATE TABLE project_owners (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_ref  TEXT NOT NULL, -- e.g. 'group:commerce-eng'
    source     TEXT NOT NULL CHECK (source IN ('project_manifest', 'path_owners', 'org_default')),
    PRIMARY KEY (project_id, owner_ref)
);

-- Inferred dependency edges from the async indexer - ADVISORY ONLY, never
-- gates merges (§13.3). Kept separate from projects.declared_dependencies so
-- promotion to declared is always an explicit, visible action.
CREATE TABLE inferred_dependencies (
    project_id             UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    depends_on_project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    confidence             REAL NOT NULL DEFAULT 1.0,
    last_seen_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, depends_on_project_id)
);

-- Workspace: CitC session data model (§12.2). snapshot_ref points at
-- refs/workspaces/<id>/head; durable content lives in Git, never here.
CREATE TYPE workspace_mode AS ENUM ('sparse_local', 'remote_vm', 'josh_slice');
CREATE TYPE workspace_status AS ENUM ('active', 'detached', 'closed');

CREATE TABLE workspaces (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    monorepo_id        UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    principal_actor_id UUID NOT NULL REFERENCES actors(id),
    coding_session_id  UUID,
    base_revision      TEXT NOT NULL,
    project_affinity   TEXT[] NOT NULL DEFAULT '{}',
    write_allowlist    TEXT[] NOT NULL DEFAULT '{}',
    snapshot_ref       TEXT NOT NULL,
    mode               workspace_mode NOT NULL DEFAULT 'sparse_local',
    status             workspace_status NOT NULL DEFAULT 'active',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Change: reviewable unit (§7.4, §13.2). change_key is the stable Change-Id
-- (Gerrit-trailer discipline) that survives rebase/amend; id is the internal
-- surrogate key. depends_on_change_id is the stack parent.
CREATE TYPE change_state AS ENUM ('open', 'landed', 'abandoned');

CREATE TABLE changes (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    monorepo_id          UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    change_key           TEXT NOT NULL,
    number               BIGSERIAL NOT NULL,
    state                change_state NOT NULL DEFAULT 'open',
    base_sha             TEXT NOT NULL,
    head_sha             TEXT NOT NULL,
    git_ref              TEXT NOT NULL,
    title                TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    test_plan            TEXT NOT NULL DEFAULT '',
    authored_by_actor_id UUID NOT NULL REFERENCES actors(id),
    depends_on_change_id UUID REFERENCES changes(id),
    mechanical           BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    landed_at            TIMESTAMPTZ,
    landed_sha           TEXT,
    UNIQUE (monorepo_id, change_key),
    UNIQUE (monorepo_id, number)
);

-- Agent co-authors on a Change (§8.6).
CREATE TABLE change_assisted_by (
    change_id UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    actor_id  UUID NOT NULL REFERENCES actors(id),
    PRIMARY KEY (change_id, actor_id)
);

-- Cached affected computation for one (change, head_sha) - recomputed whenever
-- head_sha changes, never trusted stale across a rebase (§13.3, §14.4.3).
CREATE TABLE change_affected (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    change_id       UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    head_sha        TEXT NOT NULL,
    computation_id  TEXT NOT NULL,
    run_everything  BOOLEAN NOT NULL DEFAULT false,
    reason_codes    TEXT[] NOT NULL DEFAULT '{}',
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (change_id, head_sha)
);

CREATE TABLE change_affected_projects (
    change_affected_id UUID NOT NULL REFERENCES change_affected(id) ON DELETE CASCADE,
    project_id         UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY (change_affected_id, project_id)
);

-- Review comments (§13.4).
CREATE TABLE change_comments (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    change_id        UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    author_actor_id  UUID NOT NULL REFERENCES actors(id),
    body             TEXT NOT NULL,
    path             TEXT,
    line             INT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Required-owner snapshot for a Change, evaluated from touched paths, with
-- global-approver / mechanical-change relaxations applied (§7.3).
CREATE TABLE change_owner_requirements (
    change_id             UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    owner_ref             TEXT NOT NULL,
    satisfied             BOOLEAN NOT NULL DEFAULT false,
    satisfied_by_actor_id UUID REFERENCES actors(id),
    satisfied_at          TIMESTAMPTZ,
    PRIMARY KEY (change_id, owner_ref)
);

-- CheckRun (§14.4.2). UNIQUE(change_id, head_sha, name, attempt) lets reruns
-- create new attempts linked to the same logical check without clobbering history.
CREATE TYPE check_status AS ENUM ('queued', 'in_progress', 'completed');
CREATE TYPE check_conclusion AS ENUM ('success', 'failure', 'cancelled', 'skipped', 'timed_out', 'action_required', 'neutral');

CREATE TABLE check_runs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    change_id      UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    head_sha       TEXT NOT NULL,
    name           TEXT NOT NULL,
    external_id    TEXT NOT NULL,
    status         check_status NOT NULL DEFAULT 'queued',
    conclusion     check_conclusion,
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    details_url    TEXT,
    output_title   TEXT,
    output_summary TEXT,
    output_text    TEXT,
    app_id         TEXT,
    reporter       TEXT NOT NULL,
    attempt        INT NOT NULL DEFAULT 1,
    ttl_seconds    INT NOT NULL DEFAULT 86400,
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (change_id, head_sha, name, attempt),
    CHECK (status <> 'completed' OR conclusion IS NOT NULL)
);

CREATE TABLE check_annotations (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_run_id     UUID NOT NULL REFERENCES check_runs(id) ON DELETE CASCADE,
    path             TEXT NOT NULL,
    start_line       INT NOT NULL,
    end_line         INT NOT NULL,
    annotation_level TEXT NOT NULL CHECK (annotation_level IN ('notice', 'warning', 'failure')),
    message          TEXT NOT NULL,
    title            TEXT
);

-- Webhook outbox (§14.4.1): signed, retried with backoff, dead-lettered and
-- replayable. delivery_id is the value exposed in the envelope and is stable
-- across retries of the same logical delivery.
CREATE TYPE webhook_delivery_status AS ENUM ('pending', 'delivered', 'failed', 'dead_letter');

CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    delivery_id     UUID NOT NULL DEFAULT gen_random_uuid(),
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL,
    status          webhook_delivery_status NOT NULL DEFAULT 'pending',
    attempt         INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ
);

-- Mirror ref-map/cursors (§18.6). Rebuildable from the two Git histories;
-- 'frozen' implements invariant 4 (divergence freezes landing, loudly).
CREATE TABLE mirror_cursors (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    monorepo_id     UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    remote_name     TEXT NOT NULL,
    ref_name        TEXT NOT NULL,
    last_synced_sha TEXT,
    writer          TEXT NOT NULL,
    frozen          BOOLEAN NOT NULL DEFAULT false,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (monorepo_id, remote_name, ref_name)
);

CREATE INDEX idx_projects_monorepo ON projects (monorepo_id);
CREATE INDEX idx_workspaces_principal ON workspaces (principal_actor_id);
CREATE INDEX idx_changes_state ON changes (monorepo_id, state);
CREATE INDEX idx_check_runs_change ON check_runs (change_id, head_sha);
CREATE INDEX idx_webhook_deliveries_pending ON webhook_deliveries (status, next_attempt_at) WHERE status IN ('pending', 'failed');
