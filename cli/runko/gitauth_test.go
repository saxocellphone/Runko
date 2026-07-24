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

	if _, _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"neutral-ws", "alice", []string{"checkout-api"}, nil, MaterializeOptions{CloneDir: cloneDir, Dir: wsDir}); err != nil {
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
	if _, _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"legacy-a", "alice", []string{"checkout-api"}, nil, MaterializeOptions{CloneDir: cloneDir, Dir: filepath.Join(root, "legacy-a")}); err != nil {
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

	if _, _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"legacy-b", "bob", []string{"money-lib"}, nil, MaterializeOptions{CloneDir: cloneDir, Dir: filepath.Join(root, "legacy-b")}); err != nil {
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

// TestNameQualifiedEnvTokenOnTheGitPath: RUNKO_TOKEN="<name>:<token>" -
// the form `runko agent create` prints and `agent create`'s own export
// line teaches - must authenticate as that principal on the GIT path too,
// not just on the control-plane path. Both git-side sites used to hand the
// raw env value to gitUserPass, which understands only "Basic <b64>", so
// the pair went out as runko:<name>:<token> and every push 401ed while the
// same export worked for `change describe` (dogfood, 2026-07-24).
func TestNameQualifiedEnvTokenOnTheGitPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no stored login
	t.Setenv("RUNKO_RUNKOD_URL", "http://ctrl.example:8080/o/acme")
	t.Setenv("RUNKO_TOKEN", "agent-fix-rail-a1b2:tok-secret")

	// The stamped helper.
	var out strings.Builder
	if err := AuthGitCredential("get", strings.NewReader("protocol=http\nhost=ctrl.example:8080\n\n"), &out); err != nil {
		t.Fatalf("AuthGitCredential: %v", err)
	}
	if got, want := out.String(), "username=agent-fix-rail-a1b2\npassword=tok-secret\n"; got != want {
		t.Fatalf("helper answered %q, want %q", got, want)
	}

	// The per-invocation injection runko's own verbs push with.
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "remote", "add", "origin", "http://ctrl.example:8080/o/acme/acme/repo.git")
	joined := strings.Join(gitNetEnv(dir), "\n")
	if !strings.Contains(joined, "RUNKO_GIT_USER=agent-fix-rail-a1b2") || !strings.Contains(joined, "RUNKO_GIT_PASS=tok-secret") {
		t.Fatalf("gitNetEnv must split the name-qualified token, got:\n%s", joined)
	}
}

// TestEnvTokenBorrowsStoredControlPlane: an exported RUNKO_TOKEN with no
// RUNKO_RUNKOD_URL retargets WHO authenticates, not WHERE (auth.go's rule).
// The git path honors it through the same resolver.
func TestEnvTokenBorrowsStoredControlPlane(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RUNKO_RUNKOD_URL", "")
	t.Setenv("RUNKO_TOKEN", "agent-x-9f9f:agent-secret")
	if _, err := saveCredential(Credential{URL: "http://ctrl.example/o/acme", Name: "admin", Secret: "admin-pw"}); err != nil {
		t.Fatalf("saveCredential: %v", err)
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "remote", "add", "origin", "http://ctrl.example/o/acme/acme/repo.git")
	joined := strings.Join(gitNetEnv(dir), "\n")
	if !strings.Contains(joined, "credential.http://ctrl.example/.helper") {
		t.Fatalf("expected the stored login's control plane to be borrowed, got:\n%s", joined)
	}
	if !strings.Contains(joined, "RUNKO_GIT_USER=agent-x-9f9f") || strings.Contains(joined, "admin-pw") {
		t.Fatalf("the env token must win over the stored login, got:\n%s", joined)
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
