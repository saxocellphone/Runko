package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// inviteRequest mirrors runkod's due-feed row (runkod/invite.go
// inviteRequestView) - pinned by the stub in mailer_test.go, the
// watchdog convention for a dependency-free sibling service.
type inviteRequest struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Message   string    `json:"message"`
	Attempt   int       `json:"attempt"`
	CreatedAt time.Time `json:"created_at"`
}

// RunkodClient speaks the three invite-request endpoints with the deploy
// token (the feed is operator-only).
type RunkodClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (c *RunkodClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func (c *RunkodClient) DueInviteRequests(ctx context.Context) ([]inviteRequest, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/invite-requests/due", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Requests []inviteRequest `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode due feed: %w", err)
	}
	return body.Requests, nil
}

func (c *RunkodClient) AckSent(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/invite-requests/"+id+"/sent", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *RunkodClient) AckFailed(ctx context.Context, id, sendErr string) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/invite-requests/"+id+"/failed",
		map[string]string{"error": sendErr})
	if err != nil {
		return err
	}
	resp.Body.Close()
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
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", req.ID, err))
			if ackErr := m.Runkod.AckFailed(ctx, req.ID, err.Error()); ackErr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("ack failed for %s: %v", req.ID, ackErr))
			}
			continue
		}
		if err := m.Runkod.AckSent(ctx, req.ID); err != nil {
			// The email went out but the ack didn't: the row stays due and
			// a later poll re-sends - at-least-once, documented in main.go.
			res.Errors = append(res.Errors, fmt.Sprintf("ack sent for %s: %v", req.ID, err))
			continue
		}
		res.Sent = append(res.Sent, req.ID)
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
func buildMessage(from, to string, req inviteRequest) []byte {
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
	write("Requested: " + req.CreatedAt.Format(time.RFC3339))
	write("Request:   " + req.ID)
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
func (m *Mailer) send(req inviteRequest) error {
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
