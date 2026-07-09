// Credential resolution for every API surface (§15.1's interim named-token
// principals, stage 12c; extended with HTTP Basic sign-in for the web UI,
// 2026-07-07). One resolver behind requireAuth (REST), rpcMiddleware
// (Connect), and requireGitAuth (smart-HTTP), so "who is this caller" is a
// single computation everywhere - the same anti-drift stance actions.go
// takes for gate semantics.
//
// Two credential forms:
//
//	Authorization: Bearer <token>       - machines (CLI, runko-ci, MCP,
//	  bot lanes). The token alone selects the identity, as before.
//	Authorization: Basic user:password  - humans (web sign-in, git over
//	  HTTP). A named principal authenticates with name + its token as the
//	  password, BOTH matching; the anonymous deploy token works as the
//	  password with any username (the documented `git clone
//	  http://user:<token>@...` form).
//
// Deliberately NOT an auth system, still: no issuance, rotation, hashing,
// or federation - passwords are the same shared-secret tokens --principal
// already carries, checked in constant time. That stays OIDC's job
// (§15.1); this exists so a human never has to paste a raw bearer token
// into a browser.
package runkod

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// caller is one resolved credential: ok reports whether ANY credential
// matched; principal/lane are non-nil when the credential named one.
// (ok with both nil is the anonymous deploy token.) deniedOrg means the
// credential itself was VALID but the account is not a member of this
// server's org (multi-org, orghub.go) - surfaced as 403, never 401, so a
// client can tell "wrong password" from "wrong org".
type caller struct {
	ok        bool
	deniedOrg bool
	principal *Principal
	lane      *BotLane
}

// orgDeniedErr is the structured 403 for a valid account outside this org.
func orgDeniedErr(org string) *apiError {
	return typedErr(http.StatusForbidden, clierr.Error{
		Code: "not_org_member", Field: "org",
		Message:    fmt.Sprintf("your account is not a member of org %q", org),
		Suggestion: "an org admin (or an operator) can add you: POST /api/orgs/" + org + "/members",
	})
}

// callerForAuthHeader resolves an Authorization header value.
func (s *Server) callerForAuthHeader(auth string) caller {
	if token, found := strings.CutPrefix(auth, "Bearer "); found {
		return s.callerForBearer(token)
	}
	if b64, found := strings.CutPrefix(auth, "Basic "); found {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return caller{}
		}
		user, pass, found := strings.Cut(string(raw), ":")
		if !found {
			return caller{}
		}
		return s.callerForBasic(user, pass)
	}
	return caller{}
}

func (s *Server) callerForBearer(token string) caller {
	if constantTimeEquals(token, s.Token) {
		return caller{ok: true}
	}
	// Bot-lane tokens are full API clients too (§8.8 "internal bots: same
	// CLI/API surface") - lane semantics apply only at the land gate and
	// the merge-requirements view.
	for i := range s.BotLanes {
		if constantTimeEquals(token, s.BotLanes[i].Token) {
			return caller{ok: true, lane: &s.BotLanes[i]}
		}
	}
	for i := range s.Principals {
		if constantTimeEquals(token, s.Principals[i].Token) {
			return caller{ok: true, principal: &s.Principals[i]}
		}
	}
	return caller{}
}

// callerForBasic resolves a Basic user/password pair. A principal needs
// BOTH name and password to match - "bob:<alice's password>" must not
// authenticate as alice on the API. (The git transport additionally keeps
// its historical password-only principal resolution for existing remotes;
// see requireGitAuth.)
func (s *Server) callerForBasic(user, pass string) caller {
	for i := range s.Principals {
		nameOK := constantTimeEquals(user, s.Principals[i].Name)
		passOK := constantTimeEquals(pass, s.Principals[i].Token)
		if nameOK && passOK {
			return caller{ok: true, principal: &s.Principals[i]}
		}
	}
	for i := range s.BotLanes {
		nameOK := constantTimeEquals(user, s.BotLanes[i].Name)
		passOK := constantTimeEquals(pass, s.BotLanes[i].Token)
		if nameOK && passOK {
			return caller{ok: true, lane: &s.BotLanes[i]}
		}
	}
	if constantTimeEquals(pass, s.Token) {
		return caller{ok: true}
	}
	// Store-backed principals (§15.1 sign-up, signup.go): checked LAST so
	// operator config always wins a name. The PBKDF2 verification is
	// cached per (name, password) pair - Basic rides on every request.
	// Accounts are server-global (migration 0007): on an org-scoped
	// server (orghub.go) the credential can be perfectly valid and still
	// denied here - membership in THIS org is part of authentication.
	if dir := s.accountLookup(); dir != nil {
		if sp, found, err := dir(context.Background(), user); err == nil && found {
			if s.credCache.hit(user, pass) || verifyCredential(pass, sp.CredentialHash) {
				s.credCache.remember(user, pass)
				if s.OrgName != "" && s.Directory != nil {
					role, member, err := s.Directory.OrgMemberRole(context.Background(), s.OrgName, sp.Name)
					if err != nil || !member {
						return caller{deniedOrg: true}
					}
					// The org role must survive into the synthesized
					// principal: authorizeForceLand (and mirror unfreeze)
					// gate on Principal.Admin, and dropping the role here
					// meant an org's own admin could not force-land in
					// their org - only config operator principals could
					// (migration-findings #24, closed 2026-07-09).
					return caller{ok: true, principal: &Principal{Name: sp.Name, Stored: true, Admin: role == "admin"}}
				}
				return caller{ok: true, principal: &Principal{Name: sp.Name, Stored: true}}
			}
		}
	}
	return caller{}
}

// accountLookup returns the store-backed account resolver: the global
// Directory when the hub wired one (org-scoped servers MUST see all
// accounts, not their own store's empty map), else this server's Store.
func (s *Server) accountLookup() func(context.Context, string) (StoredPrincipal, bool, error) {
	if s.Directory != nil {
		return s.Directory.GetStoredPrincipal
	}
	if s.Store != nil {
		return s.Store.GetStoredPrincipal
	}
	return nil
}
