// Automerge (§13.5's "when ready" verb, 2026-07-12): a Change is ARMED
// once, then lands itself the moment its merge requirements go green -
// the loop every client has been hand-rolling as poll-and-land. One
// decision core arms/disarms; one worker sweeps armed changes, kicked
// eagerly by the events that flip mergeability (check reports, approvals,
// a parent landing) and on a slow interval for everything else (trunk
// moves, staleness expiry).
//
// The automatic land goes through the exact same landChangeCore every
// human land uses - same gate, same attribution machinery (landed_by =
// the ARMING principal), never force. States a land cannot resolve on its
// own (requires_revalidation, conflicts - both need a re-push) leave the
// bit armed and are retried only when the (head, trunk) pair changes, so
// the worker never spins on a stuck Change.
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
)

// setAutomergeCore arms or disarms the when-ready land. Only OPEN changes
// can be armed (landed is terminal, abandoned must be reopened first);
// disarming is always allowed. The armer is recorded for attribution -
// the automatic land runs under their name.
func (s *Server) setAutomergeCore(ctx context.Context, key string, enabled bool, principal *Principal) (Change, *apiError) {
	change, ok, err := s.Store.GetChange(ctx, key)
	if err != nil {
		return Change{}, internalErr(err)
	}
	if !ok {
		return Change{}, plainErr(http.StatusNotFound, "change not found")
	}
	if enabled && change.State != "open" {
		return Change{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    fmt.Sprintf("change %s is %s - only open changes can arm automerge", key, change.State),
			Suggestion: "re-push to reopen an abandoned change first",
		})
	}
	by := ""
	if principal != nil {
		by = principal.Name
	}
	updated, err := s.Store.SetChangeAutomerge(ctx, key, enabled, by)
	if err != nil {
		return Change{}, internalErr(err)
	}
	if enabled {
		log.Printf("runkod: automerge armed on %s by %q", key, by)
		s.KickAutomerge()
	} else {
		log.Printf("runkod: automerge disarmed on %s", key)
	}
	return updated, nil
}

func (s *Server) handleSetAutomerge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with enabled (bool)",
		})
		return
	}
	change, apiErr := s.setAutomergeCore(r.Context(), r.PathValue("key"), req.Enabled, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, change)
}

// KickAutomerge nudges the worker to sweep now (non-blocking; nil-safe for
// servers that never started one - tests, eval one-shots).
func (s *Server) KickAutomerge() {
	if s.automerge != nil {
		s.automerge.Kick()
	}
}

// AutomergeWorker sweeps armed open Changes and lands the mergeable ones.
type AutomergeWorker struct {
	Server   *Server
	Interval time.Duration // sweep cadence between kicks; default 30s

	kick chan struct{}
	mu   sync.Mutex
	// attempted remembers (change|head|trunkTip) triples whose land came
	// back non-retryable (revalidation/conflict) so the worker does not
	// re-attempt - and re-log - until something actually moved.
	attempted map[string]bool
}

// NewAutomergeWorker wires the worker onto its server (and the server's
// Kick path back onto the worker).
func NewAutomergeWorker(s *Server, interval time.Duration) *AutomergeWorker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	w := &AutomergeWorker{Server: s, Interval: interval, kick: make(chan struct{}, 1), attempted: map[string]bool{}}
	s.automerge = w
	return w
}

func (w *AutomergeWorker) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// Run sweeps until ctx ends.
func (w *AutomergeWorker) Run(ctx context.Context) {
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-t.C:
		}
		w.SweepOnce(ctx)
	}
}

// SweepOnce evaluates every armed open Change once. Exported for tests
// (and the eval profile's determinism).
func (w *AutomergeWorker) SweepOnce(ctx context.Context) {
	s := w.Server
	open, err := s.Store.ListChanges(ctx, "open")
	if err != nil {
		log.Printf("runkod: automerge sweep: list open changes: %v", err)
		return
	}
	trunkTip := s.trunkTip()
	for _, change := range open {
		if !change.Automerge {
			continue
		}
		attemptKey := change.ChangeKey + "|" + change.HeadSHA + "|" + trunkTip
		w.mu.Lock()
		skip := w.attempted[attemptKey]
		w.mu.Unlock()
		if skip {
			continue
		}

		reqs, err := s.mergeRequirements(ctx, change.ChangeKey, change, nil)
		if err != nil {
			log.Printf("runkod: automerge %s: merge requirements: %v", change.ChangeKey, err)
			continue
		}
		if !reqs.Mergeable {
			continue // not ready; a future kick or sweep re-evaluates
		}

		// Same core as a human land: same gate, never force, attributed
		// to the ARMING principal.
		decision, apiErr := s.landChangeCore(ctx, change.ChangeKey, change, nil, &Principal{Name: change.AutomergeBy, Stored: true}, false)
		switch {
		case apiErr != nil:
			// Gate raced shut between the check and the land - or a real
			// error; either way the next event re-evaluates.
			log.Printf("runkod: automerge %s: land refused: %s", change.ChangeKey, apiErr.Err.Message)
		case decision.Landed:
			log.Printf("runkod: automerge landed %s at %s (armed by %q)", change.ChangeKey, decision.LandedSHA, change.AutomergeBy)
		case decision.RequiresRevalidation, len(decision.Conflicts) > 0:
			// Needs a re-push (§13.5's recovery is client-side work) -
			// stay armed, but do not spin: retry only when head or trunk
			// moves.
			w.mu.Lock()
			w.attempted[attemptKey] = true
			// The memo only ever needs the CURRENT frontier; drop stale
			// entries wholesale before they accumulate.
			if len(w.attempted) > 512 {
				w.attempted = map[string]bool{attemptKey: true}
			}
			w.mu.Unlock()
			log.Printf("runkod: automerge %s: land needs a re-push (revalidation/conflict) - staying armed", change.ChangeKey)
		case decision.RaceRetryExhausted:
			// Transient contention: the next sweep retries.
		}
	}
}

// trunkTip resolves the current trunk head ("" on an unborn trunk) - part
// of the retry memo key so a trunk move re-arms stuck attempts.
func (s *Server) trunkTip() string {
	out, err := exec.Command("git", "--git-dir", s.RepoDir, "rev-parse", "--verify", "-q", "refs/heads/"+s.TrunkRef).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
