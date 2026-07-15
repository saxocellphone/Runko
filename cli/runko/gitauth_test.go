package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthGitCredentialAnswersOwnHostOnly pins the helper protocol: `get`
// for the stored login's host answers username/password; a foreign host -
// or a non-get action - stays silent so git falls through and a foreign
// remote is never fed runko's credential.
func TestAuthGitCredentialAnswersOwnHostOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_RUNKOD_URL", "")
	t.Setenv("RUNKO_TOKEN", "")
	if _, err := saveCredential(Credential{URL: "http://ctrl.example:8080/o/acme", Name: "alice", Secret: "s3cr3t"}); err != nil {
		t.Fatalf("saveCredential: %v", err)
	}

	var out strings.Builder
	err := AuthGitCredential("get", strings.NewReader("protocol=http\nhost=ctrl.example:8080\n\n"), &out)
	if err != nil {
		t.Fatalf("AuthGitCredential: %v", err)
	}
	if got := out.String(); got != "username=alice\npassword=s3cr3t\n" {
		t.Fatalf("unexpected helper answer:\n%s", got)
	}

	for name, req := range map[string]struct{ action, input string }{
		"foreign host": {"get", "protocol=http\nhost=github.com\n\n"},
		"wrong scheme": {"get", "protocol=https\nhost=ctrl.example:8080\n\n"},
		"store action": {"store", "protocol=http\nhost=ctrl.example:8080\n\n"},
	} {
		out.Reset()
		if err := AuthGitCredential(req.action, strings.NewReader(req.input), &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: expected silence, got %q", name, out.String())
		}
	}
}

// TestStoreRemoteIsCredentialNeutral is §12.7's core auth decision at the
// store level: the origin URL carries no userinfo, the credential helper
// is stamped instead, and runko's own snapshot push authenticates via the
// per-invocation header - no secret ever lands in .git/config.
func TestStoreRemoteIsCredentialNeutral(t *testing.T) {
	srv, bare := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "store")
	wsDir := filepath.Join(root, "neutral-ws")

	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"neutral-ws", "alice", []string{"checkout-api"}, cloneDir, wsDir); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	remote := mustGit(t, cloneDir, "config", "remote.origin.url")
	if u, err := url.Parse(remote); err != nil || u.User != nil {
		t.Fatalf("store remote must be credential-neutral, got %q", remote)
	}
	helper := mustGit(t, cloneDir, "config", "credential.helper")
	if !strings.HasPrefix(helper, "!") {
		t.Fatalf("expected a stamped credential helper, got %q", helper)
	}

	// The verb path: snapshot pushes with the stored login injected per
	// invocation (gitauth.go), against the neutral remote.
	writeFile(t, wsDir, "commerce/checkout/wip.go", "package main // neutral\n")
	if _, err := WorkspaceSnapshot(wsDir, "neutral"); err != nil {
		t.Fatalf("WorkspaceSnapshot over a neutral remote: %v", err)
	}
	if sha := mustGit(t, bare, "rev-parse", "refs/workspaces/neutral-ws/head"); sha == "" {
		t.Fatalf("snapshot ref missing on the served repo")
	}

	// The raw-git path: a plain fetch (what a blobless clone's lazy blob
	// fault-in does) authenticates through the stamped helper alone.
	mustGit(t, wsDir, "fetch", "origin")
}

// TestPreS127StoreIsNeutralizedOnReuse: a store created before §12.7
// carries its creator's token in the origin URL - the misattribution bug
// that forced clone-per-task. The next create/attach through it strips
// the userinfo and stamps the helper (the retrofit pattern the verb-nudge
// hooks established).
func TestPreS127StoreIsNeutralizedOnReuse(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	root := t.TempDir()
	cloneDir := filepath.Join(root, "legacy-store")
	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"legacy-a", "alice", []string{"checkout-api"}, cloneDir, filepath.Join(root, "legacy-a")); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	// Regress the store to the pre-§12.7 shape: creator's token in the URL.
	u, err := url.Parse(mustGit(t, cloneDir, "config", "remote.origin.url"))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("runko", "sekret")
	mustGit(t, cloneDir, "config", "remote.origin.url", u.String())
	mustGit(t, cloneDir, "config", "--unset", "credential.helper")

	if _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"legacy-b", "bob", []string{"money-lib"}, cloneDir, filepath.Join(root, "legacy-b")); err != nil {
		t.Fatalf("WorkspaceCreate over the legacy store: %v", err)
	}
	remote := mustGit(t, cloneDir, "config", "remote.origin.url")
	if pu, err := url.Parse(remote); err != nil || pu.User != nil {
		t.Fatalf("legacy store should have been neutralized, got %q", remote)
	}
	if helper := mustGit(t, cloneDir, "config", "credential.helper"); !strings.HasPrefix(helper, "!") {
		t.Fatalf("expected the retrofit to stamp the credential helper, got %q", helper)
	}
}

// TestGitNetEnvLegacyEmbeddedURLInjectsNothing: a checkout whose origin
// still embeds a credential (pre-§12.7, never re-ensured) must not get a
// second Authorization header injected beside the URL-derived one.
func TestGitNetEnvLegacyEmbeddedURLInjectsNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_TOKEN", "")
	if _, err := saveCredential(Credential{URL: "http://ctrl.example/o/acme", Secret: "tok"}); err != nil {
		t.Fatalf("saveCredential: %v", err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "remote", "add", "origin", "http://user:pass@ctrl.example/repo.git")
	if env := gitNetEnv(dir); env != nil {
		t.Fatalf("expected no injection over an embedded-credential remote, got %v", env)
	}
	mustGit(t, dir, "remote", "set-url", "origin", "http://ctrl.example/repo.git")
	env := gitNetEnv(dir)
	joined := strings.Join(env, "\n")
	if len(env) == 0 || !strings.Contains(joined, "credential.http://ctrl.example/.helper") || !strings.Contains(joined, "RUNKO_GIT_PASS=tok") {
		t.Fatalf("expected an env-fed helper injection over a neutral remote, got %v", env)
	}
}

// TestAuthGitCredentialEnvFallback: the stamped helper answers from the
// verb-local RUNKO_RUNKOD_URL/RUNKO_TOKEN environment when no login is
// stored - hooks and headless agents inherit an environment, and git >=
// 2.46's proactiveAuth consults the helper chain BEFORE the first
// request, so a silent helper there means a username prompt instead of a
// request (the git 2.54 CI failure).
func TestAuthGitCredentialEnvFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no stored login
	t.Setenv("RUNKO_RUNKOD_URL", "http://ctrl.example:8080/o/acme")
	t.Setenv("RUNKO_TOKEN", "env-tok")

	var out strings.Builder
	if err := AuthGitCredential("get", strings.NewReader("protocol=http\nhost=ctrl.example:8080\n\n"), &out); err != nil {
		t.Fatalf("AuthGitCredential: %v", err)
	}
	if got := out.String(); got != "username=runko\npassword=env-tok\n" {
		t.Fatalf("expected the env credential, got %q", got)
	}
	out.Reset()
	if err := AuthGitCredential("get", strings.NewReader("protocol=http\nhost=elsewhere.example\n\n"), &out); err != nil || out.Len() != 0 {
		t.Fatalf("foreign host must stay silent even with env credentials, got %q (%v)", out.String(), err)
	}
}

// TestCredentialHelperSpecOverride: RUNKO_CREDENTIAL_HELPER wins (tests,
// unusual installs); otherwise the spec names the running binary and the
// git-credential verb.
func TestCredentialHelperSpecOverride(t *testing.T) {
	t.Setenv("RUNKO_CREDENTIAL_HELPER", "/opt/custom-helper")
	if got := credentialHelperSpec(); got != "!/opt/custom-helper" {
		t.Fatalf("override ignored: %q", got)
	}
	t.Setenv("RUNKO_CREDENTIAL_HELPER", "")
	exe, err := os.Executable()
	if err != nil {
		t.Skip("os.Executable unavailable")
	}
	if got := credentialHelperSpec(); got != "!"+exe+" auth git-credential" {
		t.Fatalf("expected the running binary + verb, got %q", got)
	}
}
