package checks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// WebhookEnvelope mirrors docs/spec/webhooks/webhook-envelope.schema.json.
// Keep in sync by hand until codegen exists (same debt as project.Manifest,
// see project/doc.go).
type WebhookEnvelope struct {
	SpecVersion    string                `json:"spec_version"`
	DeliveryID     string                `json:"delivery_id"`
	Type           string                `json:"type"`
	OccurredAt     time.Time             `json:"occurred_at"`
	OrgID          string                `json:"org_id"`
	MonorepoID     string                `json:"monorepo_id"`
	Change         WebhookChange         `json:"change"`
	Affected       *WebhookAffected      `json:"affected,omitempty"`
	ChecksExpected []string              `json:"checks_expected,omitempty"`
	Rerun          *WebhookRerun         `json:"rerun,omitempty"`
	Comment        *WebhookComment       `json:"comment,omitempty"`
	ReviewRequest  *WebhookReviewRequest `json:"review_request,omitempty"`
	API            WebhookAPI            `json:"api"`
}

type WebhookChange struct {
	ID      string       `json:"id"`
	Number  int64        `json:"number"`
	URL     string       `json:"url"`
	State   string       `json:"state"`
	BaseSHA string       `json:"base_sha"`
	HeadSHA string       `json:"head_sha"`
	GitRef  string       `json:"git_ref"`
	Title   string       `json:"title"`
	Actor   WebhookActor `json:"actor"`
}

type WebhookActor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type WebhookAffectedProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type WebhookAffected struct {
	ComputationID string                   `json:"computation_id"`
	Projects      []WebhookAffectedProject `json:"projects"`
	Paths         []string                 `json:"paths"`
	ReasonCodes   []string                 `json:"reason_codes"`
	RunEverything bool                     `json:"run_everything"`
}

type WebhookRerun struct {
	CheckName   string       `json:"check_name"`
	RequestedBy WebhookActor `json:"requested_by"`
}

// WebhookComment is change.commented's payload object (§13.4.1). It carries
// ids and the anchor, NEVER the body - consumers fetch bodies via the API so
// CI logs don't accumulate review text (the schema's own rule).
type WebhookComment struct {
	ID       string       `json:"id"`
	ParentID string       `json:"parent_id,omitempty"`
	Path     string       `json:"path,omitempty"`
	Side     string       `json:"side,omitempty"`
	Line     int          `json:"line,omitempty"`
	Resolved bool         `json:"resolved,omitempty"`
	Author   WebhookActor `json:"author"`
}

// WebhookReviewRequest is change.review_requested's payload object
// (§13.4.2); the reviewer enters the derived attention set.
type WebhookReviewRequest struct {
	Reviewer    WebhookActor `json:"reviewer"`
	RequestedBy WebhookActor `json:"requested_by"`
}

type WebhookAPI struct {
	ChangeURL   string `json:"change_url"`
	AffectedURL string `json:"affected_url"`
	ChecksURL   string `json:"checks_url"`
}

// MarshalEnvelope serializes an envelope to its canonical JSON form, for
// validation against docs/spec/webhooks/webhook-envelope.schema.json in
// contract tests and for signing/delivery.
func MarshalEnvelope(env WebhookEnvelope) ([]byte, error) {
	return json.Marshal(env)
}

// SignPayload computes the HMAC-SHA256 signature of a webhook payload,
// hex-encoded, matching §14.4.1's "signed webhooks (HMAC)" requirement.
func SignPayload(secret []byte, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a hex-encoded HMAC-SHA256 signature in constant
// time, as a receiving CI plugin would.
func VerifySignature(secret []byte, payload []byte, signature string) bool {
	expected := SignPayload(secret, payload)
	return hmac.Equal([]byte(expected), []byte(signature))
}
