package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// smtpSink is a minimal in-test SMTP server: real protocol over a real
// socket (the repo's no-mocks rule - the httptest sibling for SMTP),
// scripted just far enough for net/smtp's client. PlainAuth permits
// unencrypted AUTH because the peer is 127.0.0.1; STARTTLS is never
// advertised, so the client never attempts it.
type smtpSink struct {
	ln         net.Listener
	rejectAuth bool

	mu       sync.Mutex
	authed   bool
	mailFrom string
	rcptTo   []string
	data     string
}

func newSMTPSink(t *testing.T, rejectAuth bool) *smtpSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtp sink listen: %v", err)
	}
	s := &smtpSink{ln: ln, rejectAuth: rejectAuth}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *smtpSink) addr() string { return s.ln.Addr().String() }

func (s *smtpSink) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *smtpSink) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { fmt.Fprintf(conn, "%s\r\n", line) }
	reply("220 sink ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			reply("250-sink")
			reply("250 AUTH PLAIN")
		case strings.HasPrefix(verb, "AUTH"):
			if s.rejectAuth {
				reply("535 5.7.8 authentication credentials invalid")
				continue
			}
			s.mu.Lock()
			s.authed = true
			s.mu.Unlock()
			reply("235 2.7.0 accepted")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			s.mu.Lock()
			s.mailFrom = line[len("MAIL FROM:"):]
			s.mu.Unlock()
			reply("250 ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			s.mu.Lock()
			s.rcptTo = append(s.rcptTo, line[len("RCPT TO:"):])
			s.mu.Unlock()
			reply("250 ok")
		case verb == "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" || dl == ".\n" {
					break
				}
				data.WriteString(dl)
			}
			s.mu.Lock()
			s.data = data.String()
			s.mu.Unlock()
			reply("250 queued")
		case verb == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (s *smtpSink) snapshot() (bool, string, []string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authed, s.mailFrom, s.rcptTo, s.data
}

// fakeRunkod stubs the three invite-request endpoints, pinning the wire
// contract (runkod/invite.go) and recording acks.
type fakeRunkod struct {
	mu     sync.Mutex
	due    []inviteRequest
	sent   []string
	failed map[string]string
}

func (f *fakeRunkod) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.failed = map[string]string{}
	mux := http.NewServeMux()
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer sekret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/invite-requests/due", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": f.due})
	}))
	mux.HandleFunc("POST /api/invite-requests/{id}/sent", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		f.sent = append(f.sent, id)
		for i, req := range f.due { // acked rows leave the feed
			if req.ID == id {
				f.due = append(f.due[:i], f.due[i+1:]...)
				break
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "sent"})
	}))
	mux.HandleFunc("POST /api/invite-requests/{id}/failed", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.failed[r.PathValue("id")] = body.Error
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "status": "failed"})
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestMailer(runkod *httptest.Server, sink *smtpSink) *Mailer {
	return &Mailer{
		Runkod: &RunkodClient{
			BaseURL: runkod.URL, Token: "sekret",
			Client: &http.Client{Timeout: 5 * time.Second},
		},
		SMTPAddr: sink.addr(),
		SMTPUser: "operator@example.com",
		SMTPPass: "app-password",
		From:     "operator@example.com",
		To:       "owner@example.com",
	}
}

// One due request end to end: polled, mailed through the real SMTP
// dialog (AUTH included), acked sent, and gone from the next poll.
func TestPollOnceSendsAndAcks(t *testing.T) {
	sink := newSMTPSink(t, false)
	fake := &fakeRunkod{due: []inviteRequest{{
		ID: "inv_1", Name: "Ada Lovelace", Email: "ada@example.com",
		Message:   "analytical engines need CI too",
		CreatedAt: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
	}}}
	m := newTestMailer(fake.server(t), sink)

	res := m.PollOnce(t.Context())
	if len(res.Errors) != 0 || len(res.Failed) != 0 || len(res.Sent) != 1 || res.Sent[0] != "inv_1" {
		t.Fatalf("PollOnce: %+v", res)
	}
	if len(fake.sent) != 1 || fake.sent[0] != "inv_1" || len(fake.failed) != 0 {
		t.Fatalf("acks: sent=%v failed=%v", fake.sent, fake.failed)
	}

	authed, mailFrom, rcptTo, data := sink.snapshot()
	if !authed {
		t.Fatal("client never authenticated")
	}
	if !strings.Contains(mailFrom, "operator@example.com") ||
		len(rcptTo) != 1 || !strings.Contains(rcptTo[0], "owner@example.com") {
		t.Fatalf("envelope: from=%q to=%v", mailFrom, rcptTo)
	}
	for _, want := range []string{
		"Reply-To: ada@example.com",
		"Subject: [runko] Invite request from Ada Lovelace",
		"To: owner@example.com",
		"analytical engines need CI too",
		"Request:   inv_1",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("message missing %q:\n%s", want, data)
		}
	}

	// The acked row left the feed: a second poll is a no-op.
	if res := m.PollOnce(t.Context()); len(res.Sent)+len(res.Failed)+len(res.Errors) != 0 {
		t.Fatalf("second poll: %+v", res)
	}
}

// A refused AUTH fails the send and acks /failed with the SMTP error -
// the server's row state does the rescheduling, never the mailer.
func TestPollOnceAcksFailure(t *testing.T) {
	sink := newSMTPSink(t, true)
	fake := &fakeRunkod{due: []inviteRequest{{ID: "inv_9", Name: "Bob", Email: "bob@example.com"}}}
	m := newTestMailer(fake.server(t), sink)

	res := m.PollOnce(t.Context())
	if len(res.Sent) != 0 || len(res.Failed) != 1 || len(res.Errors) != 0 {
		t.Fatalf("PollOnce: %+v", res)
	}
	if msg, ok := fake.failed["inv_9"]; !ok || !strings.Contains(msg, "535") {
		t.Fatalf("failed ack: %v", fake.failed)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sent ack on failure: %v", fake.sent)
	}
}

// Defense in depth: even if hostile input reached the mailer, header
// fields cannot grow extra lines. (runkod refuses control characters at
// intake; this pins the second layer.)
func TestBuildMessageSanitizesHeaders(t *testing.T) {
	msg := string(buildMessage("op@example.com", "owner@example.com", inviteRequest{
		ID: "inv_2", Name: "Eve\r\nBcc: everyone@example.com", Email: "eve@example.com",
	}))
	if strings.Contains(msg, "\r\nBcc:") {
		t.Fatalf("header injection survived:\n%s", msg)
	}
	if !strings.Contains(msg, "Subject: [runko] Invite request from EveBcc: everyone@example.com") {
		// The CR/LF is stripped, the text (harmlessly) remains in the one
		// Subject line.
		t.Fatalf("unexpected subject:\n%s", msg)
	}
}
