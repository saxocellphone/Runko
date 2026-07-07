package runkod

import (
	"context"
	"fmt"
	"sort"
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
	// AuthoredBy / LandedBy are §7.5 attribution via §15.1's interim
	// named-token principals (stage 12c): the principal name that pushed /
	// landed, "" when the anonymous deploy token did. An amend by a
	// different principal overwrites AuthoredBy - last pusher owns the
	// head, which is also who self-approval must be checked against.
	AuthoredBy string
	LandedBy   string
}

// Approval is one recorded owner approval on a Change - the satisfied half
// of §13.5's "required human owners approved" gate. OwnerRef names the owner
// requirement being satisfied (e.g. "group:commerce-eng"); ApprovedBy is who
// granted it. Until real AuthN (§15.1) exists, ApprovedBy is client-supplied
// text trusted because the deploy token gates the API - the same v1 trust
// boundary report-check's Reporter field already lives with. The REQUIRED
// side is never stored: it's recomputed from the tree at read time
// (tree-as-truth, §10.3), so approvals recorded here only ever satisfy
// requirements the tree still asserts.
type Approval struct {
	OwnerRef   string
	ApprovedBy string
	// HeadSHA is the Change head this approval was granted for. The merge
	// gate counts an approval only while this matches the current head
	// (§13.5); "" (pre-stage-12c rows) always reads as stale, fail closed.
	HeadSHA string
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
	CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title, authoredBy string) (Change, error)
	GetChange(ctx context.Context, changeKey string) (Change, bool, error)

	// ListChanges returns every Change in the given state, newest first;
	// state "" means all states (§28.3 stage 12c-③ - the UI's first page).
	ListChanges(ctx context.Context, state string) ([]Change, error)

	// MarkChangeAbandoned moves an open Change to "abandoned" (§7.4's third
	// state, settable for the first time in stage 12c-③). Abandoning an
	// already-abandoned Change is an idempotent no-op; abandoning a LANDED
	// Change is an error - landed is terminal, trunk already has the code.
	MarkChangeAbandoned(ctx context.Context, changeKey string) (Change, error)

	// MarkChangeLanded records a successful land.Land outcome (§13.5, §28.3
	// stage 11b): state -> "landed", landedSHA recorded as-is (may differ
	// from HeadSHA - see Change.LandedSHA's doc comment). landedBy is the
	// landing principal's name, "" for the anonymous deploy token.
	MarkChangeLanded(ctx context.Context, changeKey, landedSHA, landedBy string) (Change, error)

	// RecordApproval records that ownerRef's approval requirement on
	// changeKey is satisfied for headSHA specifically (§13.5, decided
	// 2026-07-07: approvals bind to the approved head - an amend moves the
	// head and the approval stops counting; the row survives for audit).
	// Idempotent: approving the same ownerRef twice is not an error, the
	// latest ApprovedBy/HeadSHA wins.
	RecordApproval(ctx context.Context, changeKey, ownerRef, approvedBy, headSHA string) error
	// ListApprovals returns every recorded approval for changeKey, sorted by
	// OwnerRef for deterministic output.
	ListApprovals(ctx context.Context, changeKey string) ([]Approval, error)

	// UpsertCheckRun creates a check run for (changeKey, headSHA, name) if
	// none exists yet, or updates status/conclusion in place otherwise -
	// report-check posts a status transition for the SAME logical run
	// (queued -> in_progress -> completed), a different flow from
	// RerunCheck's explicit new-attempt semantics (§14.4.2). After a
	// rerun, the update targets the rerun's (latest) attempt.
	UpsertCheckRun(ctx context.Context, changeKey, headSHA string, run checks.CheckRunView) error
	// ListCheckRuns returns one view per check NAME at (changeKey, headSHA)
	// - the latest attempt where attempts exist, since that's the only one
	// the merge gate cares about (§14.4.2).
	ListCheckRuns(ctx context.Context, changeKey, headSHA string) ([]checks.CheckRunView, error)
	// RerunCheck resets checkName at the Change's CURRENT head to a fresh
	// queued attempt (§14.4.2's re-run flow; stage 12c-③ wires it to the
	// wire for the first time) and returns the new view. requestedBy is
	// audit attribution, "" for the anonymous deploy token.
	RerunCheck(ctx context.Context, changeKey, checkName, requestedBy string) (checks.CheckRunView, error)

	// CreateWorkspace registers one workspace (§12.2, §28.3 stage 12b);
	// errors if the ID is already taken. GetWorkspace/ListWorkspaces/
	// UpdateWorkspaceBase are the registry reads and the update-base write.
	// Registry rows are metadata only - snapshot CONTENT lives in Git as
	// refs/workspaces/<id>/head commits, never in the Store.
	CreateWorkspace(ctx context.Context, ws Workspace) (Workspace, error)
	GetWorkspace(ctx context.Context, id string) (Workspace, bool, error)
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	UpdateWorkspaceBase(ctx context.Context, id, baseRevision string) (Workspace, error)

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
	approvals  map[string]map[string]Approval            // changeKey -> ownerRef -> approval
	workspaces map[string]Workspace
	deliveries map[string]*memDelivery
	nextID     int
	// Now overrides the clock check-run timestamps use; nil means time.Now
	// (tests inject a fake clock to exercise §14.4.2 staleness).
	Now func() time.Time
}

func (s *MemStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
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
		approvals:  make(map[string]map[string]Approval),
		workspaces: make(map[string]Workspace),
		deliveries: make(map[string]*memDelivery),
	}
}

func (s *MemStore) CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title, authoredBy string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.changes[changeKey]; ok {
		existing.HeadSHA = headSHA
		existing.GitRef = gitRef
		existing.AuthoredBy = authoredBy
		if existing.State == "abandoned" {
			// Re-pushing an abandoned Change reopens it (§7.4; the webhook
			// enum modeled change.reopened from day one). Landed stays
			// landed - terminal.
			existing.State = "open"
		}
		s.changes[changeKey] = existing
		return existing, nil
	}
	change := Change{
		ChangeKey: changeKey, State: "open",
		BaseSHA: baseSHA, HeadSHA: headSHA, GitRef: gitRef, Title: title,
		AuthoredBy: authoredBy,
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

func (s *MemStore) ListChanges(ctx context.Context, state string) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Change, 0, len(s.changes))
	for _, c := range s.changes {
		if state == "" || c.State == state {
			out = append(out, c)
		}
	}
	// MemStore has no monotonic change number (Postgres orders by it,
	// newest first); sort by ChangeKey for a deterministic listing.
	sort.Slice(out, func(i, j int) bool { return out[i].ChangeKey < out[j].ChangeKey })
	return out, nil
}

func (s *MemStore) MarkChangeAbandoned(ctx context.Context, changeKey string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	switch c.State {
	case "landed":
		return Change{}, fmt.Errorf("runkod: change %q already landed - landed is terminal", changeKey)
	case "abandoned":
		return c, nil // idempotent
	}
	c.State = "abandoned"
	s.changes[changeKey] = c
	return c, nil
}

func (s *MemStore) MarkChangeLanded(ctx context.Context, changeKey, landedSHA, landedBy string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	c.State = "landed"
	c.LandedSHA = landedSHA
	c.LandedBy = landedBy
	s.changes[changeKey] = c
	return c, nil
}

func (s *MemStore) RecordApproval(ctx context.Context, changeKey, ownerRef, approvedBy, headSHA string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.changes[changeKey]; !ok {
		return fmt.Errorf("runkod: no such change %q", changeKey)
	}
	if s.approvals[changeKey] == nil {
		s.approvals[changeKey] = make(map[string]Approval)
	}
	s.approvals[changeKey][ownerRef] = Approval{OwnerRef: ownerRef, ApprovedBy: approvedBy, HeadSHA: headSHA}
	return nil
}

func (s *MemStore) ListApprovals(ctx context.Context, changeKey string) ([]Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byRef := s.approvals[changeKey]
	out := make([]Approval, 0, len(byRef))
	for _, a := range byRef {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OwnerRef < out[j].OwnerRef })
	return out, nil
}

func checkRunKey(changeKey, headSHA string) string { return changeKey + "|" + headSHA }

func (s *MemStore) UpsertCheckRun(ctx context.Context, changeKey, headSHA string, run checks.CheckRunView) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := checkRunKey(changeKey, headSHA)
	if s.checkRuns[key] == nil {
		s.checkRuns[key] = make(map[string]checks.CheckRunView)
	}
	if run.LastSeenAt.IsZero() {
		run.LastSeenAt = s.now()
	}
	if run.TTLSeconds == 0 {
		run.TTLSeconds = checks.DefaultTTLSeconds
	}
	s.checkRuns[key][run.Name] = run
	return nil
}

func (s *MemStore) RerunCheck(ctx context.Context, changeKey, checkName, requestedBy string) (checks.CheckRunView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change, ok := s.changes[changeKey]
	if !ok {
		return checks.CheckRunView{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	key := checkRunKey(changeKey, change.HeadSHA)
	if s.checkRuns[key] == nil {
		s.checkRuns[key] = make(map[string]checks.CheckRunView)
	}
	// MemStore keeps one view per name, so a rerun RESETS it to queued
	// rather than growing an attempt history - the same latest-attempt view
	// ListCheckRuns reports from Postgres, which does keep the history.
	run := checks.CheckRunView{
		Name: checkName, Status: checks.CheckStatusQueued,
		LastSeenAt: s.now(), TTLSeconds: checks.DefaultTTLSeconds,
	}
	s.checkRuns[key][checkName] = run
	_ = requestedBy // audit attribution is the caller's webhook/log concern in MemStore
	return run, nil
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

func (s *MemStore) CreateWorkspace(ctx context.Context, ws Workspace) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.workspaces[ws.ID]; taken {
		return Workspace{}, fmt.Errorf("runkod: workspace %q already exists", ws.ID)
	}
	s.workspaces[ws.ID] = ws
	return ws, nil
}

func (s *MemStore) GetWorkspace(ctx context.Context, id string) (Workspace, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.workspaces[id]
	return ws, ok, nil
}

func (s *MemStore) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Workspace, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemStore) UpdateWorkspaceBase(ctx context.Context, id, baseRevision string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.workspaces[id]
	if !ok {
		return Workspace{}, fmt.Errorf("runkod: no such workspace %q", id)
	}
	ws.BaseRevision = baseRevision
	s.workspaces[id] = ws
	return ws, nil
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
