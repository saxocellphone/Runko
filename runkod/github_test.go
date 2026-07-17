package runkod

// `github connect` endpoint tests (2026-07-16). The
// "GitHub" side is a local bare repo behind a stub GithubRemote
// constructor: the endpoint's contract - verify over the wire, persist in
// org settings, arm the worker, trigger the first sync - is provider-
// independent, and the App-credentialed https auth it rides in production
// is unit-tested in runkogithubapp and platform/mirror.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/mirror"
)

// githubConnectFixture is an smFixture whose server carries the full
// connect wiring: an UNARMED mirror worker, org settings, principals
// (one admin, one agent), and a GithubRemote stub answering with a path
// remote at target.
func githubConnectFixture(t *testing.T) (*smFixture, string) {
	t.Helper()
	f := newSMFixture(t)
	target := newBareRepo(t)
	f.srv.Mirror = &MirrorWorker{Store: f.store, TrunkRef: "main", Debounce: time.Hour}
	f.srv.Directory = f.store
	f.srv.SettingsOrg = "acme"
	f.srv.Principals = []Principal{
		{Name: "root", Token: "root-token", Admin: true},
		{Name: "agent-x", Token: "agent-token", IsAgent: true},
	}
	f.srv.GithubRemote = func(repoPath string) *mirror.Remote {
		return &mirror.Remote{RepoDir: f.bare, URL: target}
	}
	return f, target
}

func postGithubConnect(t *testing.T, srv *Server, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/github/connect", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.handleGithubConnect(rec, req)
	return rec
}

func TestGithubConnectVerifiesPersistsAndArms(t *testing.T) {
	f, target := githubConnectFixture(t)
	ctx := context.Background()

	rec := postGithubConnect(t, f.srv, "root-token", `{"repo":"acme/monorepo"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}

	// Persisted: the wiring survives a restart through org settings.
	settings, err := f.store.GetOrgSettings(ctx, "acme")
	if err != nil || settings.GithubMirrorRepo != "acme/monorepo" {
		t.Fatalf("settings after connect: %+v (%v)", settings, err)
	}

	// Armed: the worker syncs to the connected repo without any restart.
	if f.srv.Mirror.remote() == nil {
		t.Fatal("worker not armed")
	}
	if err := f.srv.Mirror.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after connect: %v", err)
	}
	localTrunk, _ := gitRevParse(f.bare, "refs/heads/main")
	if got, err := gitfixtureRunGit(target, "rev-parse", "refs/heads/main"); err != nil || got != localTrunk {
		t.Fatalf("connected mirror trunk: want %s, got %s (%v)", localTrunk, got, err)
	}

	// Reconnect to a DIFFERENT repo repoints the same worker live.
	rec = postGithubConnect(t, f.srv, "root-token", `{"repo":"acme/other"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reconnect: %d %s", rec.Code, rec.Body)
	}
	settings, _ = f.store.GetOrgSettings(ctx, "acme")
	if settings.GithubMirrorRepo != "acme/other" {
		t.Fatalf("reconnect settings: %+v", settings)
	}
}

func TestGithubConnectRefusesAgents(t *testing.T) {
	f, _ := githubConnectFixture(t)
	rec := postGithubConnect(t, f.srv, "agent-token", `{"repo":"acme/monorepo"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent connect: want 403, got %d %s", rec.Code, rec.Body)
	}
	if settings, _ := f.store.GetOrgSettings(context.Background(), "acme"); settings.GithubMirrorRepo != "" {
		t.Fatalf("agent refusal must not persist wiring: %+v", settings)
	}
}

func TestGithubConnectWithoutAppCredentials(t *testing.T) {
	f, _ := githubConnectFixture(t)
	f.srv.GithubRemote = nil
	rec := postGithubConnect(t, f.srv, "root-token", `{"repo":"acme/monorepo"}`)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "github_app_not_configured") {
		t.Fatalf("no-app connect: want 412 github_app_not_configured, got %d %s", rec.Code, rec.Body)
	}
}

func TestGithubConnectValidatesRepoShape(t *testing.T) {
	f, _ := githubConnectFixture(t)
	for body, wantCode := range map[string]string{
		`{}`:                              "missing_field",
		`{"repo":"not-a-repo"}`:           "invalid_repo",
		`{"repo":"a/b/c"}`:                "invalid_repo",
		`{"repo":"https://github.com/a"}`: "invalid_repo",
	} {
		rec := postGithubConnect(t, f.srv, "root-token", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), wantCode) {
			t.Fatalf("connect %s: want 400 %s, got %d %s", body, wantCode, rec.Code, rec.Body)
		}
	}
}

func TestGithubConnectUnreachableRepoStaysUnwired(t *testing.T) {
	f, _ := githubConnectFixture(t)
	f.srv.GithubRemote = func(repoPath string) *mirror.Remote {
		return &mirror.Remote{RepoDir: f.bare, URL: t.TempDir() + "/does-not-exist.git"}
	}
	rec := postGithubConnect(t, f.srv, "root-token", `{"repo":"acme/monorepo"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable connect: want 502, got %d %s", rec.Code, rec.Body)
	}
	if settings, _ := f.store.GetOrgSettings(context.Background(), "acme"); settings.GithubMirrorRepo != "" {
		t.Fatalf("failed verify must not persist wiring: %+v", settings)
	}
	if f.srv.Mirror.remote() != nil {
		t.Fatal("failed verify must not arm the worker")
	}
}

func TestValidGithubRepoPath(t *testing.T) {
	for path, want := range map[string]bool{
		"acme/monorepo":    true,
		"a-b_c.d/repo.git": true, // charset is permissive; GitHub rejects its own invalids
		"acme":             false,
		"acme/":            false,
		"/repo":            false,
		"a/b/c":            false,
		"acme/repo name":   false,
		"https://x.y/a/b":  false,
		"owner/repo\ttab":  false,
	} {
		if got := validGithubRepoPath(path); got != want {
			t.Errorf("validGithubRepoPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestPutOrgSettingsCarriesGithubWiringForward pins the settings-PUT
// contract: github_mirror_repo is connect-owned, so a settings-page save
// (which PUTs the whole shape) must never silently disconnect the mirror.
func TestPutOrgSettingsCarriesGithubWiringForward(t *testing.T) {
	srv, hub := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme failed")
	}
	ctx := context.Background()
	settings, err := hub.Directory.GetOrgSettings(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrgSettings: %v", err)
	}
	settings.GithubMirrorRepo = "acme/monorepo"
	if err := hub.Directory.UpdateOrgSettings(ctx, "acme", settings); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}

	status, body := hubDo(t, srv, "PUT", "/api/orgs/acme/settings", "alice", "alicepw123", "",
		map[string]any{"description": "a settings-page save"})
	if status != http.StatusOK {
		t.Fatalf("PUT settings: %d %v", status, body)
	}
	got, _ := hub.Directory.GetOrgSettings(ctx, "acme")
	if got.GithubMirrorRepo != "acme/monorepo" || got.Description != "a settings-page save" {
		t.Fatalf("wiring not carried forward: %+v", got)
	}
}
