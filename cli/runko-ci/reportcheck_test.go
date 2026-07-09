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

func TestReportCheckPostsSignedBearerRequest(t *testing.T) {
	var gotAuth string
	var gotReport CheckRunReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	report := CheckRunReport{
		Name: "unit", ExternalID: "job-42", Status: "completed",
		Conclusion: "success", Reporter: "github-actions",
	}
	err := ReportCheck(context.Background(), server.Client(), server.URL, "tok_abc", report)
	if err != nil {
		t.Fatalf("ReportCheck: %v", err)
	}
	if gotAuth != "Bearer tok_abc" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer tok_abc")
	}
	if gotReport != report {
		t.Fatalf("server received %+v, want %+v", gotReport, report)
	}
}

func TestReportCheckServerErrorSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := ReportCheck(context.Background(), server.Client(), server.URL, "bad-token", CheckRunReport{
		Name: "unit", ExternalID: "job-1", Status: "queued", Reporter: "gha",
	})
	if err == nil {
		t.Fatalf("expected an error for a 401 response")
	}
}

// TestReportCheckRetriesTransient5xx pins migration-findings #33: the
// daemon deploys as a single-replica Recreate pod, so every deploy is a
// brief 503 window - a report that treats one 503 as final leaves the
// check unreported forever. The POST is an upsert, so retrying is safe;
// 4xx (the test above) stays fatal.
func TestReportCheckRetriesTransient5xx(t *testing.T) {
	oldBackoff := reportCheckBackoff
	reportCheckBackoff = time.Millisecond
	defer func() { reportCheckBackoff = oldBackoff }()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := ReportCheck(context.Background(), server.Client(), server.URL, "tok", CheckRunReport{
		Name: "unit", ExternalID: "job-1", Status: "completed", Conclusion: "success", Reporter: "gha",
	})
	if err != nil {
		t.Fatalf("expected retries to succeed after transient 503s, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected exactly 3 attempts (503, 503, 200), got %d", got)
	}
}

// A persistent 5xx still fails after the attempt budget.
func TestReportCheckExhaustsRetryBudget(t *testing.T) {
	oldBackoff := reportCheckBackoff
	reportCheckBackoff = time.Millisecond
	defer func() { reportCheckBackoff = oldBackoff }()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := ReportCheck(context.Background(), server.Client(), server.URL, "tok", CheckRunReport{
		Name: "unit", ExternalID: "job-1", Status: "queued", Reporter: "gha",
	})
	if err == nil {
		t.Fatalf("expected an error after exhausting retries")
	}
	if got := atomic.LoadInt32(&calls); got != int32(reportCheckAttempts) {
		t.Fatalf("expected %d attempts, got %d", reportCheckAttempts, got)
	}
}
