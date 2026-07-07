package runkod

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
	"github.com/saxocellphone/runko/search"
)

// newTestServer creates a real bare repo with one seeded Change (via a real
// Processor.Process call against real git objects), wraps it in an
// httptest.Server, and returns the server plus the ChangeID to query.
func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	// ci.checks declares "unit" as required (§14.9) - needed so
	// TestAPIPostCheckAndMergeRequirementsRoundTrip's posted "unit" check
	// actually gates anything; required check names come from what's
	// DECLARED, not from whatever happens to get posted (api.go's
	// requiredCheckNames).
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), result.ChangeID
}

func authedGet(t *testing.T, srv *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// TestGitSmartHTTPRequiresAuth exercises a real `git ls-remote` (the
// lightest real git-over-HTTP operation) against the served repo, proving
// requireGitAuth actually gates the transport itself, not just the REST
// API - the gap this stage's own testing found (see api.go's comment).
func TestGitSmartHTTPRequiresAuth(t *testing.T) {
	bare := newBareRepo(t)
	if err := EnableHTTPReceivePack(bare); err != nil {
		t.Fatalf("EnableHTTPReceivePack: %v", err)
	}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: NewMemStore(), Processor: newTestProcessor(bare, NewMemStore()), Token: "sekret"}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	repoURL := httpSrv.URL + "/" + RepoMountName(bare) + "/"
	if _, err := gitfixtureRunGit(t.TempDir(), "ls-remote", repoURL); err == nil {
		t.Fatalf("expected ls-remote without credentials to fail (401)")
	}

	authedURL := strings.Replace(repoURL, "http://", "http://runko:sekret@", 1)
	if _, err := gitfixtureRunGit(t.TempDir(), "ls-remote", authedURL); err != nil {
		t.Fatalf("expected ls-remote WITH the deploy token to succeed, got: %v", err)
	}
}

func TestAPIGetChangeRequiresAuth(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/changes/"+changeID, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", resp.StatusCode)
	}
	resp = authedGet(t, srv, "/api/changes/"+changeID, "wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong token, got %d", resp.StatusCode)
	}
}

func TestAPIGetChangeReturnsPersistedChange(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/changes/"+changeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got Change
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ChangeKey != changeID {
		t.Fatalf("expected change_key %s, got %+v", changeID, got)
	}
}

func TestAPIGetChangeNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/changes/no-such-change", "sekret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPIGetAffectedComputesLive(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/changes/"+changeID+"/affected", "sekret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Projects []struct {
			Name string
			Path string
		}
		RunEverything bool
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "checkout-api" {
		t.Fatalf("expected checkout-api affected, got %+v", result.Projects)
	}
	if result.RunEverything {
		t.Fatalf("did not expect RunEverything for a project-scoped change")
	}
}

func TestAPIPostCheckAndMergeRequirementsRoundTrip(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{
		"name": "unit", "external_id": "job-1", "status": "queued", "reporter": "github-actions",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/checks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sekret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST checks: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	mrResp := authedGet(t, srv, "/api/changes/"+changeID+"/merge-requirements", "sekret")
	var mr struct {
		Checks struct {
			Required []string
			Pending  []string
		}
		Mergeable bool
	}
	if err := json.NewDecoder(mrResp.Body).Decode(&mr); err != nil {
		t.Fatalf("decode merge-requirements: %v", err)
	}
	if mr.Mergeable {
		t.Fatalf("expected not mergeable while a check is still queued, got %+v", mr)
	}
	if len(mr.Checks.Pending) != 1 || mr.Checks.Pending[0] != "unit" {
		t.Fatalf("expected 'unit' pending, got %+v", mr.Checks)
	}

	// Now report it completed/successful and confirm the Change becomes mergeable.
	body2, _ := json.Marshal(map[string]string{
		"name": "unit", "external_id": "job-1", "status": "completed", "conclusion": "success", "reporter": "github-actions",
	})
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/checks", bytes.NewReader(body2))
	req2.Header.Set("Authorization", "Bearer sekret")
	if _, err := srv.Client().Do(req2); err != nil {
		t.Fatalf("POST checks (completed): %v", err)
	}

	mrResp2 := authedGet(t, srv, "/api/changes/"+changeID+"/merge-requirements", "sekret")
	var mr2 struct {
		Mergeable bool
	}
	if err := json.NewDecoder(mrResp2.Body).Decode(&mr2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !mr2.Mergeable {
		t.Fatalf("expected mergeable after the check completed successfully")
	}
}

func TestAPISearchRequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/search?q=checkout", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", resp.StatusCode)
	}
}

// TestAPISearchNotConfiguredReturnsStructuredError guards the "NO silent
// git-grep fallback" rule (§8.2): a server with no Searcher configured
// (the newTestServer default) must surface the §6.5 structured error, not a
// generic 500 or an empty-but-200 result a caller could mistake for "no
// matches".
func TestAPISearchNotConfiguredReturnsStructuredError(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/search?q=checkout", "sekret")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no searcher is configured, got %d", resp.StatusCode)
	}
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ce.Code != "search_not_configured" {
		t.Fatalf("expected code search_not_configured, got %+v", ce)
	}
}

func TestAPISearchMissingQueryIsBadRequest(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/search", "sekret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without ?q=, got %d", resp.StatusCode)
	}
}

// stubSearcher is a fake search.CodeSearcher returning canned hits, so this
// test exercises runkod's own project-tagging layer (tagProjects) without
// depending on a real zoekt-webserver.
type stubSearcher struct{ result search.Result }

func (s stubSearcher) Search(_ context.Context, _ string, _ search.SearchOptions) (*search.Result, error) {
	r := s.result
	return &r, nil
}

// TestAPISearchProjectTagsHits proves handleSearch fills in Hit.Project by
// scanning the repo's current trunk state (§13.3's longest-prefix rule) -
// the "project-tagged hits through the daemon" stage 11 DAG entry names
// explicitly, not something a bare CodeSearcher client can do on its own
// (it has no notion of PROJECT.yaml).
func TestAPISearchProjectTagsHits(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	searcher := stubSearcher{result: search.Result{
		Query: "main",
		Hits:  []search.Hit{{Path: "commerce/checkout/main.go", LineNumber: 1, Line: "package main"}},
	}}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: newTestProcessor(bare, store), Token: "sekret", Searcher: searcher}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	resp := authedGet(t, httpSrv, "/api/search?q="+url.QueryEscape("main"), "sekret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result search.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Project != "checkout-api" {
		t.Fatalf("expected the hit tagged with project checkout-api, got %+v", result.Hits)
	}
}
