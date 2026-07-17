package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/checks"
)

func TestAuthHeaderValueAndGitUserPass(t *testing.T) {
	if got := authHeaderValue("sekret"); got != "Bearer sekret" {
		t.Fatalf("raw token: %q", got)
	}
	basic := Credential{Name: "val", Secret: "pw"}.AuthHeader()
	if got := authHeaderValue(basic); got != basic {
		t.Fatalf("pre-rendered header must pass through, got %q", got)
	}
	if u, p := gitUserPass(basic); u != "val" || p != "pw" {
		t.Fatalf("gitUserPass(basic) = %q %q", u, p)
	}
	if u, p := gitUserPass("sekret"); u != "runko" || p != "sekret" {
		t.Fatalf("gitUserPass(token) = %q %q", u, p)
	}
}

// whoamiStub answers /api/whoami accepting exactly one credential.
func whoamiStub(t *testing.T, wantAuth, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/whoami" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"name": name, "anonymous": name == ""})
	}))
}

func TestAuthLoginStoresValidatedCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // os.UserConfigDir sandbox
	want := Credential{Name: "val", Secret: "hunter2hunter2"}.AuthHeader()
	srv := whoamiStub(t, want, "val")
	defer srv.Close()

	// A wrong password is rejected and stores NOTHING.
	_, err := AuthLogin(context.Background(), srv.Client(), srv.URL, "val", "wrong",
		bufio.NewReader(strings.NewReader("")), os.Stdout)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "bad_credential" {
		t.Fatalf("wrong password: want bad_credential, got %v", err)
	}
	if _, found, _ := loadCredential(); found {
		t.Fatalf("a rejected login must not store a credential")
	}

	// The secret can come from the stdin prompt.
	if _, err := AuthLogin(context.Background(), srv.Client(), srv.URL, "val", "",
		bufio.NewReader(strings.NewReader("hunter2hunter2\n")), os.Stdout); err != nil {
		t.Fatalf("AuthLogin: %v", err)
	}
	cred, found, err := loadCredential()
	if err != nil || !found || cred.Name != "val" || cred.Secret != "hunter2hunter2" || cred.URL != srv.URL {
		t.Fatalf("stored credential: %+v found=%v err=%v", cred, found, err)
	}
	path, _ := credentialPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file must be 0600, got %v (%v)", info.Mode(), err)
	}

	// resolveCredential: stored login is the fallback; explicit flags win.
	got, err := resolveCredential("", "")
	if err != nil || got.AuthHeader() != want {
		t.Fatalf("resolve from store: %+v err=%v", got, err)
	}
	flagged, err := resolveCredential("http://other", "raw-token")
	if err != nil || flagged.URL != "http://other" || flagged.AuthHeader() != "Bearer raw-token" {
		t.Fatalf("flags must win: %+v err=%v", flagged, err)
	}
}

// Sign-in failures are structured for every shape the control plane
// answers (the multi-org difficulties: a valid account against the wrong
// org, a typo'd org, an archived one) - never a bare "whoami: HTTP NNN".
func TestAuthLoginFailuresAreStructured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/o/mine/"):
			// Valid credential, wrong org: rpcMiddleware's plain-text 403.
			http.Error(w, "forbidden: not a member of org mine", http.StatusForbidden)
		case strings.HasPrefix(r.URL.Path, "/o/ghost/"):
			// The org router's structured 404.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(clierr.Error{
				Code: "unknown_org", Field: "org",
				Message:    `no org named "ghost" on this control plane`,
				Suggestion: "GET /api/orgs lists yours",
			})
		case strings.HasPrefix(r.URL.Path, "/o/attic/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(clierr.Error{
				Code: "org_archived", Field: "org",
				Message: `org "attic" is archived`,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	for _, tc := range []struct{ org, wantCode string }{
		{"mine", "not_org_member"},
		{"ghost", "unknown_org"},
		{"attic", "org_archived"},
	} {
		_, err := AuthLogin(context.Background(), srv.Client(), srv.URL+"/o/"+tc.org, "val", "pw123456789",
			bufio.NewReader(strings.NewReader("")), os.Stdout)
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != tc.wantCode {
			t.Fatalf("org %s: want %s, got %v", tc.org, tc.wantCode, err)
		}
		if _, found, _ := loadCredential(); found {
			t.Fatalf("org %s: a refused login must not store a credential", tc.org)
		}
	}
}

func TestResolveCredentialWithoutLoginIsStructured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := resolveCredential("", "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_logged_in" {
		t.Fatalf("want not_logged_in, got %v", err)
	}
}

// change create commits WIP as one Change with a stable Change-Id, and
// change requirements defaults to HEAD's trailer.
func TestChangeCreateAndHeadChangeID(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")

	// Empty tree: structured refusal.
	_, err := CreateChange(dir, "first change", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "nothing_to_commit" {
		t.Fatalf("want nothing_to_commit, got %v", err)
	}

	writeFile(t, dir, "svc/main.go", "package main\n")
	id, err := CreateChange(dir, "first change", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if !strings.HasPrefix(id, "I") || len(id) != 41 {
		t.Fatalf("unexpected change id %q", id)
	}
	msg := mustGit(t, dir, "log", "-1", "--format=%B")
	if !strings.Contains(msg, "Change-Id: "+id) {
		t.Fatalf("commit message must carry the Change-Id trailer, got %q", msg)
	}
	headID, err := headChangeID(dir)
	if err != nil || headID != id {
		t.Fatalf("headChangeID: %q err=%v", headID, err)
	}

	// A second create stacks: a new commit, a NEW Change identity.
	writeFile(t, dir, "svc/handler.go", "package main // handler\n")
	id2, err := CreateChange(dir, "second change", false)
	if err != nil {
		t.Fatalf("second CreateChange: %v", err)
	}
	if id2 == id {
		t.Fatalf("stacked change must get its own Change-Id")
	}
}

func TestChangeRequirementsFetchAndPrintShape(t *testing.T) {
	reqs := checks.MergeRequirements{
		ChangeID:          "I123",
		RequiredOwners:    []string{"group:commerce"},
		OutstandingOwners: []string{"group:commerce"},
		RequiredChecks:    []string{"unit"},
		PendingChecks:     []string{"unit"},
		Blockers:          []string{"owner approval outstanding: group:commerce"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/changes/I123/merge-requirements" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(reqs)
	}))
	defer srv.Close()

	got, err := ChangeRequirements(context.Background(), srv.Client(),
		Credential{URL: srv.URL, Secret: "sekret"}, "I123")
	if err != nil {
		t.Fatalf("ChangeRequirements: %v", err)
	}
	if got.Mergeable || len(got.RequiredOwners) != 1 || len(got.PendingChecks) != 1 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	_, err = ChangeRequirements(context.Background(), srv.Client(),
		Credential{URL: srv.URL, Secret: "sekret"}, "I999")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unknown_change" {
		t.Fatalf("want unknown_change for a 404, got %v", err)
	}
}

// signupStub answers the hub's POST /api/signup (201 + org info) and the
// new org's whoami; it records the last signup body and workspace-create
// owner it saw, for assertions.
func signupStub(t *testing.T) (*httptest.Server, *map[string]string) {
	t.Helper()
	lastSignup := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/signup":
			json.NewDecoder(r.Body).Decode(&lastSignup)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"name": lastSignup["name"],
				"org":  map[string]any{"name": lastSignup["org"], "role": "admin", "api_base": "/o/" + lastSignup["org"], "git_url": "/o/" + lastSignup["org"] + "/" + lastSignup["org"] + ".git"},
			})
		case "/o/" + lastSignup["org"] + "/api/whoami":
			json.NewEncoder(w).Encode(map[string]any{"name": lastSignup["name"]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &lastSignup
}

// TestAuthSignupRegistersAndStoresOrgScopedCredential: §6.10's first
// contact - one signup registers the account AND stores the credential
// already pointed at the created org's mount, so signup is login.
func TestAuthSignupRegistersAndStoresOrgScopedCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, lastSignup := signupStub(t)

	// Password can come from the (hidden) prompt, like auth login.
	if _, err := AuthSignup(context.Background(), srv.Client(), srv.URL,
		"val", "", "acme", "create", "", bufio.NewReader(strings.NewReader("hunter2hunter2\n")), os.Stdout); err != nil {
		t.Fatalf("AuthSignup: %v", err)
	}
	if (*lastSignup)["org_mode"] != "create" || (*lastSignup)["password"] != "hunter2hunter2" {
		t.Fatalf("signup body: %v", *lastSignup)
	}
	cred, found, err := loadCredential()
	if err != nil || !found {
		t.Fatalf("expected a stored credential, found=%v err=%v", found, err)
	}
	if cred.URL != srv.URL+"/o/acme" || cred.Name != "val" || cred.Secret != "hunter2hunter2" {
		t.Fatalf("stored credential must point at the new org's mount: %+v", cred)
	}
}

// TestAuthSignupRefusalIsStructuredAndStoresNothing: the server's §6.5
// refusal shapes (here org_exists) pass through verbatim, and a failed
// signup never half-stores a credential.
func TestAuthSignupRefusalIsStructuredAndStoresNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(clierr.Error{Code: "org_exists", Field: "org", Message: "an org named \"acme\" already exists"})
	}))
	defer srv.Close()

	_, err := AuthSignup(context.Background(), srv.Client(), srv.URL,
		"val", "hunter2hunter2", "acme", "create", "", bufio.NewReader(strings.NewReader("")), os.Stdout)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "org_exists" {
		t.Fatalf("want org_exists, got %v", err)
	}
	if _, found, _ := loadCredential(); found {
		t.Fatalf("a refused signup must not store a credential")
	}
}

// TestOrgCreateRebindsStoredLogin: after `org create`, the stored login
// points at the new org (the hub cloned the account's credential there -
// per-org accounts), verified by whoami before saving; --no-switch keeps
// the old binding. Re-typing the password into a second auth login is the
// exact onboarding toll §6.10 deletes.
func TestOrgCreateRebindsStoredLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/orgs":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"name": "acme", "role": "admin", "api_base": "/o/acme", "git_url": "/o/acme/acme.git"})
		case "/o/acme/api/whoami":
			json.NewEncoder(w).Encode(map[string]any{"name": "val"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if _, err := saveCredential(Credential{URL: srv.URL, Name: "val", Secret: "hunter2hunter2"}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if err := cmdOrg([]string{"create", "--name", "acme"}); err != nil {
		t.Fatalf("org create: %v", err)
	}
	cred, _, _ := loadCredential()
	if cred.URL != srv.URL+"/o/acme" {
		t.Fatalf("expected the stored login rebound to the new org, got %q", cred.URL)
	}

	// --no-switch: the binding stays put.
	if _, err := saveCredential(Credential{URL: srv.URL, Name: "val", Secret: "hunter2hunter2"}); err != nil {
		t.Fatalf("re-seed credential: %v", err)
	}
	if err := cmdOrg([]string{"create", "--name", "acme", "--no-switch"}); err != nil {
		t.Fatalf("org create --no-switch: %v", err)
	}
	if cred, _, _ := loadCredential(); cred.URL != srv.URL {
		t.Fatalf("--no-switch must keep the stored login, got %q", cred.URL)
	}
}

// TestWorkspaceCreateDefaultsByToStoredLogin: the stored login already
// says who you are - --by is an override, not a toll (§6.10).
func TestWorkspaceCreateDefaultsByToStoredLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var gotOwner string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner string `json:"owner"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotOwner = body.Owner
		// Refuse after recording: the test needs no worktree machinery.
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(clierr.Error{Code: "invalid_workspace_name", Message: "stub"})
	}))
	defer srv.Close()

	if _, err := saveCredential(Credential{URL: srv.URL, Name: "val", Secret: "hunter2hunter2"}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	err := cmdWorkspace([]string{"create", "--name", "ws1", "--project", "repo"})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "invalid_workspace_name" {
		t.Fatalf("expected the stub refusal to surface, got %v", err)
	}
	if gotOwner != "val" {
		t.Fatalf("expected --by to default to the stored login's principal, got %q", gotOwner)
	}
}
