package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
)

func TestConnectGithubDecodesResult(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/github/connect" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		gotBody, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(GithubConnectResult{
			Org: "acme", Repo: "acme/monorepo",
			RemoteURL: "https://github.com/acme/monorepo.git",
			Mirror:    "armed; first sync triggered (watch GET /api/mirror/status)",
		})
	}))
	defer server.Close()

	res, err := ConnectGithub(context.Background(), http.DefaultClient,
		Credential{URL: server.URL, Secret: "sekret"}, "acme/monorepo")
	if err != nil {
		t.Fatalf("ConnectGithub: %v", err)
	}
	if res.Org != "acme" || res.RemoteURL != "https://github.com/acme/monorepo.git" {
		t.Fatalf("decoded result: %+v", res)
	}
	var sent map[string]string
	if err := json.Unmarshal(gotBody, &sent); err != nil || sent["repo"] != "acme/monorepo" {
		t.Fatalf("request body: %s (%v)", gotBody, err)
	}
}

func TestConnectGithubSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		json.NewEncoder(w).Encode(clierr.Error{
			Code:       "github_app_not_configured",
			Message:    "this deployment holds no GitHub App credentials, so it cannot mint push tokens",
			Suggestion: "start runkod with --github-app-id and --github-app-key-file (one App credential serves every org), then retry",
		})
	}))
	defer server.Close()

	_, err := ConnectGithub(context.Background(), http.DefaultClient,
		Credential{URL: server.URL, Secret: "sekret"}, "acme/monorepo")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "github_app_not_configured" {
		t.Fatalf("expected structured github_app_not_configured, got %T: %v", err, err)
	}
}

func TestGithubMirrorStatusDecodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/mirror/status" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		// runkod's mirrorStatus is an untagged struct: Go field names on
		// the wire - this test pins that the CLI decodes that shape.
		json.NewEncoder(w).Encode(map[string]any{
			"Configured": true,
			"RemoteURL":  "https://github.com/acme/monorepo.git",
			"Cursors": []map[string]any{
				{"Ref": "refs/heads/main", "LastSyncedSHA": "abc123", "Frozen": true, "UpdatedAt": time.Now()},
			},
			"LastError":  "mirror: refs/heads/main is frozen",
			"LastSyncAt": time.Now(),
		})
	}))
	defer server.Close()

	status, err := GithubMirrorStatus(context.Background(), http.DefaultClient,
		Credential{URL: server.URL, Secret: "sekret"})
	if err != nil {
		t.Fatalf("GithubMirrorStatus: %v", err)
	}
	if !status.Configured || len(status.Cursors) != 1 || !status.Cursors[0].Frozen || status.Cursors[0].Ref != "refs/heads/main" {
		t.Fatalf("decoded status: %+v", status)
	}
}
