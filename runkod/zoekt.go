package runkod

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/saxocellphone/runko/search"
)

// ZoektIndexWorker debounces search.Indexer runs so a burst of trunk
// advances coalesces into a single reindex rather than one zoekt-git-index
// process spawn per ref update (§28.3 stage 11's "debounced on trunk
// advance" decision). Trigger is safe to call from any goroutine; nil-safe
// so a daemon started without --search-index-dir (indexing not configured)
// can call Trigger unconditionally without a nil check at every call site.
//
// Scope note: Processor.commit only calls Trigger for updates to
// refs/heads/<trunk> itself. Direct pushes to trunk are always rejected by
// receive.Decide (§6.9 - "trunk is closed to direct push"), and the land
// engine (land/, stage 7) is not yet wired into this daemon (doc.go's
// deferred list) - so in the current wiring, trunk never actually advances
// through runkod, and this trigger point is not yet reachable in practice.
// It is wired now, correctly, at the one place that will start firing for
// real the moment a later stage connects land.Land's ref update to this
// daemon, rather than leaving that wiring as an afterthought then.
type ZoektIndexWorker struct {
	Indexer search.Indexer
	RepoDir string
	// Debounce is how long to wait after a Trigger before actually running
	// Indexer.Index, coalescing further Triggers received in the meantime.
	// Zero runs synchronously in the calling goroutine's stead (a
	// background goroutine still, but with no coalescing window) - tests
	// use a short Debounce rather than zero so they can assert coalescing.
	Debounce time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	running bool
	lastErr error
}

// Trigger schedules (or reschedules, if one is already pending) a reindex.
// A nil *ZoektIndexWorker or nil Indexer is a no-op, so callers don't need
// to guard every call site on whether indexing was configured.
func (w *ZoektIndexWorker) Trigger() {
	if w == nil || w.Indexer == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.Debounce, w.run)
}

func (w *ZoektIndexWorker) run() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	w.mu.Lock()
	w.running = true
	w.mu.Unlock()

	err := w.Indexer.Index(ctx, w.RepoDir)

	w.mu.Lock()
	w.running = false
	w.lastErr = err
	w.mu.Unlock()

	if err != nil {
		log.Printf("runkod: zoekt reindex failed: %v", err)
	}
}

// LastErr reports the most recent completed run's error (nil if it
// succeeded, or if no run has completed yet) - used by tests to observe
// completion without a fixed sleep, and available for a future health/
// status endpoint.
func (w *ZoektIndexWorker) LastErr() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}
