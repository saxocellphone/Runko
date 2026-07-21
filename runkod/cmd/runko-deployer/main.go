// runko-deployer is the inverted CD trigger's GitOps writer (§14.10): it
// subscribes to runkod's deploy.images_ready webhook and pins the reported
// image digests into a GitOps repo's kustomization, which Argo CD then rolls.
// GitHub only builds and reports the digests; Runko drives the rollout.
//
// It rides the runkod image (like runko-watchdog/runko-bridge), speaks only
// git-over-https to any host (provider-agnostic), edits kustomization.yaml in
// Go (no kustomize binary), and holds the ONE credential that writes the
// deploy repo. Delivery contract: it answers runkod's outbox 2xx only after a
// successful pin (or a delivered no-op), so a failed write rides the outbox
// backoff instead of silently dropping a rollout.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/runkogithubapp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "runko-deployer: %v\n", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv("RUNKO_DEPLOYER_" + key); v != "" {
		return v
	}
	return fallback
}

type deployer struct {
	secret       []byte
	org          string
	repoURL      string
	tokenFn      func() (string, error) // push credential: static token, or a per-push App mint ("" = anonymous)
	branch       string
	kustomizeDir string
	dryRun       bool

	mu     sync.Mutex // serialize GitOps writes: one commit at a time
	seenMu sync.Mutex
	seen   map[string]bool
	now    func() time.Time
}

func run(args []string) error {
	fs := flag.NewFlagSet("runko-deployer", flag.ExitOnError)
	addr := fs.String("addr", env("ADDR", ":8082"), "listen address [RUNKO_DEPLOYER_ADDR]")
	secret := fs.String("webhook-secret", env("WEBHOOK_SECRET", ""), "HMAC secret shared with runkod's --webhook-secret [RUNKO_DEPLOYER_WEBHOOK_SECRET]")
	org := fs.String("org", env("ORG", ""), "only act on deploy.images_ready for this org_id ('' = any) [RUNKO_DEPLOYER_ORG]")
	repoURL := fs.String("repo-url", env("REPO_URL", ""), "https clone URL of the GitOps repo to pin digests into [RUNKO_DEPLOYER_REPO_URL]")
	token := fs.String("token", env("GITHUB_TOKEN", ""), "write token for repo-url (prefer the env var) [RUNKO_DEPLOYER_GITHUB_TOKEN]")
	appID := fs.String("github-app-id", env("GITHUB_APP_ID", ""), "GitHub App id: mint short-lived installation tokens for repo-url instead of holding a static --token (runkogithubapp/README.md) [RUNKO_DEPLOYER_GITHUB_APP_ID]")
	appKeyFile := fs.String("github-app-key-file", env("GITHUB_APP_KEY_FILE", ""), "path to the App private key PEM (required with --github-app-id) [RUNKO_DEPLOYER_GITHUB_APP_KEY_FILE]")
	githubAPI := fs.String("github-api", env("GITHUB_API", "https://api.github.com"), "GitHub API base for App auth; GHES: https://<host>/api/v3 [RUNKO_DEPLOYER_GITHUB_API]")
	branch := fs.String("branch", env("BRANCH", "main"), "branch of repo-url to commit to [RUNKO_DEPLOYER_BRANCH]")
	kustomizeDir := fs.String("kustomize-dir", env("KUSTOMIZE_DIR", "apps/monorepo-platform"), "dir under repo-url holding kustomization.yaml [RUNKO_DEPLOYER_KUSTOMIZE_DIR]")
	dryRun := fs.Bool("dry-run", env("DRY_RUN", "") != "", "compute the pin and log the diff, but do not commit/push [RUNKO_DEPLOYER_DRY_RUN]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, v := range map[string]string{"--webhook-secret": *secret, "--repo-url": *repoURL} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}

	// The push credential: App auth mints a short-lived installation token
	// per push, scoped to repo-url alone (runkogithubapp/README.md), so no
	// standing secret can write the deploy repo; --token stays as the
	// static-PAT fallback, and neither leaves pushes anonymous (fine for
	// dry-run and filesystem test remotes).
	tokenFn := func() (string, error) { return *token, nil }
	authMode := "static-token"
	if *token == "" {
		authMode = "anonymous"
	}
	if (*appID == "") != (*appKeyFile == "") {
		return fmt.Errorf("--github-app-id and --github-app-key-file come together (App auth needs both)")
	}
	if *appID != "" {
		keyPEM, err := os.ReadFile(*appKeyFile)
		if err != nil {
			return fmt.Errorf("--github-app-key-file: %w", err)
		}
		app, err := runkogithubapp.New(*appID, keyPEM, *githubAPI)
		if err != nil {
			return err
		}
		ownerRepo := app.RepoPath(*repoURL)
		if ownerRepo == "" {
			return fmt.Errorf("--github-app-id is set but --repo-url %q is not an https remote on the App's GitHub host", *repoURL)
		}
		tokenFn = app.TokenSource(ownerRepo)
		authMode = "github-app:" + ownerRepo
	}

	d := &deployer{
		secret: []byte(*secret), org: *org, repoURL: *repoURL, tokenFn: tokenFn,
		branch: *branch, kustomizeDir: *kustomizeDir, dryRun: *dryRun,
		seen: map[string]bool{}, now: time.Now,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", d.handleWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	fmt.Printf("runko-deployer: pinning deploy.images_ready digests into %s (%s), dir=%s, auth=%s, dry-run=%v\n", *repoURL, *branch, *kustomizeDir, authMode, *dryRun)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func (d *deployer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	sig := strings.TrimPrefix(r.Header.Get(checks.SignatureHeader), "sha256=")
	if !checks.VerifySignature(d.secret, payload, sig) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	// runkod fans EVERY event to the webhook URL; act only on
	// deploy.images_ready (everything else 204s so the outbox stops retrying).
	var head struct {
		Type       string `json:"type"`
		OrgID      string `json:"org_id"`
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if head.Type != "deploy.images_ready" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if d.org != "" && head.OrgID != d.org {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if d.alreadySeen(head.DeliveryID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var hook checks.DeployImagesReadyWebhook
	if err := json.Unmarshal(payload, &hook); err != nil {
		http.Error(w, "bad deploy payload", http.StatusBadRequest)
		return
	}
	if err := d.apply(r.Context(), hook); err != nil {
		log.Printf("runko-deployer: %s: %v", short(hook.Deploy.TrunkSHA), err)
		http.Error(w, "deploy failed", http.StatusBadGateway) // outbox retries
		return
	}
	d.markSeen(head.DeliveryID)
	w.WriteHeader(http.StatusNoContent)
}

// apply clones the GitOps repo, pins the reported digests into its
// kustomization, and commits+pushes (or logs the diff in dry-run).
func (d *deployer) apply(ctx context.Context, hook checks.DeployImagesReadyWebhook) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	dir, err := os.MkdirTemp("", "runko-deployer-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Clone anonymously (the deploy repo is public); authenticate the PUSH
	// only, via the ephemeral checkout's config - never argv or logs.
	if err := d.git(ctx, "", "clone", "--depth", "1", "--branch", d.branch, d.repoURL, dir); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	rel := filepath.Join(d.kustomizeDir, "kustomization.yaml")
	changed, err := pinImages(filepath.Join(dir, rel), hook.Deploy.Images)
	if err != nil {
		return err
	}
	if !changed {
		log.Printf("runko-deployer: %s: digests already pinned - no commit", short(hook.Deploy.TrunkSHA))
		return nil
	}
	diff, _ := d.gitOut(ctx, dir, "diff", "--", rel)
	if d.dryRun {
		log.Printf("runko-deployer: DRY-RUN %s would pin:\n%s", short(hook.Deploy.TrunkSHA), diff)
		return nil
	}

	if err := d.git(ctx, dir, "add", rel); err != nil {
		return err
	}
	msg := fmt.Sprintf("monorepo-platform: image bump from runko@%s", short(hook.Deploy.TrunkSHA))
	body := fmt.Sprintf("Runko-driven GitOps write-back (deploy.images_ready). Change: %s. Provenance: %s", hook.Deploy.ChangeKey, hook.Deploy.Provenance)
	if err := d.git(ctx, dir,
		"-c", "user.name=runko-release", "-c", "user.email=runko-release@users.noreply.github.com",
		"commit", "-m", msg, "-m", body); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if err := d.setPushAuth(ctx, dir); err != nil {
		return err
	}
	// Rebase-retry to survive a concurrent human push to the deploy repo.
	for i := 0; i < 3; i++ {
		if err := d.git(ctx, dir, "push", "origin", "HEAD:"+d.branch); err == nil {
			log.Printf("runko-deployer: %s: pushed image bump to %s", short(hook.Deploy.TrunkSHA), d.repoURL)
			return nil
		}
		if err := d.git(ctx, dir, "pull", "--rebase", "origin", d.branch); err != nil {
			return fmt.Errorf("rebase before push retry: %w", err)
		}
	}
	return fmt.Errorf("could not push the bump after 3 attempts")
}

func (d *deployer) git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func (d *deployer) gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// setPushAuth points origin at an authenticated URL in the ephemeral
// checkout's config (not argv/logs), for the push only. The credential
// is resolved HERE, per push: App mode mints (or serves a cached, still
// -fresh) installation token, so a pin arriving hours after boot never
// rides an expired one. No credential => the anonymous origin stays
// (test remotes on the local filesystem).
func (d *deployer) setPushAuth(ctx context.Context, dir string) error {
	if !strings.HasPrefix(d.repoURL, "https://") {
		return nil
	}
	tok, err := d.tokenFn()
	if err != nil {
		return fmt.Errorf("resolve push credential: %w", err)
	}
	if tok == "" {
		return nil
	}
	authed := "https://x-access-token:" + tok + "@" + strings.TrimPrefix(d.repoURL, "https://")
	return d.git(ctx, dir, "remote", "set-url", "origin", authed)
}

func short(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

func (d *deployer) alreadySeen(id string) bool {
	if id == "" {
		return false
	}
	d.seenMu.Lock()
	defer d.seenMu.Unlock()
	return d.seen[id]
}

func (d *deployer) markSeen(id string) {
	if id == "" {
		return
	}
	d.seenMu.Lock()
	defer d.seenMu.Unlock()
	d.seen[id] = true
}
