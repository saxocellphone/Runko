package runkod

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/receive"
)

// Principal is one named-token identity (docs/design.md §15.1's interim
// registry, decided 2026-07-07, §28.3 stage 12c): {name, token, is_agent,
// policy}, generalizing what §14.10.2 bot lanes already do with per-lane
// tokens. Deliberately NOT an auth system - no issuance, rotation, or
// federation (that stays OIDC's job); it exists because four already-built
// enforcement points are inert without any principal identity at all:
// self-approval denial (§8.7), authored_by/landed_by attribution (§7.5),
// owner-only workspace-snapshot push (§12.2), and receive-time AgentPolicy
// evaluation (§8.7 - built at stage 6, unreachable while every caller is
// the same anonymous deploy token).
//
// A Principal is distinct from a BotLane: a lane changes WHAT the merge
// gate requires (owners waived within a path allowlist); a principal
// changes WHO the caller is. Both are full API clients.
type Principal struct {
	Name    string
	Token   string
	IsAgent bool
	// Policy is enforced at receive time when IsAgent (§8.7) - for both
	// change pushes and workspace snapshots. Ignored for human principals.
	Policy receive.AgentPolicy
}

// principalFor resolves the API caller's principal from the Authorization
// header (constant-time token match, like laneFor). Nil means the caller
// authenticated some other way (deploy token, bot lane) or not at all.
func (s *Server) principalFor(r *http.Request) *Principal {
	return s.principalForAuthHeader(r.Header.Get("Authorization"))
}

// principalForAuthHeader is principalFor over a raw Authorization header
// value - the Connect RPC surface (rpc.go) carries headers on
// connect.Request rather than *http.Request.
func (s *Server) principalForAuthHeader(auth string) *Principal {
	for i := range s.Principals {
		want := "Bearer " + s.Principals[i].Token
		if subtle.ConstantTimeCompare([]byte(auth), []byte(want)) == 1 {
			return &s.Principals[i]
		}
	}
	return nil
}

// principalForBasicAuth resolves a git smart-HTTP caller (HTTP Basic; the
// password carries the token, §14.11).
func (s *Server) principalForBasicAuth(pass string) *Principal {
	for i := range s.Principals {
		if subtle.ConstantTimeCompare([]byte(pass), []byte(s.Principals[i].Token)) == 1 {
			return &s.Principals[i]
		}
	}
	return nil
}

// principalByName is the Processor-side lookup: by the time a push reaches
// the pre-receive funnel, identity is a REMOTE_USER name (set by
// requireGitAuth, inherited through http-backend -> receive-pack -> hook,
// forwarded hook->daemon like the quarantine vars), not a token.
func (p *Processor) principalByName(name string) *Principal {
	if name == "" {
		return nil
	}
	for i := range p.Principals {
		if p.Principals[i].Name == name {
			return &p.Principals[i]
		}
	}
	return nil
}

// remoteUser extracts REMOTE_USER from the env slice the hook forwarded.
// "" means the push authenticated with the anonymous deploy token.
func remoteUser(extraEnv []string) string {
	for _, kv := range extraEnv {
		if v, ok := strings.CutPrefix(kv, "REMOTE_USER="); ok {
			return v
		}
	}
	return ""
}
