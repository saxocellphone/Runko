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

// callerForAuthHeader resolves an Authorization header value, applying
// this server's org-membership gate to store-backed accounts.
func (s *Server) callerForAuthHeader(auth string) caller {
	return s.callerForAuthHeaderOpts(auth, true)
}

// callerForAuthHeaderGlobal resolves identity WITHOUT the org-membership
// gate - for the hub's global-account surfaces (org listing/creation,
// orghub.go): "who are you" is server-global even when "what may you
// reach" is org-scoped.
func (s *Server) callerForAuthHeaderGlobal(auth string) caller {
	return s.callerForAuthHeaderOpts(auth, false)
}

func (s *Server) callerForAuthHeaderOpts(auth string, gated bool) caller {
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
		return s.callerForBasicOpts(user, pass, gated)
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
	// Ephemeral agent principals (agentprincipal.go): one indexed lookup
	// by token hash; expired/revoked rows do not authenticate. Org-scoped
	// rows in THIS server's store, so no membership gate applies.
	if pr := s.agentByToken(token); pr != nil {
		return caller{ok: true, principal: pr}
	}
	return caller{}
}

// callerForBasic resolves a Basic user/password pair. A principal needs
// BOTH name and password to match - "bob:<alice's password>" must not
// authenticate as alice on the API. (The git transport additionally keeps
// its historical password-only principal resolution for existing remotes;
// see requireGitAuth.)
func (s *Server) callerForBasic(user, pass string) caller {
	return s.callerForBasicOpts(user, pass, true)
}

func (s *Server) callerForBasicOpts(user, pass string, gated bool) caller {
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
	// Ephemeral agent principals: name + token both matching, like every
	// Basic credential; cheap (one sha256) so checked before the PBKDF2
	// account path.
	if pr := s.agentByBasic(user, pass); pr != nil {
		return caller{ok: true, principal: pr}
	}
	// Store-backed principals (§15.1 sign-up, signup.go): checked LAST so
	// operator config always wins a name. The PBKDF2 verification is
	// cached per (org, name, password) - Basic rides on every request.
	// Accounts are PER-ORG (migration 0017, user direction 2026-07-13,
	// superseding 0007's global rows): the account that signs in here is
	// (this org, name); the same name elsewhere is someone else's
	// account. A credential that verifies only against ANOTHER org's
	// same-named account answers deniedOrg - 403, never 401 - keeping
	// "wrong password" and "wrong org" distinguishable.
	if lookup := s.accountLookup(); lookup != nil {
		if sp, found, err := lookup(context.Background(), s.OrgName, user); err == nil && found {
			if s.credCache.hit(credKey(s.OrgName, user), pass) || verifyCredential(pass, sp.CredentialHash) {
				s.credCache.remember(credKey(s.OrgName, user), pass)
				if gated && s.OrgName != "" && s.Directory != nil {
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
		// No in-org account (or its password didn't match): when the same
		// credential verifies against another org's account, the answer
		// is "wrong org", not "wrong password". Hub-global surfaces
		// (gated=false) go further and RESOLVE the cross-org identity -
		// "who are you" is server-global even though reach is per-org.
		if s.Directory != nil {
			if sp, ok := s.crossOrgAccount(user, pass); ok {
				if gated {
					return caller{deniedOrg: true}
				}
				return caller{ok: true, principal: &Principal{Name: sp.Name, Stored: true}}
			}
		}
	}
	return caller{}
}

// crossOrgAccount finds an account with this name IN ANOTHER ORG whose
// hash the password verifies - the "valid credential, wrong org" case.
func (s *Server) crossOrgAccount(user, pass string) (StoredPrincipal, bool) {
	rows, err := s.Directory.ListPrincipalOrgs(context.Background(), user)
	if err != nil {
		return StoredPrincipal{}, false
	}
	for _, sp := range rows {
		if sp.Org == s.OrgName {
			continue // the in-org row already had its chance above
		}
		if s.credCache.hit(credKey(sp.Org, user), pass) || verifyCredential(pass, sp.CredentialHash) {
			s.credCache.remember(credKey(sp.Org, user), pass)
			return sp, true
		}
	}
	return StoredPrincipal{}, false
}

// credKey scopes credential-cache entries by org: with per-org accounts
// the same (name, password) may be valid for one org's account and not
// another's. "\x00" can appear in neither an org name nor an account name.
func credKey(org, name string) string { return org + "\x00" + name }

// accountLookup returns the store-backed account resolver: the global
// Directory when the hub wired one (org-scoped servers MUST see every
// org's account rows, not their own store's empty map), else this
// server's Store.
func (s *Server) accountLookup() func(ctx context.Context, org, name string) (StoredPrincipal, bool, error) {
	if s.Directory != nil {
		return s.Directory.GetStoredPrincipal
	}
	if s.Store != nil {
		return s.Store.GetStoredPrincipal
	}
	return nil
}
