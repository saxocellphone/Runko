package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/githubapp"
)

func newTestBridge(githubURL string) *bridge {
	return &bridge{
		secret:      []byte("hmac-secret"),
		org:         "runko",
		dispatchURL: githubURL + "/repos/acme/runko/dispatches",
		token:       func() (string, error) { return "ghp_test", nil },
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

// TestBridgeAppAuthMintsInstallationToken drives the whole App-auth
// chain against a stub GitHub: installation resolved from the repo, an
// installation token minted through the App JWT, and the dispatch sent
// with that minted token as its Bearer.
func TestBridgeAppAuthMintsInstallationToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	var dispatchAuth atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/runko/installation", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 7})
	})
	mux.HandleFunc("POST /app/installations/7/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_minted",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})
	mux.HandleFunc("POST /repos/acme/runko/dispatches", func(w http.ResponseWriter, r *http.Request) {
		dispatchAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})
	gh := httptest.NewServer(mux)
	defer gh.Close()

	app, err := githubapp.New("12345", keyPEM, gh.URL)
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}
	b := newTestBridge(gh.URL)
	b.token = app.TokenSource("acme/runko")

	rec := httptest.NewRecorder()
	b.handleWebhook(rec, signedRequest(t, b, updatedEnvelope("d-app")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bridge status: want 204, got %d: %s", rec.Code, rec.Body)
	}
	if got, _ := dispatchAuth.Load().(string); got != "Bearer ghs_minted" {
		t.Fatalf("dispatch must carry the minted installation token, got %q", got)
	}
}

// TestBridgeTokenMintFailureSurfacesForRetry: no token, no dispatch - a
// 502 hands the delivery back to the outbox, and the delivery id stays
// unseen so the retry is not deduped away.
func TestBridgeTokenMintFailureSurfacesForRetry(t *testing.T) {
	var called atomic.Bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gh.Close()
	b := newTestBridge(gh.URL)
	b.token = func() (string, error) { return "", errors.New("mint failed") }

	rec := httptest.NewRecorder()
	b.handleWebhook(rec, signedRequest(t, b, updatedEnvelope("d-mint-fail")))
	if rec.Code != http.StatusBadGateway || called.Load() {
		t.Fatalf("want 502 with no github call, got %d called=%v", rec.Code, called.Load())
	}
	if b.seen.contains("d-mint-fail") {
		t.Fatal("failed mint marked seen - outbox retry would be dropped")
	}
}

// TestGithubTokenSourceValidation pins the flag contract: exactly one of
// PAT or App auth, and App auth needs its key file.
func TestGithubTokenSourceValidation(t *testing.T) {
	cases := []struct {
		name                string
		pat, appID, keyFile string
		wantErr             string
	}{
		{"neither", "", "", "", "github auth is required"},
		{"both", "ghp_x", "12345", "", "mutually exclusive"},
		{"app without key", "", "12345", "", "--github-app-key-file is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := githubTokenSource(tc.pat, tc.appID, tc.keyFile, "https://api.github.com", "acme/runko")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
	src, err := githubTokenSource("ghp_x", "", "", "https://api.github.com", "acme/runko")
	if err != nil {
		t.Fatalf("PAT source: %v", err)
	}
	if tok, _ := src(); tok != "ghp_x" {
		t.Fatalf("PAT source: got %q", tok)
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
