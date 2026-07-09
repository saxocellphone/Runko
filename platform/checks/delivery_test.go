package checks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeliverSuccess(t *testing.T) {
	var gotSignature, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get(SignatureHeader)
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	payload := []byte(`{"hello":"world"}`)
	secret := []byte("shh-its-a-secret")

	result := Deliver(context.Background(), server.Client(), server.URL, payload, secret)
	if !result.Success || result.StatusCode != http.StatusOK {
		t.Fatalf("expected success, got %+v", result)
	}
	if gotBody != string(payload) {
		t.Fatalf("server received body %q, want %q", gotBody, payload)
	}
	wantSig := "sha256=" + SignPayload(secret, payload)
	if gotSignature != wantSig {
		t.Fatalf("signature header = %q, want %q", gotSignature, wantSig)
	}
	if !VerifySignature(secret, payload, SignPayload(secret, payload)) {
		t.Fatalf("VerifySignature should accept a signature produced by SignPayload")
	}
	if VerifySignature(secret, payload, "deadbeef") {
		t.Fatalf("VerifySignature should reject a wrong signature")
	}
}

func TestDeliverServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	result := Deliver(context.Background(), server.Client(), server.URL, []byte(`{}`), []byte("secret"))
	if result.Success {
		t.Fatalf("expected failure for a 500 response")
	}
	if result.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", result.StatusCode)
	}
	if result.Err == nil {
		t.Fatalf("expected a non-nil error")
	}
}

func TestDeliverConnectionRefused(t *testing.T) {
	// A closed server: nothing is listening on this address.
	server := httptest.NewServer(nil)
	url := server.URL
	server.Close()

	result := Deliver(context.Background(), http.DefaultClient, url, []byte(`{}`), []byte("secret"))
	if result.Success || result.Err == nil {
		t.Fatalf("expected a connection error, got %+v", result)
	}
}

func TestNextBackoffDoublesAndCaps(t *testing.T) {
	base := 1 * time.Second
	max := 30 * time.Second

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second}, // would be 32s, capped at 30s
		{7, 30 * time.Second},
	}
	for _, tc := range cases {
		got := NextBackoff(tc.attempt, base, max)
		if got != tc.want {
			t.Errorf("NextBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
