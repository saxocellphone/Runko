// Bot lanes: §14.10.2's path-scoped auto-land for trusted GitOps writers
// (image bumpers, Renovate), §28.3 stage 11c.
package runkod

import (
	"net/http"

	"github.com/saxocellphone/runko/affected"
)

// BotLane is one trusted bot's land grant (§14.10.2): "a trusted bot is an
// AgentIdentity whose policy grants can_land_changes: true constrained to a
// path allowlist and a required-check set". A lane land waives the human
// owner-approval gate - that is its entire purpose (an image bump must not
// need a human click) - but ONLY for Changes whose every touched path falls
// inside PathAllowlist, and only with the lane's RequiredChecks green on top
// of whatever the tree already requires (project ci.checks + org globals).
// This is strictly stronger than GitHub's all-or-nothing branch-protection
// bypass lists: the bypass itself is path-scoped.
//
// Identity is a per-lane bearer token until real AuthN exists (§15.1) - the
// same v1 trust boundary as the deploy token itself. Lane tokens are full
// API clients (§8.8 "internal bots: same CLI/API surface"); the lane
// semantics apply only where they differ from a human client's: the land
// gate and the merge-requirements view it reads.
type BotLane struct {
	Name  string
	Token string
	// PathAllowlist is affected.MatchPath glob patterns (the same syntax as
	// AgentPolicy.DenylistPaths and root-invalidation patterns). Every
	// touched path of a Change must match at least one pattern for this
	// lane to land it.
	PathAllowlist []string
	// RequiredChecks are check names that must be green for this lane to
	// land, in addition to (never instead of) the checks the tree itself
	// requires. Always non-empty: a lane without its own check set is an
	// unchecked auto-land grant, which §14.10.2 deliberately does not model.
	RequiredChecks []string
}

// laneFor resolves the bot lane a request authenticated as, or nil for the
// main deploy token (or an unauthenticated request - requireAuth rejects
// those before any handler runs).
func (s *Server) laneFor(r *http.Request) *BotLane {
	return s.laneForAuthHeader(r.Header.Get("Authorization"))
}

// laneForAuthHeader is laneFor over a raw Authorization header value, for
// the Connect RPC surface (rpc.go).
func (s *Server) laneForAuthHeader(auth string) *BotLane {
	return s.callerForAuthHeader(auth).lane
}

// pathsOutsideAllowlist returns the touched paths the lane's allowlist does
// not cover - non-empty means the lane cannot land this Change at all.
func (l *BotLane) pathsOutsideAllowlist(paths []string) []string {
	var out []string
	for _, p := range paths {
		matched := false
		for _, pat := range l.PathAllowlist {
			if affected.MatchPath(pat, p) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, p)
		}
	}
	return out
}
