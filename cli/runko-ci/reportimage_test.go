package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReportImagePostsBearerRequest(t *testing.T) {
	var gotAuth string
	var gotReport ImageReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	report := ImageReport{
		Image: "runkod", ImageRef: "ghcr.io/saxocellphone/runko/runkod",
		Digest: "sha256:abc", RunURL: "https://ci/run/1", Reporter: "github-actions",
	}
	if err := ReportImage(context.Background(), server.Client(), server.URL, "tok_abc", report); err != nil {
		t.Fatalf("ReportImage: %v", err)
	}
	if gotAuth != "Bearer tok_abc" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer tok_abc")
	}
	if gotReport != report {
		t.Fatalf("server received %+v, want %+v", gotReport, report)
	}
}

func TestReportImageServerErrorSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := ReportImage(context.Background(), server.Client(), server.URL, "bad-token", ImageReport{
		Image: "runkod", Digest: "sha256:abc",
	})
	if err == nil {
		t.Fatalf("expected an error for a 401 response")
	}
}

// TestReportImageRetriesTransient5xx: same single-replica 503-window
// robustness report-check needs (migration-findings #33) - the upsert POST
// makes retrying safe.
func TestReportImageRetriesTransient5xx(t *testing.T) {
	oldBackoff := reportImageBackoff
	reportImageBackoff = time.Millisecond
	defer func() { reportImageBackoff = oldBackoff }()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := ReportImage(context.Background(), server.Client(), server.URL, "tok", ImageReport{
		Image: "web", Digest: "sha256:def",
	})
	if err != nil {
		t.Fatalf("expected retries to succeed after transient 503s, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected exactly 3 attempts (503, 503, 200), got %d", got)
	}
}
