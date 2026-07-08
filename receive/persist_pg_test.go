package receive

import (
	"context"
	"testing"

	"github.com/saxocellphone/runko/internal/dbgen"
	"github.com/saxocellphone/runko/internal/dbtest"
)

// TestCreateOrUpdateChangeAgainstLivePostgres exercises CreateOrUpdateChange
// (§7.4: "commits are versions of a Change, not the Change itself") against
// a real database: creating a Change for a new Change-Id, then pushing a
// second commit under the SAME Change-Id must update the existing row's
// head_sha rather than creating a second Change - the whole point of
// keying on change_key.
//
// Skips unless RUNKO_TEST_DATABASE_URL is set (see internal/dbtest, db/README.md).
func TestCreateOrUpdateChangeAgainstLivePostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Connect(t)
	fx := dbtest.Seed(t, ctx, db, t.Name())
	q := dbgen.New()

	decision := Decision{Accepted: true, ChangeID: "Iabc123"}

	created, err := CreateOrUpdateChange(ctx, db, q, fx.MonorepoID, fx.ActorID, decision, "base1", "head1", "refs/changes/1/head", "Add checkout retries", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (create): %v", err)
	}
	if created.HeadSha != "head1" || created.BaseSha != "base1" {
		t.Fatalf("unexpected created change: %+v", created)
	}
	if created.State != dbgen.ChangeStateOpen {
		t.Fatalf("expected a new Change to start open, got %v", created.State)
	}

	updated, err := CreateOrUpdateChange(ctx, db, q, fx.MonorepoID, fx.ActorID, decision, "base1", "head2", "refs/changes/1/head", "Add checkout retries", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (update): %v", err)
	}
	if updated.ID != created.ID {
		t.Fatalf("expected the same Change row to be reused (id %s), got a different one (id %s)", created.ID, updated.ID)
	}
	if updated.HeadSha != "head2" {
		t.Fatalf("expected head_sha to advance to head2, got %q", updated.HeadSha)
	}

	all, err := q.ListOpenChanges(ctx, db, dbgen.ListOpenChangesParams{MonorepoID: fx.MonorepoID, Limit: 100, Offset: 0})
	if err != nil {
		t.Fatalf("ListOpenChanges: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one Change to exist for this change_key, got %d: %+v", len(all), all)
	}
}

func TestCreateOrUpdateChangeRejectsUnacceptedDecisionAgainstLivePostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Connect(t)
	fx := dbtest.Seed(t, ctx, db, t.Name())
	q := dbgen.New()

	_, err := CreateOrUpdateChange(ctx, db, q, fx.MonorepoID, fx.ActorID, Decision{Accepted: false}, "base1", "head1", "refs/changes/1/head", "title", "", "")
	if err != ErrDecisionRejected {
		t.Fatalf("expected ErrDecisionRejected, got %v", err)
	}
}
