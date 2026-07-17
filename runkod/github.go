// GitHub wiring endpoint (2026-07-16): one verb turns
// "wire this org to GitHub" into a single API call. POST
// /api/github/connect verifies the deployment's GitHub App can actually
// push to the named repo (one ls-remote over the wire - reachability and
// App installation in the same probe), persists the wiring in the org's
// settings (github_mirror_repo - it survives restarts), and arms the
// org's mirror worker on the spot. No daemon restart, no per-org flag
// editing; the flag config (--mirror-remote/--org-mirror) keeps working
// and wins over stored wiring at boot.
package runkod

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// errMisconfiguredGithubWiring means GithubRemote was set without the
// Mirror/Directory/SettingsOrg wiring - a cmd/runkod assembly bug, not a
// caller mistake.
var errMisconfiguredGithubWiring = errors.New("github connect: server has GithubRemote but no Mirror/Directory/SettingsOrg wiring")

// validGithubRepoPath is the "owner/name" shape POST /api/github/connect
// accepts - exactly one slash, GitHub's identifier charset on both halves.
func validGithubRepoPath(p string) bool {
	owner, name, ok := strings.Cut(p, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false
	}
	for _, half := range []string{owner, name} {
		for _, r := range half {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			default:
				return false
			}
		}
	}
	return true
}

// handleGithubConnect is gated like mirror unfreeze (admins and the
// deploy token; agents never) - it writes org-level wiring.
func (s *Server) handleGithubConnect(w http.ResponseWriter, r *http.Request) {
	if apiErr := authorizeForceLand(s.principalFor(r), s.laneFor(r)); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	var body struct {
		Repo string `json:"repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Repo == "" {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "repo",
			Message:    "which GitHub repo to wire this org to",
			Suggestion: `POST {"repo": "owner/name"}`,
		}))
		return
	}
	if !validGithubRepoPath(body.Repo) {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_repo", Field: "repo",
			Message:    "repo must be owner/name (the path on the GitHub host, not a URL)",
			Suggestion: "runko github connect --repo acme/monorepo",
		}))
		return
	}
	if s.GithubRemote == nil {
		writeAPIError(w, typedErr(http.StatusPreconditionFailed, clierr.Error{
			Code:       "github_app_not_configured",
			Message:    "this deployment holds no GitHub App credentials, so it cannot mint push tokens",
			Suggestion: "start runkod with --github-app-id and --github-app-key-file (one App credential serves every org), then retry",
		}))
		return
	}
	if s.Mirror == nil || s.Directory == nil || s.SettingsOrg == "" {
		writeAPIError(w, internalErr(errMisconfiguredGithubWiring))
		return
	}

	remote := s.GithubRemote(body.Repo)
	// One wire probe proves the whole chain: the repo exists, the App is
	// installed on it, and a minted installation token authenticates.
	// The githubapp errors are deliberately self-describing ("not
	// installed on X - install it and retry"), so they pass through.
	if _, err := remote.LsRemote("refs/heads/" + s.TrunkRef); err != nil {
		writeAPIError(w, typedErr(http.StatusBadGateway, clierr.Error{
			Code:       "github_unreachable",
			Message:    err.Error(),
			Suggestion: "install the GitHub App on " + body.Repo + " (GitHub -> Settings -> GitHub Apps -> Install App) or check the repo name",
		}))
		return
	}

	settings, err := s.Directory.GetOrgSettings(r.Context(), s.SettingsOrg)
	if err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	previous := settings.GithubMirrorRepo
	settings.GithubMirrorRepo = body.Repo
	if err := s.Directory.UpdateOrgSettings(r.Context(), s.SettingsOrg, settings); err != nil {
		writeAPIError(w, internalErr(err))
		return
	}

	s.Mirror.SetRemote(remote)
	s.Mirror.Trigger()
	log.Printf("runkod: org %s wired to github repo %s by %s (was %q) - mirror armed and first sync triggered",
		s.SettingsOrg, body.Repo, forceActor(s.principalFor(r)), previous)
	writeJSON(w, http.StatusOK, map[string]string{
		"org":        s.SettingsOrg,
		"repo":       body.Repo,
		"remote_url": remote.URL,
		"mirror":     "armed; first sync triggered (watch GET /api/mirror/status)",
	})
}
