-- Review conversation (§13.4.1-13.4.2, decided 2026-07-10; DAG stage 16).
-- change_comments existed since 0001 (stage 2 built it speculatively, never
-- wired); this brings it up to the decided model. head_sha follows 0002's
-- approval precedent: comments bind to the Change head they were written
-- against - a differing (or NULL, pre-migration) head reads as "outdated",
-- never repositioned. parent_id makes threads one level deep (the handler
-- enforces root-only parents; the column is just the edge). resolved lives
-- on the root comment.
ALTER TABLE change_comments ADD COLUMN head_sha TEXT;
ALTER TABLE change_comments ADD COLUMN side TEXT CHECK (side IN ('base', 'head'));
ALTER TABLE change_comments ADD COLUMN parent_id UUID REFERENCES change_comments(id) ON DELETE CASCADE;
ALTER TABLE change_comments ADD COLUMN resolved BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX idx_change_comments_change ON change_comments (change_id, created_at);

-- Review requests (§13.4.2). reviewer/requested_by are principal names
-- (§15.1 interim registry); the attention set is DERIVED at read time from
-- these rows + approvals + comments + head_sha - nothing else is stored.
-- Re-requesting is an idempotent upsert (PK).
CREATE TABLE change_review_requests (
    change_id    UUID NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
    reviewer     TEXT NOT NULL,
    requested_by TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (change_id, reviewer)
);
