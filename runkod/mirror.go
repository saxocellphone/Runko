// Outbound mirror worker (§18.6, M1 - decided 2026-07-08): pushes trunk,
// tags, and change refs to a downstream mirror on any git host. Runko is
// the source of truth; the mirror is transport (§14.3), so a lagging or
// broken mirror NEVER blocks landing - it degrades to a status readout and
// a metric, and divergence freezes the affected ref's MIRRORING (not
// landing - that's the inbound stage-1 rule, §18.6.4, for when the mirror
// is the SoR) until an admin unfreezes with a diff report.
//
// Provider-agnostic: everything here is the git wire protocol via the
// mirror package. Workspace snapshot refs are deliberately never mirrored
// - they are personal WIP (§12.2).
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/mirror"
)

// mirrorRemoteName keys mirror_cursors rows. One mirror per daemon in M1;
// the column exists so M2 can carry several.
const mirrorRemoteName = "mirror"

// MirrorWorker debounces outbound syncs (the ZoektIndexWorker pattern) and
// reconciles periodically so restarts and missed triggers self-heal (the
// OutboxWorker pattern). Trigger is nil-safe: a daemon with no mirror
// configured calls it unconditionally.
type MirrorWorker struct {
	Remote   *mirror.Remote
	Store    Store
	TrunkRef string
	// Debounce coalesces a burst of triggers (a stack push updates many
	// change refs) into one sync.
	Debounce time.Duration
	// Interval is the reconcile cadence; zero disables the loop (tests
	// drive SyncOnce directly).
	Interval time.Duration

	// syncMu serializes whole syncs: the debounced trigger and the
	// reconcile ticker (or a status-driven manual sync) may fire
	// concurrently, and two syncs of the SAME worker racing each other's
	// trunk lease read as a phantom foreign write and freeze the cursor -
	// caught by the mirror tests' land-then-sync sequence.
	syncMu     sync.Mutex
	mu         sync.Mutex
	timer      *time.Timer
	lastErr    error
	lastSyncAt time.Time
}

// Trigger schedules (or reschedules) a sync.
func (w *MirrorWorker) Trigger() {
	if w == nil || w.Remote == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.Debounce, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := w.SyncOnce(ctx); err != nil {
			log.Printf("runkod: mirror sync failed: %v", err)
		}
	})
}

// Run is the reconcile loop; returns when ctx is done.
func (w *MirrorWorker) Run(ctx context.Context) {
	if w == nil || w.Remote == nil || w.Interval <= 0 {
		return
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.SyncOnce(ctx); err != nil {
				log.Printf("runkod: mirror reconcile failed: %v", err)
			}
		}
	}
}

// SyncOnce pushes trunk (leased, cursor-tracked), then tags and change
// refs (wildcard namespaces, cursor rows for status only). The first error
// is remembered for /api/mirror/status but later namespaces still run - a
// frozen trunk must not stop change-ref backup, and vice versa.
func (w *MirrorWorker) SyncOnce(ctx context.Context) error {
	w.syncMu.Lock()
	defer w.syncMu.Unlock()
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	record(w.syncTrunk(ctx))
	record(w.syncNamespace(ctx, "refs/tags/*", "refs/tags/*:refs/tags/*"))
	record(w.syncNamespace(ctx, "refs/changes/*", "+refs/changes/*:refs/changes/*"))

	w.mu.Lock()
	w.lastErr = firstErr
	w.lastSyncAt = time.Now()
	w.mu.Unlock()
	return firstErr
}

// syncTrunk is the leased half (§18.6.1): the push succeeds only if the
// mirror's trunk still points where our cursor says we left it. Anything
// else is a foreign write - freeze, never overwrite (§18.6.4).
func (w *MirrorWorker) syncTrunk(ctx context.Context) error {
	trunk := "refs/heads/" + w.TrunkRef
	localTip, err := gitRevParse(w.Remote.RepoDir, trunk)
	if err != nil {
		return nil // unborn trunk - nothing to mirror yet
	}

	cursor, known, err := w.Store.GetMirrorCursor(ctx, mirrorRemoteName, trunk)
	if err != nil {
		return err
	}
	if known && cursor.Frozen {
		return fmt.Errorf("mirror: %s is frozen - unfreeze via POST /api/mirror/unfreeze after reviewing the divergence", trunk)
	}
	if known && cursor.LastSyncedSHA == localTip {
		return nil // current
	}

	remoteSHA, err := w.Remote.LsRemote(trunk)
	if err != nil {
		return fmt.Errorf("mirror: ls-remote: %w", err)
	}
	expected := ""
	if known {
		expected = cursor.LastSyncedSHA
	}
	if remoteSHA != expected && remoteSHA != localTip {
		// Someone wrote the mirror who wasn't us (or the cursor was lost
		// and the mirror isn't empty): invariant 4. No auto-reconcile.
		if err := w.Store.FreezeMirrorCursor(ctx, mirrorRemoteName, trunk); err != nil {
			return err
		}
		return fmt.Errorf("mirror: %s diverged (mirror at %.12s, expected %.12s) - frozen; unfreeze via POST /api/mirror/unfreeze after review", trunk, remoteSHA, expected)
	}
	if remoteSHA != localTip {
		if err := w.Remote.PushWithLease(trunk, remoteSHA); err != nil {
			// Lost the lease between ls-remote and push: same verdict.
			if freezeErr := w.Store.FreezeMirrorCursor(ctx, mirrorRemoteName, trunk); freezeErr != nil {
				return freezeErr
			}
			return fmt.Errorf("mirror: lease lost pushing %s - frozen: %w", trunk, err)
		}
	}
	return w.Store.UpsertMirrorCursor(ctx, mirrorRemoteName, trunk, localTip)
}

// syncNamespace pushes a wildcard refspec and keeps a cursor row purely as
// a status heartbeat (wildcards have no single SHA to lease on; the change
// namespace is server-owned on both sides, tags are append-only by
// convention and a rejected tag rewrite surfaces as the recorded error).
func (w *MirrorWorker) syncNamespace(ctx context.Context, name, refspec string) error {
	if err := w.Remote.PushRefspecs(refspec); err != nil {
		return fmt.Errorf("mirror: push %s: %w", name, err)
	}
	return w.Store.UpsertMirrorCursor(ctx, mirrorRemoteName, name, "")
}

// mirrorStatus is GET /api/mirror/status's shape.
type mirrorStatus struct {
	Configured bool
	RemoteURL  string // credentials never appear; URL carries none by design
	Cursors    []MirrorCursor
	LastError  string `json:",omitempty"`
	LastSyncAt time.Time
}

func (s *Server) handleMirrorStatus(w http.ResponseWriter, r *http.Request) {
	if s.Mirror == nil || s.Mirror.Remote == nil {
		writeJSON(w, http.StatusOK, mirrorStatus{Configured: false})
		return
	}
	cursors, err := s.Store.ListMirrorCursors(r.Context(), mirrorRemoteName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Mirror.mu.Lock()
	lastErr, lastAt := s.Mirror.lastErr, s.Mirror.lastSyncAt
	s.Mirror.mu.Unlock()
	status := mirrorStatus{Configured: true, RemoteURL: s.Mirror.Remote.URL, Cursors: cursors, LastSyncAt: lastAt}
	if lastErr != nil {
		status.LastError = lastErr.Error()
	}
	writeJSON(w, http.StatusOK, status)
}

// handleMirrorUnfreeze is §18.6.4's explicit admin action: adopt the
// mirror's OBSERVED tip as the new lease expectation (so the next sync
// overwrites the divergence exactly once, atomically) and report the diff
// - both tips, so the admin sees what they just sanctioned overwriting.
// Gated like force-land: admins and the deploy token; agents never.
func (s *Server) handleMirrorUnfreeze(w http.ResponseWriter, r *http.Request) {
	if s.Mirror == nil || s.Mirror.Remote == nil {
		http.Error(w, "no mirror configured", http.StatusNotFound)
		return
	}
	if apiErr := authorizeForceLand(s.principalFor(r), s.laneFor(r)); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Ref == "" {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "ref",
			Message:    "which frozen ref to unfreeze",
			Suggestion: `POST {"ref": "refs/heads/` + s.TrunkRef + `"}`,
		})
		return
	}

	remoteSHA, err := s.Mirror.Remote.LsRemote(body.Ref)
	if err != nil {
		http.Error(w, fmt.Sprintf("ls-remote: %v", err), http.StatusBadGateway)
		return
	}
	localTip, _ := gitRevParse(s.RepoDir, body.Ref)
	if err := s.Store.UpsertMirrorCursor(r.Context(), mirrorRemoteName, body.Ref, remoteSHA); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("runkod: mirror %s UNFROZEN by %s - mirror tip %.12s will be overwritten with local %.12s on next sync",
		body.Ref, forceActor(s.principalFor(r)), remoteSHA, localTip)
	s.Mirror.Trigger()
	writeJSON(w, http.StatusOK, map[string]string{
		"ref":        body.Ref,
		"mirror_tip": remoteSHA,
		"local_tip":  localTip,
		"action":     "cursor re-pointed at the mirror's observed tip; the next sync overwrites it with local truth",
	})
}

func gitRevParse(repoDir, ref string) (string, error) {
	p := &Processor{RepoDir: repoDir}
	return p.runGit(nil, "rev-parse", "--verify", ref)
}

// mirrorFrozenCount powers the runkod_mirror_frozen gauge.
func (s *Server) mirrorFrozenCount(ctx context.Context) int {
	if s.Mirror == nil {
		return 0
	}
	cursors, err := s.Store.ListMirrorCursors(ctx, mirrorRemoteName)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range cursors {
		if c.Frozen {
			n++
		}
	}
	return n
}
