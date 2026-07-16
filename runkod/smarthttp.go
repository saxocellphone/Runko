package runkod

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitHTTPHandler returns an http.Handler serving repoDir (a bare repo) over
// git's own smart-HTTP protocol via `git http-backend` - shelled out to
// exactly like every other git operation in this codebase (§28.2 rule 4:
// never reimplement git, shell out to it). Go's stdlib net/http/cgi already
// implements the CGI env-var/header/streaming plumbing http-backend expects,
// so this is thin wiring, not a bespoke CGI implementation.
//
// This handler does not itself enforce §7.4's write-path policy (direct
// trunk push rejection, Change creation) - that enforcement lives in the
// pre-receive hook installed into repoDir/hooks/pre-receive (hook.go), which
// git invokes for every push regardless of transport. Mount the returned
// handler at "/" + RepoMountName(repoDir) + "/".
func GitHTTPHandler(repoDir string) (*cgi.Handler, error) {
	backend, err := gitHTTPBackendPath()
	if err != nil {
		return nil, err
	}
	projectRoot := filepath.Dir(filepath.Clean(repoDir))
	return &cgi.Handler{
		Path: backend,
		Dir:  repoDir,
		Env: []string{
			"GIT_PROJECT_ROOT=" + projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}, nil
}

// RepoMountName is the URL path segment a repo is served under -
// git-http-backend expects PATH_INFO's first segment to be the repo
// directory's own basename (relative to GIT_PROJECT_ROOT), so a client
// clones via http://host:port/<RepoMountName>/.
func RepoMountName(repoDir string) string {
	return filepath.Base(filepath.Clean(repoDir))
}

// rewriteGitMount serves next (a git smart-HTTP handler expecting paths
// under /<mount>/) at /<alias>/ by rewriting the request path. Org repos
// live on disk at <orgs-dir>/<org>/repo.git, so http-backend only answers
// under /repo.git/ - but `git clone .../repo.git` names every checkout
// "repo". The org-named alias (<org>.git) is what gets advertised, so a
// plain clone lands in a folder named after the org.
func rewriteGitMount(alias, mount string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + mount + strings.TrimPrefix(r.URL.Path, "/"+alias)
		// Path is authoritative for CGI PATH_INFO only when RawPath is
		// empty; git's URLs carry no escaping, so drop it.
		r2.URL.RawPath = ""
		next.ServeHTTP(w, r2)
	})
}

// gitHTTPBackendPath locates git-http-backend, which ships in git's
// exec-path (e.g. /usr/lib/git-core), not necessarily on PATH.
func gitHTTPBackendPath() (string, error) {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return "", fmt.Errorf("runkod: locate git --exec-path: %w", err)
	}
	path := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := exec.LookPath(path); err != nil {
		return "", fmt.Errorf("runkod: git-http-backend not found at %s: %w", path, err)
	}
	return path, nil
}

// EnableHTTPReceivePack turns on `http.receivepack` for repoDir - off by
// git's own default for safety, but required for smart-HTTP push (git push
// over http/https) to work at all; write-path *policy* is still entirely
// the pre-receive hook's job (hook.go), this only permits the transport.
//
// It also enables uploadpack.allowFilter: without it, a client's
// `clone --filter=blob:none` (the §12.3 blobless-clone workspace substrate,
// and runko-ci checkout's partial clone, §14.4.4) is SILENTLY downgraded to
// a full clone - git warns and proceeds, so nothing fails, the workspace
// just quietly pays for every blob in history.
func EnableHTTPReceivePack(repoDir string) error {
	for _, kv := range [][2]string{
		{"http.receivepack", "true"},
		{"uploadpack.allowFilter", "true"},
		// Without this a `git push -o ...` client errors out with "the
		// receiving end does not support push options" - runko change push
		// uses options to carry §12.2 workspace-branch provenance.
		{"receive.advertisePushOptions", "true"},
	} {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("runkod: git config %s: %w", kv[0], err)
		}
	}
	return nil
}
