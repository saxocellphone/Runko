package runkod

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postJSON(t *testing.T, srv *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func whoamiBasic(t *testing.T, srv *httptest.Server, user, pass string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/whoami", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode whoami: %v", err)
		}
	}
	return resp.StatusCode, body
}

// The sign-up flow end to end over the wire: disabled by default, invite
// code enforced when set, structured validation, then the minted
// credential works everywhere Basic works and names the caller.
func TestSignupFlow(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
		Principals: []Principal{{Name: "alice", Token: "alice-token"}},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Default off: the endpoint exists but refuses, and the login page's
	// discovery config says so.
	resp := postJSON(t, srv, "/api/signup", `{"name":"val","password":"hunter2hunter2"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("signup while disabled: want 403, got %d", resp.StatusCode)
	}
	var cfg map[string]any
	cfgResp, _ := srv.Client().Get(srv.URL + "/api/auth/config")
	json.NewDecoder(cfgResp.Body).Decode(&cfg)
	if cfg["signup_enabled"] != false {
		t.Fatalf("auth config should report signup disabled, got %v", cfg)
	}

	server.AllowSignup = true
	server.SignupCode = "join-us"

	cases := []struct {
		body string
		want int
	}{
		{`{"name":"val","password":"hunter2hunter2","code":"wrong"}`, http.StatusForbidden},
		{`{"name":"val","password":"short","code":"join-us"}`, http.StatusBadRequest},
		{`{"name":"v!","password":"hunter2hunter2","code":"join-us"}`, http.StatusBadRequest},
		{`{"name":"alice","password":"hunter2hunter2","code":"join-us"}`, http.StatusConflict}, // operator name reserved
		{`{"name":"val","password":"hunter2hunter2","code":"join-us"}`, http.StatusCreated},
		{`{"name":"val","password":"other-password","code":"join-us"}`, http.StatusConflict}, // duplicate
		{`{"name":"val","password":"hunter2hunter2","code":"join-us"}`, http.StatusCreated},  // same credential = idempotent recovery (finding #44)
		{`{"name":"val","password":"hunter2hunter2","code":"wrong"}`, http.StatusForbidden},  // recovery passes the front gates too
	}
	for _, c := range cases {
		resp := postJSON(t, srv, "/api/signup", c.body)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != c.want {
			t.Fatalf("signup %s: want %d, got %d: %s", c.body, c.want, resp.StatusCode, body)
		}
	}

	// The minted credential authenticates via Basic and names the caller;
	// wrong passwords and unknown names still fail.
	if code, body := whoamiBasic(t, srv, "val", "hunter2hunter2"); code != http.StatusOK || body["name"] != "val" || body["anonymous"] != false {
		t.Fatalf("whoami as val: code=%d body=%v", code, body)
	}
	if code, _ := whoamiBasic(t, srv, "val", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: want 401, got %d", code)
	}
	if code, _ := whoamiBasic(t, srv, "ghost", "hunter2hunter2"); code != http.StatusUnauthorized {
		t.Fatalf("unknown name: want 401, got %d", code)
	}
	// Operator principals still authenticate (config wins names).
	if code, body := whoamiBasic(t, srv, "alice", "alice-token"); code != http.StatusOK || body["name"] != "alice" {
		t.Fatalf("whoami as alice: code=%d body=%v", code, body)
	}

	// The funnel resolves the stored principal too (attribution +
	// workspace owner-only checks treat it as a real named identity).
	if p := server.Processor.principalByName("val"); p == nil || p.Name != "val" || p.IsAgent {
		t.Fatalf("funnel principalByName(val) = %+v", p)
	}
}

// The credential works on the git transport: a signed-up principal can
// clone with name:password, exactly like an operator principal.
func TestSignupCredentialWorksOverGit(t *testing.T) {
	bare := newBareRepo(t)
	if err := EnableHTTPReceivePack(bare); err != nil {
		t.Fatalf("EnableHTTPReceivePack: %v", err)
	}
	store := NewMemStore()
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
		AllowSignup: true,
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp := postJSON(t, srv, "/api/signup", `{"name":"val","password":"hunter2hunter2"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup: %d", resp.StatusCode)
	}

	repoURL := strings.Replace(srv.URL, "http://", "http://val:hunter2hunter2@", 1) + "/" + RepoMountName(bare) + "/"
	if _, err := gitfixtureRunGit(t.TempDir(), "ls-remote", repoURL); err != nil {
		t.Fatalf("ls-remote with signed-up credential: %v", err)
	}
	badURL := strings.Replace(srv.URL, "http://", "http://val:wrong@", 1) + "/" + RepoMountName(bare) + "/"
	if _, err := gitfixtureRunGit(t.TempDir(), "ls-remote", badURL); err == nil {
		t.Fatalf("ls-remote with a wrong password should fail")
	}
}

func TestCredentialHashRoundTrip(t *testing.T) {
	hash, err := hashCredential("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashCredential: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") || strings.Contains(hash, "correct horse") {
		t.Fatalf("hash should be self-describing and never contain the password: %q", hash)
	}
	if !verifyCredential("correct horse battery staple", hash) {
		t.Fatalf("verify with the right password failed")
	}
	if verifyCredential("wrong", hash) || verifyCredential("", hash) {
		t.Fatalf("verify with a wrong password succeeded")
	}
	// Two hashes of one password differ (fresh salts).
	hash2, _ := hashCredential("correct horse battery staple")
	if hash == hash2 {
		t.Fatalf("expected per-hash random salts")
	}
}
