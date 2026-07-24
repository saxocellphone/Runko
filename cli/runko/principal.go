// The principal store: credentials for principals OTHER than the stored
// login, bound to the checkouts that author as them (§12.7's second half).
//
// `workspace create --as agent-x --token T` authenticated ONE command and
// dropped the secret on the floor: materialization stamped runko.owner, so
// the NAME survived, but every later verb in that checkout resolved the
// invoker's stored login instead - the server then refused the push
// ("workspace belongs to agent-x") and the only way through was to
// re-login the whole shell under a scratch XDG_CONFIG_HOME. That dance was
// the single most repeated tax on an agent session (dogfood, 2026-07-24).
//
// The missing piece was a PLACE for a second principal's secret. It is not
// the remote URL (that misattributed every other principal's push -
// migration finding #49 - and §12.7 removed it), and it is not
// credentials.json (single-slot, and concurrent agents on one machine
// would contend on one file). It is one file per principal:
//
//	<config>/runko/principals/<control-plane slug>/<name>.json   0600
//
// bound to a checkout by two worktree-scoped keys - runko.owner (who
// authors here, already stamped) and runko.controlplane (which control
// plane that name belongs to). Resolution stays flags > RUNKO_* env >
// checkout binding > stored login, so an explicit override always wins and
// an unbound checkout behaves exactly as before.
//
// The binding is the checkout's AUTHORING identity, not a session-wide
// login: it answers for the git transport and the verbs that write as the
// workspace's owner. Review-lane verbs (land, approve, abandon) keep the
// invoking human's own credential - a human landing an agent's change from
// its workspace is still landing it as themselves.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// principalsDir is where per-principal credentials live, beside the stored
// login (so XDG_CONFIG_HOME moves both, and a scratch config dir still
// isolates a whole session the way it does today).
func principalsDir() (string, error) {
	base, err := credentialPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(base), "principals"), nil
}

// controlPlaneSlug turns a control-plane URL into one path-safe directory
// name. Host AND path: one host serves many orgs (/o/<org>), and the same
// principal name in two orgs is two different principals.
func controlPlaneSlug(baseURL string) string {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil || u.Host == "" {
		return ""
	}
	return pathSafe(u.Host + u.Path)
}

// pathSafe reduces s to characters that are safe in one path component -
// everything else (including the separators a crafted principal name could
// carry) collapses to "_", so a store lookup can never escape its dir.
func pathSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// principalPath is where the credential for name on baseURL is stored.
func principalPath(baseURL, name string) (string, error) {
	slug, safeName := controlPlaneSlug(baseURL), pathSafe(name)
	if slug == "" || safeName == "" {
		return "", fmt.Errorf("principal store: need a control-plane URL and a principal name (got %q, %q)", baseURL, name)
	}
	dir, err := principalsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, slug, safeName+".json"), nil
}

// savePrincipal writes a named principal's credential 0600 and returns the
// path. Named only: a bare bearer token has no name to bind a checkout to.
func savePrincipal(cred Credential) (string, error) {
	if cred.Name == "" {
		return "", fmt.Errorf("principal store: only a named principal can be bound to a checkout")
	}
	path, err := principalPath(cred.URL, cred.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return path, writeCredentialFile(path, cred)
}

// loadPrincipal reads the credential bound to name on baseURL.
func loadPrincipal(baseURL, name string) (Credential, bool, error) {
	path, err := principalPath(baseURL, name)
	if err != nil {
		return Credential{}, false, err
	}
	return readCredentialFile(path)
}

// deletePrincipal removes a bound credential; a missing file is success
// (delete is called from cleanup paths that must stay idempotent).
func deletePrincipal(baseURL, name string) error {
	path, err := principalPath(baseURL, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// principalForHost finds name's credential by HOST rather than by exact
// control-plane URL: git's credential protocol tells the helper a protocol
// and a host, never which /o/<org> mount the request belongs to. A single
// match answers; two orgs on one host holding the same principal name is
// ambiguous, and answering with a guess would authenticate as the wrong
// one, so that stays silent and the default resolution takes over.
func principalForHost(name, host string) (Credential, bool) {
	dir, err := principalsDir()
	if err != nil {
		return Credential{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Credential{}, false
	}
	var found Credential
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cred, ok, err := readCredentialFile(filepath.Join(dir, e.Name(), pathSafe(name)+".json"))
		if err != nil || !ok {
			continue
		}
		if u, err := url.Parse(cred.URL); err == nil && u.Host == host {
			found, n = cred, n+1
		}
	}
	if n != 1 {
		return Credential{}, false
	}
	return found, true
}

// checkoutPrincipal is the binding: who this checkout authors as, and the
// credential to do it with. Both config keys must be present - a checkout
// stamped before this existed carries no runko.controlplane, resolves no
// binding, and behaves exactly as it did before.
func checkoutPrincipal(dir string) (Credential, bool) {
	if dir == "" {
		return Credential{}, false
	}
	owner, _ := runGit(dir, "config", "runko.owner")
	plane, _ := runGit(dir, "config", "runko.controlplane")
	if owner == "" || plane == "" {
		return Credential{}, false
	}
	cred, ok, err := loadPrincipal(plane, owner)
	if err != nil || !ok {
		return Credential{}, false
	}
	return cred, true
}

// resolveCredentialAt is resolveCredentialEnv plus the checkout binding,
// slotted between the environment and the stored login: an explicit
// --token or an exported RUNKO_TOKEN still names WHO authenticates, and
// only when neither speaks does the checkout answer for itself.
func resolveCredentialAt(dir, urlFlag, tokenFlag string) (Credential, error) {
	if tokenFlag == "" && os.Getenv("RUNKO_TOKEN") == "" {
		if cred, ok := checkoutPrincipal(dir); ok {
			if urlFlag != "" {
				cred.URL = strings.TrimSuffix(urlFlag, "/")
			}
			return cred, nil
		}
	}
	cred, err := resolveCredentialEnv(urlFlag, tokenFlag)
	// A token with nowhere to point: the checkout knows where it talks to,
	// and auth.go's rule is that a token retargets WHO authenticates, not
	// WHERE. An agent that exported RUNKO_TOKEN on a machine with no stored
	// login gets its control plane from the workspace it is working in.
	if err != nil && urlFlag == "" {
		var ce *clierr.Error
		if errors.As(err, &ce) && ce.Code == "missing_url" {
			if plane, _ := runGit(dir, "config", "runko.controlplane"); plane != "" {
				return resolveCredentialEnv(plane, tokenFlag)
			}
		}
	}
	return cred, err
}

// qualifiedCredentialHelperSpec is credentialHelperSpec pinned to one
// principal - the form stamped into a BOUND checkout, so raw git there
// (a `git push`, a blobless clone's lazy blob fetch) authenticates as the
// checkout's author rather than as whoever's login the ambient config
// resolves. Naming the principal beats having the helper guess from its
// working directory: git runs credential helpers from wherever the calling
// process stood, which for a submodule or a hook is not the worktree.
func qualifiedCredentialHelperSpec(name string) string {
	return credentialHelperSpec() + " --principal " + name
}

// checkoutConfigScope picks the git config scope a checkout's runko.*
// bindings belong in: worktree config where the shared store enabled it
// (one store, N worktrees, each authoring as its own workspace's owner),
// plain local otherwise (a standalone --jj colocated clone, a plain
// checkout). Same split stampCheckoutConfig makes, decided from the
// checkout instead of from the caller - `auth login -w` binds checkouts it
// did not materialize.
func checkoutConfigScope(dir string) string {
	if v, err := runGit(dir, "config", "extensions.worktreeConfig"); err == nil && strings.TrimSpace(v) == "true" {
		return "--worktree"
	}
	return "--local"
}

// bindCheckoutAuthoring records WHICH control plane the checkout's
// runko.owner belongs to and stamps the principal-qualified credential
// helper.
//
// The chain is RESET first (an empty helper entry) and then set: the
// shared store's own unqualified helper sits in LOCAL config, which git
// reads BEFORE worktree config, so without the reset the store helper
// would answer first - with the invoking principal's login, the exact
// misattribution this binding exists to end.
//
// Both entries are scoped to the control plane's ORIGIN, never written as
// a bare credential.helper: an unscoped reset would clear the user's
// global helper (their OS keychain) for EVERY remote this checkout talks
// to, so a `git fetch` of some unrelated host inside a workspace would
// start failing. Scoped, the reset reaches only the credentials for the
// one host runko answers for.
func bindCheckoutAuthoring(dir, name, controlPlane string) error {
	if name == "" || controlPlane == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSuffix(controlPlane, "/"))
	if err != nil || u.Host == "" {
		return fmt.Errorf("bind authoring identity: unusable control-plane URL %q", controlPlane)
	}
	scope := checkoutConfigScope(dir)
	if _, err := runGit(dir, "config", scope, "runko.controlplane", strings.TrimSuffix(controlPlane, "/")); err != nil {
		return err
	}
	key := "credential." + u.Scheme + "://" + u.Host + "/.helper"
	// --unset-all keeps re-binding idempotent (attach over an existing
	// worktree, a rebound recycled materialization). Exit 5 is git's
	// "nothing to unset" - the common case, not a failure.
	if _, err := runGit(dir, "config", scope, "--unset-all", key); err != nil {
		if !strings.Contains(err.Error(), "exit status 5") {
			return err
		}
	}
	for _, v := range []string{"", qualifiedCredentialHelperSpec(name)} {
		if _, err := runGit(dir, "config", scope, "--add", key, v); err != nil {
			return err
		}
	}
	return nil
}

// BindPrincipalToCheckout is `auth login -w`'s worker and the retrofit
// path for a checkout materialized before bindings existed: validate the
// credential, file it in the principal store, and point the checkout at it
// (runko.owner + runko.controlplane + the qualified helper). The machine's
// stored login is deliberately NOT touched - binding an agent identity to
// one workspace must not sign the human out of their own shell.
func BindPrincipalToCheckout(ctx context.Context, client *http.Client, dir string, cred Credential) (string, error) {
	if cred.Name == "" {
		return "", &clierr.Error{
			Code: "missing_field", Field: "name",
			Message:    "binding a checkout needs a named principal (--name), not a bare token",
			Suggestion: "runko auth login --name <principal> --token <tok> -w <workspace>",
		}
	}
	who, _, err := whoami(ctx, client, cred)
	if err != nil {
		return "", err
	}
	if who != "" {
		cred.Name = who
	}
	path, err := savePrincipal(cred)
	if err != nil {
		return "", err
	}
	if _, err := runGit(dir, "config", checkoutConfigScope(dir), "runko.owner", cred.Name); err != nil {
		return "", err
	}
	if err := bindCheckoutAuthoring(dir, cred.Name, cred.URL); err != nil {
		return "", err
	}
	return path, nil
}

// principalStillBound reports whether any OTHER materialization on this
// machine authors as name on the same control plane - the guard on
// deleting a bound credential when a workspace goes away, since one agent
// identity may hold several workspaces. The registry is a cache (a row may
// name a directory that no longer exists), so an unreadable checkout
// simply doesn't count as a holder.
func principalStillBound(name, controlPlane, excludeDir string) bool {
	rows, err := loadMaterializations()
	if err != nil {
		return true // unknown: keep the credential rather than break a live checkout
	}
	for _, r := range rows {
		if r.Path == excludeDir {
			continue
		}
		owner, _ := runGit(r.Path, "config", "runko.owner")
		plane, _ := runGit(r.Path, "config", "runko.controlplane")
		if owner == name && strings.TrimSuffix(plane, "/") == strings.TrimSuffix(controlPlane, "/") {
			return true
		}
	}
	return false
}

// releaseCheckoutPrincipal drops the credential a checkout was authoring
// with, once no other materialization still holds it. Best-effort: this
// runs from teardown paths (workspace delete, gc reclaim) where failing
// the verb over bookkeeping would be worse than a stale 0600 file that
// expires on its own anyway.
func releaseCheckoutPrincipal(dir string) {
	owner, _ := runGit(dir, "config", "runko.owner")
	plane, _ := runGit(dir, "config", "runko.controlplane")
	if owner == "" || plane == "" {
		return
	}
	if principalStillBound(owner, plane, dir) {
		return
	}
	_ = deletePrincipal(plane, owner)
}
