package runkod

import (
	"context"
	"net/http"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

// OutboxWorker delivers due webhook deliveries (§14.4.1) to one configured
// target URL - the "one daemon = one monorepo/org" scope this stage
// documents (doc.go): a real multi-tenant deployment would resolve the
// target per-org from webhook subscription config, not built yet. Since
// 2026-07-17 it additionally performs the native Mode C GitHub dispatch
// (githubdispatch.go) when GithubDispatch is set - URL-less deployments
// with App credentials run the worker for dispatch alone.
type OutboxWorker struct {
	Store      Store
	Client     *http.Client
	URL        string
	Secret     []byte
	BackoffMin time.Duration
	BackoffMax time.Duration
	Now        func() time.Time
	// GithubDispatch, when set, natively dispatches CI-triggering
	// envelopes to the org's connected GitHub repo - the runko-bridge
	// shim folded in. Runs after the URL delivery: both must succeed
	// for the row to be marked delivered, and a retry re-drives both
	// (receivers dedupe by delivery_id; GitHub by the workflow's
	// concurrency group).
	GithubDispatch *GithubDispatcher
}

// RunOnce polls for due deliveries and attempts each exactly once,
// recording the result via Store.RecordDeliveryResult (which schedules the
// next backoff, or dead-letters past checks.MaxDeliveryAttempts). Returns
// the number of deliveries attempted, for tests/logging.
func (w *OutboxWorker) RunOnce(ctx context.Context) (int, error) {
	now := w.now()
	due, err := w.Store.ListDueWebhookDeliveries(ctx, now)
	if err != nil {
		return 0, err
	}
	for _, d := range due {
		attempt := w.attempt(ctx, d.Payload)
		if err := w.Store.RecordDeliveryResult(ctx, d.ID, attempt, w.backoffMin(), w.backoffMax(), now); err != nil {
			return len(due), err
		}
	}
	return len(due), nil
}

// attempt performs one delivery's full fan-out: the signed URL POST when
// a URL is configured, then the native GitHub dispatch when wired.
func (w *OutboxWorker) attempt(ctx context.Context, payload []byte) checks.DeliveryAttempt {
	if w.URL != "" {
		attempt := checks.Deliver(ctx, w.client(), w.URL, payload, w.Secret)
		if !attempt.Success || w.GithubDispatch == nil {
			return attempt
		}
	}
	if w.GithubDispatch != nil {
		return w.GithubDispatch.Deliver(ctx, payload)
	}
	return checks.DeliveryAttempt{Success: true}
}

// Run polls RunOnce every interval until ctx is done - the long-running
// form a daemon's main loop starts in a goroutine.
func (w *OutboxWorker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

func (w *OutboxWorker) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return http.DefaultClient
}

func (w *OutboxWorker) backoffMin() time.Duration {
	if w.BackoffMin > 0 {
		return w.BackoffMin
	}
	return time.Second
}

func (w *OutboxWorker) backoffMax() time.Duration {
	if w.BackoffMax > 0 {
		return w.BackoffMax
	}
	return time.Minute
}

func (w *OutboxWorker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
