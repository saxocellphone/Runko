package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/land"
)

func TestLandChangeSuccessDecodesOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/changes/Ichg1/land" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"Landed": true, "LandedSHA": "abc123"})
	}))
	defer server.Close()

	outcome, err := LandChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", false)
	if err != nil {
		t.Fatalf("LandChange: %v", err)
	}
	if !outcome.Landed || outcome.LandedSHA != "abc123" {
		t.Fatalf("expected a decoded land.Outcome, got %+v", outcome)
	}
}

func TestLandChangeConflictDecodesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(clierr.Error{
			Code: "not_mergeable", Message: "change Ichg1 is not mergeable yet",
		})
	}))
	defer server.Close()

	_, err := LandChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", false)
	if err == nil {
		t.Fatalf("expected an error on 409")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "not_mergeable" {
		t.Fatalf("expected code not_mergeable, got %+v", ce)
	}
}

func TestLandChangeNotFoundIsStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := LandChange(context.Background(), http.DefaultClient, server.URL, "sekret", "no-such-change", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "not_found" {
		t.Fatalf("expected code not_found, got %+v", ce)
	}
}

func TestCmdChangeLandRequiresFlags(t *testing.T) {
	err := cmdChangeLand([]string{"--change", "Ichg1"})
	if err == nil {
		t.Fatalf("expected an error when --runkod-url/--token are missing")
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatalf("expected a validation error, not a usageError, got %v", err)
	}
}

func TestCmdChangeLandJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"Landed": true, "LandedSHA": "def456"})
	}))
	defer server.Close()

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = cmdChangeLand([]string{"--runkod-url", server.URL, "--token", "sekret", "--change", "Ichg1", "--json"})
	})
	if cmdErr != nil {
		t.Fatalf("cmdChangeLand: %v", cmdErr)
	}
	var outcome land.Outcome
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if !outcome.Landed || outcome.LandedSHA != "def456" {
		t.Fatalf("expected the decoded outcome in JSON output, got %+v", outcome)
	}
}
