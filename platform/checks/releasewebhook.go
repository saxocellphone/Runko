package checks

import "time"

// ReleaseCreatedWebhook mirrors docs/spec/webhooks/release-created.schema.json
// (§14.10.3, stage 17b) - a standalone shape, NOT the change-event envelope:
// that envelope requires a `change` object and releases are not change
// events. Same keep-in-sync-by-hand debt as WebhookEnvelope. Delivery rides
// the same outbox/HMAC/retry machinery; consumers key CD on it instead of
// tag-polling (§14.10.1).
type ReleaseCreatedWebhook struct {
	SpecVersion string                `json:"spec_version"`
	DeliveryID  string                `json:"delivery_id"`
	Type        string                `json:"type"` // always "release.created"
	OccurredAt  time.Time             `json:"occurred_at"`
	OrgID       string                `json:"org_id"`
	MonorepoID  string                `json:"monorepo_id"`
	Release     ReleaseWebhookRelease `json:"release"`
	API         ReleaseWebhookAPI     `json:"api"`
}

type ReleaseWebhookProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type ReleaseWebhookRelease struct {
	Project       ReleaseWebhookProject `json:"project"`
	Version       string                `json:"version"`
	TagRef        string                `json:"tag_ref"`
	TagSHA        string                `json:"tag_sha"`
	TargetSHA     string                `json:"target_sha,omitempty"`
	HeadChangeKey string                `json:"head_change_key"`
	// Changelog is optional in the webhook when large - consumers fetch
	// the full text via api.release_url.
	Changelog string       `json:"changelog,omitempty"`
	CreatedBy WebhookActor `json:"created_by"`
}

type ReleaseWebhookAPI struct {
	ReleaseURL string `json:"release_url"`
	ChangeURL  string `json:"change_url,omitempty"`
}
