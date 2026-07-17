package checks

import "time"

// DeployImagesReadyWebhook mirrors
// docs/spec/webhooks/deploy-images-ready.schema.json - a standalone shape,
// NOT the change-event envelope: it is keyed by the landed trunk sha, not a
// change. Emitted once every affected image for a landed commit has reported
// its built digest (via runko-ci report-image); the runko-deployer consumes
// it to pin the digests into the GitOps repo and let Argo CD roll - the
// inverted CD trigger (GitHub only builds and reports, Runko rolls).
// Delivery rides the same outbox/HMAC/retry machinery as the change
// envelope. Same keep-in-sync-by-hand debt as WebhookEnvelope.
type DeployImagesReadyWebhook struct {
	SpecVersion string       `json:"spec_version"`
	DeliveryID  string       `json:"delivery_id"`
	Type        string       `json:"type"` // always "deploy.images_ready"
	OccurredAt  time.Time    `json:"occurred_at"`
	OrgID       string       `json:"org_id"`
	MonorepoID  string       `json:"monorepo_id"`
	Deploy      DeployImages `json:"deploy"`
}

// DeployImages is the set of digest-pinned images for one landed commit -
// pinned in a single GitOps commit so Argo CD rolls once (single-replica
// Recreate; migration-findings #33).
type DeployImages struct {
	TrunkSHA   string        `json:"trunk_sha"`
	ChangeKey  string        `json:"change_key,omitempty"`
	Images     []DeployImage `json:"images"`
	Provenance string        `json:"provenance,omitempty"`
}

// DeployImage is one built image's deploy reference. ImageRef (the full
// pushed reference sans digest) keeps the deployer registry-agnostic -
// nothing hardcodes ghcr.io.
type DeployImage struct {
	Image    string `json:"image"`
	ImageRef string `json:"image_ref,omitempty"`
	Digest   string `json:"digest"`
}
