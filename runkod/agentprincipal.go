// Ephemeral agent identity (§15.1's third principal kind, 2026-07-11).
// Agents come and go - often many at once - so their credentials are
// MINTED per task over the API and die by TTL: no operator config, no
// secret edits, no restarts. The name embeds the task
// (agent-<task>-<suffix>), so attribution everywhere - authored_by,
// workspace owner, the §8.7 badge - answers "what was this agent doing"
// by construction. Minting one credential arms every agent-scoped
// enforcement already built: receive-time policy (per-change caps, the
// DAG nudge, affinity), single-use workspaces, no-sharing owner checks,
// self- and agent-approval denial.
package runkod

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/receive"
)

// AgentPrincipal is one ephemeral identity row. Only the token's sha256
// is ever stored - the token itself is returned exactly once, at mint.
type AgentPrincipal struct {
	Name      string
	Task      string
	TokenHash string // hex sha256 of the bearer token
	CreatedBy string // minting principal's name; "" for the deploy token
	CreatedAt time.Time
	ExpiresAt time.Time
	Revoked   bool
}

// Live reports whether the credential still authenticates.
func (ap AgentPrincipal) Live(now time.Time) bool {
	return !ap.Revoked && now.Before(ap.ExpiresAt)
}

// principal projects the row onto the enforcement type every guard consumes.
// The caller resolves policy via agentPolicyFor (the org's stored override, or
// DefaultAgentPolicy() when none) - so a per-org policy governs this agent.
func (ap AgentPrincipal) principal(policy receive.AgentPolicy) *Principal {
	return &Principal{Name: ap.Name, IsAgent: true, Policy: policy}
}

const (
	agentTokenBytes  = 32
	agentDefaultTTL  = 8 * time.Hour
	agentMaxTTL      = 7 * 24 * time.Hour
	agentSweepGrace  = 30 * 24 * time.Hour // expired rows swept at mint time past this
	agentNamePrefix  = "agent-"
	agentMintRetries = 4 // random-suffix collisions are ~impossible; retry anyway
)

// taskSlugPattern keeps task slugs safe as ref segments, URL path
// segments, and git Basic-auth usernames (no slashes - they would need
// percent-encoding in remote URLs).
var taskSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func hashAgentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mintAgentPrincipalCore creates the identity: agent-<task>-<suffix>, a
// random 256-bit token (returned once, stored only as its hash), TTL
// clamped to the cap. minter is the creating principal - an AGENT may
// never mint (no self-replication, no extending its own lifetime).
func (s *Server) mintAgentPrincipalCore(ctx context.Context, task string, ttl time.Duration, minter *Principal) (AgentPrincipal, string, *apiError) {
	if minter != nil && minter.IsAgent {
		return AgentPrincipal{}, "", typedErr(http.StatusForbidden, clierr.Error{
			Code: "agents_cannot_mint", Field: "caller",
			Message:    "an agent principal may not mint agent principals - no self-replication, no extending its own lifetime",
			Suggestion: "the harness (or a human credential) mints the task identity at task start",
		})
	}
	if !taskSlugPattern.MatchString(task) {
		return AgentPrincipal{}, "", typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_task_slug", Field: "task",
			Message:    fmt.Sprintf("%q is not a valid task slug", task),
			Suggestion: "lowercase letters, digits, dashes; up to 40 chars; e.g. fix-rail-alignment",
		})
	}
	if ttl <= 0 {
		ttl = agentDefaultTTL
	}
	if ttl > agentMaxTTL {
		ttl = agentMaxTTL
	}

	createdBy := ""
	if minter != nil {
		createdBy = minter.Name
	}

	var lastErr error
	for i := 0; i < agentMintRetries; i++ {
		suffix := make([]byte, 2)
		if _, err := rand.Read(suffix); err != nil {
			return AgentPrincipal{}, "", internalErr(err)
		}
		tokenRaw := make([]byte, agentTokenBytes)
		if _, err := rand.Read(tokenRaw); err != nil {
			return AgentPrincipal{}, "", internalErr(err)
		}
		token := hex.EncodeToString(tokenRaw)

		ap := AgentPrincipal{
			Name:      agentNamePrefix + task + "-" + hex.EncodeToString(suffix),
			Task:      task,
			TokenHash: hashAgentToken(token),
			CreatedBy: createdBy,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(ttl),
		}
		minted, err := s.Store.MintAgentPrincipal(ctx, ap)
		if err != nil {
			lastErr = err // presumably a name collision; new suffix, retry
			continue
		}
		return minted, token, nil
	}
	return AgentPrincipal{}, "", internalErr(fmt.Errorf("mint agent principal: %w", lastErr))
}

// agentPrincipalResponse is the wire shape; Token is present ONLY in the
// mint response.
type agentPrincipalResponse struct {
	Name      string    `json:"name"`
	Task      string    `json:"task"`
	Token     string    `json:"token,omitempty"`
	CreatedBy string    `json:"created_by"`
	ExpiresAt time.Time `json:"expires_at"`
	Live      bool      `json:"live"`
	Revoked   bool      `json:"revoked"`
}

func toAgentResponse(ap AgentPrincipal, token string) agentPrincipalResponse {
	return agentPrincipalResponse{
		Name: ap.Name, Task: ap.Task, Token: token,
		CreatedBy: ap.CreatedBy, ExpiresAt: ap.ExpiresAt,
		Live: ap.Live(time.Now()), Revoked: ap.Revoked,
	}
}

func (s *Server) handleMintAgentPrincipal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Task       string `json:"task"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with task (and optional ttl_seconds)",
		})
		return
	}
	ap, token, apiErr := s.mintAgentPrincipalCore(r.Context(), req.Task, time.Duration(req.TTLSeconds)*time.Second, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, toAgentResponse(ap, token))
}

func (s *Server) handleListAgentPrincipals(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListAgentPrincipals(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]agentPrincipalResponse, len(list))
	for i, ap := range list {
		out[i] = toAgentResponse(ap, "")
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeAgentPrincipal(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok, err := s.Store.GetAgentPrincipalByName(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "agent_not_found", Field: "name",
			Message:    fmt.Sprintf("no agent principal %q", name),
			Suggestion: "runko agent list",
		}))
		return
	}
	if err := s.Store.RevokeAgentPrincipal(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"revoked": name})
}

// agentByToken resolves a bearer token to a LIVE agent principal.
func (s *Server) agentByToken(token string) *Principal {
	if s.Store == nil {
		return nil
	}
	ap, ok, err := s.Store.GetAgentPrincipalByTokenHash(context.Background(), hashAgentToken(token))
	if err != nil || !ok || !ap.Live(time.Now()) {
		return nil
	}
	return ap.principal(agentPolicyFor(context.Background(), s.Directory, s.SettingsOrg))
}

// agentByBasic resolves a name+token Basic pair to a LIVE agent principal
// (both must match - the callerForBasic rule).
func (s *Server) agentByBasic(user, pass string) *Principal {
	if s.Store == nil {
		return nil
	}
	ap, ok, err := s.Store.GetAgentPrincipalByName(context.Background(), user)
	if err != nil || !ok || !ap.Live(time.Now()) {
		return nil
	}
	if !constantTimeEquals(hashAgentToken(pass), ap.TokenHash) {
		return nil
	}
	return ap.principal(agentPolicyFor(context.Background(), s.Directory, s.SettingsOrg))
}
