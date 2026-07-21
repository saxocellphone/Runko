package runkod

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	runkov1 "github.com/saxocellphone/runko/runkod/proto/gen/runko/v1"
	"github.com/saxocellphone/runko/runkod/proto/gen/runko/v1/runkov1connect"
)

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// TestCallerForAuthHeaderMatrix pins the credential matrix in one place:
// every form of valid credential resolves, every near-miss is rejected.
func TestCallerForAuthHeaderMatrix(t *testing.T) {
	s := &Server{
		Token: "deploy-tok",
		Principals: []Principal{
			{Name: "victor", Token: "victor-pass"},
			{Name: "bumpbot", Token: "bot-pass", IsAgent: true},
		},
		BotLanes: []BotLane{{Name: "image-bumps", Token: "lane-tok", PathAllowlist: []string{"x"}, RequiredChecks: []string{"y"}}},
	}

	cases := []struct {
		name      string
		header    string
		ok        bool
		principal string
		lane      string
	}{
		{"bearer deploy token", "Bearer deploy-tok", true, "", ""},
		{"bearer principal", "Bearer victor-pass", true, "victor", ""},
		{"bearer lane", "Bearer lane-tok", true, "", "image-bumps"},
		{"bearer wrong", "Bearer nope", false, "", ""},
		{"basic principal pair", basicHeader("victor", "victor-pass"), true, "victor", ""},
		{"basic agent pair", basicHeader("bumpbot", "bot-pass"), true, "bumpbot", ""},
		{"basic lane pair", basicHeader("image-bumps", "lane-tok"), true, "", "image-bumps"},
		// The API must not let a name claim someone else's credential or a
		// credential claim someone else's name.
		{"basic wrong name right pass", basicHeader("mallory", "victor-pass"), false, "", ""},
		{"basic right name wrong pass", basicHeader("victor", "nope"), false, "", ""},
		// Deploy token as password works with any username (the documented
		// git-clone form) and stays anonymous.
		{"basic any user deploy token", basicHeader("whoever", "deploy-tok"), true, "", ""},
		{"basic garbage b64", "Basic %%%", false, "", ""},
		{"basic no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")), false, "", ""},
		{"empty header", "", false, "", ""},
	}
	for _, tc := range cases {
		c := s.callerForAuthHeader(tc.header)
		if c.ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, c.ok, tc.ok)
			continue
		}
		gotPrincipal := ""
		if c.principal != nil {
			gotPrincipal = c.principal.Name
		}
		gotLane := ""
		if c.lane != nil {
			gotLane = c.lane.Name
		}
		if gotPrincipal != tc.principal || gotLane != tc.lane {
			t.Errorf("%s: principal=%q lane=%q, want %q/%q", tc.name, gotPrincipal, gotLane, tc.principal, tc.lane)
		}
	}
}

// TestWhoamiAcrossCredentials drives GET /api/whoami over real HTTP:
// principal, anonymous deploy token, and rejected credentials.
func TestWhoamiAcrossCredentials(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
		Principals: []Principal{{Name: "victor", Token: "victor-pass"}},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	get := func(auth string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/whoami", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("whoami: %v", err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	if code, body := get(basicHeader("victor", "victor-pass")); code != 200 || body["name"] != "victor" || body["anonymous"] != false {
		t.Fatalf("principal whoami: %d %v", code, body)
	}
	if code, body := get("Bearer sekret"); code != 200 || body["anonymous"] != true {
		t.Fatalf("deploy-token whoami: %d %v", code, body)
	}
	if code, _ := get(basicHeader("victor", "wrong")); code != http.StatusUnauthorized {
		t.Fatalf("bad password whoami: want 401, got %d", code)
	}
	if code, _ := get(""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous whoami: want 401, got %d", code)
	}

	// The dev-server sign-in flow is cross-origin: preflight must pass.
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/whoami", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("whoami preflight: %d %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

// TestRPCBasicAuthDerivesApprover proves the web sign-in flow end to end
// at the RPC layer: a Basic-authenticated principal approves WITHOUT
// sending approved_by (the server derives it), and the recorded approval
// then satisfies the gate. Also pins that a signed-in agent principal is
// still denied (§13.5) through Basic exactly as through bearer.
func TestRPCBasicAuthDerivesApprover(t *testing.T) {
	srv, _, changeID, store := newApproveTestServerWithPrincipals(t,
		Principal{Name: "victor", Token: "victor-pass"},
		Principal{Name: "bumpbot", Token: "bot-pass", IsAgent: true},
	)
	defer srv.Close()
	ctx := context.Background()

	basicClient := func(user, pass string) runkov1connect.ChangeServiceClient {
		return runkov1connect.NewChangeServiceClient(srv.Client(), srv.URL,
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", basicHeader(user, pass))
					return next(ctx, req)
				}
			})))
	}

	_, err := basicClient("bumpbot", "bot-pass").ApproveChange(ctx, connect.NewRequest(&runkov1.ApproveChangeRequest{
		ChangeId: changeID, OwnerRef: "group:commerce-eng",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("agent approval via basic: want PermissionDenied, got %v", err)
	}

	resp, err := basicClient("victor", "victor-pass").ApproveChange(ctx, connect.NewRequest(&runkov1.ApproveChangeRequest{
		ChangeId: changeID, OwnerRef: "group:commerce-eng", // no approved_by: derived from the credential
	}))
	if err != nil {
		t.Fatalf("ApproveChange via basic: %v", err)
	}
	if got := resp.Msg.Requirements.GetOwners().GetSatisfied(); len(got) != 1 || got[0] != "group:commerce-eng" {
		t.Fatalf("satisfied owners: %v", got)
	}
	approvals, err := store.ListApprovals(ctx, changeID)
	if err != nil || len(approvals) != 1 || approvals[0].ApprovedBy != "victor" {
		t.Fatalf("approval attribution: %+v, %v", approvals, err)
	}
}

// newApproveTestServerWithPrincipals is newApproveTestServer with a
// principal registry on the Server.
func newApproveTestServerWithPrincipals(t *testing.T, principals ...Principal) (*httptest.Server, string, string, Store) {
	t.Helper()
	srv, bare, changeID, store := newApproveTestServer(t)
	srv.Close()

	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor:  newTestProcessor(bare, store),
		Token:      "sekret",
		Principals: principals,
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), bare, changeID, store
}

// TestStoredOrgAdminMayForceLand pins the migration-findings #24 fix: a
// store-backed account's org role must survive into the synthesized
// principal, so an org ADMIN can force-land (and unfreeze the mirror) in
// their own org - previously the role was fetched for the membership
// check and dropped, leaving Principal.Admin false for everyone and
// force-land operator-only.
func TestStoredOrgAdminMayForceLand(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	for name, pw := range map[string]string{"alice": "alicepw123", "bob": "bobpw1234"} {
		hash, err := hashCredential(pw)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		if err := store.CreatePrincipal(ctx, "acme", name, hash, ""); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if err := store.EnsureOrg(ctx, "acme"); err != nil {
		t.Fatalf("ensure org: %v", err)
	}
	if err := store.UpsertOrgMember(ctx, "acme", "alice", "admin"); err != nil {
		t.Fatalf("alice membership: %v", err)
	}
	if err := store.UpsertOrgMember(ctx, "acme", "bob", "member"); err != nil {
		t.Fatalf("bob membership: %v", err)
	}

	s := &Server{OrgName: "acme", Directory: store, Store: NewMemStore()}

	admin := s.callerForBasic("alice", "alicepw123")
	if !admin.ok || admin.principal == nil || !admin.principal.Admin {
		t.Fatalf("org admin should resolve to an admin principal, got %+v", admin)
	}
	if apiErr := authorizeForceLand(admin.principal, nil); apiErr != nil {
		t.Fatalf("org admin should be allowed to force-land, got %v", apiErr)
	}

	member := s.callerForBasic("bob", "bobpw1234")
	if !member.ok || member.principal == nil || member.principal.Admin {
		t.Fatalf("plain member must NOT resolve to an admin principal, got %+v", member)
	}
	if apiErr := authorizeForceLand(member.principal, nil); apiErr == nil {
		t.Fatalf("plain member must not force-land")
	}
}
