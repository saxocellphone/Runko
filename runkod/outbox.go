package runkod

import (
	"context"
	"net/http"
	"time"

	"github.com/saxocellphone/runko/checks"
)

// OutboxWorker delivers due webhook deliveries (§14.4.1) to one configured
// target URL - the "one daemon = one monorepo/org" scope this stage
// documents (doc.go): a real multi-tenant deployment would resolve the
// target per-org from webhook subscription config, not built yet.
type OutboxWorker struct {
	Store      Store
	Client     *http.Client
	URL        string
	Secret     []byte
	BackoffMin time.Duration
	BackoffMax time.Duration
	Now        func() time.Time
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
		attempt := checks.Deliver(ctx, w.client(), w.URL, d.Payload, w.Secret)
		if err := w.Store.RecordDeliveryResult(ctx, d.ID, attempt, w.backoffMin(), w.backoffMax(), now); err != nil {
			return len(due), err
		}
	}
	return len(due), nil
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
