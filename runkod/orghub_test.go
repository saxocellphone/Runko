package runkod

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/receive"
)

// newTestHub assembles a mem-mode hub the way cmd/runkod does: a default
// server (signup enabled) plus injected per-org store/server constructors.
// Returned httptest server serves hub.Handler(); hub.SelfURL is pointed at
// it so org hooks would call back correctly (the hook itself only runs in
// cmd/runkod's real-binary e2e tests).
func newTestHub(t *testing.T, allowCreate bool, extraPrincipals ...Principal) (*httptest.Server, *OrgHub) {
	t.Helper()
	bare := newBareRepo(t)
	store := NewMemStore()
	def := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
		AllowSignup: true, Principals: extraPrincipals,
	}
	hub := &OrgHub{
		Default:        def,
		DefaultOrgName: "defaultorg",
		DataDir:        t.TempDir(),
		AllowOrgCreate: allowCreate,
		Directory:      store,
		NewOrgStore: func(ctx context.Context, orgName string) (Store, error) {
			return NewMemStore(), nil
		},
		NewOrgServer: func(orgName, repoDir string, orgStore Store) (*Server, error) {
			return &Server{
				RepoDir: repoDir, TrunkRef: "main", Store: orgStore,
				Processor: &Processor{RepoDir: repoDir, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: orgStore, Directory: store},
				Token:     "sekret", AllowUnpolicedLand: true, Principals: extraPrincipals,
			}, nil
		},
	}
	handler, err := hub.Handler()
	if err != nil {
		t.Fatalf("hub.Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	hub.SelfURL = srv.URL
	return srv, hub
}

func hubSignup(t *testing.T, srv *httptest.Server, name, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "password": password})
	resp, err := http.Post(srv.URL+"/api/signup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup %s: status %d", name, resp.StatusCode)
	}
}

// hubDo issues a request with Basic (user+pass) or Bearer (token) auth and
// returns status + decoded JSON body (nil if not JSON).
func hubDo(t *testing.T, srv *httptest.Server, method, path, user, pass, token string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func TestOrgLifecycleMemMode(t *testing.T) {
	srv, hub := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	hubSignup(t, srv, "bob", "bobpw1234")

	// Alice creates an org and becomes its admin.
	status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"})
	if status != http.StatusCreated {
		t.Fatalf("create org: status %d body %v", status, body)
	}
	if body["git_url"] != "/o/acme/repo.git" || body["role"] != "admin" {
		t.Fatalf("create org response: %v", body)
	}

	// Listing: alice sees the default org (shared) + acme (admin); bob
	// sees only the default org.
	status, body = hubDo(t, srv, "GET", "/api/orgs", "alice", "alicepw123", "", nil)
	if status != http.StatusOK {
		t.Fatalf("list orgs: status %d", status)
	}
	orgs := body["orgs"].([]any)
	if len(orgs) != 2 {
		t.Fatalf("alice should see 2 orgs, got %v", body)
	}
	first := orgs[0].(map[string]any)
	if first["default"] != true || first["role"] != "shared" {
		t.Fatalf("first listed org should be the shared default, got %v", first)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs", "bob", "bobpw1234", "", nil)
	if status != http.StatusOK || len(body["orgs"].([]any)) != 1 {
		t.Fatalf("bob should see only the default org, got status %d body %v", status, body)
	}

	// Membership gates the org surface: alice 200, bob 403 (a structured
	// not_org_member - NOT 401: his credential is valid), deploy token 200.
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("member access: status %d", status)
	}
	status, body = hubDo(t, srv, "GET", "/o/acme/api/changes", "bob", "bobpw1234", "", nil)
	if status != http.StatusForbidden || body["Code"] != "not_org_member" {
		t.Fatalf("non-member should get 403 not_org_member, got %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("deploy token should stay server-wide: status %d", status)
	}

	// Only admins (or operators) add members.
	status, body = hubDo(t, srv, "POST", "/api/orgs/acme/members", "bob", "bobpw1234", "", map[string]string{"name": "bob"})
	if status != http.StatusForbidden || body["Code"] != "not_org_admin" {
		t.Fatalf("non-admin add member: got %d %v", status, body)
	}
	if status, body = hubDo(t, srv, "POST", "/api/orgs/acme/members", "alice", "alicepw123", "", map[string]string{"name": "bob"}); status != http.StatusOK {
		t.Fatalf("admin add member: got %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "bob", "bobpw1234", "", nil); status != http.StatusOK {
		t.Fatalf("bob should have access after being added: status %d", status)
	}
	// Adding an account nobody registered is a typo, not an invite.
	status, body = hubDo(t, srv, "POST", "/api/orgs/acme/members", "alice", "alicepw123", "", map[string]string{"name": "ghost"})
	if status != http.StatusNotFound || body["Code"] != "unknown_principal" {
		t.Fatalf("unknown principal: got %d %v", status, body)
	}

	// Unknown org: structured 404.
	status, body = hubDo(t, srv, "GET", "/o/ghost/api/changes", "", "", "sekret", nil)
	if status != http.StatusNotFound || body["Code"] != "unknown_org" {
		t.Fatalf("unknown org: got %d %v", status, body)
	}

	// The default org is also mounted under /o/<name>/ for uniformity.
	if status, _ = hubDo(t, srv, "GET", "/o/"+hub.DefaultOrgName+"/api/changes", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("default org via /o/: status %d", status)
	}
}

func TestOrgCreateValidationAndGates(t *testing.T) {
	agent := Principal{Name: "botsy", Token: "agent-token", IsAgent: true}
	srv, hub := newTestHub(t, true, agent)
	hubSignup(t, srv, "alice", "alicepw123")

	cases := []struct {
		name       string
		wantStatus int
		wantCode   string
	}{
		{"Bad_Name", http.StatusBadRequest, "invalid_org_name"},
		{"api", http.StatusBadRequest, "invalid_org_name"}, // reserved
		{hub.DefaultOrgName, http.StatusConflict, "org_exists"},
	}
	for _, tc := range cases {
		status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": tc.name})
		if status != tc.wantStatus || body["Code"] != tc.wantCode {
			t.Fatalf("create %q: got %d %v, want %d %s", tc.name, status, body, tc.wantStatus, tc.wantCode)
		}
	}

	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme should succeed, got %d", status)
	}
	status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"})
	if status != http.StatusConflict || body["Code"] != "org_exists" {
		t.Fatalf("duplicate create: got %d %v", status, body)
	}

	// Agents never manage orgs (§8.7).
	status, body = hubDo(t, srv, "POST", "/api/orgs", "", "", "agent-token", map[string]string{"name": "agentco"})
	if status != http.StatusForbidden || body["Code"] != "agent_denied" {
		t.Fatalf("agent create: got %d %v", status, body)
	}
}

func TestOrgCreateDisabledByDefault(t *testing.T) {
	srv, _ := newTestHub(t, false)
	hubSignup(t, srv, "alice", "alicepw123")
	status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"})
	if status != http.StatusForbidden || body["Code"] != "org_create_disabled" {
		t.Fatalf("create with flag off: got %d %v", status, body)
	}
}

// TestOrgReloadFromDirectory pins the boot path: a hub rebuilt over the
// same directory re-attaches previously created orgs (the durable-store
// restart story; in PG the directory rows survive for real).
func TestOrgReloadFromDirectory(t *testing.T) {
	srv, hub := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme failed")
	}

	// Second hub over the same directory + data dir (same repos on disk).
	hub2 := &OrgHub{
		Default:        hub.Default,
		DefaultOrgName: hub.DefaultOrgName,
		DataDir:        hub.DataDir,
		Directory:      hub.Directory,
		NewOrgStore:    hub.NewOrgStore,
		NewOrgServer:   hub.NewOrgServer,
	}
	loaded, err := hub2.LoadExisting(t.Context())
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if len(loaded) != 1 || loaded[0] != "acme" {
		t.Fatalf("expected to reload [acme], got %v", loaded)
	}
	handler, err := hub2.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv2 := httptest.NewServer(handler)
	defer srv2.Close()
	if status, _ := hubDo(t, srv2, "GET", "/o/acme/api/changes", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("reloaded org should serve: status %d", status)
	}
}

// Store-level isolation (a Change pushed into one org invisible from the
// others) is proven over real git + the real binary in cmd/runkod's
// TestEndToEndDaemonOrgs - each org's Store is a separate instance by
// construction here.
