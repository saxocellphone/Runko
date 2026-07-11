package checks

import (
	"encoding/json"
	"testing"
	"time"
)

// TestReleaseCreatedWebhookMatchesSchema pins the Go struct to
// docs/spec/webhooks/release-created.schema.json (stage 17b) - the same
// contract bar the change-event envelope has had since stage 8.
func TestReleaseCreatedWebhookMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "webhooks/release-created.schema.json")

	hook := ReleaseCreatedWebhook{
		SpecVersion: "1",
		DeliveryID:  "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		Type:        "release.created",
		OccurredAt:  time.Date(2026, 7, 11, 3, 4, 5, 0, time.UTC),
		OrgID:       "org_1",
		MonorepoID:  "repo_1",
		Release: ReleaseWebhookRelease{
			Project:       ReleaseWebhookProject{ID: "checkout-api", Name: "checkout-api", Path: "commerce/checkout"},
			Version:       "1.2.3",
			TagRef:        "refs/tags/commerce/checkout/v1.2.3",
			TagSHA:        "abc1234",
			TargetSHA:     "def5678",
			HeadChangeKey: "I1f057dc0",
			Changelog:     "## 1.2.3\n- Reject invalid SKUs (I1f057dc0)",
			CreatedBy:     WebhookActor{Type: "user", ID: "victor"},
		},
		API: ReleaseWebhookAPI{ReleaseURL: "https://x/api/projects/checkout-api/releases"},
	}
	payload, err := json.Marshal(hook)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateJSON(t, sch, payload); err != nil {
		t.Fatalf("release.created payload failed schema validation: %v\npayload: %s", err, payload)
	}

	// The minimal shape (no target_sha/changelog/change_url) validates too
	// - those are the schema's optional fields.
	hook.Release.TargetSHA = ""
	hook.Release.Changelog = ""
	payload2, _ := json.Marshal(hook)
	if err := validateJSON(t, sch, payload2); err != nil {
		t.Fatalf("minimal release.created payload failed: %v\npayload: %s", err, payload2)
	}
}
