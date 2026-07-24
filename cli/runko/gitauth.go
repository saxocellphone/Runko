// runko auth git-credential + per-invocation git auth (§12.7: stores are
// credential-neutral). A workspace store's origin URL never embeds a
// credential; instead auth reaches git two ways, both resolved from the
// INVOKING principal at the moment of use:
//
//   - raw git (lazy blob fetch in a blobless clone, user-typed fetch/push)
//     asks the credential helper stamped into the store's config, which
//     execs `runko auth git-credential` - and that reads the invoker's own
//     stored login (credentialPath honors XDG_CONFIG_HOME, so two
//     principals on one machine resolve two different credentials);
//   - runko's own verbs inject a one-process, env-fed credential helper
//     (GIT_CONFIG_* environment scoped to the control plane's origin),
//     from the same flags > env > stored-login order every networked
//     command uses. A helper - not a fixed Authorization header - because
//     git >= 2.46 honors http.proactiveAuth by resolving credentials
//     through the HELPER CHAIN before the first request (found live: git
//     2.54 in CI prompted for a username at checkout while 2.43 locally
//     ignored the key), and a fixed header cannot participate in that
//     negotiation without duplicating the Authorization header.
//
// The v1 glue baked the creating principal's token into the shared clone's
// remote URL, which misattributed every other principal's push and forced
// the clone-per-task sprawl §12.7 retires (migration finding #49).
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// AuthGitCredential implements git's credential-helper protocol for the
// stamped helper. Only `get` answers; `store` and `erase` are silent
// no-ops (the stored login is runko's to manage, not git's). A request
// whose host doesn't match the resolved control plane gets no output - git
// then falls through to its other helpers, so a foreign remote is never
// fed runko's credential.
//
// principal is the --principal a BOUND checkout's helper carries
// (principal.go): raw git in a workspace worktree then authenticates as
// the checkout's author instead of as whoever the ambient login names. It
// is a preference, not a demand - an unresolvable principal falls through
// to the normal resolution rather than going silent, so a human's
// ordinary checkout keeps working when its expired agent credential is
// swept.
func AuthGitCredential(action, principal string, in io.Reader, out io.Writer) error {
	if action != "get" {
		return nil
	}
	attrs := map[string]string{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			attrs[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read credential request: %w", err)
	}
	// The checkout's own authoring identity, when this helper was stamped
	// for one and no explicit environment override outranks it.
	if principal != "" && os.Getenv("RUNKO_TOKEN") == "" {
		if cred, ok := principalForHost(principal, attrs["host"]); ok {
			user, pass := cred.GitUserPass()
			if answerCredentialRequest(out, attrs, cred.URL, [2]string{user, pass}) {
				return nil
			}
		}
	}
	// ONE resolver for both paths (auth.go's resolveCredentialEnv): flags,
	// then the RUNKO_RUNKOD_URL/RUNKO_TOKEN environment (hooks and headless
	// agents inherit an environment, not a login), then the stored login.
	// Resolving the environment by hand here instead was the git path's
	// silent divergence: it handed the raw RUNKO_TOKEN to gitUserPass, which
	// understands only the "Basic <b64>" form, so the name-qualified
	// "<name>:<token>" that every control-plane verb accepts went out as
	// Basic runko:<name>:<token> - the remote answered a bare "unauthorized"
	// while `runko change describe` with the same export worked (dogfood,
	// 2026-07-24).
	cred, err := resolveCredentialEnv("", "")
	if err != nil {
		if credentialAbsent(err) {
			return nil // nothing anywhere: stay silent, git falls through
		}
		return err
	}
	user, pass := cred.GitUserPass()
	answerCredentialRequest(out, attrs, cred.URL, [2]string{user, pass})
	return nil
}

// credentialAbsent reports whether err is resolveCredential's "there is
// nothing to resolve" refusal rather than a real failure (an unreadable or
// corrupt credentials.json). The helper protocol answers the former with
// silence - git falls through to its other helpers - while the latter
// deserves to be seen.
func credentialAbsent(err error) bool {
	var ce *clierr.Error
	return errors.As(err, &ce) && (ce.Code == "not_logged_in" || ce.Code == "missing_url")
}

// answerCredentialRequest prints the username/password pair when the
// request's protocol+host match baseURL's; reports whether it answered.
func answerCredentialRequest(out io.Writer, attrs map[string]string, baseURL string, userPass [2]string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	if attrs["host"] != u.Host || (attrs["protocol"] != "" && attrs["protocol"] != u.Scheme) {
		return false
	}
	fmt.Fprintf(out, "username=%s\npassword=%s\n", userPass[0], userPass[1])
	return true
}

// credentialHelperSpec is the helper command stamped into a store's config
// (shell form, so git passes the action as an argument). The running
// binary's absolute path keeps it working off-PATH (agent containers,
// hand-installed ~/go/bin); RUNKO_CREDENTIAL_HELPER overrides for tests
// and unusual installs.
func credentialHelperSpec() string {
	if env := os.Getenv("RUNKO_CREDENTIAL_HELPER"); env != "" {
		return "!" + env
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "runko"
	}
	return "!" + exe + " auth git-credential"
}

// envFedCredentialHelper answers git's credential `get` from two
// environment variables set in the same one-process injection - secrets
// ride the environment, never argv or any config file. Always exits 0.
const envFedCredentialHelper = `!f() { if [ "$1" = get ]; then printf 'username=%s\npassword=%s\n' "$RUNKO_GIT_USER" "$RUNKO_GIT_PASS"; fi; }; f`

// gitAuthConfigEnv builds one-process GIT_CONFIG_* environment injecting
// the in-hand credential as an env-fed helper scoped to baseURL's origin -
// per-invocation auth for runko's own network git calls, never persisted
// anywhere. A HELPER, not a fixed Authorization header: git >= 2.46's
// http.proactiveAuth resolves credentials through the helper chain before
// the first request, and 401 challenges consult the same chain on every
// version - one mechanism serves both. The chain is RESET first (empty
// helper entry) so the credential the calling verb resolved (flags > env >
// stored) is exactly the one git uses - a stamped store helper answering
// from a different stored login must not outrank it.
func gitAuthConfigEnv(baseURL, tokenOrHeader string) []string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || tokenOrHeader == "" {
		return nil
	}
	user, pass := gitUserPass(tokenOrHeader)
	scope := u.Scheme + "://" + u.Host + "/"
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential." + scope + ".helper",
		"GIT_CONFIG_VALUE_1=" + envFedCredentialHelper,
		"RUNKO_GIT_USER=" + user,
		"RUNKO_GIT_PASS=" + pass,
	}
}

// gitNetEnv resolves the credential a git command running in dir should
// use (flags > RUNKO_* env > the checkout's bound principal > the stored
// login) into gitAuthConfigEnv. Resolution is resolveCredentialAt's, the
// same one the control-plane verbs use - a push and a `change describe`
// from one shell must authenticate as the same principal or the mismatch
// surfaces as an unexplainable 401. This is where a workspace's own
// authoring identity reaches the push: `change create`/`push`/`snapshot`
// never touch the control-plane API at all, so the binding lands here.
//
// Two silences are deliberate: a remote URL that still embeds a credential
// (a pre-§12.7 clone) gets nothing - injecting a second Authorization
// header beside URL-derived auth breaks the request - and no resolvable
// credential gets nothing, leaving the stamped helper (or anonymity on
// public_read orgs) to answer.
func gitNetEnv(dir string) []string {
	if remote, err := runGit(dir, "config", "remote.origin.url"); err == nil {
		if u, err := url.Parse(remote); err == nil && u.User != nil {
			return nil
		}
	}
	cred, err := resolveCredentialAt(dir, "", "")
	if err != nil {
		return nil
	}
	return gitAuthConfigEnv(cred.URL, cred.AuthHeader())
}

// runGitNet is runGit for commands that may touch the network from a
// workspace checkout: same plumbing, plus the invoker's auth.
func runGitNet(dir string, args ...string) (string, error) {
	return runGitEnv(dir, gitNetEnv(dir), args...)
}
