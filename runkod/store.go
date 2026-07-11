package runkod

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
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
	// LandedAt is when MarkChangeLanded recorded the land - the zero Time
	// until then (Postgres has carried changes.landed_at since 0001; this
	// surfaces it). Display metadata, never a gate input.
	LandedAt time.Time
	// AuthoredBy / LandedBy are §7.5 attribution via §15.1's interim
	// named-token principals (stage 12c): the principal name that pushed /
	// landed, "" when the anonymous deploy token did. An amend by a
	// different principal overwrites AuthoredBy - last pusher owns the
	// head, which is also who self-approval must be checked against.
	AuthoredBy string
	LandedBy   string
	// LandedForced is the durable audit bit for the admin force-land
	// override (§13.5): true when the owner/check gates were bypassed.
	LandedForced bool
	// OriginWorkspace / OriginBranch are push provenance (§12.2's branch ↔
	// stack mapping): the workspace branch this Change was pushed from,
	// stamped by `runko change push` as git push options and validated
	// against the workspace registry at receive time. Empty for plain-git
	// pushers, the web create-project flow, and bot lanes - advisory
	// metadata for grouping/display, never a merge gate. An amend that
	// carries no options PRESERVES the existing origin (a plain-git amend
	// of a workspace Change must not erase provenance); an amend that
	// carries options moves it, matching AuthoredBy's last-pusher rule.
	OriginWorkspace string
	OriginBranch    string
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

// Comment is one review-conversation comment on a Change (§13.4.1, decided
// 2026-07-10). The anchor is change-level (Path empty), file-level (Path
// only), or line-level (Path+Side+Line). HeadSHA binds the comment to the
// Change head it was written against, exactly like Approval.HeadSHA - a
// comment whose HeadSHA differs from the current head renders as
// "outdated", never repositioned ("" reads as outdated, fail closed).
// ParentID non-empty makes this a reply; threads are one level deep (the
// core enforces root-only parents). Resolved is meaningful on root
// comments only.
type Comment struct {
	ID            string
	Author        string
	AuthorIsAgent bool
	Body          string
	Path          string
	Side          string // "base" | "head" | "" (non-line anchor)
	Line          int
	HeadSHA       string
	ParentID      string
	Resolved      bool
	CreatedAt     time.Time
}

// ReviewRequest is one recorded request_review (§13.4.2). Reviewer and
// RequestedBy are principal names (§15.1 interim registry). The attention
// set is DERIVED at read time from these rows + approvals + comments +
// head_sha; nothing else is stored.
type ReviewRequest struct {
	Reviewer    string
	RequestedBy string
	CreatedAt   time.Time
}

// MirrorCursor is one (remote, ref) sync cursor (§18.6): what the mirror
// last agreed with us about, and whether mirroring that ref is frozen.
type MirrorCursor struct {
	Ref           string
	LastSyncedSHA string
	Frozen        bool
	UpdatedAt     time.Time
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
	// versions of a Change, not the Change itself"). Empty origin
	// workspace/branch on an update preserves the stored origin (see
	// Change.OriginWorkspace).
	CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title, authoredBy, originWorkspace, originBranch string) (Change, error)
	GetChange(ctx context.Context, changeKey string) (Change, bool, error)

	// ListChanges returns every Change in the given state, newest first;
	// state "" means all states (§28.3 stage 12c-③ - the UI's first page).
	ListChanges(ctx context.Context, state string) ([]Change, error)

	// ListChangesPage is ListChanges bounded to one page: at most limit
	// changes starting at offset, same order, same state semantics. limit
	// <= 0 means unbounded (offset still applies). Landed history grows
	// without limit, so list READS must not materialize all of it to
	// serve a page (stage 15: the landed tab).
	ListChangesPage(ctx context.Context, state string, limit, offset int) ([]Change, error)

	// MarkChangeAbandoned moves an open Change to "abandoned" (§7.4's third
	// state, settable for the first time in stage 12c-③). Abandoning an
	// already-abandoned Change is an idempotent no-op; abandoning a LANDED
	// Change is an error - landed is terminal, trunk already has the code.
	MarkChangeAbandoned(ctx context.Context, changeKey string) (Change, error)

	// MarkChangeLanded records a successful land.Land outcome (§13.5, §28.3
	// stage 11b): state -> "landed", landedSHA recorded as-is (may differ
	// from HeadSHA - see Change.LandedSHA's doc comment). landedBy is the
	// landing principal's name, "" for the anonymous deploy token.
	MarkChangeLanded(ctx context.Context, changeKey, landedSHA, landedBy string, forced bool) (Change, error)

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

	// CreateComment records one review comment (§13.4.1). The caller
	// (commentChangeCore) has already validated the anchor, the one-level
	// thread rule, and stamped HeadSHA; the Store assigns ID and CreatedAt
	// and returns the completed row.
	CreateComment(ctx context.Context, changeKey string, c Comment) (Comment, error)
	// ListComments returns changeKey's comments oldest-first. limit <= 0
	// means unbounded (offset still applies) - the ListChangesPage
	// convention.
	ListComments(ctx context.Context, changeKey string, limit, offset int) ([]Comment, error)
	GetComment(ctx context.Context, changeKey, commentID string) (Comment, bool, error)
	// SetCommentResolved flips the resolved bit. The core has already
	// checked the comment is a root and the principal may resolve it; a
	// missing comment is an error here (the row must exist).
	SetCommentResolved(ctx context.Context, changeKey, commentID string, resolved bool) error

	// UpsertReviewRequest records a request_review (§13.4.2) - idempotent,
	// latest requestedBy wins. ListReviewRequests returns them sorted by
	// Reviewer for deterministic output.
	UpsertReviewRequest(ctx context.Context, changeKey, reviewer, requestedBy string) error
	ListReviewRequests(ctx context.Context, changeKey string) ([]ReviewRequest, error)

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

	// Mirror cursors (§18.6, outbound M1): per-(remote, ref) sync state.
	// GetMirrorCursor's bool reports row existence; UpsertMirrorCursor
	// records a successful sync (and clears frozen - the sync that lands a
	// cursor IS the proof the lease held); FreezeMirrorCursor is invariant
	// 4's loud stop, cleared only by the explicit admin unfreeze (which
	// re-points the cursor at observed remote reality via Upsert).
	GetMirrorCursor(ctx context.Context, remote, ref string) (MirrorCursor, bool, error)
	ListMirrorCursors(ctx context.Context, remote string) ([]MirrorCursor, error)
	UpsertMirrorCursor(ctx context.Context, remote, ref, lastSyncedSHA string) error
	FreezeMirrorCursor(ctx context.Context, remote, ref string) error

	// CreateWorkspace registers one workspace (§12.2, §28.3 stage 12b);
	// errors if the ID is already taken. GetWorkspace/ListWorkspaces/
	// UpdateWorkspaceBase are the registry reads and the update-base write.
	// Registry rows are metadata only - snapshot CONTENT lives in Git as
	// refs/workspaces/<id>/head commits, never in the Store.
	CreateWorkspace(ctx context.Context, ws Workspace) (Workspace, error)
	GetWorkspace(ctx context.Context, id string) (Workspace, bool, error)

	// CreatePrincipal registers a self-service human principal (§15.1
	// sign-up flow; db/migrations/0004): name + PBKDF2 credential hash
	// (credential.go), never a plaintext token. Errors if the name is
	// taken. Operator principals (--principal) stay daemon config and are
	// checked FIRST everywhere - a signup can never shadow one (the
	// handler rejects colliding names before calling this).
	CreatePrincipal(ctx context.Context, name, credentialHash string) error
	GetStoredPrincipal(ctx context.Context, name string) (StoredPrincipal, bool, error)
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	UpdateWorkspaceBase(ctx context.Context, id, baseRevision string) (Workspace, error)
	// SetWorkspaceStatus moves a workspace between active/detached/closed
	// (§12.2). "closed" is load-bearing at receive time: the funnel refuses
	// snapshot and change pushes into a closed workspace (single-use agent
	// workspaces close on task conclusion).
	SetWorkspaceStatus(ctx context.Context, id, status string) error
	// DeleteWorkspace removes the registry row outright - the id becomes
	// reusable. The row is metadata only (§12.2); the caller owns deleting
	// the workspace's snapshot refs beside it (deleteWorkspaceCore does
	// both, guards included). Deleting an unknown id is an error.
	DeleteWorkspace(ctx context.Context, id string) error

	// Ping reports whether the Store's backing service is reachable -
	// /readyz's dependency probe (§9.4's stage-14 conventions). Cheap
	// enough to call on every readiness scrape.
	Ping(ctx context.Context) error

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
// StoredPrincipal is one self-service registered identity (§15.1 sign-up)
// - always human; agent principals carry policy and remain operator
// config.
type StoredPrincipal struct {
	Name           string
	CredentialHash string
}

type MemStore struct {
	mu         sync.Mutex
	changes    map[string]Change
	checkRuns  map[string]map[string]checks.CheckRunView // changeKey|headSHA -> name -> run
	approvals  map[string]map[string]Approval            // changeKey -> ownerRef -> approval
	comments   map[string][]Comment                      // changeKey -> comments, creation order
	reviewReqs map[string]map[string]ReviewRequest       // changeKey -> reviewer -> request
	workspaces map[string]Workspace
	principals map[string]StoredPrincipal
	deliveries map[string]*memDelivery
	mirrors    map[string]MirrorCursor
	// Directory state (orghub.go): org registry + memberships + settings.
	// Only the hub's designated directory store (the default org's)
	// carries these - per-org MemStores leave them empty.
	orgNames    []string
	orgMembers  map[string]map[string]string // org -> principal -> role
	orgSettings map[string]OrgSettings
	orgArchived map[string]bool
	nextID      int
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
		comments:   make(map[string][]Comment),
		reviewReqs: make(map[string]map[string]ReviewRequest),
		workspaces: make(map[string]Workspace),
		deliveries: make(map[string]*memDelivery),
		mirrors:    make(map[string]MirrorCursor),
	}
}

func mirrorKey(remote, ref string) string { return remote + "\x00" + ref }

func (s *MemStore) GetMirrorCursor(ctx context.Context, remote, ref string) (MirrorCursor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.mirrors[mirrorKey(remote, ref)]
	return c, ok, nil
}

func (s *MemStore) ListMirrorCursors(ctx context.Context, remote string) ([]MirrorCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []MirrorCursor
	prefix := remote + "\x00"
	for k, c := range s.mirrors {
		if strings.HasPrefix(k, prefix) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

func (s *MemStore) UpsertMirrorCursor(ctx context.Context, remote, ref, lastSyncedSHA string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mirrors[mirrorKey(remote, ref)] = MirrorCursor{Ref: ref, LastSyncedSHA: lastSyncedSHA, Frozen: false, UpdatedAt: s.now()}
	return nil
}

func (s *MemStore) FreezeMirrorCursor(ctx context.Context, remote, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.mirrors[mirrorKey(remote, ref)]
	c.Ref = ref
	c.Frozen = true
	c.UpdatedAt = s.now()
	s.mirrors[mirrorKey(remote, ref)] = c
	return nil
}

func (s *MemStore) CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title, authoredBy, originWorkspace, originBranch string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.changes[changeKey]; ok {
		existing.HeadSHA = headSHA
		existing.GitRef = gitRef
		// Title moves with the head like authored_by: an amend that rewords
		// the commit subject is the pusher renaming the Change.
		existing.Title = title
		existing.AuthoredBy = authoredBy
		// base_sha moves with the head (found by compose edge case E7): the
		// pusher computed it as merge-base(new head, trunk). Freezing the
		// creation-time base made §13.5's requires_revalidation a permanent
		// dead end - the prescribed rebase+re-push never cleared it.
		existing.BaseSHA = baseSHA
		if originWorkspace != "" {
			existing.OriginWorkspace = originWorkspace
			existing.OriginBranch = originBranch
		}
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
		AuthoredBy:      authoredBy,
		OriginWorkspace: originWorkspace, OriginBranch: originBranch,
	}
	s.changes[changeKey] = change
	return change, nil
}

// Ping is trivially healthy for the in-memory profile.
func (s *MemStore) Ping(ctx context.Context) error { return nil }

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

func (s *MemStore) ListChangesPage(ctx context.Context, state string, limit, offset int) ([]Change, error) {
	all, err := s.ListChanges(ctx, state)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(all) {
		return nil, nil
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit:limit]
	}
	return all, nil
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

func (s *MemStore) MarkChangeLanded(ctx context.Context, changeKey, landedSHA, landedBy string, forced bool) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	c.State = "landed"
	c.LandedSHA = landedSHA
	c.LandedBy = landedBy
	c.LandedForced = forced
	c.LandedAt = s.now()
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

func (s *MemStore) CreateComment(ctx context.Context, changeKey string, c Comment) (Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.changes[changeKey]; !ok {
		return Comment{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	s.nextID++
	c.ID = fmt.Sprintf("cmt_%d", s.nextID)
	c.CreatedAt = s.now()
	s.comments[changeKey] = append(s.comments[changeKey], c)
	return c, nil
}

func (s *MemStore) ListComments(ctx context.Context, changeKey string, limit, offset int) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.comments[changeKey]
	if offset < 0 {
		offset = 0
	}
	if offset >= len(all) {
		return []Comment{}, nil
	}
	rest := all[offset:]
	if limit > 0 && limit < len(rest) {
		rest = rest[:limit]
	}
	out := make([]Comment, len(rest))
	copy(out, rest)
	return out, nil
}

func (s *MemStore) GetComment(ctx context.Context, changeKey, commentID string) (Comment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.comments[changeKey] {
		if c.ID == commentID {
			return c, true, nil
		}
	}
	return Comment{}, false, nil
}

func (s *MemStore) SetCommentResolved(ctx context.Context, changeKey, commentID string, resolved bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.comments[changeKey]
	for i := range list {
		if list[i].ID == commentID {
			list[i].Resolved = resolved
			return nil
		}
	}
	return fmt.Errorf("runkod: no such comment %q on change %q", commentID, changeKey)
}

func (s *MemStore) UpsertReviewRequest(ctx context.Context, changeKey, reviewer, requestedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.changes[changeKey]; !ok {
		return fmt.Errorf("runkod: no such change %q", changeKey)
	}
	if s.reviewReqs[changeKey] == nil {
		s.reviewReqs[changeKey] = make(map[string]ReviewRequest)
	}
	existing, ok := s.reviewReqs[changeKey][reviewer]
	created := s.now()
	if ok {
		created = existing.CreatedAt
	}
	s.reviewReqs[changeKey][reviewer] = ReviewRequest{Reviewer: reviewer, RequestedBy: requestedBy, CreatedAt: created}
	return nil
}

func (s *MemStore) ListReviewRequests(ctx context.Context, changeKey string) ([]ReviewRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byReviewer := s.reviewReqs[changeKey]
	out := make([]ReviewRequest, 0, len(byReviewer))
	for _, r := range byReviewer {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reviewer < out[j].Reviewer })
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
	// A report without a link must not erase the link an earlier transition
	// carried - the same COALESCE PostgresStore's upsert applies.
	if run.DetailsURL == "" {
		run.DetailsURL = s.checkRuns[key][run.Name].DetailsURL
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

func (s *MemStore) CreatePrincipal(ctx context.Context, name, credentialHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.principals == nil {
		s.principals = make(map[string]StoredPrincipal)
	}
	if _, taken := s.principals[name]; taken {
		return fmt.Errorf("runkod: principal %q already exists", name)
	}
	s.principals[name] = StoredPrincipal{Name: name, CredentialHash: credentialHash}
	return nil
}

func (s *MemStore) GetStoredPrincipal(ctx context.Context, name string) (StoredPrincipal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.principals[name]
	return sp, ok, nil
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

func (s *MemStore) SetWorkspaceStatus(ctx context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.workspaces[id]
	if !ok {
		return fmt.Errorf("runkod: no such workspace %q", id)
	}
	ws.Status = status
	s.workspaces[id] = ws
	return nil
}

func (s *MemStore) DeleteWorkspace(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[id]; !ok {
		return fmt.Errorf("runkod: no such workspace %q", id)
	}
	delete(s.workspaces, id)
	return nil
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

// Directory (orghub.go): the mem-mode global account + membership view.
// Orgs and memberships live only in the hub's designated directory store
// (the default org's MemStore) - like every other MemStore fact, they do
// not survive a restart, which is the documented eval-profile semantic.
var _ Directory = (*MemStore)(nil)

func (s *MemStore) EnsureOrg(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orgMembers == nil {
		s.orgMembers = map[string]map[string]string{}
	}
	if _, ok := s.orgMembers[name]; !ok {
		s.orgMembers[name] = map[string]string{}
		s.orgNames = append(s.orgNames, name)
	}
	return nil
}

func (s *MemStore) OrgMemberRole(ctx context.Context, orgName, principal string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	role, ok := s.orgMembers[orgName][principal]
	return role, ok, nil
}

func (s *MemStore) UpsertOrgMember(ctx context.Context, orgName, principal, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	members, ok := s.orgMembers[orgName]
	if !ok {
		return fmt.Errorf("runkod: no org named %q", orgName)
	}
	members[principal] = role
	return nil
}

func (s *MemStore) ListOrgMemberships(ctx context.Context, principal string) ([]OrgMembership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OrgMembership
	for _, org := range s.orgNames {
		if s.orgArchived[org] {
			continue
		}
		if role, ok := s.orgMembers[org][principal]; ok {
			out = append(out, OrgMembership{Org: org, Role: role})
		}
	}
	return out, nil
}

func (s *MemStore) ListOrgNames(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.orgNames...), nil
}

func (s *MemStore) ListOrgRecords(ctx context.Context) ([]OrgRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OrgRecord, 0, len(s.orgNames))
	for _, n := range s.orgNames {
		out = append(out, OrgRecord{Name: n, Archived: s.orgArchived[n]})
	}
	return out, nil
}

func (s *MemStore) SetOrgArchived(ctx context.Context, orgName string, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orgArchived == nil {
		s.orgArchived = map[string]bool{}
	}
	s.orgArchived[orgName] = archived
	return nil
}

func (s *MemStore) RemoveOrgMember(ctx context.Context, orgName, principal string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.orgMembers[orgName], principal)
	return nil
}

func (s *MemStore) ListOrgMembers(ctx context.Context, orgName string) ([]OrgMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OrgMember
	for name, role := range s.orgMembers[orgName] {
		out = append(out, OrgMember{Name: name, Role: role})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetOrgSettings: the mem directory answers the zero value for ANY org
// name, known or not - the default org has no EnsureOrg call in mem mode
// and must still be configurable.
func (s *MemStore) GetOrgSettings(ctx context.Context, orgName string) (OrgSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orgSettings[orgName], nil
}

func (s *MemStore) UpdateOrgSettings(ctx context.Context, orgName string, settings OrgSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orgSettings == nil {
		s.orgSettings = map[string]OrgSettings{}
	}
	s.orgSettings[orgName] = settings
	return nil
}
