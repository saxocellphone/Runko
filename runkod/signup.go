// Self-service sign-up (§15.1's interim identity, extended 2026-07-08):
// POST /api/signup creates a store-backed human principal so joining an
// org doesn't require an operator editing daemon flags. Gated hard:
// disabled unless --allow-signup (default-deny posture, §28.3 stage 11c's
// spirit), optionally requiring a shared invite code (--signup-code).
// Operator principals (--principal) always win name lookups; a signup can
// never shadow one. Still deliberately not an auth system - no sessions,
// no rotation, no reset; OIDC remains the real answer (§15.1).
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/saxocellphone/runko/internal/clierr"
)

// principalNamePattern: same conservative charset as workspace ids, plus
// a length cap - the name doubles as git author/attribution text.
var principalNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,62}$`)

const minPasswordLength = 8

type signupRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Code     string `json:"code"`
	// org scopes the account (migration 0017: per-org identity). Set by
	// the caller - the hub passes the signup's target org, the plain
	// default-server handler passes its own OrgName - never decoded from
	// the request body (the org half has its own validated field there).
	org string
}

// signupCore validates and registers one principal; both the REST handler
// and any future transport encode over this (the actions.go pattern).
func (s *Server) signupCore(ctx context.Context, req signupRequest) *apiError {
	if !s.AllowSignup {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "signup_disabled", Field: "signup",
			Message:    "self-service sign-up is not enabled on this control plane",
			Suggestion: "ask an operator for a principal, or have them start runkod with --allow-signup",
		})
	}
	if s.SignupCode != "" && !constantTimeEquals(req.Code, s.SignupCode) {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "bad_signup_code", Field: "code",
			Message:    "this control plane requires an invite code to sign up",
			Suggestion: "ask whoever runs this Runko for the code",
		})
	}
	if !principalNamePattern.MatchString(req.Name) {
		return typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_name", Field: "name",
			Message:    fmt.Sprintf("%q is not a valid principal name", req.Name),
			Suggestion: "2-63 chars: letters, digits, dots, dashes, underscores; start with a letter or digit",
		})
	}
	if len(req.Password) < minPasswordLength {
		return typedErr(http.StatusBadRequest, clierr.Error{
			Code: "weak_password", Field: "password",
			Message:    fmt.Sprintf("password must be at least %d characters", minPasswordLength),
			Suggestion: "pick a longer one - it is your only credential here",
		})
	}

	// Operator-configured names are reserved: config always wins lookups,
	// so a colliding signup would be a confusing dead row at best and a
	// shadowing attempt at worst.
	taken := func() bool {
		for i := range s.Principals {
			if s.Principals[i].Name == req.Name {
				return true
			}
		}
		for i := range s.BotLanes {
			if s.BotLanes[i].Name == req.Name {
				return true
			}
		}
		return false
	}
	nameTaken := typedErr(http.StatusConflict, clierr.Error{
		Code: "name_taken", Field: "name",
		Message:    fmt.Sprintf("the name %q is already in use", req.Name),
		Suggestion: "pick a different name",
	})
	if taken() {
		return nameTaken
	}
	if _, exists, err := s.Store.GetStoredPrincipal(ctx, req.org, req.Name); err != nil {
		return internalErr(err)
	} else if exists {
		return nameTaken
	}

	hash, err := hashCredential(req.Password)
	if err != nil {
		return internalErr(err)
	}
	if err := s.Store.CreatePrincipal(ctx, req.org, req.Name, hash); err != nil {
		// A racing duplicate insert lands here via the unique constraint.
		return nameTaken
	}
	return nil
}

// signupOrRecoverCore is signupCore plus idempotent recovery (finding
// #44): when the name already belongs to a stored account AND the
// presented password verifies, this "signup" is the same person
// re-presenting their own credential - typically retrying after an
// interrupted org-create stranded the account - so the account half is a
// no-op instead of a 409, and the caller proceeds to the org half. The
// front gates (signup enabled, invite code) apply unchanged, and a
// non-matching password keeps the name_taken contract - recovery never
// becomes an oracle beyond what sign-in already answers.
func (s *Server) signupOrRecoverCore(ctx context.Context, req signupRequest) (recovered bool, apiErr *apiError) {
	if lookup := s.accountLookup(); lookup != nil && s.AllowSignup &&
		(s.SignupCode == "" || constantTimeEquals(req.Code, s.SignupCode)) {
		if sp, found, err := lookup(ctx, req.org, req.Name); err == nil && found {
			if s.credCache.hit(credKey(req.org, req.Name), req.Password) || verifyCredential(req.Password, sp.CredentialHash) {
				s.credCache.remember(credKey(req.org, req.Name), req.Password)
				return true, nil
			}
		}
	}
	return false, s.signupCore(ctx, req)
}

// handleSignup is deliberately UNAUTHENTICATED (it mints the credential);
// handleAuthConfig too (the login page needs to know whether to offer
// sign-up before anyone has signed in). Neither leaks anything a 401
// wouldn't: config is two booleans, signup only ever confirms/denies.
func publicCORS(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", method+", OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next(w, r)
	}
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {name, password, code?}",
		}))
		return
	}
	req.org = s.OrgName
	if _, apiErr := s.signupOrRecoverCore(r.Context(), req); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": req.Name})
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"signup_enabled":          s.AllowSignup,
		"code_required":           s.AllowSignup && s.SignupCode != "",
		"invite_requests_enabled": s.AllowInviteRequests,
	})
}
