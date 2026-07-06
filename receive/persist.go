package receive

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/saxocellphone/runko/internal/dbgen"
)

// ErrDecisionRejected is returned by CreateOrUpdateChange if asked to persist
// a rejected Decision - callers must check Decision.Accepted first.
var ErrDecisionRejected = errors.New("receive: cannot persist a rejected decision")

// CreateOrUpdateChange persists an accepted Decision as a Change row: creates
// one if change_key (the Change-Id) is new for this monorepo, otherwise
// updates its head_sha/git_ref - "commits are versions of a Change, not the
// Change itself" (§7.4). gitRef is the caller's responsibility (e.g.
// "refs/changes/<number>/head") since the display number isn't known until
// after a fresh Change's row exists.
//
// Untested against a live Postgres in this environment (no Docker/Postgres
// available here - see CLAUDE.md); the query shapes are sqlc-verified
// (internal/dbgen) but this wiring has not been run against a real database.
func CreateOrUpdateChange(
	ctx context.Context,
	db dbgen.DBTX,
	q *dbgen.Queries,
	monorepoID uuid.UUID,
	authorActorID uuid.UUID,
	decision Decision,
	baseSHA, headSHA, gitRef, title string,
) (*dbgen.Change, error) {
	if !decision.Accepted {
		return nil, ErrDecisionRejected
	}

	existing, err := q.GetChangeByKey(ctx, db, dbgen.GetChangeByKeyParams{
		MonorepoID: monorepoID,
		ChangeKey:  decision.ChangeID,
	})
	switch {
	case err == nil:
		return q.UpdateChangeHead(ctx, db, dbgen.UpdateChangeHeadParams{
			ID:      existing.ID,
			HeadSha: headSHA,
			GitRef:  gitRef,
		})
	case errors.Is(err, pgx.ErrNoRows):
		// Expected: no existing Change with this Change-Id yet - fall through to create.
	default:
		return nil, fmt.Errorf("receive: look up existing change: %w", err)
	}

	change, err := q.CreateChange(ctx, db, dbgen.CreateChangeParams{
		MonorepoID:        monorepoID,
		ChangeKey:         decision.ChangeID,
		State:             dbgen.ChangeStateOpen,
		BaseSha:           baseSHA,
		HeadSha:           headSHA,
		GitRef:            gitRef,
		Title:             title,
		AuthoredByActorID: authorActorID,
	})
	if err != nil {
		return nil, fmt.Errorf("receive: create change: %w", err)
	}
	return change, nil
}
