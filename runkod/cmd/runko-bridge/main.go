// runko-bridge is the reference §14.4 "Mode C" CI plugin for GitHub
// Actions: it translates runkod's signed webhook envelopes into GitHub
// repository_dispatch events, because GitHub Actions cannot trigger on
// runko's refs/changes/* (push workflows fire for branches/tags only) and
// GitHub's dispatch API speaks a different body and auth than the §14.4.1
// envelope. One bridge instance serves one org -> one GitHub repo pair
// (docs/migration-findings.md #14: this is the productization seam §18.3's
// CI shadow period needs).
//
// GitHub auth is either a PAT (--github-token) or, preferred since
// 2026-07-16 (runkod/README.md), a GitHub App (--github-app-id +
// --github-app-key-file): every bridge instance shares the one App
// credential and mints its own installation tokens, so onboarding an org
// stops requiring a fresh PAT - install the App on the repo instead.
//
// Delivery contract: runkod's outbox retries with backoff until it sees a
// 2xx, so the bridge answers 2xx ONLY after GitHub accepted the dispatch
// (204) - a GitHub failure surfaces as 502 and the outbox re-drives it.
// Events that are not for this bridge (wrong org, uninteresting type,
// already-seen delivery_id) are acknowledged with 204 so the outbox never
// retries them.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/runkogithubapp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "runko-bridge: %v\n", err)
		os.Exit(1)
	}
}

func envString(key, fallback string) string {
	if v := os.Getenv("RUNKO_BRIDGE_" + key); v != "" {
		return v
	}
	return fallback
}

func run(args []string) error {
	fs := flag.NewFlagSet("runko-bridge", flag.ExitOnError)
	addr := fs.String("addr", envString("ADDR", ":8081"), "listen address [RUNKO_BRIDGE_ADDR]")
	secret := fs.String("webhook-secret", envString("WEBHOOK_SECRET", ""), "HMAC secret shared with runkod's --webhook-secret [RUNKO_BRIDGE_WEBHOOK_SECRET]")
	org := fs.String("org", envString("ORG", ""), "only forward envelopes whose org_id matches this org name [RUNKO_BRIDGE_ORG]")
	githubRepo := fs.String("github-repo", envString("GITHUB_REPO", ""), "owner/name of the GitHub repo receiving repository_dispatch [RUNKO_BRIDGE_GITHUB_REPO]")
	githubToken := fs.String("github-token", envString("GITHUB_TOKEN", ""), "GitHub PAT with contents:write (prefer the env var); alternative: App auth below [RUNKO_BRIDGE_GITHUB_TOKEN]")
	githubAppID := fs.String("github-app-id", envString("GITHUB_APP_ID", ""), "GitHub App id: dispatch with minted installation tokens instead of a per-org PAT (2026-07-16, runkod/README.md) [RUNKO_BRIDGE_GITHUB_APP_ID]")
	githubAppKeyFile := fs.String("github-app-key-file", envString("GITHUB_APP_KEY_FILE", ""), "path to the GitHub App private key PEM (required with --github-app-id) [RUNKO_BRIDGE_GITHUB_APP_KEY_FILE]")
	githubAPI := fs.String("github-api", envString("GITHUB_API", "https://api.github.com"), "GitHub API base URL (tests point this at a stub) [RUNKO_BRIDGE_GITHUB_API]")
	eventType := fs.String("event-type", envString("EVENT_TYPE", "runko-change"), "repository_dispatch event_type the workflow subscribes to [RUNKO_BRIDGE_EVENT_TYPE]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"--webhook-secret": *secret, "--org": *org, "--github-repo": *githubRepo,
	} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	token, err := githubTokenSource(*githubToken, *githubAppID, *githubAppKeyFile, *githubAPI, *githubRepo)
	if err != nil {
		return err
	}

	b := &bridge{
		secret:      []byte(*secret),
		org:         *org,
		dispatchURL: strings.TrimRight(*githubAPI, "/") + "/repos/" + *githubRepo + "/dispatches",
		token:       token,
		eventType:   *eventType,
		client:      &http.Client{Timeout: 30 * time.Second},
		seen:        newSeenSet(512),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", b.handleWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	fmt.Printf("runko-bridge: forwarding org %q webhooks to %s as %q\n", *org, b.dispatchURL, *eventType)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

// githubTokenSource resolves the bridge's GitHub auth: a static PAT, or
// GitHub App installation tokens minted per dispatch (2026-07-16,
// runkod/README.md - one App credential across every org's bridge, so a
// new org's setup is just installing the App on its mirror repo).
func githubTokenSource(pat, appID, appKeyFile, apiBase, repo string) (func() (string, error), error) {
	switch {
	case pat != "" && appID != "":
		return nil, fmt.Errorf("--github-token and --github-app-id are mutually exclusive - pick PAT or App auth")
	case pat != "":
		return func() (string, error) { return pat, nil }, nil
	case appID != "":
		if appKeyFile == "" {
			return nil, fmt.Errorf("--github-app-key-file is required with --github-app-id")
		}
		keyPEM, err := os.ReadFile(appKeyFile)
		if err != nil {
			return nil, fmt.Errorf("--github-app-key-file: %w", err)
		}
		app, err := runkogithubapp.New(appID, keyPEM, apiBase)
		if err != nil {
			return nil, err
		}
		return app.TokenSource(repo), nil
	default:
		return nil, fmt.Errorf("github auth is required: --github-token (PAT) or --github-app-id + --github-app-key-file (App)")
	}
}

type bridge struct {
	secret      []byte
	org         string
	dispatchURL string
	token       func() (string, error)
	eventType   string
	client      *http.Client
	seen        *seenSet
}

// dispatchPayload is the repository_dispatch client_payload (GitHub caps it
// at 10 top-level keys). The workflow checks out git_ref from the MIRROR
// (refs/changes/<id>/head) and reports back via runko-ci report-check.
type dispatchPayload struct {
	ChangeID         string   `json:"change_id"`
	HeadSHA          string   `json:"head_sha"`
	BaseSHA          string   `json:"base_sha"`
	GitRef           string   `json:"git_ref"`
	Trigger          string   `json:"trigger"`
	RerunCheck       string   `json:"rerun_check,omitempty"`
	DeliveryID       string   `json:"delivery_id"`
	AffectedProjects []string `json:"affected_projects"`
}

func (b *bridge) handleWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	sig := strings.TrimPrefix(r.Header.Get(checks.SignatureHeader), "sha256=")
	if !checks.VerifySignature(b.secret, payload, sig) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	var env checks.WebhookEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		http.Error(w, "bad envelope", http.StatusBadRequest)
		return
	}

	// Acknowledged-but-ignored cases answer 204 so the outbox never
	// retries them: uninteresting types (change.landed needs no CI run),
	// other orgs' events (one daemon-wide webhook URL fans every org into
	// this bridge), and unattributable events from a pre-R1 daemon.
	if env.Type != "change.updated" && env.Type != "change.check_rerun_requested" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if env.OrgID == "" {
		log.Printf("runko-bridge: dropping unattributable %s event %s (empty org_id - daemon predates org stamping?)", env.Type, env.DeliveryID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if env.OrgID != b.org {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if b.seen.contains(env.DeliveryID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	dp := dispatchPayload{
		ChangeID:   env.Change.ID,
		HeadSHA:    env.Change.HeadSHA,
		BaseSHA:    env.Change.BaseSHA,
		GitRef:     env.Change.GitRef,
		Trigger:    env.Type,
		DeliveryID: env.DeliveryID,
	}
	if env.Rerun != nil {
		dp.RerunCheck = env.Rerun.CheckName
	}
	if env.Affected != nil {
		for _, p := range env.Affected.Projects {
			dp.AffectedProjects = append(dp.AffectedProjects, p.Name)
		}
	}

	// Minted per dispatch under App auth (cached until near expiry); a
	// failed mint is a 502 like any GitHub failure - the outbox re-drives.
	token, err := b.token()
	if err != nil {
		log.Printf("runko-bridge: github token for %s: %v", env.DeliveryID, err)
		http.Error(w, "github auth unavailable", http.StatusBadGateway)
		return
	}

	body, err := json.Marshal(map[string]any{"event_type": b.eventType, "client_payload": dp})
	if err != nil {
		http.Error(w, "marshal dispatch", http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, b.dispatchURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build dispatch request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		log.Printf("runko-bridge: dispatch %s: %v", env.DeliveryID, err)
		http.Error(w, "github unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Printf("runko-bridge: dispatch %s: github %d: %s", env.DeliveryID, resp.StatusCode, msg)
		// Non-2xx back to the outbox so its backoff re-drives the event.
		http.Error(w, fmt.Sprintf("github returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	b.seen.add(env.DeliveryID)
	log.Printf("runko-bridge: dispatched %s for change %s@%s (%s)", b.eventType, env.Change.ID, env.Change.HeadSHA, env.Type)
	w.WriteHeader(http.StatusNoContent)
}

// seenSet is a bounded FIFO set of delivery ids - enough to drop the
// outbox's redeliveries within a process lifetime without unbounded memory.
// Restarts forget it, which is safe: the workflow's concurrency group
// (change_id + head_sha) dedupes at the GitHub end.
type seenSet struct {
	mu    sync.Mutex
	max   int
	order []string
	set   map[string]bool
}

func newSeenSet(max int) *seenSet {
	return &seenSet{max: max, set: make(map[string]bool)}
}

func (s *seenSet) contains(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set[id]
}

func (s *seenSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set[id] {
		return
	}
	s.set[id] = true
	s.order = append(s.order, id)
	if len(s.order) > s.max {
		delete(s.set, s.order[0])
		s.order = s.order[1:]
	}
}
