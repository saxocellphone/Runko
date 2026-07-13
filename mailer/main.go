// runko-mailer is the invite-request notifier (mailer/PROJECT.yaml):
// §15.1's invite-request flow grown its delivery half. runkod stores
// "how do I get the invite code?" submissions as deployment-wide outbox
// rows; this service polls the operator-only due feed and emails each
// one to the deployment operator, Reply-To'd to the requester, so
// replying with the code is the whole fulfillment loop. Sent/failed acks
// go back to runkod, which owns backoff and dead-lettering (the §14.4.1
// webhook-outbox model) - a mailer crash or redeploy never loses a
// request, and a send-then-crash-before-ack at worst duplicates one
// email to the operator (at-least-once, same as the outbox).
//
// Deployment shape mirrors runko-watchdog: ships in the runkod image,
// runs as its own single-replica Deployment, config via flags with
// RUNKO_MAILER_* env fallbacks, /healthz for probes. Egress-only: no
// ingress, no state.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "runko-mailer: %v\n", err)
		os.Exit(1)
	}
}

func envString(key, fallback string) string {
	if v := os.Getenv("RUNKO_MAILER_" + key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv("RUNKO_MAILER_" + key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("runko-mailer: ignoring unparseable RUNKO_MAILER_%s=%q", key, v)
	}
	return fallback
}

func run(args []string) error {
	fs := flag.NewFlagSet("runko-mailer", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", envString("RUNKOD_URL", ""), "runkod DEPLOYMENT ROOT base URL (not /o/<org> - invite requests are deployment-wide), e.g. http://runkod:8080 [RUNKO_MAILER_RUNKOD_URL]")
	runkodToken := fs.String("runkod-token", envString("RUNKOD_TOKEN", ""), "runkod deploy token - the feed is operator-only (prefer the env var) [RUNKO_MAILER_RUNKOD_TOKEN]")
	smtpAddr := fs.String("smtp-addr", envString("SMTP_ADDR", "smtp.gmail.com:587"), "SMTP host:port; STARTTLS is used when the server advertises it [RUNKO_MAILER_SMTP_ADDR]")
	smtpUser := fs.String("smtp-user", envString("SMTP_USER", ""), "SMTP AUTH user; empty = no AUTH (tests, open relays) [RUNKO_MAILER_SMTP_USER]")
	smtpPassword := fs.String("smtp-password", envString("SMTP_PASSWORD", ""), "SMTP AUTH password, e.g. a Gmail app password (prefer the env var) [RUNKO_MAILER_SMTP_PASSWORD]")
	from := fs.String("from", envString("FROM", ""), "envelope/header From address; defaults to --smtp-user [RUNKO_MAILER_FROM]")
	to := fs.String("to", envString("TO", ""), "the operator address invite requests are mailed to [RUNKO_MAILER_TO]")
	interval := fs.Duration("interval", envDuration("INTERVAL", time.Minute), "time between feed polls [RUNKO_MAILER_INTERVAL]")
	addr := fs.String("addr", envString("ADDR", ":8083"), "listen address for /healthz [RUNKO_MAILER_ADDR]")
	once := fs.Bool("once", false, "run a single poll and exit (smoke tests, cron)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		*from = *smtpUser
	}
	for name, v := range map[string]string{
		"--runkod-url": *runkodURL, "--runkod-token": *runkodToken, "--to": *to, "--from": *from,
	} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	m := &Mailer{
		Runkod: &RunkodClient{
			BaseURL: strings.TrimRight(*runkodURL, "/"),
			Token:   *runkodToken,
			Client:  &http.Client{Timeout: 30 * time.Second},
		},
		SMTPAddr: *smtpAddr,
		SMTPUser: *smtpUser,
		SMTPPass: *smtpPassword,
		From:     *from,
		To:       *to,
	}

	poll := func(ctx context.Context) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		res := m.PollOnce(cctx)
		for _, id := range res.Sent {
			log.Printf("runko-mailer: mailed invite request %s to %s", id, *to)
		}
		for _, f := range res.Failed {
			log.Printf("runko-mailer: send failed (server reschedules): %s", f)
		}
		for _, e := range res.Errors {
			log.Printf("runko-mailer: ERROR %s", e)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *once {
		poll(ctx)
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("runko-mailer: healthz server: %v", err)
		}
	}()

	log.Printf("runko-mailer: draining %s to %s via %s every %s", *runkodURL, *to, *smtpAddr, *interval)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	poll(ctx)
	for {
		select {
		case <-ctx.Done():
			shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shCtx)
			log.Printf("runko-mailer: shutting down")
			return nil
		case <-ticker.C:
			poll(ctx)
		}
	}
}
