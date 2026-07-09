package runkod

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/platform/receive"
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

// hubSignup registers an account joining the shared default org - since
// the org-required signup contract, every account arrives into SOME org.
func hubSignup(t *testing.T, srv *httptest.Server, name, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"name": name, "password": password, "org": "defaultorg", "org_mode": "join",
	})
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
	// Signup joined the default org, so the row names the real role.
	if first["default"] != true || first["role"] != "member" {
		t.Fatalf("first listed org should be the joined default, got %v", first)
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

func TestOrgSettingsAndMembers(t *testing.T) {
	srv, _ := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	hubSignup(t, srv, "bob", "bobpw1234")
	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme failed")
	}
	if status, _ := hubDo(t, srv, "POST", "/api/orgs/acme/members", "alice", "alicepw123", "", map[string]string{"name": "bob"}); status != http.StatusOK {
		t.Fatalf("add bob failed")
	}

	// Members may read settings; only admins may write.
	status, body := hubDo(t, srv, "GET", "/api/orgs/acme/settings", "bob", "bobpw1234", "", nil)
	if status != http.StatusOK {
		t.Fatalf("member GET settings: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "PUT", "/api/orgs/acme/settings", "bob", "bobpw1234", "",
		map[string]any{"description": "nope"})
	if status != http.StatusForbidden || body["Code"] != "not_org_admin" {
		t.Fatalf("member PUT settings: %d %v", status, body)
	}

	// Admin write normalizes check names (trim, dedupe, drop empties).
	status, body = hubDo(t, srv, "PUT", "/api/orgs/acme/settings", "alice", "alicepw123", "",
		map[string]any{"description": "the acme org", "global_required_checks": []string{" lint ", "lint", "", "e2e"}})
	if status != http.StatusOK {
		t.Fatalf("admin PUT settings: %d %v", status, body)
	}
	settings := body["settings"].(map[string]any)
	got := settings["global_required_checks"].([]any)
	if len(got) != 2 || got[0] != "lint" || got[1] != "e2e" {
		t.Fatalf("checks not normalized: %v", got)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs/acme/settings", "alice", "alicepw123", "", nil)
	if status != http.StatusOK || body["settings"].(map[string]any)["description"] != "the acme org" {
		t.Fatalf("settings did not persist: %d %v", status, body)
	}

	// Member listing, then removal - bob loses org access entirely.
	status, body = hubDo(t, srv, "GET", "/api/orgs/acme/members", "bob", "bobpw1234", "", nil)
	if status != http.StatusOK || len(body["members"].([]any)) != 2 {
		t.Fatalf("list members: %d %v", status, body)
	}
	if status, body = hubDo(t, srv, "DELETE", "/api/orgs/acme/members/bob", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("remove bob: %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "bob", "bobpw1234", "", nil); status != http.StatusForbidden {
		t.Fatalf("bob should lose access after removal: %d", status)
	}
	status, body = hubDo(t, srv, "DELETE", "/api/orgs/acme/members/ghost", "alice", "alicepw123", "", nil)
	if status != http.StatusNotFound || body["Code"] != "not_a_member" {
		t.Fatalf("remove non-member: %d %v", status, body)
	}
}

// TestDefaultOrgSettingsAndAdminRole pins the default org's special rules:
// shared read for every valid credential, writes for its admins/operators,
// and a real membership row surfacing its role in the org listing (the
// "make <user> the org admin" flow).
func TestDefaultOrgSettingsAndAdminRole(t *testing.T) {
	srv, hub := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	def := hub.DefaultOrgName

	// Everyone reads; a non-admin account cannot write; the operator can.
	if status, _ := hubDo(t, srv, "GET", "/api/orgs/"+def+"/settings", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("shared read of default settings: %d", status)
	}
	if status, _ := hubDo(t, srv, "PUT", "/api/orgs/"+def+"/settings", "alice", "alicepw123", "", map[string]any{"description": "x"}); status != http.StatusForbidden {
		t.Fatalf("non-admin write to default settings should 403, got %d", status)
	}
	if status, _ := hubDo(t, srv, "PUT", "/api/orgs/"+def+"/settings", "", "", "sekret", map[string]any{"description": "the shared org"}); status != http.StatusOK {
		t.Fatalf("operator write to default settings failed")
	}

	// Operator promotes alice to default-org admin; she can now write,
	// and the org listing names her role instead of "shared".
	// (Mem-mode needs the default org registered in the directory first.)
	if err := hub.Directory.EnsureOrg(t.Context(), def); err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if status, body := hubDo(t, srv, "POST", "/api/orgs/"+def+"/members", "", "", "sekret", map[string]string{"name": "alice", "role": "admin"}); status != http.StatusOK {
		t.Fatalf("promote alice: %d %v", status, body)
	}
	if status, _ := hubDo(t, srv, "PUT", "/api/orgs/"+def+"/settings", "alice", "alicepw123", "", map[string]any{"description": "run by alice"}); status != http.StatusOK {
		t.Fatalf("default-org admin write failed")
	}
	status, body := hubDo(t, srv, "GET", "/api/orgs", "alice", "alicepw123", "", nil)
	if status != http.StatusOK {
		t.Fatalf("list orgs: %d", status)
	}
	first := body["orgs"].([]any)[0].(map[string]any)
	if first["default"] != true || first["role"] != "admin" {
		t.Fatalf("default org should list alice's admin role, got %v", first)
	}
}

// TestOrgSettingsChecksGateMerge proves stored org settings reach the
// §13.5 gate: a check required via the settings page blocks mergeability
// exactly like --global-required-checks.
func TestOrgSettingsChecksGateMerge(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	resp := authedGet(t, srv, "/api/changes/"+changeID+"/merge-requirements", "sekret")
	before := readBody(t, resp)
	if strings.Contains(before, "org-smoke-check") {
		t.Fatalf("org check present before settings set: %s", before)
	}

	// newTestServer's Server has no SettingsOrg; wire one through the
	// same MemStore the way cmd/runkod does, then re-ask.
	srv2, changeID2, server, store := newTestServerWithHandle(t)
	defer srv2.Close()
	server.SettingsOrg = "defaultorg"
	server.Directory = store
	if err := store.UpdateOrgSettings(t.Context(), "defaultorg", OrgSettings{GlobalRequiredChecks: []string{"org-smoke-check"}}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	resp = authedGet(t, srv2, "/api/changes/"+changeID2+"/merge-requirements", "sekret")
	after := readBody(t, resp)
	if !strings.Contains(after, "org-smoke-check") || !strings.Contains(after, `"mergeable":false`) {
		t.Fatalf("org-settings check should gate the merge: %s", after)
	}
}

// TestSignupWithOrg pins the org-required sign-up: every account arrives
// into an org - create (you become admin) or join (open to anyone, for
// now: per-org email invites are the recorded follow-up) - and a rejected
// org never strands a half-created account.
func TestSignupWithOrg(t *testing.T) {
	srv, _ := newTestHub(t, true)

	// No org at all: refused, nothing created.
	status, body := hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123"})
	if status != http.StatusBadRequest || body["Code"] != "missing_org" {
		t.Fatalf("org-less signup: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123", "org": "somewhere", "org_mode": "sideways"})
	if status != http.StatusBadRequest || body["Code"] != "invalid_org_mode" {
		t.Fatalf("bad org_mode: %d %v", status, body)
	}

	// Org validation runs BEFORE account creation: bad org, no account.
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123", "org": "Bad_Org", "org_mode": "create"})
	if status != http.StatusBadRequest || body["Code"] != "invalid_org_name" {
		t.Fatalf("signup with bad org: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123", "org": "ghost", "org_mode": "join"})
	if status != http.StatusNotFound || body["Code"] != "unknown_org" {
		t.Fatalf("join of unknown org: %d %v", status, body)
	}

	// The same account name still signs up cleanly - nothing was created.
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123", "org": "carols-org", "org_mode": "create"})
	if status != http.StatusCreated {
		t.Fatalf("signup with org: %d %v", status, body)
	}
	orgResp := body["org"].(map[string]any)
	if orgResp["name"] != "carols-org" || orgResp["role"] != "admin" {
		t.Fatalf("signup org response: %v", body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/carols-org/api/changes", "carol", "carolpw123", "", nil); status != http.StatusOK {
		t.Fatalf("carol should reach her org immediately: %d", status)
	}

	// Creating a taken name: conflict, no account row - dave then JOINS
	// it instead (open join, the current policy) and gets member access.
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "dave", "password": "davepw1234", "org": "carols-org", "org_mode": "create"})
	if status != http.StatusConflict || body["Code"] != "org_exists" {
		t.Fatalf("signup creating taken org: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "dave", "password": "davepw1234", "org": "carols-org", "org_mode": "join"})
	if status != http.StatusCreated || body["org"].(map[string]any)["role"] != "member" {
		t.Fatalf("signup joining existing org: %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/carols-org/api/changes", "dave", "davepw1234", "", nil); status != http.StatusOK {
		t.Fatalf("dave should reach the joined org immediately: %d", status)
	}

	// Discovery config advertises the org-create option.
	status, body = hubDo(t, srv, "GET", "/api/auth/config", "", "", "", nil)
	if status != http.StatusOK || body["org_create_enabled"] != true {
		t.Fatalf("auth config: %d %v", status, body)
	}
}

// TestSignupWithOrgDisabled: org creation off -> create-mode signup is a
// structured refusal, joining (the default org included) still works.
func TestSignupWithOrgDisabled(t *testing.T) {
	srv, _ := newTestHub(t, false)
	status, body := hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "erin", "password": "erinpw1234", "org": "erins-org", "org_mode": "create"})
	if status != http.StatusForbidden || body["Code"] != "org_create_disabled" {
		t.Fatalf("org signup with creation off: %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "erin", "password": "erinpw1234", "org": "defaultorg", "org_mode": "join"}); status != http.StatusCreated {
		t.Fatalf("joining the default org should still work: %d", status)
	}
	status, body = hubDo(t, srv, "GET", "/api/auth/config", "", "", "", nil)
	if status != http.StatusOK || body["org_create_enabled"] != false {
		t.Fatalf("auth config should advertise org creation off: %d %v", status, body)
	}
}
