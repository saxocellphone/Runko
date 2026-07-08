package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
)

func TestApproveChangeSuccessDecodesMergeRequirements(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/changes/Ichg1/approve" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if req["owner_ref"] != "group:commerce-eng" || req["approved_by"] != "alice" {
			t.Fatalf("unexpected body: %v", req)
		}
		// Respond with the daemon's real wire shape (checks.MergeRequirements'
		// own MarshalJSON), so this test also pins that the CLI can decode it.
		json.NewEncoder(w).Encode(checks.MergeRequirements{
			ChangeID:        "Ichg1",
			RequiredOwners:  []string{"group:commerce-eng"},
			SatisfiedOwners: []string{"group:commerce-eng"},
			Mergeable:       true,
		})
	}))
	defer server.Close()

	reqs, err := ApproveChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", "group:commerce-eng", "alice")
	if err != nil {
		t.Fatalf("ApproveChange: %v", err)
	}
	if !reqs.Mergeable || len(reqs.SatisfiedOwners) != 1 || reqs.SatisfiedOwners[0] != "group:commerce-eng" {
		t.Fatalf("expected decoded merge requirements, got %+v", reqs)
	}
}

func TestApproveChangeBadRequestDecodesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(clierr.Error{
			Code: "not_a_required_owner", Field: "owner_ref",
			Message: `"group:other" is not a required owner for change Ichg1`,
		})
	}))
	defer server.Close()

	_, err := ApproveChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", "group:other", "alice")
	if err == nil {
		t.Fatalf("expected an error on 400")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "not_a_required_owner" {
		t.Fatalf("expected code not_a_required_owner, got %+v", ce)
	}
}

func TestApproveChangeNotFoundIsStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := ApproveChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", "group:commerce-eng", "alice")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("expected a not_found clierr.Error, got %T: %v", err, err)
	}
}

func TestCmdChangeApproveRequiresFlags(t *testing.T) {
	err := cmdChangeApprove([]string{"--change", "Ichg1"})
	if err == nil {
		t.Fatalf("expected an error when required flags are missing")
	}
}

// TestApproveChangeForbiddenDecodesStructuredError pins the 2026-07-08
// dogfood finding: the daemon's 403 self_approval_denied carried a clear
// explanation, and the CLI printed bare "returned 403". Every non-2xx
// status with a structured body must surface it (decodeAPIError), not just
// 400.
func TestApproveChangeForbiddenDecodesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(clierr.Error{
			Code: "self_approval_denied", Field: "approved_by",
			Message: `"saxo" pushed this change and may not approve it`,
		})
	}))
	defer server.Close()

	_, err := ApproveChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", "group:commerce-eng", "saxo")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "self_approval_denied" {
		t.Fatalf("expected the structured 403 to surface, got %v", err)
	}
	if !strings.Contains(ce.Message, "may not approve") {
		t.Fatalf("expected the daemon's explanation, got %q", ce.Message)
	}
}
