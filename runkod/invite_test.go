package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mailerv1 "github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1"
	"github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1/mailerv1connect"
)

func newInviteServer(t *testing.T, allow bool) (*httptest.Server, *MemStore) {
	t.Helper()
	bare := newBareRepo(t)
	store := NewMemStore()
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret",
		AllowInviteRequests: allow,
		BotLanes:            []BotLane{{Name: "relbot", Token: "relbot-token"}},
		Principals:          []Principal{{Name: "robo", Token: "robo-token", IsAgent: true}},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, store
}

// postInvite posts one intake body from the given (X-Forwarded-For) ip.
func postInvite(t *testing.T, srv *httptest.Server, ip, body string) (int, map[string]any) {
	t.Helper()
	return postIntake(t, srv, "/api/invite-requests", ip, body)
}

func postContact(t *testing.T, srv *httptest.Server, ip, body string) (int, map[string]any) {
	t.Helper()
	return postIntake(t, srv, "/api/contact", ip, body)
}

func postIntake(t *testing.T, srv *httptest.Server, path, ip, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func inviteAPI(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func errCode(body map[string]any) string {
	code, _ := body["Code"].(string)
	return code
}

// The drain surface is Connect now (InviteFeedService, §13.3.1): these
// helpers drive the real generated client against the real handler, token
// in the Authorization header exactly like the mailer.
func inviteFeed(t *testing.T, srv *httptest.Server, token string, call func(client mailerv1connect.InviteFeedServiceClient, opt connect.ClientOption) error) error {
	t.Helper()
	opt := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		}
	}))
	return call(mailerv1connect.NewInviteFeedServiceClient(srv.Client(), srv.URL, opt), opt)
}

func dueRequests(t *testing.T, srv *httptest.Server) []*mailerv1.InviteRequest {
	t.Helper()
	var out []*mailerv1.InviteRequest
	err := inviteFeed(t, srv, "sekret", func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
		resp, err := c.ListDue(context.Background(), connect.NewRequest(&mailerv1.ListDueRequest{}))
		if err != nil {
			return err
		}
		out = resp.Msg.Requests
		return nil
	})
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	return out
}

// The intake end to end: disabled by default (and discoverable as such),
// then a stored request flows through the due feed and a sent ack
// removes it.
func TestInviteRequestFlow(t *testing.T) {
	srv, _ := newInviteServer(t, false)
	code, body := postInvite(t, srv, "", `{"name":"Ada","email":"ada@example.com"}`)
	if code != http.StatusForbidden || errCode(body) != "invite_requests_disabled" {
		t.Fatalf("disabled intake: %d %v", code, body)
	}
	_, cfg := inviteAPI(t, srv, http.MethodGet, "/api/auth/config", "", "")
	if cfg["invite_requests_enabled"] != false {
		t.Fatalf("auth config should advertise the disabled intake: %v", cfg)
	}

	srv, _ = newInviteServer(t, true)
	_, cfg = inviteAPI(t, srv, http.MethodGet, "/api/auth/config", "", "")
	if cfg["invite_requests_enabled"] != true {
		t.Fatalf("auth config should advertise the enabled intake: %v", cfg)
	}
	code, body = postInvite(t, srv, "", `{"name":"Ada Lovelace","email":"ada@example.com","message":"analytical engines need CI too"}`)
	if code != http.StatusAccepted || body["status"] != "received" {
		t.Fatalf("intake: %d %v", code, body)
	}

	reqs := dueRequests(t, srv)
	if len(reqs) != 1 {
		t.Fatalf("due feed: want 1 row, got %v", reqs)
	}
	row := reqs[0]
	if row.Name != "Ada Lovelace" || row.Email != "ada@example.com" ||
		row.Message != "analytical engines need CI too" {
		t.Fatalf("due row: %v", row)
	}

	err := inviteFeed(t, srv, "sekret", func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
		ack, err := c.MarkSent(context.Background(), connect.NewRequest(&mailerv1.MarkSentRequest{Id: row.Id}))
		if err != nil {
			return err
		}
		if ack.Msg.Status != "sent" {
			t.Fatalf("sent ack: %v", ack.Msg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if reqs := dueRequests(t, srv); len(reqs) != 0 {
		t.Fatalf("sent row still due: %v", reqs)
	}
	err = inviteFeed(t, srv, "sekret", func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
		_, err := c.MarkSent(context.Background(), connect.NewRequest(&mailerv1.MarkSentRequest{Id: "nope"}))
		return err
	})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown id ack: want NotFound, got %v", err)
	}
}

// The drain surface is operator-only: bot lanes and agent principals are
// full API clients elsewhere, but invite rows carry strangers' emails.
func TestInviteFeedOperatorOnly(t *testing.T) {
	srv, _ := newInviteServer(t, true)
	err := inviteFeed(t, srv, "", func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
		_, err := c.ListDue(context.Background(), connect.NewRequest(&mailerv1.ListDueRequest{}))
		return err
	})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous feed: want Unauthenticated, got %v", err)
	}
	for _, token := range []string{"relbot-token", "robo-token"} {
		err := inviteFeed(t, srv, token, func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
			_, err := c.ListDue(context.Background(), connect.NewRequest(&mailerv1.ListDueRequest{}))
			return err
		})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("feed with %s: want PermissionDenied, got %v", token, err)
		}
		err = inviteFeed(t, srv, token, func(c mailerv1connect.InviteFeedServiceClient, _ connect.ClientOption) error {
			_, err := c.MarkSent(context.Background(), connect.NewRequest(&mailerv1.MarkSentRequest{Id: "x"}))
			return err
		})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("ack with %s: want PermissionDenied, got %v", token, err)
		}
	}
}

func TestInviteRequestValidation(t *testing.T) {
	srv, _ := newInviteServer(t, true)
	cases := []struct {
		name, body, code string
	}{
		{"empty name", `{"name":"","email":"a@b.com"}`, "invalid_name"},
		{"control chars", `{"name":"a\nb","email":"a@b.com"}`, "invalid_name"},
		{"long name", fmt.Sprintf(`{"name":%q,"email":"a@b.com"}`, strings.Repeat("n", 121)), "invalid_name"},
		{"not an email", `{"name":"Ada","email":"not-an-email"}`, "invalid_email"},
		{"display name form", `{"name":"Ada","email":"Ada <ada@example.com>"}`, "invalid_email"},
		{"long message", fmt.Sprintf(`{"name":"Ada","email":"a@b.com","message":%q}`, strings.Repeat("m", 2001)), "message_too_long"},
		{"not json", `nope`, "bad_request"},
	}
	for _, tc := range cases {
		code, body := postInvite(t, srv, "", tc.body)
		if code != http.StatusBadRequest || errCode(body) != tc.code {
			t.Errorf("%s: want 400 %s, got %d %v", tc.name, tc.code, code, body)
		}
	}
	if reqs := dueRequests(t, srv); len(reqs) != 0 {
		t.Fatalf("rejected intake stored rows: %v", reqs)
	}
}

// Honeypot and duplicate-email both answer the same 202 as success - the
// endpoint never confirms an address or teaches a bot - but store at most
// one live row.
func TestInviteRequestHoneypotAndDuplicate(t *testing.T) {
	srv, _ := newInviteServer(t, true)
	code, body := postInvite(t, srv, "", `{"name":"Bot","email":"bot@spam.com","website":"spam.example"}`)
	if code != http.StatusAccepted {
		t.Fatalf("honeypot: want silent 202, got %d %v", code, body)
	}
	if reqs := dueRequests(t, srv); len(reqs) != 0 {
		t.Fatalf("honeypot stored a row: %v", reqs)
	}

	for _, email := range []string{"ada@example.com", "ADA@example.com"} {
		code, _ := postInvite(t, srv, "", fmt.Sprintf(`{"name":"Ada","email":%q}`, email))
		if code != http.StatusAccepted {
			t.Fatalf("intake %s: %d", email, code)
		}
	}
	if reqs := dueRequests(t, srv); len(reqs) != 1 {
		t.Fatalf("duplicate live email: want 1 row, got %v", reqs)
	}
}

func TestInviteRequestRateLimit(t *testing.T) {
	srv, _ := newInviteServer(t, true)
	for i := 0; i < inviteRateLimit; i++ {
		code, body := postInvite(t, srv, "10.0.0.9",
			fmt.Sprintf(`{"name":"n","email":"n%d@example.com"}`, i))
		if code != http.StatusAccepted {
			t.Fatalf("post %d: %d %v", i, code, body)
		}
	}
	code, body := postInvite(t, srv, "10.0.0.9", `{"name":"n","email":"over@example.com"}`)
	if code != http.StatusTooManyRequests || errCode(body) != "rate_limited" {
		t.Fatalf("over the window: want 429 rate_limited, got %d %v", code, body)
	}
	// Another key is unaffected: the window is per-IP.
	if code, _ := postInvite(t, srv, "10.0.0.10", `{"name":"n","email":"other@example.com"}`); code != http.StatusAccepted {
		t.Fatalf("second ip: want 202, got %d", code)
	}
}

// The outbox lifecycle on the store: failures back off with a bumped
// next_attempt_at until MaxDeliveryAttempts dead-letters the row; a
// dead-lettered or sent row leaves the due feed; success clears it.
func TestInviteRequestRetryLifecycle(t *testing.T) {
	store := NewMemStore()
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return base }
	ctx := context.Background()

	req, created, err := store.CreateInviteRequest(ctx, kindInvite, "Ada", "ada@example.com", "hi")
	if err != nil || !created {
		t.Fatalf("create: %v created=%v", err, created)
	}

	now := base
	for attempt := 1; ; attempt++ {
		got, err := store.RecordInviteSendResult(ctx, req.ID, "smtp: 535 auth failed",
			inviteBackoffBase, inviteBackoffMax, now)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if got.Attempt != attempt || got.LastError != "smtp: 535 auth failed" {
			t.Fatalf("attempt %d bookkeeping: %+v", attempt, got)
		}
		if !got.NextAttemptAt.After(now) {
			t.Fatalf("attempt %d: next_attempt_at did not back off: %+v", attempt, got)
		}
		if due, _ := store.ListDueInviteRequests(ctx, now); len(due) != 0 {
			t.Fatalf("attempt %d: row due before its backoff: %v", attempt, due)
		}
		if got.Status == "dead_letter" {
			if due, _ := store.ListDueInviteRequests(ctx, now.Add(48*time.Hour)); len(due) != 0 {
				t.Fatalf("dead-lettered row still drains: %v", due)
			}
			break
		}
		if got.Status != "failed" {
			t.Fatalf("attempt %d: status %q", attempt, got.Status)
		}
		if due, _ := store.ListDueInviteRequests(ctx, got.NextAttemptAt); len(due) != 1 {
			t.Fatalf("attempt %d: row not due after its backoff", attempt)
		}
		now = got.NextAttemptAt
	}

	// A fresh row acked with no error is sent, immediately out of the feed.
	req2, _, _ := store.CreateInviteRequest(ctx, kindInvite, "Grace", "grace@example.com", "")
	got, err := store.RecordInviteSendResult(ctx, req2.ID, "", inviteBackoffBase, inviteBackoffMax, base)
	if err != nil || got.Status != "sent" {
		t.Fatalf("sent ack: %v %+v", err, got)
	}
	if due, _ := store.ListDueInviteRequests(ctx, base.Add(time.Hour)); len(due) != 0 {
		t.Fatalf("sent row still due: %v", due)
	}
}

// The contact intake (POST /api/contact, 2026-07-20): the landing page's
// contact form rides the invite outbox as kind=contact - same gate, same
// honeypot, its own dedupe scope, and a required message.
func TestContactIntake(t *testing.T) {
	srv, _ := newInviteServer(t, false)
	code, body := postContact(t, srv, "", `{"name":"Ada","email":"ada@example.com","message":"hello"}`)
	if code != http.StatusForbidden || errCode(body) != "contact_disabled" {
		t.Fatalf("disabled contact intake: %d %v", code, body)
	}

	srv, _ = newInviteServer(t, true)
	// A contact message with nothing to say is refused; an invite ask
	// without one stays fine.
	code, body = postContact(t, srv, "", `{"name":"Ada","email":"ada@example.com","message":"  "}`)
	if code != http.StatusBadRequest || errCode(body) != "empty_message" {
		t.Fatalf("empty contact message: %d %v", code, body)
	}
	if code, _ := postInvite(t, srv, "", `{"name":"Ada","email":"ada@example.com"}`); code != http.StatusAccepted {
		t.Fatalf("invite ask without message: want 202, got %d", code)
	}

	// The same address can hold a live contact message NEXT TO its live
	// invite request; a second contact message collapses (per-kind dedupe).
	for i := 0; i < 2; i++ {
		code, body = postContact(t, srv, "", `{"name":"Ada","email":"ada@example.com","message":"how do I self-host?"}`)
		if code != http.StatusAccepted {
			t.Fatalf("contact intake %d: %d %v", i, code, body)
		}
	}
	reqs := dueRequests(t, srv)
	if len(reqs) != 2 {
		t.Fatalf("due feed: want invite+contact for one address, got %v", reqs)
	}
	kinds := map[string]int{}
	for _, r := range reqs {
		kinds[r.Kind]++
	}
	if kinds["invite"] != 1 || kinds["contact"] != 1 {
		t.Fatalf("due kinds: %v", kinds)
	}

	// The honeypot stays a silent 202 storing nothing.
	code, _ = postContact(t, srv, "", `{"name":"Bot","email":"bot@spam.com","message":"buy things","website":"spam.example"}`)
	if code != http.StatusAccepted {
		t.Fatalf("honeypot: want 202, got %d", code)
	}
	if reqs := dueRequests(t, srv); len(reqs) != 2 {
		t.Fatalf("honeypot stored a row: %v", reqs)
	}
}
