// Package mirror implements the outbound half of §18's mirror story: the
// monorepo (SoR here, §18.1 stage 2 posture) pushed to a downstream mirror
// on any git host - "hosted somewhere trustworthy" as a property, not a
// GitHub integration.
//
// PROVIDER-AGNOSTIC BY CONSTRUCTION (decided 2026-07-08): this package
// speaks only the git wire protocol - ls-remote and push - never a
// provider's REST API, and imports no provider SDK. Any smart-HTTPS git
// host (GitHub, GitLab, Gitea/Forgejo, Bitbucket, another bare runkod, a
// plain filesystem path in tests) is a valid mirror target. The ONLY
// provider-specific atom in the outbound direction is the basic-auth
// USERNAME convention for token auth, isolated in Remote.Username:
//
//	GitHub  fine-grained/classic PAT  -> "x-access-token" (the default)
//	GitLab  project access token      -> "oauth2"
//	Gitea / Forgejo                   -> any non-empty string
//	Bitbucket Cloud app password      -> the account username
//
// Where providers genuinely diverge - inbound webhooks, PR-merge ingestion
// as external Changes (§18.6.3), commit statuses - is M2's Provider seam,
// deliberately NOT modeled here yet (docs/mirror.md records the shape).
//
// Credentials never enter argv: the Authorization header rides
// GIT_CONFIG_* environment variables (git's env-borne config), so a `ps`
// on the daemon host shows no token.
package mirror

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Remote is one mirror target for one local bare repo.
type Remote struct {
	// RepoDir is the local bare repo (the source of truth) pushes run from.
	RepoDir string
	// URL is any git remote URL. https:// URLs get token auth (below);
	// everything else (ssh, local path) is used exactly as given.
	URL string
	// Username is the basic-auth user for token auth over https - see the
	// package doc's provider table. Empty defaults to "x-access-token".
	Username string
	// Token is the provider access token (password half of basic auth).
	// Empty means no auth is injected (ssh remotes, path remotes, public
	// push targets behind other auth).
	Token string
}

func (r *Remote) username() string {
	if r.Username == "" {
		return "x-access-token"
	}
	return r.Username
}

// env returns the git env-borne config injecting the Authorization header
// for https remotes - nil when no token applies.
func (r *Remote) env() []string {
	if r.Token == "" || !strings.HasPrefix(r.URL, "https://") {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte(r.username() + ":" + r.Token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
	}
}

func (r *Remote) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.RepoDir
	cmd.Env = append(os.Environ(), r.env()...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// LsRemote returns the mirror's current SHA for ref ("" when the ref does
// not exist there).
func (r *Remote) LsRemote(ref string) (string, error) {
	out, err := r.git("ls-remote", r.URL, ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// PushWithLease pushes localRef to the mirror, succeeding only if the
// mirror's ref still points at expected ("" = the ref must not exist yet) -
// §18.6.1's single-writer lease as a git primitive. A lease failure means
// someone else wrote the mirror; the CALLER decides what freezes.
func (r *Remote) PushWithLease(ref, expected string) error {
	_, err := r.git("push", "--force-with-lease="+ref+":"+expected, r.URL, "+"+ref+":"+ref)
	return err
}

// PushRefspecs pushes refspecs as given (used for the wildcard namespaces:
// tags fast-forward-only, change refs forced - that namespace is
// server-owned on BOTH sides, so force is overwrite-of-our-own-writes).
func (r *Remote) PushRefspecs(refspecs ...string) error {
	_, err := r.git(append([]string{"push", r.URL}, refspecs...)...)
	return err
}
