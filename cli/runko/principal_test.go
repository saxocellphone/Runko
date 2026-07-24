package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

// TestWorkspaceCheckoutAuthorsAsItsOwner is the papercut this whole file
// exists for: a workspace materialized for one principal must authenticate
// as THAT principal from then on, even though the machine is logged in as
// somebody else. Before the binding, the name survived (runko.owner) and
// the secret did not, so every later push resolved the invoker's login and
// the server refused it as another owner's workspace - recoverable only by
// re-logging the whole shell under a scratch XDG_CONFIG_HOME.
func TestWorkspaceCheckoutAuthorsAsItsOwner(t *testing.T) {
	srv, _ := startWorkspaceServer(t) // stored login: the anonymous bearer
	const owner = "agent-checkout-a1b2"
	if _, err := savePrincipal(Credential{URL: srv.URL, Name: owner, Secret: "agent-tok"}); err != nil {
		t.Fatalf("savePrincipal: %v", err)
	}
	root := t.TempDir()
	_, dir, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"bound-ws", owner, []string{"checkout-api"}, nil,
		MaterializeOptions{CloneDir: filepath.Join(root, "store"), Dir: filepath.Join(root, "bound-ws")})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	if got := mustGit(t, dir, "config", "runko.controlplane"); got != strings.TrimSuffix(srv.URL, "/") {
		t.Errorf("runko.controlplane = %q, want %q", got, srv.URL)
	}
	// The worktree's helper chain for the control plane: reset first, then
	// the qualified helper - without the reset the store's own unqualified
	// helper (LOCAL config, read first) would answer with the machine's
	// login instead.
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	chain := mustGit(t, dir, "config", "--worktree", "--get-all", "credential."+u.Scheme+"://"+u.Host+"/.helper")
	lines := strings.Split(chain, "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != "" || !strings.HasSuffix(lines[1], "--principal "+owner) {
		t.Errorf("worktree helper chain = %q, want an empty reset entry then a --principal %s helper", chain, owner)
	}
	// Scoped, never global: an unscoped reset would clear the user's own
	// credential helper for every OTHER remote this checkout talks to.
	if got, err := runGit(dir, "config", "--worktree", "--get-all", "credential.helper"); err == nil && strings.TrimSpace(got) != "" {
		t.Errorf("the binding must not touch the unscoped helper chain, got %q", got)
	}

	// The binding resolves, and the git path authenticates with it.
	bound, ok := checkoutPrincipal(dir)
	if !ok || bound.Name != owner || bound.Secret != "agent-tok" {
		t.Fatalf("checkoutPrincipal = %+v, %v; want the owner's credential", bound, ok)
	}
	joined := strings.Join(gitNetEnv(dir), "\n")
	if !strings.Contains(joined, "RUNKO_GIT_USER="+owner) || !strings.Contains(joined, "RUNKO_GIT_PASS=agent-tok") {
		t.Errorf("the push must authenticate as the checkout's owner, got:\n%s", joined)
	}
	// ... and the pre-flight that used to hand out the XDG recipe is quiet.
	if err := checkPushIdentity(dir); err != nil {
		t.Errorf("a bound checkout must not trip the owner pre-flight: %v", err)
	}
}

// TestCheckoutBindingLosesToExplicitOverride: the binding sits BELOW the
// flags and the RUNKO_* environment. An operator who exports a token is
// making a deliberate choice about who authenticates, and a checkout must
// never silently outrank it.
func TestCheckoutBindingLosesToExplicitOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_RUNKOD_URL", "")
	t.Setenv("RUNKO_TOKEN", "")
	dir := boundCheckout(t, "http://ctrl.example/o/acme", "agent-bound-1111", "bound-tok")

	cred, err := resolveCredentialAt(dir, "", "")
	if err != nil || cred.Name != "agent-bound-1111" {
		t.Fatalf("unbound resolution = %+v (%v); want the binding", cred, err)
	}
	t.Setenv("RUNKO_TOKEN", "someone-else:other-tok")
	cred, err = resolveCredentialAt(dir, "", "")
	if err != nil || cred.Name != "someone-else" || cred.Secret != "other-tok" {
		t.Fatalf("env override = %+v (%v); want someone-else", cred, err)
	}
	t.Setenv("RUNKO_TOKEN", "")
	cred, err = resolveCredentialAt(dir, "http://ctrl.example/o/acme", "flag-tok")
	if err != nil || cred.Secret != "flag-tok" || cred.Name != "" {
		t.Fatalf("--token override = %+v (%v); want the bare flag token", cred, err)
	}
}

// TestUnboundCheckoutResolvesTheStoredLogin: a checkout materialized
// before bindings existed carries no runko.controlplane, so it resolves
// exactly what it always did.
func TestUnboundCheckoutResolvesTheStoredLogin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_TOKEN", "")
	t.Setenv("RUNKO_RUNKOD_URL", "")
	if _, err := saveCredential(Credential{URL: "http://ctrl.example/o/acme", Name: "alice", Secret: "pw"}); err != nil {
		t.Fatalf("saveCredential: %v", err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "runko.owner", "agent-legacy-2222") // name, no control plane
	cred, err := resolveCredentialAt(dir, "", "")
	if err != nil || cred.Name != "alice" {
		t.Fatalf("resolved %+v (%v); want the stored login", cred, err)
	}
}

// TestPrincipalForHostNeedsOneAnswer: git's credential protocol names a
// host, never an org mount, so a principal name held on two orgs of ONE
// host is ambiguous - answering with a guess would authenticate as the
// wrong principal, so the helper stays silent and the normal resolution
// takes over.
func TestPrincipalForHostNeedsOneAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := savePrincipal(Credential{URL: "http://ctrl.example/o/acme", Name: "agent-x-3333", Secret: "a"}); err != nil {
		t.Fatalf("savePrincipal: %v", err)
	}
	if cred, ok := principalForHost("agent-x-3333", "ctrl.example"); !ok || cred.Secret != "a" {
		t.Fatalf("single match = %+v, %v; want the credential", cred, ok)
	}
	if _, ok := principalForHost("agent-x-3333", "elsewhere.example"); ok {
		t.Errorf("a foreign host must not match")
	}
	if _, err := savePrincipal(Credential{URL: "http://ctrl.example/o/other", Name: "agent-x-3333", Secret: "b"}); err != nil {
		t.Fatalf("savePrincipal: %v", err)
	}
	if cred, ok := principalForHost("agent-x-3333", "ctrl.example"); ok {
		t.Errorf("ambiguous name must stay silent, got %+v", cred)
	}
}

// TestPrincipalPathStaysInItsStore: a principal name is server-validated,
// but the store must not depend on that - separators and traversal
// collapse to one safe component.
func TestPrincipalPathStaysInItsStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := principalsDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../../etc/passwd", "a/b", "agent-ok-4444"} {
		p, err := principalPath("http://ctrl.example/o/acme", name)
		if err != nil {
			t.Fatalf("principalPath(%q): %v", name, err)
		}
		if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(dir)+string(filepath.Separator)) {
			t.Errorf("principalPath(%q) escaped the store: %s", name, p)
		}
	}
}

// TestReleaseCheckoutPrincipalKeepsSharedIdentities: one agent identity
// may hold several workspaces, so reclaiming one materialization must not
// pull the credential out from under the others.
func TestReleaseCheckoutPrincipalKeepsSharedIdentities(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const plane, name = "http://ctrl.example/o/acme", "agent-two-ws-5555"
	first := boundCheckout(t, plane, name, "tok")
	second := boundCheckout(t, plane, name, "tok")
	for _, d := range []string{first, second} {
		if err := recordMaterialization(Materialization{Workspace: filepath.Base(d), Path: d, RunkodURL: plane}); err != nil {
			t.Fatalf("recordMaterialization: %v", err)
		}
	}

	releaseCheckoutPrincipal(first)
	if _, ok, _ := loadPrincipal(plane, name); !ok {
		t.Fatalf("credential dropped while another checkout still authors as %s", name)
	}
	if err := dropMaterialization(first); err != nil {
		t.Fatal(err)
	}
	releaseCheckoutPrincipal(second)
	if _, ok, _ := loadPrincipal(plane, name); ok {
		t.Errorf("last checkout released; credential should be gone")
	}
}

// TestCheckPushIdentityStillCatchesAnUnboundMismatch: the pre-flight keeps
// its job where there is no binding to answer, and its advice is now the
// one-command bind rather than the XDG_CONFIG_HOME re-login dance.
func TestCheckPushIdentityStillCatchesAnUnboundMismatch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_TOKEN", "")
	if _, err := saveCredential(Credential{URL: "http://ctrl.example/o/acme", Name: "admin", Secret: "pw"}); err != nil {
		t.Fatalf("saveCredential: %v", err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "runko.owner", "agent-unbound-6666")
	mustGit(t, dir, "config", "runko.controlplane", "http://ctrl.example/o/acme")

	err := checkPushIdentity(dir)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "workspace_owner_mismatch" {
		t.Fatalf("checkPushIdentity = %v; want workspace_owner_mismatch", err)
	}
	if !strings.Contains(ce.Suggestion, "auth login --name agent-unbound-6666") || !strings.Contains(ce.Suggestion, "-w ") {
		t.Errorf("suggestion should teach the bind: %q", ce.Suggestion)
	}
	if strings.Contains(ce.Suggestion, "XDG_CONFIG_HOME") {
		t.Errorf("the XDG re-login dance is what this replaced: %q", ce.Suggestion)
	}
}

// TestBindPrincipalToCheckout is `auth login -w`'s worker: validate, file
// the credential, point the checkout at it - and leave the machine's own
// stored login alone.
func TestBindPrincipalToCheckout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_TOKEN", "")
	t.Setenv("RUNKO_RUNKOD_URL", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/whoami" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "agent-bind-7777"})
	}))
	defer srv.Close()
	if _, err := saveCredential(Credential{URL: srv.URL, Name: "admin", Secret: "pw"}); err != nil {
		t.Fatalf("saveCredential: %v", err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")

	path, err := BindPrincipalToCheckout(context.Background(), srv.Client(), dir,
		Credential{URL: srv.URL, Name: "agent-bind-7777", Secret: "agent-tok"})
	if err != nil {
		t.Fatalf("BindPrincipalToCheckout: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored credential must be 0600: %v %v", info, err)
	}
	if got := mustGit(t, dir, "config", "runko.owner"); got != "agent-bind-7777" {
		t.Errorf("runko.owner = %q", got)
	}
	bound, ok := checkoutPrincipal(dir)
	if !ok || bound.Secret != "agent-tok" {
		t.Fatalf("checkoutPrincipal = %+v, %v", bound, ok)
	}
	if stored, _, _ := loadCredential(); stored.Name != "admin" {
		t.Errorf("binding a checkout must not touch the machine's login, got %q", stored.Name)
	}
	// A bare token has no name to bind a checkout to.
	if _, err := BindPrincipalToCheckout(context.Background(), srv.Client(), dir, Credential{URL: srv.URL, Secret: "tok"}); err == nil {
		t.Errorf("binding a nameless credential must be refused")
	}
}

// boundCheckout is a git checkout wired the way a materialized workspace
// is: an owner, a control plane, and that principal's credential on file.
func boundCheckout(t *testing.T, plane, name, secret string) string {
	t.Helper()
	if _, err := savePrincipal(Credential{URL: plane, Name: name, Secret: secret}); err != nil {
		t.Fatalf("savePrincipal: %v", err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "runko.owner", name)
	mustGit(t, dir, "config", "runko.controlplane", plane)
	return dir
}
