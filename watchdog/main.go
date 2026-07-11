// runko-watchdog is the CI reconciler (watchdog/PROJECT.yaml): §14.4.2's
// staleness rule says "a dead CI must block loudly, not hang silently" -
// this service is the loudness grown hands. It sweeps every open Change's
// merge requirements and closes the two dogfood-observed gaps between
// Runko and its CI:
//
//   - the check's GitHub Actions run finished but the result never reached
//     report-check (runner died mid-teardown, network blip): the run's real
//     conclusion is force-reported, attributed to "ci-watchdog";
//   - the check never reported at all (dispatch lost before CI ever saw
//     it): one rescue rerun re-fires the §14.4.1 webhook chain.
//
// Deployment shape mirrors runko-bridge: ships in the runkod image, runs
// as its own single-replica Deployment, config via flags with
// RUNKO_WATCHDOG_* env fallbacks, /healthz for probes.
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
		fmt.Fprintf(os.Stderr, "runko-watchdog: %v\n", err)
		os.Exit(1)
	}
}

func envString(key, fallback string) string {
	if v := os.Getenv("RUNKO_WATCHDOG_" + key); v != "" {
		return v
	}
	return fallback
}

func run(args []string) error {
	fs := flag.NewFlagSet("runko-watchdog", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", envString("RUNKOD_URL", ""), "runkod org base URL, e.g. https://host/o/runko [RUNKO_WATCHDOG_RUNKOD_URL]")
	runkodToken := fs.String("runkod-token", envString("RUNKOD_TOKEN", ""), "runkod deploy token (prefer the env var) [RUNKO_WATCHDOG_RUNKOD_TOKEN]")
	githubRepo := fs.String("github-repo", envString("GITHUB_REPO", ""), "owner/name whose Actions runs details_urls may point at - an allowlist, not a default [RUNKO_WATCHDOG_GITHUB_REPO]")
	githubToken := fs.String("github-token", envString("GITHUB_TOKEN", ""), "GitHub token with actions:read; may be empty for public repos [RUNKO_WATCHDOG_GITHUB_TOKEN]")
	githubAPI := fs.String("github-api", envString("GITHUB_API", "https://api.github.com"), "GitHub API base URL (tests point this at a stub) [RUNKO_WATCHDOG_GITHUB_API]")
	interval := fs.Duration("interval", envDuration("INTERVAL", time.Minute), "time between sweeps [RUNKO_WATCHDOG_INTERVAL]")
	grace := fs.Duration("grace", envDuration("GRACE", 5*time.Minute), "how long a check may sit pending before the watchdog acts [RUNKO_WATCHDOG_GRACE]")
	rerun := fs.Bool("rerun", envString("RERUN", "true") == "true", "rescue never-reported checks with ONE rerun each [RUNKO_WATCHDOG_RERUN]")
	addr := fs.String("addr", envString("ADDR", ":8082"), "listen address for /healthz [RUNKO_WATCHDOG_ADDR]")
	once := fs.Bool("once", false, "run a single sweep and exit (smoke tests, cron)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"--runkod-url": *runkodURL, "--runkod-token": *runkodToken, "--github-repo": *githubRepo,
	} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	w := &Watchdog{
		Runkod: &RunkodClient{
			BaseURL: strings.TrimRight(*runkodURL, "/"),
			Token:   *runkodToken,
			Client:  &http.Client{Timeout: 30 * time.Second},
		},
		GitHub: &GitHubClient{
			APIBase: strings.TrimRight(*githubAPI, "/"),
			Repo:    *githubRepo,
			Token:   *githubToken,
			Client:  &http.Client{Timeout: 30 * time.Second},
		},
		Grace:       *grace,
		EnableRerun: *rerun,
	}

	sweep := func(ctx context.Context) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		res := w.Sweep(cctx)
		for _, r := range res.Reported {
			log.Printf("runko-watchdog: force-reported %s (run already completed)", r)
		}
		for _, r := range res.Rescued {
			log.Printf("runko-watchdog: rescue rerun for never-reported %s", r)
		}
		for _, e := range res.Errors {
			log.Printf("runko-watchdog: ERROR %s", e)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *once {
		sweep(ctx)
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
			log.Printf("runko-watchdog: healthz server: %v", err)
		}
	}()

	log.Printf("runko-watchdog: watching %s against %s every %s (grace %s, rerun %v)",
		*runkodURL, *githubRepo, *interval, *grace, *rerun)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shCtx)
			log.Printf("runko-watchdog: shutting down")
			return nil
		case <-ticker.C:
			sweep(ctx)
		}
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv("RUNKO_WATCHDOG_" + key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("runko-watchdog: ignoring unparseable RUNKO_WATCHDOG_%s=%q", key, v)
	}
	return fallback
}
