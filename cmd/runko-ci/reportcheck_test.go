package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
