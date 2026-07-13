package runkod

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/saxocellphone/runko/platform/receive"
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
	// Admin marks an operator-grade principal: may force-land (§13.5's
	// gate override, 2026-07-08). Never combinable with IsAgent at the
	// enforcement site - agents may not force regardless of flags.
	Admin bool
	// Policy is enforced at receive time when IsAgent (§8.7) - for both
	// change pushes and workspace snapshots. Ignored for human principals.
	Policy receive.AgentPolicy
	// Stored marks a self-service account (§15.1 sign-up) resolved from
	// the store/directory, as opposed to operator flag config. Operator
	// principals are server-wide; stored accounts are membership-gated
	// per org (orghub.go).
	Stored bool
}

// principalFor resolves the API caller's principal from the Authorization
// header (bearer token or Basic name+password - see auth.go). Nil means
// the caller authenticated some other way (deploy token, bot lane) or not
// at all.
func (s *Server) principalFor(r *http.Request) *Principal {
	return s.principalForAuthHeader(r.Header.Get("Authorization"))
}

// principalForAuthHeader is principalFor over a raw Authorization header
// value - the Connect RPC surface (rpc.go) carries headers on
// connect.Request rather than *http.Request.
func (s *Server) principalForAuthHeader(auth string) *Principal {
	return s.callerForAuthHeader(auth).principal
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
	// Store-backed principals (§15.1 sign-up) are always human - no agent
	// policy to enforce - but they must resolve here so workspace
	// owner-only pushes and authored_by attribution treat them as the
	// named identities they are. Accounts are per-org (migration 0017):
	// the row that authenticated this push is (this org, name), resolved
	// through the hub's Directory (orghub.go) when wired, else this
	// store's own rows.
	var lookup func(ctx context.Context, org, name string) (StoredPrincipal, bool, error)
	switch {
	case p.Directory != nil:
		lookup = p.Directory.GetStoredPrincipal
	case p.Store != nil:
		lookup = p.Store.GetStoredPrincipal
	}
	if lookup != nil {
		if sp, found, err := lookup(context.Background(), p.OrgName, name); err == nil && found {
			return &Principal{Name: sp.Name, Stored: true}
		}
	}
	// Ephemeral agent principals (agentprincipal.go): by the time a push
	// reaches the funnel the credential already authenticated, so a live
	// row here arms the §8.7 enforcement (per-change caps, affinity, the
	// DAG nudge) under the agent's task-named identity.
	if p.Store != nil {
		if ap, found, err := p.Store.GetAgentPrincipalByName(context.Background(), name); err == nil && found && ap.Live(time.Now()) {
			return ap.principal()
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

// remoteLane extracts REMOTE_LANE - the bot-lane sibling of REMOTE_USER
// (§14.10.3, stage 17). "" means the push did not authenticate as a lane.
func remoteLane(extraEnv []string) string {
	for _, kv := range extraEnv {
		if v, ok := strings.CutPrefix(kv, "REMOTE_LANE="); ok {
			return v
		}
	}
	return ""
}

// laneByName is principalByName's bot-lane sibling: lanes are flag config
// only (no store-backed lanes exist), so this is a plain registry scan.
func (p *Processor) laneByName(name string) *BotLane {
	if name == "" {
		return nil
	}
	for i := range p.BotLanes {
		if p.BotLanes[i].Name == name {
			return &p.BotLanes[i]
		}
	}
	return nil
}
