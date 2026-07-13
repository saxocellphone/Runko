package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

// TestDescribeChangeSendsOnlySetFields pins the omitted-field-preserves
// wire contract: a nil field never reaches the daemon, so it cannot
// clobber what is stored; an explicit empty string does.
func TestDescribeChangeSendsOnlySetFields(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/changes/Ichg1/describe" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		json.NewEncoder(w).Encode(ChangeInfo{
			ChangeKey: "Ichg1", State: "open", Title: "t",
			Description: "the blurb", TestPlan: "drove it",
		})
	}))
	defer server.Close()

	desc := "the blurb"
	change, err := DescribeChange(context.Background(), http.DefaultClient, server.URL, "Bearer sekret", "Ichg1", &desc, nil)
	if err != nil {
		t.Fatalf("DescribeChange: %v", err)
	}
	if _, present := gotBody["test_plan"]; present {
		t.Fatalf("an unset test plan must not be sent at all, got %v", gotBody)
	}
	if gotBody["description"] != "the blurb" {
		t.Fatalf("body: %v", gotBody)
	}
	if change.Description != "the blurb" || change.TestPlan != "drove it" {
		t.Fatalf("decoded change: %+v", change)
	}
}

func TestDescribeChangeDecodesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(clierr.Error{
			Code: "invalid_state", Field: "change",
			Message: "change Ichg1 is landed - only open changes take a description",
		})
	}))
	defer server.Close()

	x := "too late"
	_, err := DescribeChange(context.Background(), http.DefaultClient, server.URL, "Bearer sekret", "Ichg1", &x, nil)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "invalid_state" {
		t.Fatalf("want structured invalid_state, got %v", err)
	}
}
