package runkod

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

func postDeployImage(t *testing.T, srv *Server, sha, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/deploys/"+sha+"/images", strings.NewReader(body))
	req.SetPathValue("sha", sha)
	rr := httptest.NewRecorder()
	srv.handlePostDeployImage(rr, req)
	return rr
}

// TestDeployImageEndpointEmitsImagesReadyOnce drives the CD report-back path:
// report-image fills the record opened at land, and the completing report
// emits deploy.images_ready exactly once (the runko-deployer's rollout signal).
func TestDeployImageEndpointEmitsImagesReadyOnce(t *testing.T) {
	mem := NewMemStore()
	srv := &Server{Store: mem, SettingsOrg: "org"}
	ctx := context.Background()
	if err := mem.OpenDeployRecord(ctx, "sha1", "Ichange", "https://ci/run/1", []string{"runkod", "web"}); err != nil {
		t.Fatalf("open: %v", err)
	}

	// Well-formed registry content digests (sha256:<64 hex>).
	digestRunkod := "sha256:" + strings.Repeat("a", 64)
	digestWeb := "sha256:" + strings.Repeat("b", 64)

	// First of two images: 201, nothing ready yet.
	if rr := postDeployImage(t, srv, "sha1", `{"image":"runkod","image_ref":"ghcr.io/x/runkod","digest":"`+digestRunkod+`","reporter":"gha"}`); rr.Code != http.StatusCreated {
		t.Fatalf("first report: got %d, body %q", rr.Code, rr.Body.String())
	}
	if due, _ := mem.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour)); len(due) != 0 {
		t.Fatalf("no webhook until the expected set is complete, got %d", len(due))
	}

	// Completing image: 201 + exactly one deploy.images_ready enqueued.
	if rr := postDeployImage(t, srv, "sha1", `{"image":"web","digest":"`+digestWeb+`"}`); rr.Code != http.StatusCreated {
		t.Fatalf("second report: got %d", rr.Code)
	}
	due, err := mem.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("want exactly one deploy.images_ready, got %d", len(due))
	}
	var hook checks.DeployImagesReadyWebhook
	if err := json.Unmarshal(due[0].Payload, &hook); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if hook.Type != "deploy.images_ready" || hook.Deploy.TrunkSHA != "sha1" || len(hook.Deploy.Images) != 2 {
		t.Fatalf("images_ready payload: %+v", hook)
	}

	// A late duplicate report must NOT re-emit (Argo rolls once).
	if rr := postDeployImage(t, srv, "sha1", `{"image":"runkod","digest":"`+digestRunkod+`"}`); rr.Code != http.StatusCreated {
		t.Fatalf("dup report: got %d", rr.Code)
	}
	if due, _ := mem.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour)); len(due) != 1 {
		t.Fatalf("duplicate report must not re-emit, got %d deliveries", len(due))
	}

	// A report for a sha with no open record is 404 (expected set unknown).
	if rr := postDeployImage(t, srv, "nope", `{"image":"runkod","digest":"`+digestRunkod+`"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown sha: got %d", rr.Code)
	}

	// Missing required fields: 400.
	if rr := postDeployImage(t, srv, "sha1", `{"image":"runkod"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing digest: got %d", rr.Code)
	}
}

// TestDeployImageEndpointRejectsMalformedDigest guards the input-validation gap
// that turned a hand-typed diagnostic value (sha256:diagnostic) into an
// unpullable image pin and an outage: a digest that is not sha256:<64 hex> must
// be rejected with 400 BEFORE it can reach a deploy record, even when a record
// is open for the commit.
func TestDeployImageEndpointRejectsMalformedDigest(t *testing.T) {
	mem := NewMemStore()
	srv := &Server{Store: mem, SettingsOrg: "org"}
	ctx := context.Background()
	if err := mem.OpenDeployRecord(ctx, "sha1", "Ichange", "https://ci/run/1", []string{"runkod"}); err != nil {
		t.Fatalf("open: %v", err)
	}

	bad := []string{
		"sha256:diagnostic",                 // the outage value
		"sha256:aaa",                        // too short
		"sha256:" + strings.Repeat("a", 63), // 63 hex, off by one
		"sha256:" + strings.Repeat("a", 65), // 65 hex, off by one
		"sha256:" + strings.Repeat("A", 64), // uppercase hex
		"sha256:" + strings.Repeat("g", 64), // non-hex
		strings.Repeat("a", 64),             // missing algorithm prefix
		"sha512:" + strings.Repeat("a", 64), // wrong algorithm
	}
	for _, d := range bad {
		rr := postDeployImage(t, srv, "sha1", `{"image":"runkod","digest":"`+d+`"}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("digest %q: got %d, want 400", d, rr.Code)
		}
	}

	// The rejected reports must not have opened/advanced the record: nothing ready.
	if due, _ := mem.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour)); len(due) != 0 {
		t.Fatalf("malformed reports must not emit a webhook, got %d", len(due))
	}
}
