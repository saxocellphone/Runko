package checks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/saxocellphone/runko/internal/dbgen"
)

// RerunCheck creates a new CheckRun attempt linked to the same
// (change, head_sha, name), per §14.4.2's re-run flow: "new CheckRun attempt
// linked to the same (change, head_sha, name)." Returns the new attempt
// number.
//
// Untested against a live Postgres in this environment (no Docker/Postgres
// available here - see CLAUDE.md); the query shapes are sqlc-verified
// (internal/dbgen) but this wiring has not been run against a real database.
func RerunCheck(ctx context.Context, db dbgen.DBTX, q *dbgen.Queries, changeID uuid.UUID, headSHA, name, externalID, reporter string) (*dbgen.CheckRun, error) {
	latest, err := q.GetLatestCheckRunAttempt(ctx, db, dbgen.GetLatestCheckRunAttemptParams{
		ChangeID: changeID, HeadSha: headSHA, Name: name,
	})
	attempt := int32(1)
	switch {
	case err == nil:
		attempt = latest.Attempt + 1
	case errors.Is(err, pgx.ErrNoRows):
		// No prior attempt - this is the first CheckRun for this name, not
		// really a "re-run", but the caller path is the same either way.
	default:
		return nil, fmt.Errorf("checks: look up latest attempt: %w", err)
	}

	run, err := q.CreateCheckRun(ctx, db, dbgen.CreateCheckRunParams{
		ChangeID:   changeID,
		HeadSha:    headSHA,
		Name:       name,
		ExternalID: externalID,
		Status:     dbgen.CheckStatusQueued,
		Reporter:   reporter,
		Attempt:    attempt,
		TtlSeconds: DefaultTTLSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("checks: create rerun attempt: %w", err)
	}
	return run, nil
}

// EnqueueWebhook marshals and enqueues a webhook envelope in the outbox
// (§14.4.1) - delivery itself happens out-of-band via Deliver, driven by a
// worker polling ListDueWebhookDeliveries (not implemented here; that's
// operational wiring, not funnel logic).
func EnqueueWebhook(ctx context.Context, db dbgen.DBTX, q *dbgen.Queries, orgID uuid.UUID, env WebhookEnvelope) (*dbgen.WebhookDelivery, error) {
	payload, err := MarshalEnvelope(env)
	if err != nil {
		return nil, fmt.Errorf("checks: marshal envelope: %w", err)
	}
	delivery, err := q.EnqueueWebhookDelivery(ctx, db, dbgen.EnqueueWebhookDeliveryParams{
		OrgID:     orgID,
		EventType: env.Type,
		Payload:   payload,
	})
	if err != nil {
		return nil, fmt.Errorf("checks: enqueue webhook: %w", err)
	}
	return delivery, nil
}

// RecordDeliveryResult applies one Deliver() attempt's outcome to a
// webhook_deliveries row: marks it delivered, or schedules the next
// exponential-backoff retry, or dead-letters it past MaxDeliveryAttempts
// (§14.4.1).
func RecordDeliveryResult(ctx context.Context, db dbgen.DBTX, q *dbgen.Queries, deliveryID uuid.UUID, attemptNumber int, result DeliveryAttempt, backoffBase, backoffMax time.Duration, now time.Time) error {
	if result.Success {
		return q.MarkWebhookDelivered(ctx, db, deliveryID)
	}

	status := dbgen.WebhookDeliveryStatusFailed
	if attemptNumber >= MaxDeliveryAttempts {
		status = dbgen.WebhookDeliveryStatusDeadLetter
	}

	var lastErr *string
	if result.Err != nil {
		msg := result.Err.Error()
		lastErr = &msg
	}

	nextAttempt := now.Add(NextBackoff(attemptNumber, backoffBase, backoffMax))
	return q.MarkWebhookFailed(ctx, db, dbgen.MarkWebhookFailedParams{
		ID:            deliveryID,
		Status:        status,
		NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true},
		LastError:     lastErr,
	})
}
