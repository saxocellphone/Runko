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
	// Description / TestPlan are §8.6's change summaries: prose about what
	// the change does and how it was verified, set explicitly via
	// POST /api/changes/{key}/describe - never derived from the commit
	// message. Unlike Title they do NOT move with the head: an amend
	// re-gates checks and approvals but keeps the blurb (automerge's
	// arming survives amends for the same reason). Display metadata and
	// §14.10.3 release-changelog input, never a merge gate.
	Description string
	TestPlan    string
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
	// Automerge arms the when-ready land (§13.5): the AutomergeWorker
	// lands this Change automatically once merge requirements go green.
	// Survives amends by design - §13.5's amend semantics already reset
	// approvals and checks, so nothing lands ungated. AutomergeBy is the
	// arming principal; it becomes landed_by on the automatic land.
	Automerge   bool
	AutomergeBy string
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

// Release is one immutable release record (§14.10.3, stage 17b): project
// version = annotated tag = trunk commit = newest landed Change. There are
// no update or delete verbs anywhere - a wrong release is followed by a
// corrected one (GitHub immutable-releases parity); the tag -> commit ->
// Change chain is the attestation anchor.
type Release struct {
	ProjectName string
	ProjectPath string
	Version     string
	TagRef      string
	TagSHA      string
	TargetSHA   string
	// HeadChangeKey is the newest landed Change the release includes -
	// the changelog spans (previous release's target, this Change].
	HeadChangeKey string
	Changelog     string
	CreatedBy     string
	CreatedAt     time.Time
}

// WorkspaceEvent is one stats-only workspace-activity row (§12.6, stage
// 18): what happened in a workspace and how big it was - never file
// content (§12.1). IDs are strictly increasing per store (BIGSERIAL in
// Postgres; MemStore mirrors that) - the timeline orders and clients
// dedupe by them.
type WorkspaceEvent struct {
	ID           int64
	Type         string // one of the WorkspaceEvent* consts below
	WorkspaceID  string
	Branch       string
	Actor        string // principal name; "" = the anonymous deploy token
	SHA          string // snapshot/head sha at emission
	ChangeKey    string // set on change_* events
	FilesChanged int
	Additions    int // numstat totals; binary files count 0/0
	Deletions    int
	OccurredAt   time.Time
}

// Workspace-event types (§12.6) - mirrored by the workspace_events
// event_type CHECK constraint; a new type is a migration, not just a
// const.
const (
	WorkspaceEventSnapshotPushed  = "snapshot_pushed"
	WorkspaceEventChangePushed    = "change_pushed"
	WorkspaceEventChangeLanded    = "change_landed"
	WorkspaceEventChangeAbandoned = "change_abandoned"
	WorkspaceEventWorkspaceClosed = "workspace_closed"
)

// workspaceEventRetentionCap bounds each workspace's timeline (§12.6):
// RecordWorkspaceEvent prunes oldest-first past this after every insert.
const workspaceEventRetentionCap = 500

// WorkspaceEventAgentActivity is the bus-only poke type published after an
// accepted activity batch (§12.6.1). It is deliberately NOT in the stored
// const block above: passing it to RecordWorkspaceEvent would violate the
// workspace_events event_type CHECK (loudly in Postgres, silently in
// MemStore). Activity rows live in workspace_activity; the bus frame only
// tells watchers to refetch via ListWorkspaceActivity.
const WorkspaceEventAgentActivity = "agent_activity"

// WorkspaceActivity is one harness-reported agent-activity row (§12.6.1,
// stage 19): what the agent SAYS it is doing - reading, editing, running.
// Client-claimed and observability-only by decision: nothing may feed
// these rows into policy, gates, or affected computation. Detail is
// metadata (a path, a command line), truncated and secret-scanned at
// ingest - never file content (§12.1). IDs are strictly increasing per
// store, like WorkspaceEvent's.
type WorkspaceActivity struct {
	ID          int64
	WorkspaceID string
	Actor       string // principal name from ingest auth; "" = the anonymous deploy token
	Kind        string // one of the WorkspaceActivity* consts below
	Detail      string
	SessionID   string // harness coding-session id (§7.2's audit link); "" when unreported
	OccurredAt  time.Time
}

// Workspace-activity kinds (§12.6.1) - mirrored by the workspace_activity
// kind CHECK constraint. The vocabulary is deliberately soft at the edge:
// ingest coerces unknown kinds to "note" (telemetry never fails the work
// it describes), so only these ever reach the store.
const (
	WorkspaceActivityRead    = "read"
	WorkspaceActivityEdit    = "edit"
	WorkspaceActivityCommand = "command"
	WorkspaceActivitySearch  = "search"
	WorkspaceActivityNote    = "note"
)

// workspaceActivityRetentionCap bounds each workspace's activity feed
// (§12.6.1): RecordWorkspaceActivity prunes oldest-first past this after
// every batch. Higher than the timeline's cap - agents emit tool calls at
// hertz - and still bounded: this is a live view, not an archive.
const workspaceActivityRetentionCap = 1000

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

// InviteRequest is one public-intake submission bound for the operator's
// mailbox (§15.1 invite requests, decided 2026-07-13) - a deployment-wide
// outbox row the mailer service drains and acks. Kind separates the login
// gate's "how do I get the invite code?" asks from the landing page's
// contact messages; both ride the same lifecycle.
type InviteRequest struct {
	ID            string
	Kind          string // "invite" | "contact"
	Name          string
	Email         string
	Message       string
	Status        string // "pending" | "sent" | "failed" | "dead_letter"
	Attempt       int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// errNoInviteRequest lets the ack handlers answer a structured 404
// without string-matching store errors.
var errNoInviteRequest = fmt.Errorf("runkod: no such invite request")

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

	// SetChangeAutomerge arms (with the arming principal recorded) or
	// disarms the when-ready land on an open Change.
	SetChangeAutomerge(ctx context.Context, changeKey string, enabled bool, by string) (Change, error)

	// UpdateChangeDescription sets §8.6's summary fields (see
	// Change.Description). Both values are written as given - the describe
	// endpoint resolves omitted-field-preserves semantics before calling.
	UpdateChangeDescription(ctx context.Context, changeKey, description, testPlan string) (Change, error)

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

	// CreateRelease records one immutable release row (§14.10.3, stage
	// 17b); a duplicate (project, version) is an error (the UNIQUE
	// constraint - callers surface it as version_exists). The Store
	// deliberately has no update/delete verbs for releases.
	CreateRelease(ctx context.Context, r Release) (Release, error)
	// ListReleases returns a project's releases newest-first; limit <= 0
	// means unbounded (the ListChangesPage convention).
	ListReleases(ctx context.Context, projectName string, limit, offset int) ([]Release, error)
	// GetLatestRelease returns the newest release for projectName; ok
	// false when the project has never been released.
	GetLatestRelease(ctx context.Context, projectName string) (Release, bool, error)

	// UpsertCheckRun creates a check run for (changeKey, headSHA, name) if
	// none exists yet, or updates status/conclusion in place otherwise -
	// report-check posts a status transition for the SAME logical run
	// (queued -> in_progress -> completed), a different flow from
	// RerunCheck's explicit new-attempt semantics (§14.4.2). After a
	// rerun, the update targets the rerun's (latest) attempt.
	UpsertCheckRun(ctx context.Context, changeKey, headSHA string, run checks.CheckRunView) error
	// CopyPassingCheckRuns clones the latest PASSING completed attempt of
	// each check name from fromHead to toHead, stamped with its provenance
	// (CheckRunView.CopiedFromHeadSHA) - the §13.5 trivial-rebase
	// carry-forward (2026-07-15). Names already reported at toHead are
	// left alone (a racing real report is fresher truth than a copy).
	// Returns the copied names.
	CopyPassingCheckRuns(ctx context.Context, changeKey, fromHead, toHead string) ([]string, error)
	// ListCheckRuns returns one view per check NAME at (changeKey, headSHA)
	// - the latest attempt where attempts exist, since that's the only one
	// the merge gate cares about (§14.4.2).
	ListCheckRuns(ctx context.Context, changeKey, headSHA string) ([]checks.CheckRunView, error)
	// RerunCheck resets checkName at the Change's CURRENT head to a fresh
	// queued attempt (§14.4.2's re-run flow; stage 12c-③ wires it to the
	// wire for the first time) and returns the new view. requestedBy is
	// audit attribution, "" for the anonymous deploy token.
	RerunCheck(ctx context.Context, changeKey, checkName, requestedBy string) (checks.CheckRunView, error)

	// Deploy records (§14.10 inverted CD trigger; db/migrations/0021): the
	// server-of-record for a landed commit's rollout. OpenDeployRecord (called
	// on land) names the affected images the post-land build must report and is
	// idempotent - a re-land never resets reported digests. RecordDeployImage
	// upserts one reported digest keyed (trunk_sha, image) and reports whether
	// THIS call completed the expected set (pending->ready), so the caller
	// emits deploy.images_ready exactly once; ok is false for an unknown sha.
	// GetDeployRecord reads the record (ok false when the land opened none).
	OpenDeployRecord(ctx context.Context, trunkSHA, changeKey, provenance string, expected []string) error
	RecordDeployImage(ctx context.Context, trunkSHA string, img DeployImageRow) (rec DeployRecord, ok, nowReady bool, err error)
	GetDeployRecord(ctx context.Context, trunkSHA string) (DeployRecord, bool, error)

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
	// (credential.go), never a plaintext token. PER-ORG since migration
	// 0017 (user direction 2026-07-13): an account is (org, name) - the
	// same name in two orgs is two independent accounts. Errors if the
	// name is taken IN THAT ORG. Operator principals (--principal) stay
	// daemon config, server-wide, and are checked FIRST everywhere - a
	// signup can never shadow one (the handler rejects colliding names
	// before calling this). email is OPTIONAL (migration 0022): "" means
	// the person skipped it and the column stays NULL.
	CreatePrincipal(ctx context.Context, org, name, credentialHash, email string) error
	GetStoredPrincipal(ctx context.Context, org, name string) (StoredPrincipal, bool, error)

	// Agent principals: ephemeral per-task identities (agentprincipal.go).
	// Mint errors on a name collision (the caller retries with a fresh
	// suffix); lookups return rows regardless of liveness - callers apply
	// Live() so an expired credential fails auth while its row still
	// answers "who was this" for attribution.
	MintAgentPrincipal(ctx context.Context, ap AgentPrincipal) (AgentPrincipal, error)
	GetAgentPrincipalByName(ctx context.Context, name string) (AgentPrincipal, bool, error)
	GetAgentPrincipalByTokenHash(ctx context.Context, hash string) (AgentPrincipal, bool, error)
	ListAgentPrincipals(ctx context.Context) ([]AgentPrincipal, error)
	RevokeAgentPrincipal(ctx context.Context, name string) error
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

	// RecordWorkspaceEvent appends one stats-only activity row (§12.6)
	// and prunes the workspace's timeline to workspaceEventRetentionCap.
	// The returned event carries the store-assigned strictly-increasing
	// ID. Zero OccurredAt means "now".
	RecordWorkspaceEvent(ctx context.Context, ev WorkspaceEvent) (WorkspaceEvent, error)
	// ListWorkspaceEvents returns a workspace's timeline newest-first;
	// limit <= 0 means unbounded (the ListReleases convention).
	ListWorkspaceEvents(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceEvent, error)
	// RecordWorkspaceActivity appends one harness-reported batch
	// (§12.6.1) and prunes the workspace's feed to
	// workspaceActivityRetentionCap. Ingest normalizes kind/detail
	// before calling; rows come back with IDs and timestamps assigned.
	RecordWorkspaceActivity(ctx context.Context, events []WorkspaceActivity) ([]WorkspaceActivity, error)
	// ListWorkspaceActivity returns a workspace's activity feed
	// newest-first; limit <= 0 means unbounded.
	ListWorkspaceActivity(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceActivity, error)
	// LatestWorkspaceActivity returns each listed workspace's newest
	// activity row (§12.6.1's at-a-glance line); workspaces that never
	// reported are absent from the map.
	LatestWorkspaceActivity(ctx context.Context, workspaceIDs []string) (map[string]WorkspaceActivity, error)
	// DeleteWorkspaceActivity removes a workspace's whole feed -
	// workspace delete only (close keeps it), like the timeline.
	DeleteWorkspaceActivity(ctx context.Context, workspaceID string) error
	// DeleteWorkspaceEvents removes a workspace's whole timeline -
	// deleteWorkspaceCore's companion to DeleteWorkspace. Closing a
	// workspace keeps its history; deleting it does not.
	DeleteWorkspaceEvents(ctx context.Context, workspaceID string) error

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

	// Invite requests (§15.1, decided 2026-07-13): deployment-wide rows
	// with the webhook-outbox lifecycle; only the default (root) server
	// registers the routes, so per-org stores never see these calls.
	// CreateInviteRequest reports created=false (and writes nothing) when
	// a live - pending or failed - request of the same kind already holds
	// the same email, case-insensitively: the intake answers an idempotent
	// 202, and an invite ask never shadows a contact message or vice versa.
	CreateInviteRequest(ctx context.Context, kind, name, email, message string) (req InviteRequest, created bool, err error)
	// ListDueInviteRequests returns pending/failed rows whose
	// next_attempt_at has passed, oldest-due first.
	ListDueInviteRequests(ctx context.Context, now time.Time) ([]InviteRequest, error)
	// RecordInviteSendResult acks one mailer attempt: sendErr == "" marks
	// the row sent; anything else bumps attempt, stamps last_error, and
	// either reschedules (checks.NextBackoff) or dead-letters at
	// checks.MaxDeliveryAttempts. Unknown ids return errNoInviteRequest.
	RecordInviteSendResult(ctx context.Context, id, sendErr string, backoffBase, backoffMax time.Duration, now time.Time) (InviteRequest, error)
	// CountLiveInviteRequests counts pending+failed rows - the intake's
	// backlog cap.
	CountLiveInviteRequests(ctx context.Context) (int, error)
}

// MemStore is an in-memory Store - the "Eval / dev" deployment profile
// (§9.3), not merely a test double (see doc.go). Safe for concurrent use.
// StoredPrincipal is one self-service registered identity (§15.1 sign-up)
// - always human; agent principals carry policy and remain operator
// config.
type StoredPrincipal struct {
	// Org scopes the account (migration 0017): the same name in two orgs
	// is two independent accounts.
	Org            string
	Name           string
	CredentialHash string
	// Email is optional (migration 0022, 2026-07-20): "" is the NULL
	// column - an account that predates the field, or a person who
	// skipped it. Collected, never verified, never a credential: nothing
	// authenticates or authorizes on it.
	Email string
}

type MemStore struct {
	mu            sync.Mutex
	changes       map[string]Change
	checkRuns     map[string]map[string]checks.CheckRunView // changeKey|headSHA -> name -> run
	approvals     map[string]map[string]Approval            // changeKey -> ownerRef -> approval
	comments      map[string][]Comment                      // changeKey -> comments, creation order
	reviewReqs    map[string]map[string]ReviewRequest       // changeKey -> reviewer -> request
	releases      map[string][]Release                      // projectName -> releases, creation order
	deployRecords map[string]*memDeployRecord               // trunk_sha -> deploy record (§14.10 CD trigger)
	workspaces    map[string]Workspace
	wsEvents      map[string][]WorkspaceEvent // workspaceID -> events, id order
	wsEventSeq    int64
	wsActivity    map[string][]WorkspaceActivity // workspaceID -> activity, id order
	wsActivitySeq int64
	agents        map[string]AgentPrincipal
	principals    map[string]StoredPrincipal // key: org + "\x00" + name
	deliveries    map[string]*memDelivery
	inviteReqs    map[string]*InviteRequest
	mirrors       map[string]MirrorCursor
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
		changes:       make(map[string]Change),
		checkRuns:     make(map[string]map[string]checks.CheckRunView),
		approvals:     make(map[string]map[string]Approval),
		comments:      make(map[string][]Comment),
		reviewReqs:    make(map[string]map[string]ReviewRequest),
		releases:      make(map[string][]Release),
		deployRecords: make(map[string]*memDeployRecord),
		workspaces:    make(map[string]Workspace),
		wsEvents:      make(map[string][]WorkspaceEvent),
		wsActivity:    make(map[string][]WorkspaceActivity),
		agents:        make(map[string]AgentPrincipal),
		deliveries:    make(map[string]*memDelivery),
		inviteReqs:    make(map[string]*InviteRequest),
		mirrors:       make(map[string]MirrorCursor),
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
	// Landed listings read in landing order - landed_at DESC, matching
	// Postgres (finding #45). Everything else: MemStore has no monotonic
	// change number (Postgres orders by it, newest first); sort by
	// ChangeKey for a deterministic listing. ChangeKey also breaks
	// landed_at ties, which same-instant fake clocks make common.
	sort.Slice(out, func(i, j int) bool {
		if state == "landed" && !out[i].LandedAt.Equal(out[j].LandedAt) {
			return out[i].LandedAt.After(out[j].LandedAt)
		}
		return out[i].ChangeKey < out[j].ChangeKey
	})
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

func (s *MemStore) SetChangeAutomerge(ctx context.Context, changeKey string, enabled bool, by string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	c.Automerge = enabled
	c.AutomergeBy = ""
	if enabled {
		c.AutomergeBy = by
	}
	s.changes[changeKey] = c
	return c, nil
}

func (s *MemStore) UpdateChangeDescription(ctx context.Context, changeKey, description, testPlan string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.changes[changeKey]
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	c.Description = description
	c.TestPlan = testPlan
	s.changes[changeKey] = c
	return c, nil
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

func (s *MemStore) CreateRelease(ctx context.Context, r Release) (Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.releases[r.ProjectName] {
		if existing.Version == r.Version {
			return Release{}, fmt.Errorf("runkod: release %s %s already exists", r.ProjectName, r.Version)
		}
	}
	r.CreatedAt = s.now()
	s.releases[r.ProjectName] = append(s.releases[r.ProjectName], r)
	return r, nil
}

func (s *MemStore) ListReleases(ctx context.Context, projectName string, limit, offset int) ([]Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.releases[projectName]
	// Newest first (creation order reversed), the SQL ORDER BY mirror.
	rev := make([]Release, len(all))
	for i, r := range all {
		rev[len(all)-1-i] = r
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rev) {
		return []Release{}, nil
	}
	rev = rev[offset:]
	if limit > 0 && limit < len(rev) {
		rev = rev[:limit]
	}
	return rev, nil
}

func (s *MemStore) GetLatestRelease(ctx context.Context, projectName string) (Release, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.releases[projectName]
	if len(all) == 0 {
		return Release{}, false, nil
	}
	return all[len(all)-1], true, nil
}

// DeployRecord is the inverted CD trigger's server-of-record for one landed
// trunk commit (§14.10, db/migrations/0021): the affected images whose digests
// the post-land build must report, plus the digests reported so far. When
// Reported covers Expected the record is "ready" and runkod emits
// deploy.images_ready; the runko-deployer pins the digests and Argo CD rolls.
type DeployRecord struct {
	TrunkSHA   string
	ChangeKey  string
	Expected   []string
	Reported   []DeployImageRow
	State      string // "pending" | "ready"
	Provenance string
}

// DeployImageRow is one built image's reported digest. ImageRef is the full
// pushed reference sans digest, so the deployer pins image_ref@digest and
// stays registry-agnostic.
type DeployImageRow struct {
	Image    string
	ImageRef string
	Digest   string
	RunURL   string
}

// memDeployRecord is MemStore's mutable form (a map for O(1) image upsert);
// the Store methods return the immutable DeployRecord view.
type memDeployRecord struct {
	changeKey  string
	expected   []string
	provenance string
	reported   map[string]DeployImageRow
	state      string
}

func (s *MemStore) OpenDeployRecord(ctx context.Context, trunkSHA, changeKey, provenance string, expected []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.deployRecords[trunkSHA]; ok {
		return nil // idempotent: never reset an existing record's reported digests
	}
	s.deployRecords[trunkSHA] = &memDeployRecord{
		changeKey:  changeKey,
		expected:   append([]string(nil), expected...),
		provenance: provenance,
		reported:   map[string]DeployImageRow{},
		state:      "pending",
	}
	return nil
}

func (s *MemStore) RecordDeployImage(ctx context.Context, trunkSHA string, img DeployImageRow) (DeployRecord, bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.deployRecords[trunkSHA]
	if !ok {
		return DeployRecord{}, false, false, nil
	}
	r.reported[img.Image] = img
	nowReady := false
	if r.state != "ready" && deployComplete(r.expected, r.reported) {
		r.state = "ready"
		nowReady = true
	}
	return memDeployToDomain(trunkSHA, r), true, nowReady, nil
}

func (s *MemStore) GetDeployRecord(ctx context.Context, trunkSHA string) (DeployRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.deployRecords[trunkSHA]
	if !ok {
		return DeployRecord{}, false, nil
	}
	return memDeployToDomain(trunkSHA, r), true, nil
}

// deployComplete reports whether every expected image has a reported digest.
func deployComplete(expected []string, reported map[string]DeployImageRow) bool {
	for _, e := range expected {
		if _, ok := reported[e]; !ok {
			return false
		}
	}
	return true
}

func memDeployToDomain(trunkSHA string, r *memDeployRecord) DeployRecord {
	imgs := make([]DeployImageRow, 0, len(r.reported))
	for _, img := range r.reported {
		imgs = append(imgs, img)
	}
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Image < imgs[j].Image })
	return DeployRecord{
		TrunkSHA:   trunkSHA,
		ChangeKey:  r.changeKey,
		Expected:   append([]string(nil), r.expected...),
		Reported:   imgs,
		State:      r.state,
		Provenance: r.provenance,
	}
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

// CopyPassingCheckRuns: the MemStore half of the §13.5 trivial-rebase
// carry-forward - see the Store interface doc.
func (s *MemStore) CopyPassingCheckRuns(ctx context.Context, changeKey, fromHead, toHead string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.checkRuns[checkRunKey(changeKey, fromHead)]
	toKey := checkRunKey(changeKey, toHead)
	if s.checkRuns[toKey] == nil {
		s.checkRuns[toKey] = make(map[string]checks.CheckRunView)
	}
	var names []string
	for name, run := range from {
		if run.Status != checks.CheckStatusCompleted || run.Conclusion != checks.ConclusionSuccess {
			continue
		}
		if _, taken := s.checkRuns[toKey][name]; taken {
			continue // a run already reported at the new head wins
		}
		run.CopiedFromHeadSHA = fromHead
		s.checkRuns[toKey][name] = run
		names = append(names, name)
	}
	return names, nil
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
	ws.CreatedAt = s.now().Unix()
	s.workspaces[ws.ID] = ws
	return ws, nil
}

func (s *MemStore) GetWorkspace(ctx context.Context, id string) (Workspace, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.workspaces[id]
	return ws, ok, nil
}

func principalKey(org, name string) string { return org + "\x00" + name }

func (s *MemStore) CreatePrincipal(ctx context.Context, org, name, credentialHash, email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.principals == nil {
		s.principals = make(map[string]StoredPrincipal)
	}
	if _, taken := s.principals[principalKey(org, name)]; taken {
		return fmt.Errorf("runkod: principal %q already exists in org %q", name, org)
	}
	s.principals[principalKey(org, name)] = StoredPrincipal{Org: org, Name: name, CredentialHash: credentialHash, Email: email}
	return nil
}

func (s *MemStore) GetStoredPrincipal(ctx context.Context, org, name string) (StoredPrincipal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, ok := s.principals[principalKey(org, name)]
	return sp, ok, nil
}

// ListPrincipalOrgs returns every org-scoped account carrying this name -
// the hub's cross-org resolution and the 403-vs-401 distinction.
func (s *MemStore) ListPrincipalOrgs(ctx context.Context, name string) ([]StoredPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []StoredPrincipal
	for _, sp := range s.principals {
		if sp.Name == name {
			out = append(out, sp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return out, nil
}

func (s *MemStore) MintAgentPrincipal(ctx context.Context, ap AgentPrincipal) (AgentPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.agents[ap.Name]; taken {
		return AgentPrincipal{}, fmt.Errorf("runkod: agent principal %q already exists", ap.Name)
	}
	s.agents[ap.Name] = ap
	return ap, nil
}

func (s *MemStore) GetAgentPrincipalByName(ctx context.Context, name string) (AgentPrincipal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ap, ok := s.agents[name]
	return ap, ok, nil
}

func (s *MemStore) GetAgentPrincipalByTokenHash(ctx context.Context, hash string) (AgentPrincipal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ap := range s.agents {
		if ap.TokenHash == hash {
			return ap, true, nil
		}
	}
	return AgentPrincipal{}, false, nil
}

func (s *MemStore) ListAgentPrincipals(ctx context.Context) ([]AgentPrincipal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentPrincipal, 0, len(s.agents))
	for _, ap := range s.agents {
		out = append(out, ap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemStore) RevokeAgentPrincipal(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ap, ok := s.agents[name]
	if !ok {
		return fmt.Errorf("runkod: no such agent principal %q", name)
	}
	ap.Revoked = true
	s.agents[name] = ap
	return nil
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

func (s *MemStore) RecordWorkspaceEvent(ctx context.Context, ev WorkspaceEvent) (WorkspaceEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsEventSeq++
	ev.ID = s.wsEventSeq
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = s.now()
	}
	evs := append(s.wsEvents[ev.WorkspaceID], ev)
	if over := len(evs) - workspaceEventRetentionCap; over > 0 {
		evs = append([]WorkspaceEvent(nil), evs[over:]...)
	}
	s.wsEvents[ev.WorkspaceID] = evs
	return ev, nil
}

func (s *MemStore) ListWorkspaceEvents(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.wsEvents[workspaceID]
	rev := make([]WorkspaceEvent, len(all))
	for i, ev := range all {
		rev[len(all)-1-i] = ev
	}
	if offset >= len(rev) {
		return []WorkspaceEvent{}, nil
	}
	rev = rev[offset:]
	if limit > 0 && limit < len(rev) {
		rev = rev[:limit]
	}
	return rev, nil
}

func (s *MemStore) DeleteWorkspaceEvents(ctx context.Context, workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.wsEvents, workspaceID)
	return nil
}

func (s *MemStore) RecordWorkspaceActivity(ctx context.Context, events []WorkspaceActivity) ([]WorkspaceActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkspaceActivity, 0, len(events))
	for _, ev := range events {
		s.wsActivitySeq++
		ev.ID = s.wsActivitySeq
		if ev.OccurredAt.IsZero() {
			ev.OccurredAt = s.now()
		}
		evs := append(s.wsActivity[ev.WorkspaceID], ev)
		if over := len(evs) - workspaceActivityRetentionCap; over > 0 {
			evs = append([]WorkspaceActivity(nil), evs[over:]...)
		}
		s.wsActivity[ev.WorkspaceID] = evs
		out = append(out, ev)
	}
	return out, nil
}

func (s *MemStore) ListWorkspaceActivity(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.wsActivity[workspaceID]
	rev := make([]WorkspaceActivity, len(all))
	for i, ev := range all {
		rev[len(all)-1-i] = ev
	}
	if offset >= len(rev) {
		return []WorkspaceActivity{}, nil
	}
	rev = rev[offset:]
	if limit > 0 && limit < len(rev) {
		rev = rev[:limit]
	}
	return rev, nil
}

func (s *MemStore) LatestWorkspaceActivity(ctx context.Context, workspaceIDs []string) (map[string]WorkspaceActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]WorkspaceActivity, len(workspaceIDs))
	for _, id := range workspaceIDs {
		if evs := s.wsActivity[id]; len(evs) > 0 {
			out[id] = evs[len(evs)-1]
		}
	}
	return out, nil
}

func (s *MemStore) DeleteWorkspaceActivity(ctx context.Context, workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.wsActivity, workspaceID)
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

func (s *MemStore) CreateInviteRequest(ctx context.Context, kind, name, email, message string) (InviteRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.inviteReqs {
		if r.Kind == kind && (r.Status == "pending" || r.Status == "failed") && strings.EqualFold(r.Email, email) {
			return InviteRequest{}, false, nil
		}
	}
	s.nextID++
	req := &InviteRequest{
		ID: fmt.Sprintf("inv_%d", s.nextID), Kind: kind, Name: name, Email: email, Message: message,
		Status: "pending", CreatedAt: s.now(), NextAttemptAt: s.now(),
	}
	s.inviteReqs[req.ID] = req
	return *req, true, nil
}

func (s *MemStore) ListDueInviteRequests(ctx context.Context, now time.Time) ([]InviteRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []InviteRequest
	for _, r := range s.inviteReqs {
		if r.Status != "pending" && r.Status != "failed" {
			continue
		}
		if r.NextAttemptAt.After(now) {
			continue
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextAttemptAt.Equal(out[j].NextAttemptAt) {
			return out[i].NextAttemptAt.Before(out[j].NextAttemptAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemStore) RecordInviteSendResult(ctx context.Context, id, sendErr string, backoffBase, backoffMax time.Duration, now time.Time) (InviteRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.inviteReqs[id]
	if !ok {
		return InviteRequest{}, errNoInviteRequest
	}
	if sendErr == "" {
		r.Status = "sent"
		return *r, nil
	}
	r.Attempt++
	if r.Attempt >= checks.MaxDeliveryAttempts {
		r.Status = "dead_letter"
	} else {
		r.Status = "failed"
	}
	r.NextAttemptAt = now.Add(checks.NextBackoff(r.Attempt, backoffBase, backoffMax))
	r.LastError = sendErr
	return *r, nil
}

func (s *MemStore) CountLiveInviteRequests(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.inviteReqs {
		if r.Status == "pending" || r.Status == "failed" {
			n++
		}
	}
	return n, nil
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
