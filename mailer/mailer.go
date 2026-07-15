package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"connectrpc.com/connect"

	mailerv1 "github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1"
	"github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1/mailerv1connect"
)

// RunkodClient speaks InviteFeedService - runkod's in-boundary contract
// (runkod/proto/mailer/v1, §13.3.1) - with the deploy token (the feed is
// operator-only). The generated-client import is exactly the declared
// dependency edge mailer/PROJECT.yaml carries: a feed reshape now puts
// this project in the affected closure instead of failing at runtime.
type RunkodClient struct {
	feed mailerv1connect.InviteFeedServiceClient
}

// NewRunkodClient dials baseURL (the org mount, e.g. https://host/o/org)
// over Connect, stamping the bearer token on every call.
func NewRunkodClient(baseURL, token string, hc *http.Client) *RunkodClient {
	auth := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
	return &RunkodClient{feed: mailerv1connect.NewInviteFeedServiceClient(hc, strings.TrimRight(baseURL, "/"), auth)}
}

func (c *RunkodClient) DueInviteRequests(ctx context.Context) ([]*mailerv1.InviteRequest, error) {
	resp, err := c.feed.ListDue(ctx, connect.NewRequest(&mailerv1.ListDueRequest{}))
	if err != nil {
		return nil, fmt.Errorf("ListDue: %w", err)
	}
	return resp.Msg.Requests, nil
}

func (c *RunkodClient) AckSent(ctx context.Context, id string) error {
	if _, err := c.feed.MarkSent(ctx, connect.NewRequest(&mailerv1.MarkSentRequest{Id: id})); err != nil {
		return fmt.Errorf("MarkSent %s: %w", id, err)
	}
	return nil
}

func (c *RunkodClient) AckFailed(ctx context.Context, id, sendErr string) error {
	if _, err := c.feed.MarkFailed(ctx, connect.NewRequest(&mailerv1.MarkFailedRequest{Id: id, Error: sendErr})); err != nil {
		return fmt.Errorf("MarkFailed %s: %w", id, err)
	}
	return nil
}

// Mailer drains the due feed: one email per row, then the matching ack.
// Stateless on purpose - which rows exist, how often to retry, and when
// to give up are all the server's row state.
type Mailer struct {
	Runkod   *RunkodClient
	SMTPAddr string
	SMTPUser string
	SMTPPass string
	From     string
	To       string
}

// PollResult reports one PollOnce for main's log lines.
type PollResult struct {
	Sent   []string // request ids mailed and acked sent
	Failed []string // "id: send error" - acked failed, server reschedules
	Errors []string // feed/ack errors (nothing acked; row stays due)
}

func (m *Mailer) PollOnce(ctx context.Context) PollResult {
	var res PollResult
	due, err := m.Runkod.DueInviteRequests(ctx)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return res
	}
	for _, req := range due {
		if err := m.send(req); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", req.Id, err))
			if ackErr := m.Runkod.AckFailed(ctx, req.Id, err.Error()); ackErr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("ack failed for %s: %v", req.Id, ackErr))
			}
			continue
		}
		if err := m.Runkod.AckSent(ctx, req.Id); err != nil {
			// The email went out but the ack didn't: the row stays due and
			// a later poll re-sends - at-least-once, documented in main.go.
			res.Errors = append(res.Errors, fmt.Sprintf("ack sent for %s: %v", req.Id, err))
			continue
		}
		res.Sent = append(res.Sent, req.Id)
	}
	return res
}

// sanitizeHeader strips CR/LF and other control bytes so no field a
// stranger typed can smuggle an extra SMTP header. runkod already
// refuses control characters at intake; this is defense in depth.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// buildMessage renders one RFC 5322 message. Reply-To is the requester:
// the operator's reply carries the invite code straight back.
func buildMessage(from, to string, req *mailerv1.InviteRequest) []byte {
	var b strings.Builder
	write := func(line string) { b.WriteString(line + "\r\n") }
	write("From: " + sanitizeHeader(from))
	write("To: " + sanitizeHeader(to))
	write("Reply-To: " + sanitizeHeader(req.Email))
	write("Subject: " + sanitizeHeader("[runko] Invite request from "+req.Name))
	write("Date: " + time.Now().Format(time.RFC1123Z))
	write("MIME-Version: 1.0")
	write("Content-Type: text/plain; charset=utf-8")
	write("")
	write("A visitor asked for this deployment's invite code.")
	write("")
	// Single-line body fields get the same stripping: a multi-line name
	// could otherwise fake the Email: line the operator replies to.
	write("Name:      " + sanitizeHeader(req.Name))
	write("Email:     " + sanitizeHeader(req.Email))
	write("Requested: " + req.CreatedAt.AsTime().Format(time.RFC3339))
	write("Request:   " + req.Id)
	if msg := strings.TrimSpace(req.Message); msg != "" {
		write("")
		for _, line := range strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n") {
			write(line)
		}
	}
	write("")
	write("Reply to this email with the invite code to let them in.")
	return []byte(b.String())
}

// send delivers one message over SMTP. This is smtp.SendMail's own
// sequence with an overall connection deadline added, so one hung SMTP
// conversation can never wedge the poll loop.
func (m *Mailer) send(req *mailerv1.InviteRequest) error {
	host, _, err := net.SplitHostPort(m.SMTPAddr)
	if err != nil {
		return fmt.Errorf("smtp addr %q: %w", m.SMTPAddr, err)
	}
	conn, err := net.DialTimeout("tcp", m.SMTPAddr, 15*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Minute))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if m.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", m.SMTPUser, m.SMTPPass, host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(m.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(buildMessage(m.From, m.To, req)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
