// Invite requests (§15.1, decided 2026-07-13): the ASK half of the
// invite-code gate. A code-gated deployment (--signup-code) had no way
// for a stranger to request the code - the login gate dead-ended. This
// file is the intake (public POST, signup.go's core/handler shape) and
// the mailer's drain surface (operator-only due feed + sent/failed acks;
// the runko-mailer service in mailer/ does the actual SMTP). Rows carry
// the §14.4.1 webhook-outbox lifecycle; backoff and dead-lettering are
// computed here, never in the mailer.
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/saxocellphone/runko/internal/clierr"
)

const (
	maxInviteName    = 120
	maxInviteEmail   = 254 // RFC 5321's path limit; net/mail validates shape
	maxInviteMessage = 2000

	// inviteRateLimit/-Window: per-IP fixed window on the public intake.
	// Best-effort (an ingress without X-Forwarded-For degrades the key to
	// the ingress address); the backlog cap below is the hard backstop.
	inviteRateLimit  = 5
	inviteRateWindow = time.Hour
	// inviteBacklogCap refuses new intake while this many live rows wait -
	// bounds both mailbox-bombing and table growth.
	inviteBacklogCap = 500

	// Local backoff constants so invite retry stays tunable independently
	// of webhook delivery policy (which shares only the curve/attempt cap).
	inviteBackoffBase = time.Minute
	inviteBackoffMax  = time.Hour
)

type inviteRequestBody struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
	// Website is the honeypot: the form renders it off-screen, humans
	// leave it empty, and a filled value gets a silent 202 with nothing
	// stored - bots learn nothing.
	Website string `json:"website"`
}

// inviteRequestCore validates and stores one intake submission; the REST
// handler decodes over this (the signup.go pattern). A "" return means
// answer the idempotent 202; the honeypot and duplicate-email paths
// deliberately return success without storing.
func (s *Server) inviteRequestCore(ctx context.Context, req inviteRequestBody, ip string) *apiError {
	if !s.AllowInviteRequests {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "invite_requests_disabled", Field: "invite",
			Message:    "this control plane does not take invite requests",
			Suggestion: "ask whoever runs this Runko for an invite another way",
		})
	}
	if req.Website != "" {
		return nil // honeypot: silent success, nothing stored
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxInviteName || hasControlChars(name) {
		return typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_name", Field: "name",
			Message:    "name must be 1-120 printable characters",
			Suggestion: "tell us what to call you in the reply",
		})
	}
	email := strings.TrimSpace(req.Email)
	// Require the bare-address form: a display name or any CR/LF here
	// would otherwise ride into the mailer's Reply-To header.
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > maxInviteEmail {
		return typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_email", Field: "email",
			Message:    "email must be a plain address like you@example.com",
			Suggestion: "the invite code arrives as a reply to this address",
		})
	}
	if len(req.Message) > maxInviteMessage {
		return typedErr(http.StatusBadRequest, clierr.Error{
			Code: "message_too_long", Field: "message",
			Message:    fmt.Sprintf("message must be at most %d characters", maxInviteMessage),
			Suggestion: "a sentence or two is plenty",
		})
	}
	rateLimited := typedErr(http.StatusTooManyRequests, clierr.Error{
		Code: "rate_limited", Field: "invite",
		Message:    "too many invite requests right now",
		Suggestion: "try again in an hour",
	})
	if !s.inviteLimiter.allow(ip, time.Now()) {
		return rateLimited
	}
	if live, err := s.Store.CountLiveInviteRequests(ctx); err != nil {
		return internalErr(err)
	} else if live >= inviteBacklogCap {
		return rateLimited
	}
	if _, _, err := s.Store.CreateInviteRequest(ctx, name, email, req.Message); err != nil {
		return internalErr(err)
	}
	// created=false (a live request already holds this email) is the same
	// 202 as created=true: the endpoint never becomes an address oracle.
	return nil
}

func hasControlChars(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func (s *Server) handleCreateInviteRequest(w http.ResponseWriter, r *http.Request) {
	var req inviteRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {name, email, message?}",
		}))
		return
	}
	if apiErr := s.inviteRequestCore(r.Context(), req, clientIP(r)); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "received"})
}

// clientIP keys the rate limiter: first X-Forwarded-For hop when an
// ingress forwards it (spoofable - accepted as best-effort), else the
// peer address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// inviteLimiter is a per-key fixed window. No pruning loop: the intake is
// low-volume by design (the window cap bounds each key's slice, and keys
// arrive at human scale; a hot deployment gets the backlog cap first).
type inviteLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *inviteLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < inviteRateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= inviteRateLimit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// The drain surface (due feed + sent/failed acks) is served over Connect
// from runkod's in-boundary contract - see invitefeed.go and
// runkod/proto/mailer/v1 (§13.3.1). This file keeps the public intake and
// the retry/backoff policy the RPC handlers share.
