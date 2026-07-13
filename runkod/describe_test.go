package runkod

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func strPtr(s string) *string { return &s }

// TestDescribeChangeSetAndPreserve is the feature (§8.6 Summaries): the
// blurb sets, an omitted field is left alone, an explicit "" clears, and -
// unlike Title - an amend does NOT move it: re-gating is the head's
// business, the description survives.
func TestDescribeChangeSetAndPreserve(t *testing.T) {
	srv, _, store, changeID := automergeFixture(t)
	ctx := context.Background()

	set, apiErr := srv.describeChangeCore(ctx, changeID,
		strPtr("Teach the daemon to serve change descriptions."), strPtr("make check; drove the endpoint"))
	if apiErr != nil {
		t.Fatalf("describe: %+v", apiErr)
	}
	if set.Description != "Teach the daemon to serve change descriptions." || set.TestPlan != "make check; drove the endpoint" {
		t.Fatalf("set: %+v", set)
	}

	// Omitted description preserves it; only the test plan moves.
	updated, apiErr := srv.describeChangeCore(ctx, changeID, nil, strPtr("also drove the web page"))
	if apiErr != nil {
		t.Fatalf("update test plan: %+v", apiErr)
	}
	if updated.Description != set.Description || updated.TestPlan != "also drove the web page" {
		t.Fatalf("omitted field must preserve: %+v", updated)
	}

	// An amend (same Change-Id, new head) keeps the blurb - description is
	// change-scoped state like automerge's arming, not head-scoped like
	// Title.
	amended, err := store.CreateOrUpdateChange(ctx, changeID, updated.BaseSHA,
		"1111111111111111111111111111111111111111", updated.GitRef, "reworded title", "val", "", "")
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if amended.Description != updated.Description || amended.TestPlan != updated.TestPlan {
		t.Fatalf("amend must preserve description/test plan: %+v", amended)
	}

	// Explicit "" clears.
	cleared, apiErr := srv.describeChangeCore(ctx, changeID, strPtr(""), nil)
	if apiErr != nil {
		t.Fatalf("clear: %+v", apiErr)
	}
	if cleared.Description != "" || cleared.TestPlan != amended.TestPlan {
		t.Fatalf("explicit empty must clear only its own field: %+v", cleared)
	}

	// The Connect surface serves the same field (proto field 8, previously
	// marked "not yet served").
	if got := srv.protoChange(amended).Description; got != amended.Description {
		t.Fatalf("protoChange description: %q", got)
	}
}

// TestDescribeChangeGuardsAndHTTP: unknown change 404s, a non-open change
// refuses with the structured invalid_state, an empty body 400s, and the
// real HTTP handler round-trips - including the REST GET carrying the new
// fields for the CLI/MCP view.
func TestDescribeChangeGuardsAndHTTP(t *testing.T) {
	srv, _, store, changeID := automergeFixture(t)
	ctx := context.Background()

	if _, apiErr := srv.describeChangeCore(ctx, "Iffffffffffffffffffffffffffffffffffffffff", strPtr("x"), nil); apiErr == nil || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown change: want 404, got %+v", apiErr)
	}

	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	hs := httptest.NewServer(handler)
	defer hs.Close()

	post := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/changes/"+changeID+"/describe", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer sekret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatalf("POST describe: %v", err)
		}
		return resp
	}

	// Nothing to set: structured 400.
	resp := post(`{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body: want 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(`{"description":"A small blurb about the change.","test_plan":"drove it end-to-end"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("describe: want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The plain REST read (the CLI's and MCP's view) carries both fields.
	greq, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/changes/"+changeID, nil)
	greq.Header.Set("Authorization", "Bearer sekret")
	gresp, err := hs.Client().Do(greq)
	if err != nil {
		t.Fatalf("GET change: %v", err)
	}
	defer gresp.Body.Close()
	var got struct{ Description, TestPlan string }
	if err := json.NewDecoder(gresp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Description != "A small blurb about the change." || got.TestPlan != "drove it end-to-end" {
		t.Fatalf("GET view: %+v", got)
	}

	// Abandoned: describing is refused with the structured 409.
	if _, err := store.MarkChangeAbandoned(ctx, changeID); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, apiErr := srv.describeChangeCore(ctx, changeID, strPtr("too late"), nil); apiErr == nil || apiErr.Err.Code != "invalid_state" {
		t.Fatalf("describe on abandoned: want invalid_state, got %+v", apiErr)
	}
}
