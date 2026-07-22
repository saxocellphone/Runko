package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/index"
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

// TestWorkspaceCreateWithNewPaths is the greenfield bootstrap (2026-07-16
// dogfood review, finding 3): a workspace can carry affinity for a project
// that does NOT exist at trunk yet, so the change that creates it has a
// workspace to be born in. New paths join the write allowlist (= the cone)
// alongside resolved project paths; a project-less workspace with only a
// new path is legal; junk paths and already-indexed paths are refused
// with structured errors.
func TestWorkspaceCreateWithNewPaths(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t)
	defer srv.Close()

	resp := apiDo(t, srv, http.MethodPost, "/api/workspaces",
		`{"name": "greenfield", "owner": "alice", "projects": ["checkout-api"], "new_paths": ["services/newproj/"]}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var created workspaceResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Sorted union of the resolved project path and the (slash-trimmed)
	// new path - what affinity enforcement and the sparse cone both read.
	if len(created.SparsePatterns) != 2 || created.SparsePatterns[0] != "commerce/checkout" || created.SparsePatterns[1] != "services/newproj" {
		t.Fatalf("unexpected sparse patterns: %v", created.SparsePatterns)
	}

	// A workspace for ONLY the not-yet-born project is the whole point.
	resp = apiDo(t, srv, http.MethodPost, "/api/workspaces",
		`{"name": "greenfield-only", "owner": "alice", "new_paths": ["services/other"]}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("project-less create with a new path: expected 201, got %d: %s", resp.StatusCode, body)
	}

	for body, wantCode := range map[string]string{
		`{"name": "bad-dotdot", "owner": "alice", "new_paths": ["../escape"]}`:         "invalid_new_path",
		`{"name": "bad-root", "owner": "alice", "new_paths": ["."]}`:                   "invalid_new_path",
		`{"name": "bad-exists", "owner": "alice", "new_paths": ["commerce/checkout"]}`: "project_exists_at_path",
	} {
		resp := apiDo(t, srv, http.MethodPost, "/api/workspaces", body)
		var ce struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil || ce.Code != wantCode {
			t.Fatalf("body %s: code = %q (status %d, err %v), want %s", body, ce.Code, resp.StatusCode, err, wantCode)
		}
	}
}

// TestExpandConeToDeps: the READ cone auto-widens to the transitive
// declared-dependency closure of the affinity projects (so a checkout can
// build across projects with no manual `git sparse-checkout add`), while
// unrelated projects stay out and NewPaths / dangling edges carry through.
func TestExpandConeToDeps(t *testing.T) {
	indexed := []index.IndexedProject{
		{Name: "runkod", Path: "runkod", DeclaredDependencies: []string{"platform", "db"}, ContractDir: "runkod/proto"},
		{Name: "platform", Path: "platform", DeclaredDependencies: []string{"internal"}},
		{Name: "internal", Path: "internal"},
		{Name: "db", Path: "db"},
		{Name: "mailer", Path: "mailer", Consumes: []string{"runkod"}}, // consumes-only, no deps
		{Name: "web", Path: "web"},                                     // unrelated - must NOT enter the cone
	}

	// affinity runkod -> runkod + its transitive deps, never web.
	got := expandConeToDeps([]string{"runkod"}, []string{"runkod"}, indexed)
	if want := []string{"db", "internal", "platform", "runkod"}; !slices.Equal(got, want) {
		t.Fatalf("cone = %v, want %v", got, want)
	}

	// A consumes-only project gets ONLY the provider's contract surface
	// (ContractDir), never runkod's whole tree or its build deps (§13.3.1).
	got3 := expandConeToDeps([]string{"mailer"}, []string{"mailer"}, indexed)
	if want := []string{"mailer", "runkod/proto"}; !slices.Equal(got3, want) {
		t.Fatalf("consumes cone = %v, want %v", got3, want)
	}

	// A NewPath allowlist entry (no indexed project) and a dangling affinity
	// name both survive without breaking the walk.
	got2 := expandConeToDeps([]string{"platform", "notindexed"}, []string{"platform", "services/new"}, indexed)
	for _, want := range []string{"platform", "internal", "services/new"} {
		if !slices.Contains(got2, want) {
			t.Errorf("cone2 %v missing %q", got2, want)
		}
	}
	if slices.Contains(got2, "runkod") || slices.Contains(got2, "web") {
		t.Errorf("cone2 %v pulled in an unrelated project", got2)
	}
}
