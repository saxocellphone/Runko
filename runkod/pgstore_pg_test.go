package runkod

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/dbgen"
	"github.com/saxocellphone/runko/internal/dbtest"
	"github.com/saxocellphone/runko/platform/checks"
)

// pgTestDSN resets the schema (via dbtest.Connect, same as every other
// *_pg_test.go in this repo) and returns the raw DSN string
// BootstrapPostgresStore needs - it opens its own pool, separate from
// dbtest's, exercising the real production bootstrap path rather than a
// test-only shortcut.
func pgTestDSN(t *testing.T) string {
	t.Helper()
	dbtest.Connect(t) // resets schema; skips the test if RUNKO_TEST_DATABASE_URL is unset
	return os.Getenv("RUNKO_TEST_DATABASE_URL")
}

func newTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := pgTestDSN(t)
	store, err := BootstrapPostgresStore(context.Background(), dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore: %v", err)
	}
	t.Cleanup(store.Pool.Close)
	return store
}

func TestBootstrapPostgresStoreIsIdempotent(t *testing.T) {
	dsn := pgTestDSN(t)
	ctx := context.Background()

	first, err := BootstrapPostgresStore(ctx, dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (first): %v", err)
	}
	defer first.Pool.Close()

	second, err := BootstrapPostgresStore(ctx, dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (second): %v", err)
	}
	defer second.Pool.Close()

	if first.OrgID != second.OrgID || first.MonorepoID != second.MonorepoID || first.AuthorActorID != second.AuthorActorID {
		t.Fatalf("expected bootstrapping twice with the same org name to return the same IDs, got %+v vs %+v", first, second)
	}
}

func TestPostgresStoreCreateOrUpdateChangeAndGetChange(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	created, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (create): %v", err)
	}
	if created.State != "open" {
		t.Fatalf("expected a new Change to start open, got %+v", created)
	}

	updated, err := store.CreateOrUpdateChange(ctx, "Iabc", "base2", "head2", "refs/changes/1/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (update): %v", err)
	}
	if updated.HeadSHA != "head2" {
		t.Fatalf("expected head_sha to advance, got %+v", updated)
	}
	if updated.BaseSHA != "base2" {
		t.Fatalf("expected base_sha to move with the amend (edge case E7: a frozen base makes requires_revalidation permanent), got %+v", updated)
	}

	got, ok, err := store.GetChange(ctx, "Iabc")
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if got.HeadSHA != "head2" {
		t.Fatalf("expected GetChange to reflect the update, got %+v", got)
	}

	if _, ok, err := store.GetChange(ctx, "no-such-change"); err != nil || ok {
		t.Fatalf("expected GetChange for an unknown key to return ok=false, got ok=%v err=%v", ok, err)
	}
}

func TestPostgresStoreCheckRunUpsertReflectsLatestStatus(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}

	if err := store.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusQueued}); err != nil {
		t.Fatalf("UpsertCheckRun (queued): %v", err)
	}
	if err := store.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess}); err != nil {
		t.Fatalf("UpsertCheckRun (completed): %v", err)
	}

	runs, err := store.ListCheckRuns(ctx, "Iabc", "head1")
	if err != nil {
		t.Fatalf("ListCheckRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected the status transition to update the SAME run (attempt 1), not create a second one, got %d: %+v", len(runs), runs)
	}
	if runs[0].Status != checks.CheckStatusCompleted || runs[0].Conclusion != checks.ConclusionSuccess {
		t.Fatalf("expected the run to reflect the latest status, got %+v", runs[0])
	}
}

// TestPostgresStoreApprovalRoundTrip exercises stage 2's
// change_owner_requirements table through its first real caller (§28.3
// stage 11c): recording an approval creates the requirement row, satisfies
// it, and attributes it to a real actors row (external_ref = approved_by),
// which ListApprovals resolves back to the name.
func TestPostgresStoreApprovalRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}

	approvals, err := store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals (empty): %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("expected no approvals yet, got %+v", approvals)
	}

	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice", "head1"); err != nil {
		t.Fatalf("RecordApproval: %v", err)
	}
	// Idempotent: approving the same ref again is not an error.
	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice", "head1"); err != nil {
		t.Fatalf("RecordApproval (repeat): %v", err)
	}

	approvals, err = store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].OwnerRef != "group:commerce-eng" || approvals[0].ApprovedBy != "alice" {
		t.Fatalf("expected one approval by alice for group:commerce-eng, got %+v", approvals)
	}
	// §13.5: the approval round-trips WITH the head it was granted for -
	// the merge gate's staleness comparison depends on this surviving
	// Postgres, not just MemStore.
	if approvals[0].HeadSHA != "head1" {
		t.Fatalf("expected the approval bound to head1, got %q", approvals[0].HeadSHA)
	}

	// Re-approving after an amend re-binds the row to the new head (the
	// PK is (change_id, owner_ref) - one row per owner, latest head wins).
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head2", "refs/changes/1/head", "title", "", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange (amend): %v", err)
	}
	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice", "head2"); err != nil {
		t.Fatalf("RecordApproval (re-approve): %v", err)
	}
	approvals, err = store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].HeadSHA != "head2" {
		t.Fatalf("expected the re-approval bound to head2, got %+v", approvals)
	}
}

// TestPostgresStoreWorkspaceRoundTrip exercises stage 2's workspaces table
// through its first real caller (§28.3 stage 12b). The human workspace ID
// lives inside snapshot_ref (no dedicated column), so this also pins the
// derive-both-ways mapping dbWorkspaceToWorkspace does.
func TestPostgresStoreWorkspaceRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	ws := Workspace{
		ID: "payments-fix", Owner: "alice",
		BaseRevision:    "abc123",
		ProjectAffinity: []string{"checkout-api"},
		WriteAllowlist:  []string{"commerce/checkout"},
		SnapshotRef:     "refs/workspaces/payments-fix/head",
		Status:          "active",
	}
	created, err := store.CreateWorkspace(ctx, ws)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if created.ID != "payments-fix" || created.Owner != "alice" || created.Status != "active" {
		t.Fatalf("unexpected created workspace: %+v", created)
	}

	if _, err := store.CreateWorkspace(ctx, ws); err == nil {
		t.Fatalf("expected a duplicate workspace ID to error")
	}

	got, ok, err := store.GetWorkspace(ctx, "payments-fix")
	if err != nil || !ok {
		t.Fatalf("GetWorkspace: ok=%v err=%v", ok, err)
	}
	if got.BaseRevision != "abc123" || len(got.WriteAllowlist) != 1 || got.WriteAllowlist[0] != "commerce/checkout" {
		t.Fatalf("unexpected workspace: %+v", got)
	}
	if _, ok, err := store.GetWorkspace(ctx, "no-such"); err != nil || ok {
		t.Fatalf("expected ok=false for an unknown workspace, got ok=%v err=%v", ok, err)
	}

	// A second workspace with EMPTY affinity/allowlist pins the nil->[]
	// normalization (the stage-9a index.Sync NOT NULL lesson).
	if _, err := store.CreateWorkspace(ctx, Workspace{
		ID: "bare-ws", Owner: "alice", BaseRevision: "abc123",
		SnapshotRef: "refs/workspaces/bare-ws/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace with nil slices: %v", err)
	}

	list, err := store.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 2 || list[0].ID != "bare-ws" || list[1].ID != "payments-fix" {
		t.Fatalf("unexpected list: %+v", list)
	}

	updated, err := store.UpdateWorkspaceBase(ctx, "payments-fix", "def456")
	if err != nil || updated.BaseRevision != "def456" {
		t.Fatalf("UpdateWorkspaceBase: %+v err=%v", updated, err)
	}

	// Status transitions round-trip (UpdateWorkspaceStatus's first caller:
	// single-use agent workspaces make "closed" load-bearing at receive).
	if err := store.SetWorkspaceStatus(ctx, "payments-fix", "closed"); err != nil {
		t.Fatalf("SetWorkspaceStatus: %v", err)
	}
	if ws, _, _ := store.GetWorkspace(ctx, "payments-fix"); ws.Status != "closed" {
		t.Fatalf("expected closed to persist, got %q", ws.Status)
	}

	// Deletion is a hard delete: the row is gone and the id reusable.
	if err := store.DeleteWorkspace(ctx, "bare-ws"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, ok, _ := store.GetWorkspace(ctx, "bare-ws"); ok {
		t.Fatalf("deleted workspace must be gone")
	}
	if err := store.DeleteWorkspace(ctx, "bare-ws"); err == nil {
		t.Fatalf("deleting an unknown workspace must error")
	}
	if _, err := store.CreateWorkspace(ctx, Workspace{
		ID: "bare-ws", Owner: "bob", BaseRevision: "abc123",
		SnapshotRef: "refs/workspaces/bare-ws/head", Status: "active",
	}); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

func TestPostgresStoreWebhookOutboxLifecycle(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	now := time.Now()

	id, err := store.EnqueueWebhook(ctx, "change.opened", []byte(`{"type":"change.opened"}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	due, err := store.ListDueWebhookDeliveries(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	if len(due) != 1 || due[0].ID != id {
		t.Fatalf("expected the enqueued delivery to be due, got %+v", due)
	}

	if err := store.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: false, Err: context.DeadlineExceeded}, time.Hour, time.Hour, now); err != nil {
		t.Fatalf("RecordDeliveryResult (failure): %v", err)
	}
	due, err = store.ListDueWebhookDeliveries(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries after failure: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected the failed delivery to not be due again until its backoff elapses, got %+v", due)
	}

	if err := store.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: true}, time.Hour, time.Hour, now); err != nil {
		t.Fatalf("RecordDeliveryResult (success): %v", err)
	}
	due, err = store.ListDueWebhookDeliveries(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries after success: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected a delivered delivery to never be due again, got %+v", due)
	}
}

// TestPostgresStoreWebhookOutboxIsOrgScoped pins migration-findings #27:
// the daemon runs one OutboxWorker per org server over the same pool, so
// ListDueWebhookDeliveries must return only the calling org's rows - the
// unfiltered version made every worker deliver every org's envelopes
// (observed live as triple repository_dispatch per push).
func TestPostgresStoreWebhookOutboxIsOrgScoped(t *testing.T) {
	dsn := pgTestDSN(t)
	ctx := context.Background()

	acme, err := BootstrapPostgresStore(ctx, dsn, "acme", "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (acme): %v", err)
	}
	defer acme.Pool.Close()
	globex, err := BootstrapPostgresStore(ctx, dsn, "globex", "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (globex): %v", err)
	}
	defer globex.Pool.Close()

	acmeID, err := acme.EnqueueWebhook(ctx, "change.updated", []byte(`{"org":"acme"}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook (acme): %v", err)
	}
	if _, err := globex.EnqueueWebhook(ctx, "change.updated", []byte(`{"org":"globex"}`)); err != nil {
		t.Fatalf("EnqueueWebhook (globex): %v", err)
	}

	due, err := acme.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries (acme): %v", err)
	}
	if len(due) != 1 || due[0].ID != acmeID {
		t.Fatalf("acme's worker must see exactly its own delivery, got %+v", due)
	}
}

// TestPostgresStoreAttributionRoundTrip pins stage 12c-②'s authored_by/
// landed_by attribution through real actors rows: named principals become
// upserted actors, the anonymous deploy token ("") maps to the bootstrap
// placeholder and reads back as "", and an amend moves authored_by to the
// last pusher (who self-approval is checked against).
func TestPostgresStoreAttributionRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	created, err := store.CreateOrUpdateChange(ctx, "Iattr", "base1", "head1", "refs/changes/2/head", "title", "alice", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}
	if created.AuthoredBy != "alice" {
		t.Fatalf("expected AuthoredBy=alice, got %q", created.AuthoredBy)
	}

	// Amend by a different principal: last pusher owns the head.
	updated, err := store.CreateOrUpdateChange(ctx, "Iattr", "base1", "head2", "refs/changes/2/head", "title", "bob", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (amend): %v", err)
	}
	if updated.AuthoredBy != "bob" {
		t.Fatalf("expected AuthoredBy to move to bob on amend, got %q", updated.AuthoredBy)
	}

	landed, err := store.MarkChangeLanded(ctx, "Iattr", "head2", "carol", true)
	if err != nil {
		t.Fatalf("MarkChangeLanded: %v", err)
	}
	if landed.LandedBy != "carol" || landed.State != "landed" {
		t.Fatalf("expected landed by carol, got %+v", landed)
	}
	if !landed.LandedForced {
		t.Fatalf("expected landed_forced audit bit to persist and read back, got %+v", landed)
	}

	// Anonymous stays anonymous, both fields.
	anon, err := store.CreateOrUpdateChange(ctx, "Ianon", "base1", "head1", "refs/changes/3/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (anon): %v", err)
	}
	if anon.AuthoredBy != "" {
		t.Fatalf("expected anonymous AuthoredBy to read back empty, got %q", anon.AuthoredBy)
	}
	if _, err := store.MarkChangeLanded(ctx, "Ianon", "head1", "", false); err != nil {
		t.Fatalf("MarkChangeLanded (anon): %v", err)
	}
	got, ok, err := store.GetChange(ctx, "Ianon")
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if got.LandedForced {
		t.Fatalf("expected an ordinary land to read back landed_forced=false, got %+v", got)
	}
	if got.LandedBy != "" {
		t.Fatalf("expected anonymous LandedBy to read back empty, got %q", got.LandedBy)
	}

	// The same names must come back through the LIST path: ListChanges
	// hydrates attribution via one batch GetActorsByIDs query rather than
	// per-row GetActor round-trips (stage 15: the landed tab paid one
	// round-trip per name at 44 changes).
	// Automerge round-trips: arm records the armer, disarm clears it.
	if armed, err := store.SetChangeAutomerge(ctx, "Iattr", true, "val"); err != nil || !armed.Automerge || armed.AutomergeBy != "val" {
		t.Fatalf("arm automerge: %+v err=%v", armed, err)
	}
	if disarmed, err := store.SetChangeAutomerge(ctx, "Iattr", false, ""); err != nil || disarmed.Automerge || disarmed.AutomergeBy != "" {
		t.Fatalf("disarm automerge: %+v err=%v", disarmed, err)
	}

	landedList, err := store.ListChanges(ctx, "landed")
	if err != nil || len(landedList) != 2 {
		t.Fatalf("ListChanges(landed): got %d changes (%v)", len(landedList), err)
	}
	byKey := map[string]Change{}
	for _, c := range landedList {
		byKey[c.ChangeKey] = c
	}
	if c := byKey["Iattr"]; c.AuthoredBy != "bob" || c.LandedBy != "carol" {
		t.Fatalf("batch hydration: expected Iattr authored=bob landed=carol, got %+v", c)
	}
	if c := byKey["Ianon"]; c.AuthoredBy != "" || c.LandedBy != "" {
		t.Fatalf("batch hydration: expected Ianon anonymous both ways, got %+v", c)
	}
}

// TestPostgresStoreLifecycleAndRerunAttempts covers stage 12c-③'s Postgres
// side: list/abandon/reopen state transitions, and - the subtle one - the
// attempt semantics around reruns: RerunCheck creates attempt N+1, a result
// posted AFTERWARDS must complete the rerun's attempt (UpsertCheckRun now
// resolves the latest attempt; it used to hardcode attempt 1, which would
// have stranded every rerun as forever-queued), and ListCheckRuns collapses
// history to one latest-attempt view per name.
func TestPostgresStoreLifecycleAndRerunAttempts(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	if _, err := store.CreateOrUpdateChange(ctx, "I1", "b", "h1", "r1", "t1", "", "", ""); err != nil {
		t.Fatalf("create I1: %v", err)
	}
	if _, err := store.CreateOrUpdateChange(ctx, "I2", "b", "h2", "r2", "t2", "", "", ""); err != nil {
		t.Fatalf("create I2: %v", err)
	}

	open, err := store.ListChanges(ctx, "open")
	if err != nil || len(open) != 2 {
		t.Fatalf("expected 2 open changes, got %v (%v)", open, err)
	}
	if open[0].ChangeKey != "I2" {
		t.Fatalf("expected newest-first ordering (I2 has the higher number), got %+v", open)
	}

	// ListChangesPage pages the same listing at the SQL layer (stage 15):
	// limit/offset window it newest-first; past-the-end reads are empty,
	// not an error; limit 0 means unbounded.
	if page, err := store.ListChangesPage(ctx, "open", 1, 0); err != nil || len(page) != 1 || page[0].ChangeKey != "I2" {
		t.Fatalf("page(1,0): want [I2], got %+v (%v)", page, err)
	}
	if page, err := store.ListChangesPage(ctx, "open", 1, 1); err != nil || len(page) != 1 || page[0].ChangeKey != "I1" {
		t.Fatalf("page(1,1): want [I1], got %+v (%v)", page, err)
	}
	if page, err := store.ListChangesPage(ctx, "open", 1, 5); err != nil || len(page) != 0 {
		t.Fatalf("page(1,5): want empty, got %+v (%v)", page, err)
	}
	if page, err := store.ListChangesPage(ctx, "open", 0, 1); err != nil || len(page) != 1 || page[0].ChangeKey != "I1" {
		t.Fatalf("page(0,1) unbounded limit: want [I1], got %+v (%v)", page, err)
	}

	// Abandon -> filtered lists reflect it; idempotent; re-push reopens.
	if _, err := store.MarkChangeAbandoned(ctx, "I1"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := store.MarkChangeAbandoned(ctx, "I1"); err != nil {
		t.Fatalf("abandon (idempotent): %v", err)
	}
	if abandoned, err := store.ListChanges(ctx, "abandoned"); err != nil || len(abandoned) != 1 || abandoned[0].ChangeKey != "I1" {
		t.Fatalf("expected I1 abandoned, got %v (%v)", abandoned, err)
	}
	reopened, err := store.CreateOrUpdateChange(ctx, "I1", "b", "h3", "r1", "t1", "", "", "")
	if err != nil || reopened.State != "open" {
		t.Fatalf("expected re-push to reopen, got %+v (%v)", reopened, err)
	}

	// Landed is terminal.
	if _, err := store.MarkChangeLanded(ctx, "I2", "h2", "", false); err != nil {
		t.Fatalf("land I2: %v", err)
	}
	if _, err := store.MarkChangeAbandoned(ctx, "I2"); err == nil {
		t.Fatalf("expected abandoning a landed change to error")
	}

	// Rerun attempt semantics on I1@h3. The first report carries the CI run
	// link; later link-less transitions must not erase it (COALESCE in the
	// upsert - the queued report usually has the URL, completed may not).
	if err := store.UpsertCheckRun(ctx, "I1", "h3", checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusQueued,
		DetailsURL: "https://ci.example.com/runs/9",
	}); err != nil {
		t.Fatalf("report queued with details_url: %v", err)
	}
	if err := store.UpsertCheckRun(ctx, "I1", "h3", checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report attempt 1: %v", err)
	}
	if runs, err := store.ListCheckRuns(ctx, "I1", "h3"); err != nil || len(runs) != 1 || runs[0].DetailsURL != "https://ci.example.com/runs/9" {
		t.Fatalf("expected the details_url to survive a link-less completion, got %+v (%v)", runs, err)
	}
	rerun, err := store.RerunCheck(ctx, "I1", "unit", "alice")
	if err != nil {
		t.Fatalf("RerunCheck: %v", err)
	}
	if rerun.Status != checks.CheckStatusQueued {
		t.Fatalf("expected the rerun view queued, got %+v", rerun)
	}
	runs, err := store.ListCheckRuns(ctx, "I1", "h3")
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected ONE latest-attempt view after rerun, got %v (%v)", runs, err)
	}
	if runs[0].Status != checks.CheckStatusQueued {
		t.Fatalf("expected the latest attempt (queued) to win, got %+v", runs[0])
	}
	if runs[0].LastSeenAt.IsZero() || runs[0].TTLSeconds <= 0 {
		t.Fatalf("expected staleness inputs populated from Postgres, got %+v", runs[0])
	}

	// A result posted after the rerun completes the RERUN's attempt.
	if err := store.UpsertCheckRun(ctx, "I1", "h3", checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report attempt 2: %v", err)
	}
	runs, err = store.ListCheckRuns(ctx, "I1", "h3")
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected one view, got %v (%v)", runs, err)
	}
	if runs[0].Status != checks.CheckStatusCompleted || runs[0].Conclusion != checks.ConclusionSuccess {
		t.Fatalf("expected the rerun attempt completed (not attempt 1 resurrected), got %+v", runs[0])
	}
}

func TestPostgresStorePrincipalRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)

	if _, found, err := store.GetStoredPrincipal(context.Background(), "val"); err != nil || found {
		t.Fatalf("empty table: found=%v err=%v", found, err)
	}
	if err := store.CreatePrincipal(context.Background(), "val", "pbkdf2-sha256$1$c2FsdA$aGFzaA"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	sp, found, err := store.GetStoredPrincipal(context.Background(), "val")
	if err != nil || !found || sp.Name != "val" || sp.CredentialHash != "pbkdf2-sha256$1$c2FsdA$aGFzaA" {
		t.Fatalf("round trip: %+v found=%v err=%v", sp, found, err)
	}
	// The unique constraint backs the handler's race path.
	if err := store.CreatePrincipal(context.Background(), "val", "other"); err == nil {
		t.Fatalf("duplicate CreatePrincipal should error")
	}
}

// TestPostgresStoreMirrorCursors round-trips the §18.6 cursor state:
// upsert (records sync + clears frozen), freeze, list.
func TestPostgresStoreMirrorCursors(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	if _, ok, err := store.GetMirrorCursor(ctx, "mirror", "refs/heads/main"); err != nil || ok {
		t.Fatalf("empty cursor: ok=%v err=%v", ok, err)
	}
	if err := store.UpsertMirrorCursor(ctx, "mirror", "refs/heads/main", "sha1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.FreezeMirrorCursor(ctx, "mirror", "refs/heads/main"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	c, ok, err := store.GetMirrorCursor(ctx, "mirror", "refs/heads/main")
	if err != nil || !ok || !c.Frozen || c.LastSyncedSHA != "sha1" {
		t.Fatalf("frozen cursor: %+v ok=%v err=%v", c, ok, err)
	}
	// Upsert IS the unfreeze (the admin action re-points and thaws in one).
	if err := store.UpsertMirrorCursor(ctx, "mirror", "refs/heads/main", "sha2"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err := store.ListMirrorCursors(ctx, "mirror")
	if err != nil || len(list) != 1 || list[0].Frozen || list[0].LastSyncedSHA != "sha2" {
		t.Fatalf("list after thaw: %+v err=%v", list, err)
	}
}

// TestPostgresDirectoryMultiOrg covers migration 0007's surface: global
// accounts, org membership CRUD, per-org store isolation on one shared
// pool - the durable half of what orghub_test.go proves in mem mode.
func TestPostgresDirectoryMultiOrg(t *testing.T) {
	ctx := context.Background()
	def := newTestPostgresStore(t)

	// Accounts are global: created via one org's store, visible via another's.
	if err := def.CreatePrincipal(ctx, "alice", "hash-a"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	acme, err := NewOrgPostgresStore(ctx, def.Pool, "acme", "main")
	if err != nil {
		t.Fatalf("NewOrgPostgresStore(acme): %v", err)
	}
	if sp, found, err := acme.GetStoredPrincipal(ctx, "alice"); err != nil || !found || sp.CredentialHash != "hash-a" {
		t.Fatalf("alice not visible from acme's store: %+v %v %v", sp, found, err)
	}

	// Membership round-trip against the org rows NewOrgPostgresStore created.
	if _, member, err := def.OrgMemberRole(ctx, "acme", "alice"); err != nil || member {
		t.Fatalf("alice should not be a member yet: %v %v", member, err)
	}
	if err := def.UpsertOrgMember(ctx, "acme", "alice", "admin"); err != nil {
		t.Fatalf("UpsertOrgMember: %v", err)
	}
	role, member, err := def.OrgMemberRole(ctx, "acme", "alice")
	if err != nil || !member || role != "admin" {
		t.Fatalf("OrgMemberRole: %q %v %v", role, member, err)
	}
	// Upsert updates role in place.
	if err := def.UpsertOrgMember(ctx, "acme", "alice", "member"); err != nil {
		t.Fatalf("UpsertOrgMember (role change): %v", err)
	}
	if role, _, _ := def.OrgMemberRole(ctx, "acme", "alice"); role != "member" {
		t.Fatalf("role should now be member, got %q", role)
	}
	memberships, err := def.ListOrgMemberships(ctx, "alice")
	if err != nil || len(memberships) != 1 || memberships[0].Org != "acme" {
		t.Fatalf("ListOrgMemberships: %+v %v", memberships, err)
	}
	names, err := def.ListOrgNames(ctx)
	if err != nil || len(names) != 2 { // the bootstrap org + acme
		t.Fatalf("ListOrgNames: %v %v", names, err)
	}
	members, err := def.ListOrgMembers(ctx, "acme")
	if err != nil || len(members) != 1 || members[0].Name != "alice" {
		t.Fatalf("ListOrgMembers: %+v %v", members, err)
	}

	// Org settings round-trip (migration 0008 JSONB).
	if settings, err := def.GetOrgSettings(ctx, "acme"); err != nil || settings.Description != "" {
		t.Fatalf("fresh settings should be zero: %+v %v", settings, err)
	}
	want := OrgSettings{Description: "acme org", GlobalRequiredChecks: []string{"lint", "e2e"}}
	if err := def.UpdateOrgSettings(ctx, "acme", want); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	if settings, err := def.GetOrgSettings(ctx, "acme"); err != nil || settings.Description != want.Description || len(settings.GlobalRequiredChecks) != 2 {
		t.Fatalf("settings round-trip: %+v %v", settings, err)
	}

	// Member removal + re-add.
	if err := def.RemoveOrgMember(ctx, "acme", "alice"); err != nil {
		t.Fatalf("RemoveOrgMember: %v", err)
	}
	if _, member, _ := def.OrgMemberRole(ctx, "acme", "alice"); member {
		t.Fatalf("alice should be removed")
	}
	if err := def.UpsertOrgMember(ctx, "acme", "alice", "member"); err != nil {
		t.Fatalf("re-add alice: %v", err)
	}

	// Archive round-trip (migration 0009): the record flips, memberships
	// hide the archived org, unarchive restores both.
	if err := def.SetOrgArchived(ctx, "acme", true); err != nil {
		t.Fatalf("SetOrgArchived: %v", err)
	}
	recs, err := def.ListOrgRecords(ctx)
	if err != nil {
		t.Fatalf("ListOrgRecords: %v", err)
	}
	archived := false
	for _, r := range recs {
		if r.Name == "acme" {
			archived = r.Archived
		}
	}
	if !archived {
		t.Fatalf("acme should read archived: %+v", recs)
	}
	if ms, _ := def.ListOrgMemberships(ctx, "alice"); len(ms) != 0 {
		t.Fatalf("memberships should hide archived orgs, got %+v", ms)
	}
	if err := def.SetOrgArchived(ctx, "acme", false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if ms, _ := def.ListOrgMemberships(ctx, "alice"); len(ms) != 1 {
		t.Fatalf("memberships should return after unarchive, got %+v", ms)
	}

	// Store isolation on the shared pool: a Change in acme is invisible
	// from the default org's store.
	if _, err := acme.CreateOrUpdateChange(ctx, "Iacme000000000000000000000000000000000000",
		"", "1111111111111111111111111111111111111111", "refs/changes/x/head", "acme change", "alice", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange in acme: %v", err)
	}
	if list, err := def.ListChanges(ctx, ""); err != nil || len(list) != 0 {
		t.Fatalf("acme change leaked into the default org: %+v %v", list, err)
	}
	if list, err := acme.ListChanges(ctx, ""); err != nil || len(list) != 1 {
		t.Fatalf("acme should list its own change: %+v %v", list, err)
	}
}

// TestPostgresStoreCommentRoundTrip exercises migration 0011 + the extended
// CreateChangeComment through their first caller (§13.4.1, stage 16): every
// new column survives Postgres (head_sha binding, side, one-level parent
// edge, resolved bit), the author round-trips as a typed actors row (agent
// badge included), and review requests upsert idempotently (§13.4.2).
func TestPostgresStoreCommentRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}

	root, err := store.CreateComment(ctx, "Iabc", Comment{
		Author: "review-bot", AuthorIsAgent: true, Body: "nit: wrap this",
		Path: "commerce/checkout/main.go", Side: "head", Line: 42, HeadSHA: "head1",
	})
	if err != nil {
		t.Fatalf("CreateComment (root): %v", err)
	}
	if root.ID == "" || root.CreatedAt.IsZero() {
		t.Fatalf("expected store-assigned id and created_at, got %+v", root)
	}

	if _, err := store.CreateComment(ctx, "Iabc", Comment{
		Author: "alice", Body: "done", HeadSHA: "head1", ParentID: root.ID,
	}); err != nil {
		t.Fatalf("CreateComment (reply): %v", err)
	}

	list, err := store.ListComments(ctx, "Iabc", 0, 0)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 comments, got %+v", list)
	}
	got := list[0]
	if got.ID != root.ID || got.Author != "review-bot" || !got.AuthorIsAgent ||
		got.Path != "commerce/checkout/main.go" || got.Side != "head" || got.Line != 42 ||
		got.HeadSHA != "head1" || got.Resolved {
		t.Fatalf("root comment did not round-trip its columns: %+v", got)
	}
	if list[1].ParentID != root.ID || list[1].AuthorIsAgent {
		t.Fatalf("reply did not round-trip parent/author: %+v", list[1])
	}

	if err := store.SetCommentResolved(ctx, "Iabc", root.ID, true); err != nil {
		t.Fatalf("SetCommentResolved: %v", err)
	}
	back, ok, err := store.GetComment(ctx, "Iabc", root.ID)
	if err != nil || !ok || !back.Resolved {
		t.Fatalf("expected the resolved bit persisted, got ok=%v err=%v %+v", ok, err, back)
	}
	if err := store.SetCommentResolved(ctx, "Iabc", "00000000-0000-0000-0000-000000000000", true); err == nil {
		t.Fatalf("expected resolving a missing comment to error")
	}

	// Review requests: idempotent upsert, latest requested_by wins, sorted reads.
	if err := store.UpsertReviewRequest(ctx, "Iabc", "bob", "alice"); err != nil {
		t.Fatalf("UpsertReviewRequest: %v", err)
	}
	if err := store.UpsertReviewRequest(ctx, "Iabc", "bob", "carol"); err != nil {
		t.Fatalf("UpsertReviewRequest (repeat): %v", err)
	}
	requests, err := store.ListReviewRequests(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}
	if len(requests) != 1 || requests[0].Reviewer != "bob" || requests[0].RequestedBy != "carol" {
		t.Fatalf("expected one upserted request from carol, got %+v", requests)
	}
}

// TestPostgresStoreReleaseRoundTrip exercises migration 0012 through its
// first caller (§14.10.3, stage 17b): rows round-trip every column, list
// newest-first, latest resolves, the UNIQUE(project, version) constraint
// refuses duplicates - and there is deliberately NO update/delete path to
// test (immutability by construction).
func TestPostgresStoreReleaseRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	if _, ok, err := store.GetLatestRelease(ctx, "checkout-api"); err != nil || ok {
		t.Fatalf("expected no releases yet, ok=%v err=%v", ok, err)
	}

	first, err := store.CreateRelease(ctx, Release{
		ProjectName: "checkout-api", ProjectPath: "commerce/checkout",
		Version: "0.1.0", TagRef: "refs/tags/checkout/v0.1.0",
		TagSHA: "tag1", TargetSHA: "commit1", HeadChangeKey: "Iabc",
		Changelog: "## 0.1.0\n- first", CreatedBy: "alice",
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if first.CreatedAt.IsZero() {
		t.Fatalf("expected a store-assigned created_at, got %+v", first)
	}
	if _, err := store.CreateRelease(ctx, Release{ProjectName: "checkout-api", Version: "0.1.0"}); err == nil {
		t.Fatalf("expected the UNIQUE constraint to refuse a duplicate version")
	}
	if _, err := store.CreateRelease(ctx, Release{
		ProjectName: "checkout-api", ProjectPath: "commerce/checkout",
		Version: "0.1.1", TagRef: "refs/tags/checkout/v0.1.1", TagSHA: "tag2", TargetSHA: "commit2",
	}); err != nil {
		t.Fatalf("CreateRelease (second): %v", err)
	}

	latest, ok, err := store.GetLatestRelease(ctx, "checkout-api")
	if err != nil || !ok || latest.Version != "0.1.1" {
		t.Fatalf("expected latest 0.1.1, got ok=%v err=%v %+v", ok, err, latest)
	}
	list, err := store.ListReleases(ctx, "checkout-api", 0, 0)
	if err != nil || len(list) != 2 || list[0].Version != "0.1.1" {
		t.Fatalf("expected [0.1.1, 0.1.0], got err=%v %+v", err, list)
	}
	if list[1].HeadChangeKey != "Iabc" || list[1].Changelog != "## 0.1.0\n- first" || list[1].CreatedBy != "alice" {
		t.Fatalf("release columns did not round-trip: %+v", list[1])
	}
}

// TestPostgresStoreAgentPrincipalRoundTrip: mint/lookup/list/revoke over
// the real table (migration 0013), including the hash lookup path auth
// rides on every request.
func TestPostgresStoreAgentPrincipalRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	ap := AgentPrincipal{
		Name: "agent-pg-task-ab12", Task: "pg-task",
		TokenHash: hashAgentToken("tok"), CreatedBy: "admin",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	minted, err := store.MintAgentPrincipal(ctx, ap)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if minted.Name != ap.Name || minted.Revoked {
		t.Fatalf("minted row: %+v", minted)
	}
	if _, err := store.MintAgentPrincipal(ctx, ap); err == nil {
		t.Fatalf("name collision must error (the mint loop's retry signal)")
	}

	byHash, ok, err := store.GetAgentPrincipalByTokenHash(ctx, hashAgentToken("tok"))
	if err != nil || !ok || byHash.Name != ap.Name || !byHash.Live(time.Now()) {
		t.Fatalf("by hash: %+v ok=%v err=%v", byHash, ok, err)
	}
	if list, err := store.ListAgentPrincipals(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if err := store.RevokeAgentPrincipal(ctx, ap.Name); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked, ok, _ := store.GetAgentPrincipalByName(ctx, ap.Name); !ok || revoked.Live(time.Now()) {
		t.Fatalf("revoked row must persist and read as not-live: %+v ok=%v", revoked, ok)
	}
}

// TestPostgresStoreWorkspaceEventsRoundTrip pins the §12.6 stats-only
// timeline: BIGSERIAL IDs order newest-first, numstat totals round-trip,
// org scoping holds, the retention prune drops oldest-first, and
// DeleteWorkspaceEvents clears exactly one workspace's history.
func TestPostgresStoreWorkspaceEventsRoundTrip(t *testing.T) {
	dsn := pgTestDSN(t)
	ctx := context.Background()

	store, err := BootstrapPostgresStore(ctx, dsn, "wsevents-acme", "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore: %v", err)
	}
	defer store.Pool.Close()
	other, err := BootstrapPostgresStore(ctx, dsn, "wsevents-globex", "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (other org): %v", err)
	}
	defer other.Pool.Close()

	first, err := store.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-a", Branch: "head",
		Actor: "agent-x", SHA: "aaa111", FilesChanged: 2, Additions: 10, Deletions: 3,
	})
	if err != nil {
		t.Fatalf("RecordWorkspaceEvent: %v", err)
	}
	if first.ID == 0 || first.OccurredAt.IsZero() {
		t.Fatalf("expected assigned ID and timestamp: %+v", first)
	}
	second, err := store.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventChangeLanded, WorkspaceID: "ws-a", Branch: "head",
		Actor: "agent-x", SHA: "bbb222", ChangeKey: "Iabc",
	})
	if err != nil {
		t.Fatalf("RecordWorkspaceEvent: %v", err)
	}
	if second.ID <= first.ID {
		t.Fatalf("IDs must be strictly increasing: %d then %d", first.ID, second.ID)
	}
	// Same workspace id in ANOTHER org must not leak into this org's list.
	if _, err := other.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-a", SHA: "other-org",
	}); err != nil {
		t.Fatalf("RecordWorkspaceEvent (other org): %v", err)
	}

	evs, err := store.ListWorkspaceEvents(ctx, "ws-a", 0, 0)
	if err != nil {
		t.Fatalf("ListWorkspaceEvents: %v", err)
	}
	if len(evs) != 2 || evs[0].ID != second.ID || evs[1].ID != first.ID {
		t.Fatalf("expected org-scoped newest-first [second, first], got %+v", evs)
	}
	if evs[1].FilesChanged != 2 || evs[1].Additions != 10 || evs[1].Deletions != 3 || evs[1].Type != WorkspaceEventSnapshotPushed {
		t.Fatalf("numstat/type lost in round-trip: %+v", evs[1])
	}
	if page, _ := store.ListWorkspaceEvents(ctx, "ws-a", 1, 1); len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("limit/offset paging broken: %+v", page)
	}

	// Exercise the prune SQL directly with a tiny cap (the store method
	// applies workspaceEventRetentionCap; 500 inserts here buys nothing):
	// keep-newest-1 must drop exactly the older event, org-scoped.
	if err := store.Queries.PruneWorkspaceEvents(ctx, store.Pool, dbgen.PruneWorkspaceEventsParams{
		MonorepoID: store.MonorepoID, WorkspaceID: "ws-a", Limit: 1,
	}); err != nil {
		t.Fatalf("PruneWorkspaceEvents: %v", err)
	}
	if evs, _ := store.ListWorkspaceEvents(ctx, "ws-a", 0, 0); len(evs) != 1 || evs[0].ID != second.ID {
		t.Fatalf("prune to 1 must keep only the newest event, got %+v", evs)
	}

	if err := store.DeleteWorkspaceEvents(ctx, "ws-a"); err != nil {
		t.Fatalf("DeleteWorkspaceEvents: %v", err)
	}
	if evs, _ := store.ListWorkspaceEvents(ctx, "ws-a", 0, 0); len(evs) != 0 {
		t.Fatalf("expected empty timeline after delete, got %+v", evs)
	}
	if evs, _ := other.ListWorkspaceEvents(ctx, "ws-a", 0, 0); len(evs) != 1 || evs[0].SHA != "other-org" {
		t.Fatalf("other org's timeline must survive, got %+v", evs)
	}
}

// TestPostgresStoreWorkspaceActivityRoundTrip pins the §12.6.1
// client-claimed feed: batch inserts with BIGSERIAL order, the kind CHECK
// (only normalized kinds arrive here), LatestWorkspaceActivity's
// DISTINCT ON, org scoping, the prune SQL, and whole-workspace delete.
func TestPostgresStoreWorkspaceActivityRoundTrip(t *testing.T) {
	dsn := pgTestDSN(t)
	ctx := context.Background()

	store, err := BootstrapPostgresStore(ctx, dsn, "wsact-acme", "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore: %v", err)
	}
	defer store.Pool.Close()
	other, err := BootstrapPostgresStore(ctx, dsn, "wsact-globex", "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (other org): %v", err)
	}
	defer other.Pool.Close()

	batch, err := store.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
		{WorkspaceID: "ws-a", Actor: "agent-x", Kind: WorkspaceActivityRead, Detail: "runkod/api.go", SessionID: "sess-1"},
		{WorkspaceID: "ws-a", Actor: "agent-x", Kind: WorkspaceActivityCommand, Detail: "go test ./..."},
	})
	if err != nil {
		t.Fatalf("RecordWorkspaceActivity: %v", err)
	}
	if len(batch) != 2 || batch[0].ID == 0 || batch[1].ID <= batch[0].ID || batch[0].OccurredAt.IsZero() {
		t.Fatalf("expected increasing IDs and timestamps, got %+v", batch)
	}
	if _, err := store.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
		{WorkspaceID: "ws-b", Kind: WorkspaceActivityEdit, Detail: "web/src/App.tsx"},
	}); err != nil {
		t.Fatalf("RecordWorkspaceActivity (ws-b): %v", err)
	}
	if _, err := other.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
		{WorkspaceID: "ws-a", Kind: WorkspaceActivityNote, Detail: "other-org"},
	}); err != nil {
		t.Fatalf("RecordWorkspaceActivity (other org): %v", err)
	}

	evs, err := store.ListWorkspaceActivity(ctx, "ws-a", 0, 0)
	if err != nil {
		t.Fatalf("ListWorkspaceActivity: %v", err)
	}
	if len(evs) != 2 || evs[0].ID != batch[1].ID || evs[1].SessionID != "sess-1" {
		t.Fatalf("expected org-scoped newest-first with fields intact, got %+v", evs)
	}
	if page, _ := store.ListWorkspaceActivity(ctx, "ws-a", 1, 1); len(page) != 1 || page[0].ID != batch[0].ID {
		t.Fatalf("limit/offset paging broken: %+v", page)
	}

	latest, err := store.LatestWorkspaceActivity(ctx, []string{"ws-a", "ws-b", "ws-none"})
	if err != nil {
		t.Fatalf("LatestWorkspaceActivity: %v", err)
	}
	if len(latest) != 2 || latest["ws-a"].ID != batch[1].ID || latest["ws-b"].Kind != WorkspaceActivityEdit {
		t.Fatalf("DISTINCT ON latest rows wrong: %+v", latest)
	}

	// Prune SQL directly with a tiny cap (the store method applies
	// workspaceActivityRetentionCap): keep-newest-1, org-scoped.
	if err := store.Queries.PruneWorkspaceActivity(ctx, store.Pool, dbgen.PruneWorkspaceActivityParams{
		MonorepoID: store.MonorepoID, WorkspaceID: "ws-a", Limit: 1,
	}); err != nil {
		t.Fatalf("PruneWorkspaceActivity: %v", err)
	}
	if evs, _ := store.ListWorkspaceActivity(ctx, "ws-a", 0, 0); len(evs) != 1 || evs[0].ID != batch[1].ID {
		t.Fatalf("prune to 1 must keep only the newest row, got %+v", evs)
	}

	if err := store.DeleteWorkspaceActivity(ctx, "ws-a"); err != nil {
		t.Fatalf("DeleteWorkspaceActivity: %v", err)
	}
	if evs, _ := store.ListWorkspaceActivity(ctx, "ws-a", 0, 0); len(evs) != 0 {
		t.Fatalf("expected empty feed after delete, got %+v", evs)
	}
	if evs, _ := other.ListWorkspaceActivity(ctx, "ws-a", 0, 0); len(evs) != 1 || evs[0].Detail != "other-org" {
		t.Fatalf("other org's feed must survive, got %+v", evs)
	}
}
