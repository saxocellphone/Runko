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
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// AuthGitCredential implements git's credential-helper protocol for the
// stamped helper. Only `get` answers; `store` and `erase` are silent
// no-ops (the stored login is runko's to manage, not git's). A request
// whose host doesn't match the stored login's control plane gets no
// output - git then falls through to its other helpers, so a foreign
// remote is never fed runko's credential.
func AuthGitCredential(action string, in io.Reader, out io.Writer) error {
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
	// Same resolution order as every networked verb: env fallback first
	// (RUNKO_RUNKOD_URL/RUNKO_TOKEN - hooks and headless agents inherit an
	// environment, not a login), then the stored credential.
	if base, tok := os.Getenv("RUNKO_RUNKOD_URL"), os.Getenv("RUNKO_TOKEN"); base != "" && tok != "" {
		if answerCredentialRequest(out, attrs, base, gitUserPassPair(tok)) {
			return nil
		}
	}
	cred, found, err := loadCredential()
	if err != nil || !found {
		return err // nothing anywhere: stay silent, git falls through
	}
	user, pass := cred.GitUserPass()
	answerCredentialRequest(out, attrs, cred.URL, [2]string{user, pass})
	return nil
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

// gitUserPassPair is gitUserPass in the array form the answerer takes.
func gitUserPassPair(tokenOrHeader string) [2]string {
	user, pass := gitUserPass(tokenOrHeader)
	return [2]string{user, pass}
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

// gitNetEnv resolves the invoking principal's credential (env fallback >
// stored login, the verb-local order `agent event` set) into
// gitAuthConfigEnv for a git command running in dir. Two silences are
// deliberate: a remote URL that still embeds a credential (a pre-§12.7
// clone) gets nothing - injecting a second Authorization header beside
// URL-derived auth breaks the request - and no resolvable credential gets
// nothing, leaving the stamped helper (or anonymity on public_read orgs)
// to answer.
func gitNetEnv(dir string) []string {
	if remote, err := runGit(dir, "config", "remote.origin.url"); err == nil {
		if u, err := url.Parse(remote); err == nil && u.User != nil {
			return nil
		}
	}
	if tok := os.Getenv("RUNKO_TOKEN"); tok != "" {
		base := os.Getenv("RUNKO_RUNKOD_URL")
		if base == "" {
			if cred, found, _ := loadCredential(); found {
				base = cred.URL
			}
		}
		return gitAuthConfigEnv(base, tok)
	}
	cred, found, err := loadCredential()
	if err != nil || !found {
		return nil
	}
	return gitAuthConfigEnv(cred.URL, cred.AuthHeader())
}

// runGitNet is runGit for commands that may touch the network from a
// workspace checkout: same plumbing, plus the invoker's auth.
func runGitNet(dir string, args ...string) (string, error) {
	return runGitEnv(dir, gitNetEnv(dir), args...)
}
