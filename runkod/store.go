package runkod

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/saxocellphone/runko/checks"
)

// Change is runkod's in-daemon view of a Change - independent of
// internal/dbgen so Store implementations don't need a live database to be
// exercised (see doc.go).
type Change struct {
	ChangeKey string // the stable Change-Id (§7.4)
	State     string // "open" | "landed" | "abandoned"
	BaseSHA   string
	HeadSHA   string
	GitRef    string
	Title     string
	// LandedSHA is the trunk-side commit the Change actually landed as -
	// HeadSHA on a fast-forward, but a NEW commit SHA when land.Land had to
	// rebase (§13.5). Empty until MarkChangeLanded is called.
	LandedSHA string
}

// WebhookDelivery is one outbox row (§14.4.1).
type WebhookDelivery struct {
	ID            string
	EventType     string
	Payload       []byte
	Status        string // "pending" | "delivered" | "failed" | "dead_letter"
	Attempt       int
	NextAttemptAt time.Time
	LastError     string
}

// Store is everything the daemon needs across the receive funnel, the
// Checks API, and the webhook outbox. Kept specific to this package's
// needs (not a generic repository interface) so a Postgres-backed
// implementation stays a thin adapter over internal/dbgen, same shape as
// index/receive/checks' existing persist.go files.
type Store interface {
	// CreateOrUpdateChange mirrors receive.CreateOrUpdateChange's
	// create-vs-update-by-change_key semantics (§7.4: "commits are
	// versions of a Change, not the Change itself").
	CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title string) (Change, error)
	GetChange(ctx context.Context, changeKey string) (Change, bool, error)

	// MarkChangeLanded records a successful land.Land outcome (§13.5, §28.3
	// stage 11b): state -> "landed", landedSHA recorded as-is (may differ
	// from HeadSHA - see Change.LandedSHA's doc comment).
	MarkChangeLanded(ctx context.Context, changeKey, landedSHA string) (Change, error)

	// UpsertCheckRun creates a check run for (changeKey, headSHA, name) if
	// none exists yet, or updates status/conclusion in place otherwise -
	// report-check posts a status transition for the SAME logical run
	// (queued -> in_progress -> completed), a different flow from
	// checks.RerunCheck's explicit new-attempt semantics (§14.4.2).
	UpsertCheckRun(ctx context.Context, changeKey, headSHA string, run checks.CheckRunView) error
	ListCheckRuns(ctx context.Context, changeKey, headSHA string) ([]checks.CheckRunView, error)

	// EnqueueWebhook enqueues one outbox row. eventType mirrors the
	// envelope's own "type" field (e.g. "change.updated") - a durable Store
	// needs it as a first-class column (internal/dbgen's webhook_deliveries.
	// event_type), not something to re-parse out of the payload later.
	EnqueueWebhook(ctx context.Context, eventType string, payload []byte) (id string, err error)
	ListDueWebhookDeliveries(ctx context.Context, now time.Time) ([]WebhookDelivery, error)
	RecordDeliveryResult(ctx context.Context, id string, result checks.DeliveryAttempt, backoffBase, backoffMax time.Duration, now time.Time) error
}

// MemStore is an in-memory Store - the "Eval / dev" deployment profile
// (§9.3), not merely a test double (see doc.go). Safe for concurrent use.
type MemStore struct {
	mu         sync.Mutex
	changes    map[string]Change
	checkRuns  map[string]map[string]checks.CheckRunView // changeKey|headSHA -> name -> run
	deliveries map[string]*memDelivery
	nextID     int
}

type memDelivery struct {
	WebhookDelivery
	attempt int
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		changes:    make(map[string]Change),
		checkRuns:  make(map[string]map[string]checks.CheckRunView),
		deliveries: make(map[string]*memDelivery),
	}
}

func (s *MemStore) CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.changes[changeKey]; ok {
		existing.HeadSHA = headSHA
		existing.GitRef = gitRef
		s.changes[changeKey] = existing
		return existing, nil
	}
	change := Change{
		ChangeKey: changeKey, State: "open",
		BaseSHA: baseSHA, HeadSHA: headSHA, GitRef: gitRef, Title: title,
	}
	s.changes[changeKey] = change
	return change, nil
}

func (s *MemStore) GetChange(ctx context.Context, changeKey string) (Change, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	return c, ok, nil
}

func (s *MemStore) MarkChangeLanded(ctx context.Context, changeKey, landedSHA string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	c.State = "landed"
	c.LandedSHA = landedSHA
	s.changes[changeKey] = c
	return c, nil
}

func checkRunKey(changeKey, headSHA string) string { return changeKey + "|" + headSHA }

func (s *MemStore) UpsertCheckRun(ctx context.Context, changeKey, headSHA string, run checks.CheckRunView) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := checkRunKey(changeKey, headSHA)
	if s.checkRuns[key] == nil {
		s.checkRuns[key] = make(map[string]checks.CheckRunView)
	}
	s.checkRuns[key][run.Name] = run
	return nil
}

func (s *MemStore) ListCheckRuns(ctx context.Context, changeKey, headSHA string) ([]checks.CheckRunView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byName := s.checkRuns[checkRunKey(changeKey, headSHA)]
	out := make([]checks.CheckRunView, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	return out, nil
}

func (s *MemStore) EnqueueWebhook(ctx context.Context, eventType string, payload []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := fmt.Sprintf("dlv_%d", s.nextID)
	s.deliveries[id] = &memDelivery{WebhookDelivery: WebhookDelivery{
		ID: id, EventType: eventType, Payload: payload, Status: "pending", NextAttemptAt: time.Time{},
	}}
	return id, nil
}

func (s *MemStore) ListDueWebhookDeliveries(ctx context.Context, now time.Time) ([]WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []WebhookDelivery
	for _, d := range s.deliveries {
		if d.Status != "pending" && d.Status != "failed" {
			continue
		}
		if d.NextAttemptAt.After(now) {
			continue
		}
		out = append(out, d.WebhookDelivery)
	}
	return out, nil
}

func (s *MemStore) RecordDeliveryResult(ctx context.Context, id string, result checks.DeliveryAttempt, backoffBase, backoffMax time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deliveries[id]
	if !ok {
		return fmt.Errorf("runkod: no such webhook delivery %q", id)
	}
	d.attempt++
	if result.Success {
		d.Status = "delivered"
		return nil
	}
	if d.attempt >= checks.MaxDeliveryAttempts {
		d.Status = "dead_letter"
	} else {
		d.Status = "failed"
	}
	d.NextAttemptAt = now.Add(checks.NextBackoff(d.attempt, backoffBase, backoffMax))
	if result.Err != nil {
		d.LastError = result.Err.Error()
	}
	d.Attempt = d.attempt
	return nil
}

var _ Store = (*MemStore)(nil)
