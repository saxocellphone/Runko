package runkod

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/agentsmd"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/receive"

	mailerv1 "github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1"
	"github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1/mailerv1connect"
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
		// Org-scoped sessions (2026-07-09): the default org is membership-
		// gated like any other - the fixture mirrors cmd/runkod's wiring.
		OrgName: "defaultorg", Directory: store,
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

// newOrglessTestHub assembles the ORG-LESS hub (2026-07-17, the
// default-org retirement): Default is an AUTH-ONLY Server - no repo, no
// Processor, its Handler never built - and DefaultOrgName is empty, so
// the hub itself serves the root and every org lives at /o/<name>/.
func newOrglessTestHub(t *testing.T, allowCreate bool, extraPrincipals ...Principal) (*httptest.Server, *OrgHub) {
	t.Helper()
	store := NewMemStore()
	def := &Server{
		Store: store, Token: "sekret", TrunkRef: "main",
		AllowSignup: true, Principals: extraPrincipals,
		Directory: store,
	}
	hub := &OrgHub{
		Default:        def,
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
	if body["git_url"] != "/o/acme/acme.git" || body["role"] != "admin" {
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
	// Signup joined the default org, so a membership row lists it with
	// its real role (no unconditional shared entry exists anymore).
	var defaultRow map[string]any
	for _, o := range orgs {
		if row := o.(map[string]any); row["default"] == true {
			defaultRow = row
		}
	}
	if defaultRow == nil || defaultRow["role"] != "member" {
		t.Fatalf("the joined default org should list with its membership role, got %v", orgs)
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

	// Only admins (or operators) add members: a NON-MEMBER is refused as
	// such (he can't even see the org), a non-admin MEMBER as non-admin.
	status, body = hubDo(t, srv, "POST", "/api/orgs/acme/members", "bob", "bobpw1234", "", map[string]string{"name": "bob"})
	if status != http.StatusForbidden || body["Code"] != "not_org_member" {
		t.Fatalf("non-member add member: got %d %v", status, body)
	}
	// Per-org identity (migration 0017): membership is a role on one of
	// the org's OWN accounts - bob's default-org account is not one, so
	// adding him is unknown_principal; he joins by signing up INTO acme.
	status, body = hubDo(t, srv, "POST", "/api/orgs/acme/members", "alice", "alicepw123", "", map[string]string{"name": "bob"})
	if status != http.StatusNotFound || body["Code"] != "unknown_principal" {
		t.Fatalf("cross-org member add must refuse: got %d %v", status, body)
	}
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "", map[string]string{
		"name": "bob", "password": "bobpw1234", "org": "acme", "org_mode": "join"})
	if status != http.StatusCreated {
		t.Fatalf("bob joining acme: got %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "bob", "bobpw1234", "", nil); status != http.StatusOK {
		t.Fatalf("bob should have access after joining: status %d", status)
	}
	status, body = hubDo(t, srv, "POST", "/api/orgs/acme/members", "bob", "bobpw1234", "", map[string]string{"name": "ghost2"})
	if status != http.StatusForbidden || body["Code"] != "not_org_admin" {
		t.Fatalf("member (non-admin) add member: got %d %v", status, body)
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
	// Per-org identity: bob arrives in acme by signing up into it.
	if status, _ := hubDo(t, srv, "POST", "/api/signup", "", "", "", map[string]string{
		"name": "bob", "password": "bobpw1234", "org": "acme", "org_mode": "join"}); status != http.StatusCreated {
		t.Fatalf("bob joining acme failed")
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
	srv, hub := newTestHub(t, true)

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

	// A malformed optional email is refused by the hub path too, and (like
	// every rejection above) creates nothing - carol signs up cleanly next.
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123", "org": "carols-org", "org_mode": "create", "email": "carol@"})
	if status != http.StatusBadRequest || body["Code"] != "invalid_email" {
		t.Fatalf("signup with a malformed email: %d %v", status, body)
	}

	// The same account name still signs up cleanly - nothing was created.
	status, body = hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "carol", "password": "carolpw123", "org": "carols-org", "org_mode": "create", "email": "carol@example.com"})
	if status != http.StatusCreated {
		t.Fatalf("signup with org: %d %v", status, body)
	}
	// The address travels the hub path into the account's own org row.
	if sp, found, err := hub.Directory.GetStoredPrincipal(t.Context(), "carols-org", "carol"); err != nil || !found || sp.Email != "carol@example.com" {
		t.Fatalf("carol's stored email: %+v found=%v err=%v", sp, found, err)
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

// TestOrgScopedSessionsIsolation pins the 2026-07-09 decision: logging in
// means logging into AN ORG. An account that belongs only to one org must
// not see, list, or reach any other - the default org included (its
// historical everyone-with-a-credential behavior is gone).
func TestOrgScopedSessionsIsolation(t *testing.T) {
	srv, _ := newTestHub(t, true)

	// zoe signs up CREATING her own org - she never joins the default.
	status, _ := hubDo(t, srv, "POST", "/api/signup", "", "", "",
		map[string]string{"name": "zoe", "password": "zoepw12345", "org": "zoes-org", "org_mode": "create"})
	if status != http.StatusCreated {
		t.Fatalf("signup: %d", status)
	}

	// Her org list is exactly her org - no default-org row, nothing else.
	status, body := hubDo(t, srv, "GET", "/api/orgs", "zoe", "zoepw12345", "", nil)
	if status != http.StatusOK {
		t.Fatalf("list orgs: %d", status)
	}
	orgs := body["orgs"].([]any)
	if len(orgs) != 1 || orgs[0].(map[string]any)["name"] != "zoes-org" {
		t.Fatalf("zoe should see exactly her org, got %v", orgs)
	}

	// The default org's API surface refuses her - 403, not 401 (her
	// credential is valid; the org just isn't hers).
	status, body = hubDo(t, srv, "GET", "/api/changes", "zoe", "zoepw12345", "", nil)
	if status != http.StatusForbidden || body["Code"] != "not_org_member" {
		t.Fatalf("root (default org) should refuse a non-member: %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/defaultorg/api/changes", "zoe", "zoepw12345", "", nil); status != http.StatusForbidden {
		t.Fatalf("default org via /o/ should refuse a non-member: %d", status)
	}
	// Default-org settings/members: same story (no shared read anymore).
	if status, _ = hubDo(t, srv, "GET", "/api/orgs/defaultorg/settings", "zoe", "zoepw12345", "", nil); status != http.StatusForbidden {
		t.Fatalf("default org settings should refuse a non-member: %d", status)
	}

	// Her own org: whoami (the login round-trip) and the API both work.
	if status, _ = hubDo(t, srv, "GET", "/o/zoes-org/api/whoami", "zoe", "zoepw12345", "", nil); status != http.StatusOK {
		t.Fatalf("org-scoped whoami (the login check) should succeed: %d", status)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/zoes-org/api/changes", "zoe", "zoepw12345", "", nil); status != http.StatusOK {
		t.Fatalf("her own org should serve her: %d", status)
	}

	// A default-org member conversely cannot reach zoes-org.
	hubSignup(t, srv, "dora", "dorapw1234") // joins defaultorg
	if status, _ = hubDo(t, srv, "GET", "/api/changes", "dora", "dorapw1234", "", nil); status != http.StatusOK {
		t.Fatalf("default-org member should reach the default org: %d", status)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/zoes-org/api/changes", "dora", "dorapw1234", "", nil); status != http.StatusForbidden {
		t.Fatalf("default-org member should not reach zoes-org: %d", status)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs", "dora", "dorapw1234", "", nil)
	if status != http.StatusOK || len(body["orgs"].([]any)) != 1 {
		t.Fatalf("dora should see exactly the default org, got %v", body)
	}
}

// TestAdminPanelAndOrgArchive covers the deployment admin surface: the
// org estate listing and the archive lifecycle (finding #19). Operators
// only; archiving closes the org's whole surface (410, uniformly) and
// hides it from member listings while keeping row + repo; unarchive
// restores routing without a restart; the default org is immovable.
func TestAdminPanelAndOrgArchive(t *testing.T) {
	srv, _ := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme failed")
	}

	// Operator-only: a store account (even an org admin) is refused.
	status, body := hubDo(t, srv, "GET", "/api/admin/orgs", "alice", "alicepw123", "", nil)
	if status != http.StatusForbidden || body["Code"] != "operator_only" {
		t.Fatalf("store account on admin surface: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "POST", "/api/admin/orgs/acme/archive", "alice", "alicepw123", "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("store account archiving: %d %v", status, body)
	}

	// The estate listing (deploy token = operator) shows both orgs.
	status, body = hubDo(t, srv, "GET", "/api/admin/orgs", "", "", "sekret", nil)
	if status != http.StatusOK {
		t.Fatalf("admin orgs: %d", status)
	}
	rows := body["orgs"].([]any)
	if len(rows) != 2 {
		t.Fatalf("estate should list default + acme, got %v", rows)
	}

	// Archive: surface closes with 410 for EVERYONE, selector listings
	// hide it, admin listing still shows it (flagged).
	if status, _ = hubDo(t, srv, "POST", "/api/admin/orgs/acme/archive", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("archive: %d", status)
	}
	// The archive lifecycle lives on the admin prefix ONLY - the old
	// org-scoped path is gone, not aliased.
	if status, _ = hubDo(t, srv, "POST", "/api/orgs/acme/unarchive", "", "", "sekret", nil); status == http.StatusOK {
		t.Fatalf("legacy /api/orgs/{org}/unarchive should not exist anymore")
	}
	status, body = hubDo(t, srv, "GET", "/o/acme/api/changes", "alice", "alicepw123", "", nil)
	if status != http.StatusGone || body["Code"] != "org_archived" {
		t.Fatalf("archived org should answer 410 org_archived, got %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "", "", "sekret", nil); status != http.StatusGone {
		t.Fatalf("archived org should be 410 even for operators, got %d", status)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs", "alice", "alicepw123", "", nil)
	if status != http.StatusOK {
		t.Fatalf("list orgs: %d", status)
	}
	for _, o := range body["orgs"].([]any) {
		if o.(map[string]any)["name"] == "acme" {
			t.Fatalf("archived org leaked into the selector listing: %v", body)
		}
	}
	status, body = hubDo(t, srv, "GET", "/api/admin/orgs", "", "", "sekret", nil)
	found := false
	for _, o := range body["orgs"].([]any) {
		row := o.(map[string]any)
		if row["name"] == "acme" {
			found = true
			if row["archived"] != true {
				t.Fatalf("admin listing should flag the archive: %v", row)
			}
		}
	}
	if !found {
		t.Fatalf("admin listing must keep archived orgs visible: %v", body)
	}

	// Name stays taken while archived.
	if status, _ = hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusConflict {
		t.Fatalf("archived org's name should stay taken: %d", status)
	}

	// Unarchive restores routing in-place.
	if status, _ = hubDo(t, srv, "POST", "/api/admin/orgs/acme/unarchive", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("unarchive: %d", status)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("unarchived org should serve again: %d", status)
	}

	// The default org is immovable.
	status, body = hubDo(t, srv, "POST", "/api/admin/orgs/defaultorg/archive", "", "", "sekret", nil)
	if status != http.StatusBadRequest || body["Code"] != "default_org_immutable" {
		t.Fatalf("default org archive: %d %v", status, body)
	}
}

// TestAdminWhoamiAndOperatorOrgCreate covers the admin panel's DEDICATED
// sign-in flow (GET /api/admin/whoami - webadmin/ never rides the
// org-scoped login) and operator org creation on the admin surface,
// which --allow-org-create does not gate: the flag scopes signup
// accounts, and the operator is whoever would flip it.
func TestAdminWhoamiAndOperatorOrgCreate(t *testing.T) {
	// allowCreate=false: self-service creation is OFF for this hub.
	srv, _ := newTestHub(t, false, Principal{Name: "op", Token: "op-secret-1"})
	hubSignup(t, srv, "alice", "alicepw123")

	// The deploy token and a flag-config principal sign in; the check
	// names the caller so the panel can show who's holding the wheel.
	status, body := hubDo(t, srv, "GET", "/api/admin/whoami", "", "", "sekret", nil)
	if status != http.StatusOK || body["operator"] != true || body["anonymous"] != true {
		t.Fatalf("deploy-token admin whoami: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "GET", "/api/admin/whoami", "op", "op-secret-1", "", nil)
	if status != http.StatusOK || body["name"] != "op" || body["anonymous"] != false {
		t.Fatalf("operator-principal admin whoami: %d %v", status, body)
	}

	// A signup account - even a future org admin - is 403, and a wrong
	// credential is 401: the panel can tell the two apart.
	status, body = hubDo(t, srv, "GET", "/api/admin/whoami", "alice", "alicepw123", "", nil)
	if status != http.StatusForbidden || body["Code"] != "operator_only" {
		t.Fatalf("store account on admin whoami: %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/api/admin/whoami", "op", "wrong-password", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("bad credential on admin whoami: %d", status)
	}

	// Self-service creation is off (403 for accounts), but the operator
	// creates on the admin surface regardless.
	status, body = hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"})
	if status != http.StatusForbidden || body["Code"] != "org_create_disabled" {
		t.Fatalf("self-service create should be disabled: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "POST", "/api/admin/orgs", "op", "op-secret-1", "", map[string]string{"name": "acme"})
	if status != http.StatusCreated || body["role"] != "operator" {
		t.Fatalf("operator create via admin surface: %d %v", status, body)
	}
	if status, _ = hubDo(t, srv, "GET", "/o/acme/api/changes", "op", "op-secret-1", "", nil); status != http.StatusOK {
		t.Fatalf("created org should serve its creator: %d", status)
	}

	// A signup account cannot use the admin creation path either.
	status, body = hubDo(t, srv, "POST", "/api/admin/orgs", "alice", "alicepw123", "", map[string]string{"name": "other"})
	if status != http.StatusForbidden || body["Code"] != "operator_only" {
		t.Fatalf("store account on admin create: %d %v", status, body)
	}
}

// The org revalidation_policy setting (§13.5, 2026-07-15): valid tiers
// store and resolve; "never" (the admin force override) and garbage are
// refused at write time.
func TestOrgRevalidationPolicySetting(t *testing.T) {
	srv, _ := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme failed")
	}

	status, body := hubDo(t, srv, "PUT", "/api/orgs/acme/settings", "alice", "alicepw123", "",
		map[string]any{"revalidation_policy": "affected-intersection"})
	if status != http.StatusOK {
		t.Fatalf("valid tier refused: %d %v", status, body)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs/acme/settings", "alice", "alicepw123", "", nil)
	if status != http.StatusOK || body["settings"].(map[string]any)["revalidation_policy"] != "affected-intersection" {
		t.Fatalf("tier did not persist: %d %v", status, body)
	}

	for _, bad := range []string{"never", "sometimes"} {
		status, body = hubDo(t, srv, "PUT", "/api/orgs/acme/settings", "alice", "alicepw123", "",
			map[string]any{"revalidation_policy": bad})
		if status != http.StatusBadRequest || body["Code"] != "invalid_revalidation_policy" {
			t.Fatalf("%q should be refused: %d %v", bad, status, body)
		}
	}
}

// TestCreatedOrgIsBornUsable pins §6.10's genesis: a creator-made org's
// trunk is never unborn - it carries the seed tree (root manifest, OWNERS
// naming the creator, AGENTS.md, the agent skill, CONTRIBUTING.md), the
// index resolves the root project with the creator as its owner, and
// `workspace create --project repo` works immediately (the trunk_unborn
// dead end is gone).
func TestCreatedOrgIsBornUsable(t *testing.T) {
	srv, hub := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme: %d %v", status, body)
	}

	gstore := gitstore.New(hub.repoDirFor("acme"))
	rev, err := gstore.ResolveRef("refs/heads/main")
	if err != nil {
		t.Fatalf("created org's trunk is unborn: %v", err)
	}
	for _, path := range []string{"PROJECT.yaml", "OWNERS", "AGENTS.md", agentsmd.SkillPath, "CONTRIBUTING.md"} {
		if _, err := gstore.GetBlob(rev, path); err != nil {
			t.Fatalf("genesis tree is missing %s: %v", path, err)
		}
	}
	// The seeded skill must be loadable: frontmatter first, so a harness
	// scanning .claude/skills/*/SKILL.md can key on its description.
	skill, err := gstore.GetBlob(rev, agentsmd.SkillPath)
	if err != nil {
		t.Fatalf("read seeded skill: %v", err)
	}
	if !bytes.HasPrefix(skill.Content, []byte("---\nname: runko\n")) {
		t.Fatalf("seeded skill does not open with frontmatter:\n%.120s", skill.Content)
	}

	// The seeded state must be what the rest of the system actually
	// consumes: the index resolves exactly one project ("repo", at root)
	// whose owner is the creator via OWNERS inheritance (§7.3).
	projects, err := index.Scan(gstore, rev, nil)
	if err != nil {
		t.Fatalf("index.Scan over genesis: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "repo" || projects[0].Path != "" {
		t.Fatalf("expected exactly the root project, got %+v", projects)
	}
	if len(projects[0].Owners) != 1 || projects[0].Owners[0].Ref != "alice" || projects[0].Owners[0].Source != "path_owners" {
		t.Fatalf("expected alice as the seeded OWNERS owner, got %+v", projects[0].Owners)
	}

	status, body := hubDo(t, srv, "POST", "/o/acme/api/workspaces", "", "", "sekret",
		map[string]any{"name": "first-ws", "owner": "alice", "projects": []string{"repo"}})
	if status != http.StatusCreated {
		t.Fatalf("workspace create against a fresh org: %d %v", status, body)
	}
}

// TestGenesisNeverRewritesABornTrunk: re-assembly re-runs seedGenesisCommit
// (crash recovery re-enters createOrg; finding #44's signup recovery), and
// a trunk with history must be left byte-for-byte alone.
func TestGenesisNeverRewritesABornTrunk(t *testing.T) {
	srv, hub := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme: %d %v", status, body)
	}

	repoDir := hub.repoDirFor("acme")
	gstore := gitstore.New(repoDir)
	before, err := gstore.ResolveRef("refs/heads/main")
	if err != nil {
		t.Fatalf("trunk after create: %v", err)
	}
	if err := seedGenesisCommit(repoDir, "main", "acme", "mallory"); err != nil {
		t.Fatalf("re-running genesis: %v", err)
	}
	after, err := gstore.ResolveRef("refs/heads/main")
	if err != nil {
		t.Fatalf("trunk after re-run: %v", err)
	}
	if before != after {
		t.Fatalf("genesis rewrote a born trunk: %s -> %s", before, after)
	}
}

// TestOrglessHub pins the default-org retirement (2026-07-17): with no
// DefaultOrgName the hub serves the ops floor and the global APIs at the
// root, a structured 404 everywhere else, and every org - the FIRST one
// included - is born via signup/admin-create and lives at /o/<name>/.
// No listing or estate row is flagged default, and no org is
// archive-immutable, because the special case no longer exists.
func TestOrglessHub(t *testing.T) {
	srv, _ := newOrglessTestHub(t, true)

	// Ops floor at the root; org surfaces are a structured 404.
	if status, _ := hubDo(t, srv, "GET", "/healthz", "", "", "", nil); status != http.StatusOK {
		t.Fatalf("healthz: %d", status)
	}
	if status, _ := hubDo(t, srv, "GET", "/readyz", "", "", "", nil); status != http.StatusOK {
		t.Fatalf("readyz: %d", status)
	}
	status, body := hubDo(t, srv, "GET", "/api/changes", "", "", "sekret", nil)
	if status != http.StatusNotFound || body["Code"] != "no_default_org" {
		t.Fatalf("root org API should be a structured 404, got %d %v", status, body)
	}
	if status, body = hubDo(t, srv, "GET", "/", "", "", "", nil); status != http.StatusNotFound || body["Code"] != "no_default_org" {
		t.Fatalf("root should answer no_default_org, got %d %v", status, body)
	}

	// Signup CREATES the first org; there is no landing zone to join.
	if status, body := signupOrg(t, srv, "alice", "alicepw123", "acme", "create"); status != http.StatusCreated {
		t.Fatalf("first-org signup: %d %v", status, body)
	}
	status, body = signupOrg(t, srv, "bob", "bobpw1234", "defaultorg", "join")
	if status != http.StatusNotFound || body["Code"] != "unknown_org" {
		t.Fatalf("joining the retired default org must be unknown_org, got %d %v", status, body)
	}
	if status, _ := signupOrg(t, srv, "bob", "bobpw1234", "acme", "join"); status != http.StatusCreated {
		t.Fatalf("joining acme: %d", status)
	}

	// The org serves at its own mount; nothing anywhere is default.
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/whoami", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("org-scoped sign-in: %d", status)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs", "alice", "alicepw123", "", nil)
	if status != http.StatusOK || len(body["orgs"].([]any)) != 1 {
		t.Fatalf("alice should see exactly acme: %d %v", status, body)
	}
	for _, o := range body["orgs"].([]any) {
		row := o.(map[string]any)
		if row["default"] == true || row["api_base"] == "" {
			t.Fatalf("no listing row may be default or root-mounted: %v", row)
		}
	}
	status, body = hubDo(t, srv, "GET", "/api/admin/orgs", "", "", "sekret", nil)
	if status != http.StatusOK {
		t.Fatalf("estate: %d", status)
	}
	rows := body["orgs"].([]any)
	if len(rows) != 1 {
		t.Fatalf("estate should hold exactly acme, got %v", rows)
	}
	if rows[0].(map[string]any)["default"] == true {
		t.Fatalf("estate must not flag a default org: %v", rows)
	}
	if status, body = hubDo(t, srv, "GET", "/api/admin/whoami", "", "", "sekret", nil); status != http.StatusOK || body["operator"] != true {
		t.Fatalf("admin whoami: %d %v", status, body)
	}

	// Archive works on ANY org - the immutable special case is gone.
	if status, _ := hubDo(t, srv, "POST", "/api/admin/orgs/acme/archive", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("archive: %d", status)
	}
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/whoami", "alice", "alicepw123", "", nil); status != http.StatusGone {
		t.Fatalf("archived org should be 410")
	}
	if status, _ := hubDo(t, srv, "POST", "/api/admin/orgs/acme/unarchive", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("unarchive: %d", status)
	}
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/whoami", "alice", "alicepw123", "", nil); status != http.StatusOK {
		t.Fatalf("unarchived org should serve again")
	}
}

// The org-less root must serve the mailer's drain surface: intake rows
// are deployment-wide, and the prod org-less flip left runko-mailer
// polling a root that answered "no root-mounted org" while submissions
// piled up pending - stored, never mailed.
func TestOrglessHubServesInviteFeed(t *testing.T) {
	srv, hub := newOrglessTestHub(t, true)
	hub.Default.AllowInviteRequests = true // read per-request; routes exist regardless

	for path, body := range map[string]string{
		"/api/invite-requests": `{"name":"Ada","email":"ada@example.com"}`,
		"/api/contact":         `{"name":"Ada","email":"ada@example.com","message":"how do I join?"}`,
	} {
		if status, resp := hubDo(t, srv, "POST", path, "", "", "", json.RawMessage(body)); status != http.StatusAccepted {
			t.Fatalf("POST %s: %d %v", path, status, resp)
		}
	}

	reqs := dueRequests(t, srv)
	if len(reqs) != 2 {
		t.Fatalf("root due feed: want the invite and the contact row, got %v", reqs)
	}
	kinds := map[string]int{}
	for _, r := range reqs {
		kinds[r.Kind]++
	}
	if kinds["invite"] != 1 || kinds["contact"] != 1 {
		t.Fatalf("due kinds at the org-less root: %v", kinds)
	}

	// The operator gate still holds at the root: anonymous is refused.
	err := inviteFeed(t, srv, "", func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
		_, err := c.ListDue(context.Background(), connect.NewRequest(&mailerv1.ListDueRequest{}))
		return err
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous feed at root: want Unauthenticated, got %v", err)
	}

	// Acks work at the root too - the full drain loop, not just the list.
	err = inviteFeed(t, srv, "sekret", func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
		for _, r := range reqs {
			if _, err := c.MarkSent(context.Background(), connect.NewRequest(&mailerv1.MarkSentRequest{Id: r.Id})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("MarkSent at root: %v", err)
	}
	if left := dueRequests(t, srv); len(left) != 0 {
		t.Fatalf("acked rows still due: %v", left)
	}
}

// TestAgentPolicyAdminAPI covers the operator-only per-org agent-policy surface
// (§8.7): the operator round-trips an override, an unknown org is 404, and a
// non-operator (store account) or an agent is refused - the policy that governs
// agents is never agent-settable.
func TestAgentPolicyAdminAPI(t *testing.T) {
	agent := Principal{Name: "botsy", Token: "agent-token", IsAgent: true}
	srv, _ := newTestHub(t, true, agent)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, _ := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create acme failed")
	}

	// Operator GET: no override yet -> the default (carries the workflow denylist).
	status, body := hubDo(t, srv, "GET", "/api/admin/orgs/acme/agent-policy", "", "", "sekret", nil)
	if status != http.StatusOK || body["overridden"] != false {
		t.Fatalf("default GET: %d %v", status, body)
	}
	if pol := body["policy"].(map[string]any); len(pol["denylist_paths"].([]any)) == 0 {
		t.Fatalf("default policy must carry the denylist, got %v", pol)
	}

	// Operator PUT a loosened policy (no denylist, owners editable).
	loose := receive.DefaultAgentPolicy()
	loose.DenylistPaths = nil
	loose.CanModifyOwners = true
	if status, body = hubDo(t, srv, "PUT", "/api/admin/orgs/acme/agent-policy", "", "", "sekret", loose); status != http.StatusOK {
		t.Fatalf("operator PUT: %d %v", status, body)
	}

	// GET reflects the override.
	status, body = hubDo(t, srv, "GET", "/api/admin/orgs/acme/agent-policy", "", "", "sekret", nil)
	if status != http.StatusOK || body["overridden"] != true {
		t.Fatalf("overridden GET: %d %v", status, body)
	}
	pol := body["policy"].(map[string]any)
	dl, _ := pol["denylist_paths"].([]any) // nil (JSON null) once the denylist is dropped
	if len(dl) != 0 || pol["can_modify_owners"] != true {
		t.Fatalf("override not applied: %v", pol)
	}

	// Unknown org -> 404.
	if status, _ = hubDo(t, srv, "GET", "/api/admin/orgs/nope/agent-policy", "", "", "sekret", nil); status != http.StatusNotFound {
		t.Fatalf("unknown org: %d", status)
	}
	// A store account (even an org admin) is NOT an operator.
	if status, body = hubDo(t, srv, "GET", "/api/admin/orgs/acme/agent-policy", "alice", "alicepw123", "", nil); status != http.StatusForbidden || body["Code"] != "operator_only" {
		t.Fatalf("store account on policy surface: %d %v", status, body)
	}
	// An agent is refused too (it never passes the operator gate).
	if status, _ = hubDo(t, srv, "PUT", "/api/admin/orgs/acme/agent-policy", "botsy", "agent-token", "", loose); status != http.StatusForbidden {
		t.Fatalf("agent on policy surface must be refused, got %d", status)
	}

	// Defense-in-depth: the handler refuses an agent caller explicitly.
	if !agentPolicyDenied(httptest.NewRecorder(), caller{principal: &Principal{IsAgent: true}}) {
		t.Fatal("agentPolicyDenied must refuse an agent caller")
	}
	if agentPolicyDenied(httptest.NewRecorder(), caller{principal: &Principal{}}) {
		t.Fatal("agentPolicyDenied must allow a non-agent caller")
	}
}
