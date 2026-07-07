package runkod

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/dbgen"
	"github.com/saxocellphone/runko/receive"
)

// PostgresStore is a Store backed by internal/dbgen - the durable
// alternative to MemStore for any deployment where surviving a daemon
// restart matters (§9.3's "Team/Company self-host" profiles, not just
// "Eval/dev"). One PostgresStore serves one (org, monorepo) pair, matching
// this stage's one-monorepo-per-daemon scope (doc.go) - multi-tenant
// routing is still deferred, same as MemStore.
type PostgresStore struct {
	Pool          *pgxpool.Pool
	Queries       *dbgen.Queries
	OrgID         uuid.UUID
	MonorepoID    uuid.UUID
	AuthorActorID uuid.UUID // placeholder "unknown" actor until real AuthN exists (doc.go)
}

// BootstrapPostgresStore connects to dsn and creates-or-fetches the single
// org/monorepo/actor row this daemon instance uses, keyed by orgName. It
// does not run migrations itself - operators apply db/migrations via the
// steps in db/README.md before pointing a daemon at a database.
func BootstrapPostgresStore(ctx context.Context, dsn, orgName, trunkRef string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("runkod: connect to postgres: %w", err)
	}
	q := dbgen.New()

	org, err := q.GetOrgByName(ctx, pool, orgName)
	if errors.Is(err, pgx.ErrNoRows) {
		org, err = q.CreateOrg(ctx, pool, orgName)
	}
	if err != nil {
		return nil, fmt.Errorf("runkod: bootstrap org %q: %w", orgName, err)
	}

	monorepo, err := q.GetMonorepoByOrg(ctx, pool, org.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		monorepo, err = q.CreateMonorepo(ctx, pool, dbgen.CreateMonorepoParams{OrgID: org.ID, TrunkRef: trunkRef})
	}
	if err != nil {
		return nil, fmt.Errorf("runkod: bootstrap monorepo for org %q: %w", orgName, err)
	}

	actor, err := q.UpsertActor(ctx, pool, dbgen.UpsertActorParams{
		OrgID: org.ID, Type: dbgen.ActorTypeUser, ExternalRef: "unknown", Metadata: []byte("{}"),
	})
	if err != nil {
		return nil, fmt.Errorf("runkod: bootstrap placeholder actor: %w", err)
	}

	return &PostgresStore{
		Pool: pool, Queries: q,
		OrgID: org.ID, MonorepoID: monorepo.ID, AuthorActorID: actor.ID,
	}, nil
}

func (s *PostgresStore) CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title string) (Change, error) {
	decision := receive.Decision{Accepted: true, ChangeID: changeKey}
	c, err := receive.CreateOrUpdateChange(ctx, s.Pool, s.Queries, s.MonorepoID, s.AuthorActorID, decision, baseSHA, headSHA, gitRef, title)
	if err != nil {
		return Change{}, err
	}
	return dbChangeToChange(c), nil
}

func (s *PostgresStore) GetChange(ctx context.Context, changeKey string) (Change, bool, error) {
	c, err := s.Queries.GetChangeByKey(ctx, s.Pool, dbgen.GetChangeByKeyParams{MonorepoID: s.MonorepoID, ChangeKey: changeKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return Change{}, false, nil
	}
	if err != nil {
		return Change{}, false, err
	}
	return dbChangeToChange(c), true, nil
}

func dbChangeToChange(c *dbgen.Change) Change {
	ch := Change{
		ChangeKey: c.ChangeKey, State: string(c.State),
		BaseSHA: c.BaseSha, HeadSHA: c.HeadSha, GitRef: c.GitRef, Title: c.Title,
	}
	if c.LandedSha != nil {
		ch.LandedSHA = *c.LandedSha
	}
	return ch
}

// MarkChangeLanded uses dbgen's LandChange query, generated straight from
// db/queries/changes.sql back in stage 2 - this stage is the first caller,
// but the query was already there waiting, since the schema always modeled
// landing as a first-class Change state transition.
func (s *PostgresStore) MarkChangeLanded(ctx context.Context, changeKey, landedSHA string) (Change, error) {
	id, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Change{}, err
	}
	c, err := s.Queries.LandChange(ctx, s.Pool, dbgen.LandChangeParams{ID: id, LandedSha: &landedSHA})
	if err != nil {
		return Change{}, err
	}
	return dbChangeToChange(c), nil
}

// resolveChangeID maps a Change-Id (this Store interface's currency) to the
// internal surrogate key check_runs.change_id actually references.
func (s *PostgresStore) resolveChangeID(ctx context.Context, changeKey string) (uuid.UUID, error) {
	c, err := s.Queries.GetChangeByKey(ctx, s.Pool, dbgen.GetChangeByKeyParams{MonorepoID: s.MonorepoID, ChangeKey: changeKey})
	if err != nil {
		return uuid.Nil, fmt.Errorf("runkod: resolve change %q: %w", changeKey, err)
	}
	return c.ID, nil
}

func (s *PostgresStore) UpsertCheckRun(ctx context.Context, changeKey, headSHA string, run checks.CheckRunView) error {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return err
	}
	var conclusion *dbgen.CheckConclusion
	if run.Conclusion != "" {
		c := dbgen.CheckConclusion(run.Conclusion)
		conclusion = &c
	}
	_, err = s.Queries.UpsertCheckRunByName(ctx, s.Pool, dbgen.UpsertCheckRunByNameParams{
		ChangeID: changeID, HeadSha: headSHA, Name: run.Name,
		ExternalID: run.Name, Status: dbgen.CheckStatus(run.Status), Conclusion: conclusion,
		Reporter: "unknown", TtlSeconds: checks.DefaultTTLSeconds,
	})
	return err
}

func (s *PostgresStore) ListCheckRuns(ctx context.Context, changeKey, headSHA string) ([]checks.CheckRunView, error) {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.Queries.ListCheckRunsForChange(ctx, s.Pool, dbgen.ListCheckRunsForChangeParams{ChangeID: changeID, HeadSha: headSHA})
	if err != nil {
		return nil, err
	}
	out := make([]checks.CheckRunView, len(rows))
	for i, r := range rows {
		view := checks.CheckRunView{Name: r.Name, Status: checks.CheckStatus(r.Status)}
		if r.Conclusion != nil {
			view.Conclusion = checks.CheckConclusion(*r.Conclusion)
		}
		out[i] = view
	}
	return out, nil
}

func (s *PostgresStore) EnqueueWebhook(ctx context.Context, eventType string, payload []byte) (string, error) {
	d, err := s.Queries.EnqueueWebhookDelivery(ctx, s.Pool, dbgen.EnqueueWebhookDeliveryParams{
		OrgID: s.OrgID, EventType: eventType, Payload: payload,
	})
	if err != nil {
		return "", err
	}
	return d.ID.String(), nil
}

func (s *PostgresStore) ListDueWebhookDeliveries(ctx context.Context, now time.Time) ([]WebhookDelivery, error) {
	rows, err := s.Queries.ListDueWebhookDeliveries(ctx, s.Pool, 100)
	if err != nil {
		return nil, err
	}
	out := make([]WebhookDelivery, len(rows))
	for i, r := range rows {
		out[i] = WebhookDelivery{
			ID: r.ID.String(), EventType: r.EventType, Payload: r.Payload,
			Status: string(r.Status), Attempt: int(r.Attempt), NextAttemptAt: r.NextAttemptAt.Time,
		}
	}
	return out, nil
}

func (s *PostgresStore) RecordDeliveryResult(ctx context.Context, id string, result checks.DeliveryAttempt, backoffBase, backoffMax time.Duration, now time.Time) error {
	deliveryID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("runkod: parse delivery id %q: %w", id, err)
	}
	current, err := s.Queries.GetWebhookDelivery(ctx, s.Pool, deliveryID)
	if err != nil {
		return fmt.Errorf("runkod: look up delivery %s: %w", id, err)
	}
	return checks.RecordDeliveryResult(ctx, s.Pool, s.Queries, deliveryID, int(current.Attempt)+1, result, backoffBase, backoffMax, now)
}

var _ Store = (*PostgresStore)(nil)
