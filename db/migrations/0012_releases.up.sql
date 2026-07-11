-- Releases (§14.10.3, decided 2026-07-10; DAG stage 17b): immutable
-- first-class records of "this project version = this tag = this trunk
-- commit = this newest landed Change". Immutability is by construction -
-- no UPDATE or DELETE query exists over this table (GitHub
-- immutable-releases parity); a wrong release is followed by a corrected
-- one, never edited. The changelog column stores the derived markdown
-- (from landed Changes since the previous release tag) verbatim: it is a
-- record of what was announced, not a view to recompute.
CREATE TABLE releases (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    monorepo_id      UUID NOT NULL REFERENCES monorepos(id) ON DELETE CASCADE,
    project_name     TEXT NOT NULL,
    project_path     TEXT NOT NULL,
    version          TEXT NOT NULL,
    tag_ref          TEXT NOT NULL,
    tag_sha          TEXT NOT NULL,
    target_sha       TEXT NOT NULL,
    head_change_key  TEXT NOT NULL DEFAULT '',
    changelog        TEXT NOT NULL DEFAULT '',
    created_by       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (monorepo_id, project_name, version)
);

CREATE INDEX idx_releases_project ON releases (monorepo_id, project_name, created_at DESC);
