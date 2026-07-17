// Control-plane sign-in/sign-up smoke matrix (docs/smoke-plan.md,
// "Control-plane sign-in/sign-up matrix"): every user path that begins at
// the web login page or `runko auth login`, driven over the FULL hub
// handler - the same mux cmd/runkod serves, org routing included. The
// two-sided contract: happy paths complete with zero error statuses for
// every credential form on every surface it may reach (S-rows), and every
// refusal is the documented structured status/code, never a bare 500
// (R-rows). The web client maps statuses onto human messages
// (web/src/api/client.ts signIn), so a drifted status here is a
// user-facing lie.
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/saxocellphone/runko/platform/receive"
)

// signinFixture is newTestHub plus the pieces the matrix needs: a bot
// lane, prod-parity org servers (operator principals and lanes are
// server-wide config in cmd/runkod), and an injectable org-store failure
// for the interrupted-signup scenario.
type signinFixture struct {
	srv *httptest.Server
	hub *OrgHub

	mu sync.Mutex
	// failOrgStore[name] > 0 makes the next NewOrgStore for that org fail
	// (decrementing per call).
	failOrgStore map[string]int
}

func newSigninHub(t *testing.T) *signinFixture {
	t.Helper()
	bare := newBareRepo(t)
	store := NewMemStore()
	operator := Principal{Name: "op", Token: "op-pass"}
	lane := BotLane{Name: "bumps", Token: "lane-tok", PathAllowlist: []string{"images/"}, RequiredChecks: []string{"noop"}}
	def := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
		AllowSignup: true,
		Principals:  []Principal{operator},
		BotLanes:    []BotLane{lane},
		OrgName:     "defaultorg", Directory: store,
	}
	fx := &signinFixture{failOrgStore: map[string]int{}}
	hub := &OrgHub{
		Default:        def,
		DefaultOrgName: "defaultorg",
		DataDir:        t.TempDir(),
		AllowOrgCreate: true,
		Directory:      store,
		NewOrgStore: func(ctx context.Context, orgName string) (Store, error) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			if fx.failOrgStore[orgName] > 0 {
				fx.failOrgStore[orgName]--
				return nil, fmt.Errorf("injected org-store failure for %q", orgName)
			}
			return NewMemStore(), nil
		},
		NewOrgServer: func(orgName, repoDir string, orgStore Store) (*Server, error) {
			return &Server{
				RepoDir: repoDir, TrunkRef: "main", Store: orgStore,
				Processor: &Processor{RepoDir: repoDir, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: orgStore, Directory: store},
				Token:     "sekret", Principals: []Principal{operator}, BotLanes: []BotLane{lane},
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
	fx.srv, fx.hub = srv, hub
	return fx
}

// signupOrg posts the hub's org-aware signup and returns status + body.
func signupOrg(t *testing.T, srv *httptest.Server, name, password, org, mode string) (int, map[string]any) {
	t.Helper()
	return hubDo(t, srv, "POST", "/api/signup", "", "", "", map[string]string{
		"name": name, "password": password, "org": org, "org_mode": mode,
	})
}

// doRawAuth issues a request with a verbatim Authorization header value
// ("" = none) and returns status + raw body text - for the malformed-
// credential rows and the code-less plain-text error surfaces.
func doRawAuth(t *testing.T, srv *httptest.Server, method, path, authHeader string, body io.Reader) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestSigninMatrixEveryCredentialEverySurface is the S-row core: each
// credential form the login page can carry, presented to each surface it
// may legitimately reach, answers 200 with the identity bits the web
// client persists (operator gates the admin panel, admin gates org
// settings, anonymous renders the deploy-token session).
func TestSigninMatrixEveryCredentialEverySurface(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv

	// alice creates acme (stored org-admin); bob joins the default org
	// AND acme - per-org identity (migration 0017) makes that two account
	// rows under one name+password, the one-human-many-orgs shape.
	if status, body := signupOrg(t, srv, "alice", "alicepw123", "acme", "create"); status != http.StatusCreated {
		t.Fatalf("alice signup: %d %v", status, body)
	}
	if status, body := signupOrg(t, srv, "bob", "bobpw12345", "defaultorg", "join"); status != http.StatusCreated {
		t.Fatalf("bob signup: %d %v", status, body)
	}
	if status, body := signupOrg(t, srv, "bob", "bobpw12345", "acme", "join"); status != http.StatusCreated {
		t.Fatalf("bob joining acme: %d %v", status, body)
	}
	// One minted agent (rows live in the default org's store).
	status, minted := hubDo(t, srv, "POST", "/api/agents", "", "", "sekret", map[string]any{"task": "smoke"})
	if status != http.StatusCreated {
		t.Fatalf("mint agent: %d %v", status, minted)
	}
	agentName, _ := minted["name"].(string)
	agentToken, _ := minted["token"].(string)
	if agentName == "" || agentToken == "" {
		t.Fatalf("mint response missing name/token: %v", minted)
	}

	cases := []struct {
		name, path, user, pass, token      string
		wantName                           string
		operator, admin, anon, agent, lane bool
	}{
		{"operator on root", "/api/whoami", "op", "op-pass", "", "op", true, false, false, false, false},
		{"operator on the default org mount", "/o/defaultorg/api/whoami", "op", "op-pass", "", "op", true, false, false, false, false},
		{"operator on a member org (membership-exempt)", "/o/acme/api/whoami", "op", "op-pass", "", "op", true, false, false, false, false},
		{"deploy token bearer on root", "/api/whoami", "", "", "sekret", "", true, false, true, false, false},
		{"deploy token as basic password, any username, org mount", "/o/acme/api/whoami", "whoever", "sekret", "", "", true, false, true, false, false},
		{"stored org creator on her org", "/o/acme/api/whoami", "alice", "alicepw123", "", "alice", false, true, false, false, false},
		{"stored joiner via root", "/api/whoami", "bob", "bobpw12345", "", "bob", false, false, false, false, false},
		{"stored joiner via /o/ mount of the same org", "/o/defaultorg/api/whoami", "bob", "bobpw12345", "", "bob", false, false, false, false, false},
		{"two-org account on its second org", "/o/acme/api/whoami", "bob", "bobpw12345", "", "bob", false, false, false, false, false},
		{"bot lane on root", "/api/whoami", "bumps", "lane-tok", "", "bumps", false, false, false, false, true},
		{"bot lane on an org mount", "/o/acme/api/whoami", "bumps", "lane-tok", "", "bumps", false, false, false, false, true},
		{"agent basic in its org", "/api/whoami", agentName, agentToken, "", agentName, false, false, false, true, false},
		{"agent bearer in its org", "/api/whoami", "", "", agentToken, agentName, false, false, false, true, false},
	}
	for _, tc := range cases {
		status, body := hubDo(t, srv, "GET", tc.path, tc.user, tc.pass, tc.token, nil)
		if status != http.StatusOK {
			t.Errorf("%s: sign-in answered %d %v (want 200)", tc.name, status, body)
			continue
		}
		gotName, _ := body["name"].(string)
		if gotName != tc.wantName {
			t.Errorf("%s: name = %q, want %q", tc.name, gotName, tc.wantName)
		}
		// Absent keys read as false - the lane/anonymous shapes omit some.
		for field, want := range map[string]bool{
			"operator": tc.operator, "admin": tc.admin, "anonymous": tc.anon,
			"is_agent": tc.agent, "lane": tc.lane,
		} {
			if got := body[field] == true; got != want {
				t.Errorf("%s: %s = %v, want %v (body %v)", tc.name, field, got, want, body)
			}
		}
	}

	// The org selector after sign-in: exactly the memberships, with roles.
	status, body := hubDo(t, srv, "GET", "/api/orgs", "bob", "bobpw12345", "", nil)
	if status != http.StatusOK {
		t.Fatalf("bob org list: %d", status)
	}
	roles := map[string]string{}
	for _, o := range body["orgs"].([]any) {
		row := o.(map[string]any)
		roles[row["name"].(string)] = row["role"].(string)
	}
	if len(roles) != 2 || roles["defaultorg"] != "member" || roles["acme"] != "member" {
		t.Fatalf("bob should hold two member roles, got %v", roles)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs", "alice", "alicepw123", "", nil)
	orgs := body["orgs"].([]any)
	if status != http.StatusOK || len(orgs) != 1 || orgs[0].(map[string]any)["role"] != "admin" {
		t.Fatalf("alice should hold exactly one admin role, got %d %v", status, body)
	}
	// An operator sees the whole estate.
	status, body = hubDo(t, srv, "GET", "/api/orgs", "op", "op-pass", "", nil)
	if status != http.StatusOK || len(body["orgs"].([]any)) != 2 {
		t.Fatalf("operator org list: %d %v", status, body)
	}
}

// TestSignupSigninWebSequence replays the web client's exact call
// sequence (client.ts fetchAuthConfig -> signUp -> signIn -> fetchOrgs)
// for both org modes, asserting zero error statuses at every step and
// that the returned org info is usable verbatim.
func TestSignupSigninWebSequence(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv

	// 1. Discovery, unauthenticated.
	status, cfg := hubDo(t, srv, "GET", "/api/auth/config", "", "", "", nil)
	if status != http.StatusOK || cfg["signup_enabled"] != true || cfg["org_create_enabled"] != true {
		t.Fatalf("auth config: %d %v", status, cfg)
	}

	// 2. The browser's CORS preflights - unauthenticated OPTIONS must be
	// 204 with a wildcard origin on every route the login page touches.
	for _, pre := range []struct{ path, method string }{
		{"/api/auth/config", http.MethodGet},
		{"/api/signup", http.MethodPost},
		{"/api/orgs", http.MethodGet},
		{"/o/defaultorg/api/whoami", http.MethodGet},
	} {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+pre.path, nil)
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Access-Control-Request-Method", pre.method)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("preflight %s: %v", pre.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("preflight %s: %d origin=%q", pre.path, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}
	}

	// 3. Create-mode signup; the response's org info drives the sign-in.
	status, body := signupOrg(t, srv, "carol", "carolpw123", "carols-org", "create")
	if status != http.StatusCreated {
		t.Fatalf("carol signup: %d %v", status, body)
	}
	org := body["org"].(map[string]any)
	if org["name"] != "carols-org" || org["role"] != "admin" ||
		org["api_base"] != "/o/carols-org" || org["git_url"] != "/o/carols-org/carols-org.git" {
		t.Fatalf("signup org info: %v", org)
	}
	// 4. The signIn round-trip the client performs next.
	status, who := hubDo(t, srv, "GET", org["api_base"].(string)+"/api/whoami", "carol", "carolpw123", "", nil)
	if status != http.StatusOK || who["name"] != "carol" || who["admin"] != true {
		t.Fatalf("carol whoami after signup: %d %v", status, who)
	}
	// 5. The org selector.
	status, body = hubDo(t, srv, "GET", "/api/orgs", "carol", "carolpw123", "", nil)
	if status != http.StatusOK || len(body["orgs"].([]any)) != 1 {
		t.Fatalf("carol org list: %d %v", status, body)
	}

	// 6. Join-mode signup into the default org: the advertised info is the
	// root form (api_base ""), and BOTH mounts serve the session.
	status, body = signupOrg(t, srv, "dan", "danpw12345", "defaultorg", "join")
	if status != http.StatusCreated {
		t.Fatalf("dan signup: %d %v", status, body)
	}
	org = body["org"].(map[string]any)
	if org["role"] != "member" || org["api_base"] != "" || org["default"] != true {
		t.Fatalf("join-default org info: %v", org)
	}
	for _, base := range []string{"", "/o/defaultorg"} {
		if status, who := hubDo(t, srv, "GET", base+"/api/whoami", "dan", "danpw12345", "", nil); status != http.StatusOK || who["name"] != "dan" {
			t.Fatalf("dan whoami via %q: %d %v", base, status, who)
		}
	}

	// 7. Opaque-password boundaries (S9): a colon inside the password
	// survives the Basic form (split on the FIRST colon), and the 8-char
	// minimum is inclusive.
	if status, body := signupOrg(t, srv, "colin", "pa:ss-word9", "defaultorg", "join"); status != http.StatusCreated {
		t.Fatalf("colon-password signup: %d %v", status, body)
	}
	if status, who := hubDo(t, srv, "GET", "/api/whoami", "colin", "pa:ss-word9", "", nil); status != http.StatusOK || who["name"] != "colin" {
		t.Fatalf("colon-password sign-in: %d %v", status, who)
	}
	if status, _ := signupOrg(t, srv, "eight", "exactly8!", "defaultorg", "join"); status != http.StatusCreated {
		t.Fatalf("8-char password must be accepted: %d", status)
	}
}

// TestSigninEdgeRefusalsAreStructured is the R-row sweep: every
// near-miss and policy refusal on the sign-in/sign-up surfaces answers
// the documented status (and clierr code where the surface speaks JSON).
func TestSigninEdgeRefusalsAreStructured(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv

	if status, _ := signupOrg(t, srv, "alice", "alicepw123", "acme", "create"); status != http.StatusCreated {
		t.Fatalf("alice signup failed")
	}
	status, minted := hubDo(t, srv, "POST", "/api/agents", "", "", "sekret", map[string]any{"task": "edge"})
	if status != http.StatusCreated {
		t.Fatalf("mint agent: %d", status)
	}
	agentName := minted["name"].(string)
	agentToken := minted["token"].(string)

	rows := []struct {
		name              string
		method, path      string
		user, pass, token string
		body              any
		wantStatus        int
		wantCode          string // "" = plain-text surface, status is the contract
	}{
		// R1: credential near-misses are 401 - wrong password, someone
		// else's password, and the wrong-case name (R12: names are
		// case-sensitive end to end).
		{"wrong password", "GET", "/o/acme/api/whoami", "alice", "wrong-password", "", nil, http.StatusUnauthorized, ""},
		{"right password, wrong name", "GET", "/o/acme/api/whoami", "mallory", "alicepw123", "", nil, http.StatusUnauthorized, ""},
		{"wrong-case account name", "GET", "/o/acme/api/whoami", "ALICE", "alicepw123", "", nil, http.StatusUnauthorized, ""},
		// R2: valid account, wrong org - 403, never 401, on both mounts.
		{"wrong org via /o/", "GET", "/o/defaultorg/api/whoami", "alice", "alicepw123", "", nil, http.StatusForbidden, ""},
		{"wrong org via root", "GET", "/api/whoami", "alice", "alicepw123", "", nil, http.StatusForbidden, ""},
		// R3: unknown org 404s with the structured body.
		{"unknown org", "GET", "/o/ghost/api/whoami", "alice", "alicepw123", "", nil, http.StatusNotFound, "unknown_org"},
		// R8: agents and lanes may not touch the hub's org APIs.
		{"agent lists orgs", "GET", "/api/orgs", agentName, agentToken, "", nil, http.StatusForbidden, "agent_denied"},
		{"agent creates an org", "POST", "/api/orgs", agentName, agentToken, "", map[string]string{"name": "agentorg"}, http.StatusForbidden, "agent_denied"},
		{"lane lists orgs", "GET", "/api/orgs", "bumps", "lane-tok", "", nil, http.StatusForbidden, "lane_denied"},
		// R9: agent rows are org-scoped; a foreign org answers 401.
		{"agent on a foreign org", "GET", "/o/acme/api/whoami", agentName, agentToken, "", nil, http.StatusUnauthorized, ""},
		// R7: signup org-half refusals over the hub.
		{"reserved org name", "POST", "/api/signup", "", "", "", map[string]string{"name": "nadia", "password": "nadiapw123", "org": "admin", "org_mode": "create"}, http.StatusBadRequest, "invalid_org_name"},
		// R6: a signup may not take a bot lane's name (operator-principal
		// and stored collisions are pinned in signup_test.go).
		{"lane name collision", "POST", "/api/signup", "", "", "", map[string]string{"name": "bumps", "password": "bumpspw123", "org": "defaultorg", "org_mode": "join"}, http.StatusConflict, "name_taken"},
		// R10: member management refusals.
		{"non-admin adds a member", "POST", "/api/orgs/acme/members", "bumps", "lane-tok", "", map[string]string{"name": "alice"}, http.StatusForbidden, "lane_denied"},
		{"unknown account as member", "POST", "/api/orgs/acme/members", "alice", "alicepw123", "", map[string]string{"name": "nobody"}, http.StatusNotFound, "unknown_principal"},
		{"invalid role", "POST", "/api/orgs/acme/members", "alice", "alicepw123", "", map[string]string{"name": "alice", "role": "emperor"}, http.StatusBadRequest, "invalid_role"},
		// R11: the deployment admin surface is operator-only even for an
		// org admin.
		{"org admin on the admin estate", "GET", "/api/admin/orgs", "alice", "alicepw123", "", nil, http.StatusForbidden, "operator_only"},
	}
	for _, tc := range rows {
		status, body := hubDo(t, srv, tc.method, tc.path, tc.user, tc.pass, tc.token, tc.body)
		if status != tc.wantStatus {
			t.Errorf("%s: status %d, want %d (body %v)", tc.name, status, tc.wantStatus, body)
			continue
		}
		if tc.wantCode != "" && body["Code"] != tc.wantCode {
			t.Errorf("%s: code %v, want %q", tc.name, body["Code"], tc.wantCode)
		}
	}

	// R1 continued: malformed Authorization headers on an org mount.
	for _, h := range []struct{ name, header string }{
		{"garbage base64", "Basic %%%"},
		{"basic without a colon", "Basic bm9jb2xvbg=="}, // "nocolon"
		{"empty password", basicHeader("alice", "")},
		{"wrong bearer", "Bearer nope"},
	} {
		if status, _ := doRawAuth(t, srv, "GET", "/o/acme/api/whoami", h.header, nil); status != http.StatusUnauthorized {
			t.Errorf("%s: want 401, got %d", h.name, status)
		}
	}

	// R4: the archive lifecycle closes and reopens the sign-in surface.
	if status, _ := signupOrg(t, srv, "erin", "erinpw1234", "arch", "create"); status != http.StatusCreated {
		t.Fatalf("erin signup failed")
	}
	if status, _ := hubDo(t, srv, "POST", "/api/orgs/arch/archive", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("archive failed")
	}
	status, body := hubDo(t, srv, "GET", "/o/arch/api/whoami", "erin", "erinpw1234", "", nil)
	if status != http.StatusGone || body["Code"] != "org_archived" {
		t.Fatalf("archived org sign-in: %d %v, want 410 org_archived", status, body)
	}
	if status, _ := hubDo(t, srv, "POST", "/api/orgs/arch/unarchive", "", "", "sekret", nil); status != http.StatusOK {
		t.Fatalf("unarchive failed")
	}
	if status, _ := hubDo(t, srv, "GET", "/o/arch/api/whoami", "erin", "erinpw1234", "", nil); status != http.StatusOK {
		t.Fatalf("unarchive must restore sign-in without a restart: %d", status)
	}
}

// TestSignupInterruptedOrgCreate pins R13: when org assembly fails AFTER
// the account row is created, the 500 names the half-done state honestly,
// a retry is another honest 500 (never a name_taken dead end - finding
// #44), and the account stays usable through both recovery paths: an
// admin adding the membership, or the account's own re-signup once the
// infrastructure recovers.
func TestSignupInterruptedOrgCreate(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv
	fx.mu.Lock()
	fx.failOrgStore["doomed"] = 1000 // every attempt fails
	fx.mu.Unlock()

	req, _ := json.Marshal(map[string]string{
		"name": "zed", "password": "zedpw12345", "org": "doomed", "org_mode": "create",
	})
	status, raw := doRawAuth(t, srv, "POST", "/api/signup", "", strings.NewReader(string(req)))
	if status != http.StatusInternalServerError || !strings.Contains(raw, `account "zed" was created, but the org was not created`) {
		t.Fatalf("interrupted signup must fail honestly: %d %q", status, raw)
	}

	// The retry recovers the account half (same name+password) and fails
	// only on the still-broken org half - with the wording flipped to
	// "already exists" so the user learns their credential is real.
	status, raw = doRawAuth(t, srv, "POST", "/api/signup", "", strings.NewReader(string(req)))
	if status != http.StatusInternalServerError || !strings.Contains(raw, `account "zed" already exists, but the org was not created`) {
		t.Fatalf("retry after interruption: %d %q", status, raw)
	}
	// A retry under the WRONG password is not a recovery - the name_taken
	// contract holds (no oracle beyond what sign-in answers).
	status, body := signupOrg(t, srv, "zed", "not-zeds-password", "doomed", "create")
	if status != http.StatusConflict || body["Code"] != "name_taken" {
		t.Fatalf("wrong-password retry: %d %v", status, body)
	}
	// Until an org half succeeds the credential reaches nothing: member
	// of no org, 403 on org surfaces, an empty selector.
	if status, _ := hubDo(t, srv, "GET", "/o/defaultorg/api/whoami", "zed", "zedpw12345", "", nil); status != http.StatusForbidden {
		t.Fatalf("orgless account on an org: want 403, got %d", status)
	}
	status, body = hubDo(t, srv, "GET", "/api/orgs", "zed", "zedpw12345", "", nil)
	if status != http.StatusOK || len(body["orgs"].([]any)) != 0 {
		t.Fatalf("orgless account org list: %d %v", status, body)
	}

	// Per-org identity: an operator can NOT paste the account into another
	// org (no acme... no defaultorg account rows exist for zed) - recovery
	// is self-service: zed signs up into a working org under the same
	// name, a fresh per-org account.
	status, body = hubDo(t, srv, "POST", "/api/orgs/defaultorg/members", "", "", "sekret", map[string]string{"name": "zed"})
	if status != http.StatusNotFound || body["Code"] != "unknown_principal" {
		t.Fatalf("cross-org member add must refuse: %d %v", status, body)
	}
	if status, _ := signupOrg(t, srv, "zed", "zedpw12345", "defaultorg", "join"); status != http.StatusCreated {
		t.Fatalf("self-service join after interruption: %d", status)
	}
	if status, who := hubDo(t, srv, "GET", "/o/defaultorg/api/whoami", "zed", "zedpw12345", "", nil); status != http.StatusOK || who["name"] != "zed" {
		t.Fatalf("recovered account sign-in: %d %v", status, who)
	}
}

// TestSignupRecoveryAfterInterruptedCreate is finding #44's happy ending:
// the org store fails once, the user retries the exact same signup, and
// the second attempt completes end to end - org created, admin role,
// sign-in clean. No admin involved.
func TestSignupRecoveryAfterInterruptedCreate(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv
	fx.mu.Lock()
	fx.failOrgStore["phoenix"] = 1 // fail once, then recover
	fx.mu.Unlock()

	status, body := signupOrg(t, srv, "pat", "patpw12345", "phoenix", "create")
	if status != http.StatusInternalServerError {
		t.Fatalf("first attempt should hit the injected failure: %d %v", status, body)
	}
	status, body = signupOrg(t, srv, "pat", "patpw12345", "phoenix", "create")
	if status != http.StatusCreated {
		t.Fatalf("retry must recover: %d %v", status, body)
	}
	if org := body["org"].(map[string]any); org["name"] != "phoenix" || org["role"] != "admin" {
		t.Fatalf("recovered signup org info: %v", body)
	}
	if status, who := hubDo(t, srv, "GET", "/o/phoenix/api/whoami", "pat", "patpw12345", "", nil); status != http.StatusOK || who["admin"] != true {
		t.Fatalf("recovered creator sign-in: %d %v", status, who)
	}
}

// TestSignupRejoinPreservesRole: re-presenting a valid credential to the
// signup endpoint is idempotent, and a re-join never demotes - an org's
// admin who re-signups into their own org keeps the admin role.
func TestSignupRejoinPreservesRole(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv
	if status, _ := signupOrg(t, srv, "alice", "alicepw123", "acme", "create"); status != http.StatusCreated {
		t.Fatalf("alice signup failed")
	}
	status, body := signupOrg(t, srv, "alice", "alicepw123", "acme", "join")
	if status != http.StatusCreated {
		t.Fatalf("re-join: %d %v", status, body)
	}
	if org := body["org"].(map[string]any); org["role"] != "admin" {
		t.Fatalf("re-join must preserve the admin role, got %v", org)
	}
	if status, body := signupOrg(t, srv, "alice", "wrong-password", "acme", "join"); status != http.StatusConflict || body["Code"] != "name_taken" {
		t.Fatalf("re-join under a wrong password: %d %v", status, body)
	}
}

// TestSameNameDifferentOrgs is the per-org identity headline (migration
// 0017, user direction 2026-07-13): two orgs each have their own "casey"
// - different humans, different passwords, no interaction. Each signs
// into their own org; each is deniedOrg (403, valid-elsewhere) on the
// other's; a password wrong for BOTH stays 401; selectors never leak the
// other's orgs; an existing account creating a second org via POST
// /api/orgs gets a cloned account there and can sign straight in.
func TestSameNameDifferentOrgs(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv

	if status, _ := signupOrg(t, srv, "casey", "first-casey-pw", "org-a", "create"); status != http.StatusCreated {
		t.Fatalf("casey@org-a signup failed")
	}
	if status, body := signupOrg(t, srv, "casey", "other-casey-pw", "org-b", "create"); status != http.StatusCreated {
		t.Fatalf("the same name in a DIFFERENT org must sign up cleanly: %d %v", status, body)
	}
	// And within one org the name stays taken.
	if status, body := signupOrg(t, srv, "casey", "third-casey-pw", "org-a", "join"); status != http.StatusConflict || body["Code"] != "name_taken" {
		t.Fatalf("same name within one org: %d %v", status, body)
	}

	whoami := func(org, pass string) (int, map[string]any) {
		return hubDo(t, srv, "GET", "/o/"+org+"/api/whoami", "casey", pass, "", nil)
	}
	if status, who := whoami("org-a", "first-casey-pw"); status != http.StatusOK || who["admin"] != true {
		t.Fatalf("casey@org-a sign-in: %d %v", status, who)
	}
	if status, who := whoami("org-b", "other-casey-pw"); status != http.StatusOK || who["admin"] != true {
		t.Fatalf("casey@org-b sign-in: %d %v", status, who)
	}
	// Each casey on the OTHER org: 403 (the credential is real, the org
	// is not theirs) - never a 401, never a sign-in.
	if status, _ := whoami("org-b", "first-casey-pw"); status != http.StatusForbidden {
		t.Fatalf("casey@org-a on org-b: want 403, got %d", status)
	}
	if status, _ := whoami("org-a", "other-casey-pw"); status != http.StatusForbidden {
		t.Fatalf("casey@org-b on org-a: want 403, got %d", status)
	}
	// Wrong for both rows: 401.
	if status, _ := whoami("org-a", "not-anyones-pw"); status != http.StatusUnauthorized {
		t.Fatalf("wrong-for-everyone password: want 401, got %d", status)
	}

	// Selectors are per-credential: each casey sees only their own org.
	for _, tc := range []struct{ pass, want string }{
		{"first-casey-pw", "org-a"},
		{"other-casey-pw", "org-b"},
	} {
		status, body := hubDo(t, srv, "GET", "/api/orgs", "casey", tc.pass, "", nil)
		if status != http.StatusOK {
			t.Fatalf("selector (%s): %d", tc.want, status)
		}
		orgs := body["orgs"].([]any)
		if len(orgs) != 1 || orgs[0].(map[string]any)["name"] != tc.want {
			t.Fatalf("selector (%s) must list exactly that org, got %v", tc.want, orgs)
		}
	}

	// A stored account creating a second org via POST /api/orgs gets its
	// account cloned into the new org - sign-in works immediately.
	if status, body := hubDo(t, srv, "POST", "/api/orgs", "casey", "first-casey-pw", "", map[string]string{"name": "org-c"}); status != http.StatusCreated {
		t.Fatalf("casey@org-a creates org-c: %d %v", status, body)
	}
	if status, who := whoami("org-c", "first-casey-pw"); status != http.StatusOK || who["admin"] != true {
		t.Fatalf("creator sign-in on the new org: %d %v", status, who)
	}
	// ... and org-b's casey still can't reach it.
	if status, _ := whoami("org-c", "other-casey-pw"); status != http.StatusForbidden {
		t.Fatalf("the other casey on org-c: want 403, got %d", status)
	}
}

// TestSameNameSamePasswordAcrossOrgs is the prod-observed edge
// (2026-07-16): one human reuses the SAME name+password combo in several
// orgs. Per-org accounts make these distinct account rows with
// coincidentally-equal credentials, so every surface must resolve the row
// for the org IN THE URL - never "any row this credential verifies
// against". The distinguishable bit is the org role: casey is org-x's
// admin but org-y's member, so a leaked resolution shows up as the wrong
// admin flag even though the name matches.
func TestSameNameSamePasswordAcrossOrgs(t *testing.T) {
	fx := newSigninHub(t)
	srv := fx.srv
	const pw = "one-pw-everywhere"

	// casey founds org-x (admin), then joins org-y (member) - same combo.
	if status, _ := signupOrg(t, srv, "casey", pw, "org-x", "create"); status != http.StatusCreated {
		t.Fatalf("casey@org-x signup failed")
	}
	if status, _ := signupOrg(t, srv, "riley", "riley-pw", "org-y", "create"); status != http.StatusCreated {
		t.Fatalf("riley@org-y signup failed")
	}
	if status, body := signupOrg(t, srv, "casey", pw, "org-y", "join"); status != http.StatusCreated {
		t.Fatalf("casey@org-y join with the same combo must work: %d %v", status, body)
	}
	// A third org casey has no account in.
	if status, _ := signupOrg(t, srv, "quinn", "quinn-pw", "org-z", "create"); status != http.StatusCreated {
		t.Fatalf("quinn@org-z signup failed")
	}

	whoami := func(org string) (int, map[string]any) {
		return hubDo(t, srv, "GET", "/o/"+org+"/api/whoami", "casey", pw, "", nil)
	}
	// The org in the URL picks the account row: same combo, different
	// role per org - admin must follow the ORG, not the first row the
	// password happens to verify against.
	if status, who := whoami("org-x"); status != http.StatusOK || who["name"] != "casey" || who["admin"] != true {
		t.Fatalf("casey on org-x must be org-x's admin casey: %d %v", status, who)
	}
	if status, who := whoami("org-y"); status != http.StatusOK || who["name"] != "casey" || who["admin"] == true {
		t.Fatalf("casey on org-y must be org-y's MEMBER casey (admin=false): %d %v", status, who)
	}
	// An org where no casey exists: the combo verifying elsewhere makes
	// this "wrong org" (403), never a sign-in and never "wrong password".
	if status, _ := whoami("org-z"); status != http.StatusForbidden {
		t.Fatalf("casey on org-z: want 403 not_org_member, got %d", status)
	}

	// The hub selector must list BOTH orgs: the ambiguous credential
	// proves both same-named accounts, and dropping either strands that
	// org's session (this is what the web org switcher renders).
	status, body := hubDo(t, srv, "GET", "/api/orgs", "casey", pw, "", nil)
	if status != http.StatusOK {
		t.Fatalf("selector: %d", status)
	}
	roles := map[string]string{}
	for _, o := range body["orgs"].([]any) {
		row := o.(map[string]any)
		roles[row["name"].(string)] = row["role"].(string)
	}
	if roles["org-x"] != "admin" || roles["org-y"] != "member" || len(roles) != 2 {
		t.Fatalf("selector must list exactly org-x(admin)+org-y(member), got %v", roles)
	}

	// Git transport, same rule (requireGitAuth rides callerForBasic): the
	// combo reaches its own orgs' repos and answers 403 - not 401, not a
	// cross-org grant - where casey has no account.
	gitRefs := func(org string) int {
		status, _ := hubDo(t, srv, "GET", "/o/"+org+"/repo.git/info/refs?service=git-upload-pack", "casey", pw, "", nil)
		return status
	}
	if got := gitRefs("org-x"); got != http.StatusOK {
		t.Fatalf("git on org-x: want 200, got %d", got)
	}
	if got := gitRefs("org-y"); got != http.StatusOK {
		t.Fatalf("git on org-y: want 200, got %d", got)
	}
	if got := gitRefs("org-z"); got != http.StatusForbidden {
		t.Fatalf("git on org-z: want 403, got %d", got)
	}

	// Signup keeps its finding-#44 idempotence per org: re-signup with
	// the matching combo is a benign rejoin preserving the ORG'S OWN
	// role (org-x: admin), and a wrong password stays name_taken - the
	// same combo existing in org-y must not make org-x's row "match".
	if status, body := signupOrg(t, srv, "casey", pw, "org-x", "join"); status != http.StatusCreated || body["org"].(map[string]any)["role"] != "admin" {
		t.Fatalf("rejoin of casey@org-x must preserve admin: %d %v", status, body)
	}
	if status, body := signupOrg(t, srv, "casey", pw, "org-y", "join"); status != http.StatusCreated || body["org"].(map[string]any)["role"] != "member" {
		t.Fatalf("rejoin of casey@org-y must stay member: %d %v", status, body)
	}
	if status, body := signupOrg(t, srv, "casey", "not-caseys-pw", "org-x", "join"); status != http.StatusConflict || body["Code"] != "name_taken" {
		t.Fatalf("re-signup under a wrong password: %d %v", status, body)
	}
}
