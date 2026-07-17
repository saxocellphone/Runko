-- Deploy records (inverted CD trigger, §14.10): when a change LANDS, runkod
-- opens a record naming the affected images whose digests the post-land build
-- must report (runko-ci report-image -> POST /api/deploys/{sha}/images). Once
-- every expected image has reported, the record flips to 'ready' and runkod
-- emits deploy.images_ready - the runko-deployer pins the digests into the
-- GitOps repo and Argo CD rolls. GitHub only builds; Runko triggers.
CREATE TABLE deploy_records (
    monorepo_id UUID NOT NULL,
    trunk_sha   TEXT NOT NULL,
    change_key  TEXT NOT NULL,
    expected    TEXT[] NOT NULL,
    state       TEXT NOT NULL DEFAULT 'pending',
    provenance  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at    TIMESTAMPTZ,
    PRIMARY KEY (monorepo_id, trunk_sha)
);

-- One row per image the build reported a digest for. image_ref is the full
-- pushed reference sans digest, so the deployer pins image_ref@digest and
-- stays registry-agnostic (nothing hardcodes ghcr.io).
CREATE TABLE deploy_images (
    monorepo_id UUID NOT NULL,
    trunk_sha   TEXT NOT NULL,
    image       TEXT NOT NULL,
    image_ref   TEXT NOT NULL DEFAULT '',
    digest      TEXT NOT NULL,
    run_url     TEXT NOT NULL DEFAULT '',
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (monorepo_id, trunk_sha, image),
    FOREIGN KEY (monorepo_id, trunk_sha)
        REFERENCES deploy_records (monorepo_id, trunk_sha) ON DELETE CASCADE
);
