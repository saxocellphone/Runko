package checks

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDeployImagesReadyWebhookMatchesSchema pins the Go struct to
// docs/spec/webhooks/deploy-images-ready.schema.json - the same contract bar
// the change-event and release.created webhooks hold.
func TestDeployImagesReadyWebhookMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "webhooks/deploy-images-ready.schema.json")

	hook := DeployImagesReadyWebhook{
		SpecVersion: "1",
		DeliveryID:  "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		Type:        "deploy.images_ready",
		OccurredAt:  time.Date(2026, 7, 17, 3, 4, 5, 0, time.UTC),
		OrgID:       "org_1",
		MonorepoID:  "repo_1",
		Deploy: DeployImages{
			TrunkSHA:  "540cf3600287f09141b414f0cb30c9830069b1b8",
			ChangeKey: "Ia6f2b6ff",
			Images: []DeployImage{
				{Image: "runkod", ImageRef: "ghcr.io/saxocellphone/runko/runkod", Digest: "sha256:aaaa"},
				{Image: "web", ImageRef: "ghcr.io/saxocellphone/runko/web", Digest: "sha256:bbbb"},
			},
			Provenance: "https://github.com/saxocellphone/runko/actions/runs/123",
		},
	}
	payload, err := json.Marshal(hook)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateJSON(t, sch, payload); err != nil {
		t.Fatalf("deploy.images_ready payload failed schema validation: %v\npayload: %s", err, payload)
	}

	// The minimal shape (no change_key/image_ref/provenance) validates too -
	// those are the schema's optional fields.
	hook.Deploy.ChangeKey = ""
	hook.Deploy.Provenance = ""
	hook.Deploy.Images = []DeployImage{{Image: "runkod", Digest: "sha256:aaaa"}}
	payload2, _ := json.Marshal(hook)
	if err := validateJSON(t, sch, payload2); err != nil {
		t.Fatalf("minimal deploy.images_ready payload failed: %v\npayload: %s", err, payload2)
	}
}
