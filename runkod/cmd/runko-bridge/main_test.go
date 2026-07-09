package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

func newTestBridge(githubURL string) *bridge {
	return &bridge{
		secret:      []byte("hmac-secret"),
		org:         "runko",
		dispatchURL: githubURL + "/repos/acme/runko/dispatches",
		githubToken: "ghp_test",
		eventType:   "runko-change",
		client:      &http.Client{Timeout: 5 * time.Second},
		seen:        newSeenSet(4),
	}
}

func signedRequest(t *testing.T, b *bridge, env checks.WebhookEnvelope) *http.Request {
	t.Helper()
	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	req.Header.Set(checks.SignatureHeader, "sha256="+checks.SignPayload(b.secret, payload))
	return req
}

func updatedEnvelope(deliveryID string) checks.WebhookEnvelope {
	return checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  deliveryID,
		Type:        "change.updated",
		OccurredAt:  time.Now(),
		OrgID:       "runko",
		Change: checks.WebhookChange{
			ID: "Iabc", State: "open", BaseSHA: "base", HeadSHA: "head",
			GitRef: "refs/changes/Iabc/head", Title: "t",
			Actor: checks.WebhookActor{Type: "user", ID: "saxo"},
		},
		Affected: &checks.WebhookAffected{
			ComputationID: "aff_1",
			Projects: []checks.WebhookAffectedProject{
				{Name: "platform", Path: ""}, {Name: "web", Path: "web"},
			},
		},
	}
}

func TestBridgeForwardsChangeUpdated(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = json.Marshal(json.RawMessage(mustRead(t, r)))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gh.Close()

	b := newTestBridge(gh.URL)
	rec := httptest.NewRecorder()
	b.handleWebhook(rec, signedRequest(t, b, updatedEnvelope("d1")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bridge status: want 204, got %d: %s", rec.Code, rec.Body)
	}
	if gotAuth != "Bearer ghp_test" {
		t.Fatalf("github auth header: got %q", gotAuth)
	}
	var dispatch struct {
		EventType     string `json:"event_type"`
		ClientPayload struct {
			ChangeID         string   `json:"change_id"`
			HeadSHA          string   `json:"head_sha"`
			GitRef           string   `json:"git_ref"`
			Trigger          string   `json:"trigger"`
			DeliveryID       string   `json:"delivery_id"`
			AffectedProjects []string `json:"affected_projects"`
		} `json:"client_payload"`
	}
	if err := json.Unmarshal(gotBody, &dispatch); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if dispatch.EventType != "runko-change" {
		t.Fatalf("event_type: got %q", dispatch.EventType)
	}
	cp := dispatch.ClientPayload
	if cp.ChangeID != "Iabc" || cp.HeadSHA != "head" || cp.GitRef != "refs/changes/Iabc/head" ||
		cp.Trigger != "change.updated" || cp.DeliveryID != "d1" {
		t.Fatalf("client_payload: %+v", cp)
	}
	if len(cp.AffectedProjects) != 2 || cp.AffectedProjects[0] != "platform" || cp.AffectedProjects[1] != "web" {
		t.Fatalf("affected_projects: %+v", cp.AffectedProjects)
	}
}

func TestBridgeRejectsBadSignature(t *testing.T) {
	var called atomic.Bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gh.Close()

	b := newTestBridge(gh.URL)
	payload, _ := checks.MarshalEnvelope(updatedEnvelope("d1"))
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	req.Header.Set(checks.SignatureHeader, "sha256=deadbeef")
	rec := httptest.NewRecorder()
	b.handleWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized || called.Load() {
		t.Fatalf("want 401 with no github call, got %d called=%v", rec.Code, called.Load())
	}
}

func TestBridgeAcksAndIgnoresForeignEvents(t *testing.T) {
	var calls atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gh.Close()
	b := newTestBridge(gh.URL)

	landed := updatedEnvelope("d-landed")
	landed.Type = "change.landed"
	otherOrg := updatedEnvelope("d-other")
	otherOrg.OrgID = "acme"
	unattributed := updatedEnvelope("d-noorg")
	unattributed.OrgID = ""

	for _, env := range []checks.WebhookEnvelope{landed, otherOrg, unattributed} {
		rec := httptest.NewRecorder()
		b.handleWebhook(rec, signedRequest(t, b, env))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s/%q: want 204 ack, got %d", env.Type, env.OrgID, rec.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("foreign events must never reach github, got %d calls", calls.Load())
	}
}

func TestBridgeDedupesDeliveryID(t *testing.T) {
	var calls atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gh.Close()
	b := newTestBridge(gh.URL)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		b.handleWebhook(rec, signedRequest(t, b, updatedEnvelope("d-same")))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: want 204, got %d", i, rec.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("redelivery must not re-dispatch, got %d calls", calls.Load())
	}
}

func TestBridgeSurfacesGitHubFailureForRetry(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer gh.Close()
	b := newTestBridge(gh.URL)

	rec := httptest.NewRecorder()
	b.handleWebhook(rec, signedRequest(t, b, updatedEnvelope("d-fail")))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("github failure must surface non-2xx (outbox retries), got %d", rec.Code)
	}
	// The failed delivery must NOT be marked seen - the retry must go through.
	if b.seen.contains("d-fail") {
		t.Fatalf("failed dispatch marked seen - outbox retry would be dropped")
	}
}

func mustRead(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	return body
}
