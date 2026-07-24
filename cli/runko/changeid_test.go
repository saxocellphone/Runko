package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

const (
	testFullID  = "Ic6794d8800000000000000000000000000000001"
	testFullID2 = "Ic6794d9900000000000000000000000000000002"
	testPrefix  = "Ic6794d"
)

// TestIsFullChangeID pins the I+40 lowercase ASCII hex shape
// resolveChangeIDArg short-circuits on.
func TestIsFullChangeID(t *testing.T) {
	if !isFullChangeID(testFullID) {
		t.Fatalf("expected full id %q to pass", testFullID)
	}
	// non-ASCII 41-byte string must not be treated as a full id
	// ('I' + 38 ASCII zeros + 2-byte UTF-8 rune = 41 bytes).
	nonASCII := "I" + strings.Repeat("0", 38) + "α"
	if len(nonASCII) != 41 {
		t.Fatalf("test setup: non-ASCII fixture want 41 bytes, got %d", len(nonASCII))
	}
	for _, bad := range []string{
		"", "Ic6794d", "I" + strings.Repeat("0", 39), "I" + strings.Repeat("0", 41),
		"X" + strings.Repeat("0", 40), "I" + strings.Repeat("g", 40),
		// Uppercase hex is not accepted without normalization.
		"I" + strings.Repeat("A", 40),
		nonASCII,
	} {
		if isFullChangeID(bad) {
			t.Errorf("expected %q to fail isFullChangeID", bad)
		}
	}
}

// TestResolveChangeIDArgFullIDNoListCall: a complete Change-Id never hits the
// control plane list endpoint (zero extra API calls).
func TestResolveChangeIDArgFullIDNoListCall(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Fatalf("unexpected request for a full Change-Id: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	got, err := resolveChangeIDArg(context.Background(), server.Client(),
		Credential{URL: server.URL, Secret: "sekret"}, testFullID)
	if err != nil {
		t.Fatalf("resolveChangeIDArg: %v", err)
	}
	if got != testFullID {
		t.Fatalf("got %q want %q", got, testFullID)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected zero API calls for a full id, got %d", hits.Load())
	}
}

// TestResolveChangeIDArgUniquePrefix: a unique prefix resolves, prints the
// stderr note with the matched title, and is what subsequent verbs would
// send to the API.
func TestResolveChangeIDArgUniquePrefix(t *testing.T) {
	var listHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/changes" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		listHits.Add(1)
		json.NewEncoder(w).Encode([]ChangeInfo{
			{ChangeKey: testFullID, Title: "landed fix", State: "landed"},
			{ChangeKey: "Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "other"},
		})
	}))
	defer server.Close()

	var warnings bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	got, err := resolveChangeIDArg(context.Background(), server.Client(),
		Credential{URL: server.URL, Secret: "sekret"}, testPrefix)
	if err != nil {
		t.Fatalf("resolveChangeIDArg: %v", err)
	}
	if got != testFullID {
		t.Fatalf("got %q want %q", got, testFullID)
	}
	if listHits.Load() != 1 {
		t.Fatalf("expected one list call, got %d", listHits.Load())
	}
	note := warnings.String()
	wantNote := `resolved ` + testPrefix + ` -> ` + testFullID + ` ("landed fix")`
	if !strings.Contains(note, wantNote) {
		t.Fatalf("expected resolution note with title %q, got %q", wantNote, note)
	}
}

// TestResolveChangeIDArgUppercasePrefix: IC6794D (uppercase hex) lowercases
// and resolves to the canonical lowercase full id.
func TestResolveChangeIDArgUppercasePrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]ChangeInfo{
			{ChangeKey: testFullID, Title: "landed fix", State: "landed"},
		})
	}))
	defer server.Close()

	var warnings bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	got, err := resolveChangeIDArg(context.Background(), server.Client(),
		Credential{URL: server.URL, Secret: "sekret"}, "IC6794D")
	if err != nil {
		t.Fatalf("resolveChangeIDArg: %v", err)
	}
	if got != testFullID {
		t.Fatalf("got %q want lowercase-canonical %q", got, testFullID)
	}
	note := warnings.String()
	// Note uses the normalized (lowercase-tail) prefix.
	if !strings.Contains(note, `resolved `+testPrefix+` -> `+testFullID+` ("landed fix")`) {
		t.Fatalf("expected resolution note for uppercase input, got %q", note)
	}
}

// TestResolveChangeIDArgAmbiguous lists candidate ids+titles and suggests
// passing more characters.
func TestResolveChangeIDArgAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]ChangeInfo{
			{ChangeKey: testFullID, Title: "first"},
			{ChangeKey: testFullID2, Title: "second"},
		})
	}))
	defer server.Close()

	_, err := resolveChangeIDArg(context.Background(), server.Client(),
		Credential{URL: server.URL, Secret: "sekret"}, testPrefix)
	if err == nil {
		t.Fatal("expected ambiguous_change error")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "ambiguous_change" {
		t.Fatalf("want ambiguous_change, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, testFullID) || !strings.Contains(msg, "first") {
		t.Fatalf("expected candidate listing, got %q", msg)
	}
	if !strings.Contains(msg, testFullID2) || !strings.Contains(msg, "second") {
		t.Fatalf("expected both candidates, got %q", msg)
	}
	if !strings.Contains(msg, "  -> ") || !strings.Contains(msg, "more characters") {
		t.Fatalf("expected a suggestion to pass more characters, got %q", msg)
	}
}

// TestResolveChangeIDArgZeroMatch keeps the requirements-style unknown_change
// message (not a raw URL).
func TestResolveChangeIDArgZeroMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]ChangeInfo{
			{ChangeKey: "Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "unrelated"},
		})
	}))
	defer server.Close()

	_, err := resolveChangeIDArg(context.Background(), server.Client(),
		Credential{URL: server.URL, Secret: "sekret"}, testPrefix)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unknown_change" {
		t.Fatalf("want unknown_change, got %v", err)
	}
	if !strings.Contains(ce.Message, "no change "+testPrefix) {
		t.Fatalf("expected not-found naming the prefix, got %q", ce.Message)
	}
	if strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "/api/") {
		t.Fatalf("not-found must not leak a URL, got %v", err)
	}
	if !strings.Contains(err.Error(), "  -> ") {
		t.Fatalf("expected a suggestion, got %v", err)
	}
}

// TestListCommentsNotFoundIsHumanized: change comments on a missing id no
// longer prints the bare transport URL.
func TestListCommentsNotFoundIsHumanized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := ListComments(context.Background(), server.Client(), server.URL, "Bearer sekret", testFullID)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unknown_change" {
		t.Fatalf("want unknown_change, got %v", err)
	}
	if !strings.Contains(ce.Message, "no change "+testFullID) {
		t.Fatalf("expected humanized message, got %q", ce.Message)
	}
	if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "/api/changes/") {
		t.Fatalf("must not print the raw URL, got %v", err)
	}
	if !strings.Contains(err.Error(), "runko change push") {
		t.Fatalf("expected the requirements-style suggestion, got %v", err)
	}
}

// TestResolveCommentNotFoundIsHonest: resolve 404s for both a missing change
// and a missing comment id, so the message names both possibilities.
func TestResolveCommentNotFoundIsHonest(t *testing.T) {
	const commentID = "cmt-typo-999"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := ResolveComment(context.Background(), server.Client(), server.URL, "Bearer sekret",
		testFullID, commentID, true)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("want not_found, got %v", err)
	}
	if !strings.Contains(ce.Message, "no change "+testFullID) {
		t.Fatalf("expected message to name the change, got %q", ce.Message)
	}
	if !strings.Contains(ce.Message, "no comment "+commentID) {
		t.Fatalf("expected message to name the comment, got %q", ce.Message)
	}
	// Must not claim the change is missing alone (the old false rewrite).
	if strings.Contains(ce.Message, "the control plane has no change") {
		t.Fatalf("must not use the change-only rewrite for resolve, got %q", ce.Message)
	}
	if !strings.Contains(err.Error(), "runko change comments --change "+testFullID) {
		t.Fatalf("expected suggestion to list comments, got %v", err)
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("must not leak raw URL, got %v", err)
	}
}

// TestCmdChangeCommentsNotFoundIsHumanized exercises the full CLI path for
// the dogfood papercut: `change comments --change <full-id>` on a 404.
func TestCmdChangeCommentsNotFoundIsHumanized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Full id: no list call. Only the comments GET.
		if r.URL.Path != "/api/changes/"+testFullID+"/comments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	err := execCLI("change", "comments", "--runkod-url", server.URL, "--token", "sekret",
		"--change", testFullID)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "the control plane has no change "+testFullID) {
		t.Fatalf("expected humanized not-found, got %q", msg)
	}
	if strings.Contains(msg, server.URL) || strings.Contains(msg, ": not found") {
		t.Fatalf("must not leak raw URL error shape, got %q", msg)
	}
}

// TestCmdChangeRequirementsUniquePrefix resolves then hits the gates endpoint
// with the full id (end-to-end through execCLI).
func TestCmdChangeRequirementsUniquePrefix(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/changes":
			json.NewEncoder(w).Encode([]ChangeInfo{
				{ChangeKey: testFullID, Title: "landed fix", State: "landed"},
			})
		case "/api/changes/" + testFullID + "/merge-requirements":
			json.NewEncoder(w).Encode(map[string]any{"Mergeable": true, "ChangeID": testFullID})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var warnings bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("change", "requirements", "--runkod-url", server.URL, "--token", "sekret",
			"--change", testPrefix)
	})
	if cmdErr != nil {
		t.Fatalf("cmdChangeRequirements: %v", cmdErr)
	}
	wantNote := `resolved ` + testPrefix + ` -> ` + testFullID + ` ("landed fix")`
	if !strings.Contains(warnings.String(), wantNote) {
		t.Fatalf("expected resolution note with title, got %q", warnings.String())
	}
	if !strings.Contains(out, testFullID) {
		t.Fatalf("expected output naming the full id, got %q", out)
	}
	if len(paths) < 2 || paths[0] != "/api/changes" || paths[1] != "/api/changes/"+testFullID+"/merge-requirements" {
		t.Fatalf("expected list then merge-requirements, got %v", paths)
	}
}
