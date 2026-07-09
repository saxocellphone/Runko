package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// newWorkspaceTestServer seeds a bare repo with two projects on trunk and
// returns a running API server plus the bare repo path.
func newWorkspaceTestServer(t *testing.T) (srv *httptest.Server, bare string, store Store) {
	t.Helper()
	bare = newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.WriteFile("libs/money/PROJECT.yaml", "schema: project/v1\nname: money-lib\ntype: library\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), bare, store
}

func apiDo(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sekret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestWorkspaceCreateGetListRoundTrip(t *testing.T) {
	srv, bare, _ := newWorkspaceTestServer(t)
	defer srv.Close()

	resp := apiDo(t, srv, http.MethodPost, "/api/workspaces",
		`{"name": "payments-fix", "owner": "alice", "projects": ["checkout-api", "money-lib"]}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var created workspaceResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID != "payments-fix" || created.Owner != "alice" || created.Status != "active" {
		t.Fatalf("unexpected workspace: %+v", created)
	}
	if created.SnapshotRef != "refs/workspaces/payments-fix/head" {
		t.Fatalf("unexpected snapshot ref: %q", created.SnapshotRef)
	}
	trunkTip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil || created.BaseRevision != trunkTip {
		t.Fatalf("expected base = trunk tip %s, got %q (err %v)", trunkTip, created.BaseRevision, err)
	}
	// The cone: both project paths, sorted - what `git sparse-checkout set
	// --cone` will consume verbatim.
	if len(created.SparsePatterns) != 2 || created.SparsePatterns[0] != "commerce/checkout" || created.SparsePatterns[1] != "libs/money" {
		t.Fatalf("unexpected sparse patterns: %v", created.SparsePatterns)
	}
	if created.RepoPath == "" {
		t.Fatalf("expected RepoPath for composing the git remote URL, got empty")
	}

	resp = apiDo(t, srv, http.MethodGet, "/api/workspaces/payments-fix", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET workspace: %d", resp.StatusCode)
	}
	var got workspaceResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.ID != "payments-fix" || len(got.SparsePatterns) != 2 {
		t.Fatalf("GET returned %+v", got)
	}

	resp = apiDo(t, srv, http.MethodGet, "/api/workspaces", "")
	var list []Workspace
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != "payments-fix" {
		t.Fatalf("LIST returned %+v", list)
	}
}

func TestWorkspaceCreateDuplicateNameConflicts(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t)
	defer srv.Close()

	body := `{"name": "payments-fix", "owner": "alice", "projects": ["checkout-api"]}`
	if resp := apiDo(t, srv, http.MethodPost, "/api/workspaces", body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d", resp.StatusCode)
	}
	resp := apiDo(t, srv, http.MethodPost, "/api/workspaces", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate, got %d", resp.StatusCode)
	}
	var ce clierr.Error
	json.NewDecoder(resp.Body).Decode(&ce)
	if ce.Code != "workspace_exists" {
		t.Fatalf("expected workspace_exists, got %+v", ce)
	}
}

func TestWorkspaceCreateUnknownProjectNamesTheCulprit(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t)
	defer srv.Close()

	resp := apiDo(t, srv, http.MethodPost, "/api/workspaces",
		`{"name": "w1", "owner": "alice", "projects": ["checkout-api", "no-such-thing"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var ce clierr.Error
	json.NewDecoder(resp.Body).Decode(&ce)
	if ce.Code != "unknown_project" || !strings.Contains(ce.Message, "no-such-thing") {
		t.Fatalf("expected unknown_project naming the culprit, got %+v", ce)
	}
}

func TestWorkspaceCreateBadNameRejected(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t)
	defer srv.Close()

	for _, bad := range []string{"", "../evil", "a/b", ".hidden"} {
		resp := apiDo(t, srv, http.MethodPost, "/api/workspaces",
			`{"name": "`+bad+`", "owner": "alice", "projects": ["checkout-api"]}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for name %q, got %d", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestWorkspaceUpdateBase(t *testing.T) {
	srv, bare, store := newWorkspaceTestServer(t)
	defer srv.Close()

	if resp := apiDo(t, srv, http.MethodPost, "/api/workspaces",
		`{"name": "w1", "owner": "alice", "projects": ["checkout-api"]}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	tip, _ := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")

	resp := apiDo(t, srv, http.MethodPost, "/api/workspaces/w1/base", `{"base_revision": "`+tip+`"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	ws, ok, _ := store.GetWorkspace(context.Background(), "w1")
	if !ok || ws.BaseRevision != tip {
		t.Fatalf("expected registry base %s, got %+v", tip, ws)
	}

	resp = apiDo(t, srv, http.MethodPost, "/api/workspaces/w1/base", `{"base_revision": "deadbeef"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown revision, got %d", resp.StatusCode)
	}
}

func TestSparsePatternsEndpoint(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t)
	defer srv.Close()

	resp := apiDo(t, srv, http.MethodGet, "/api/sparse-patterns?projects=money-lib", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got map[string][]string
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got["patterns"]) != 1 || got["patterns"][0] != "libs/money" {
		t.Fatalf("unexpected patterns: %v", got)
	}

	resp = apiDo(t, srv, http.MethodGet, "/api/sparse-patterns?projects=nope", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown project, got %d", resp.StatusCode)
	}
}
