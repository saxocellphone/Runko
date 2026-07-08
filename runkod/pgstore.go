package runkod

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

// BootstrapPostgresStore connects to dsn, brings the schema current
// (ApplyMigrations - embedded db/migrations, so `docker compose up`
// against a fresh database just works, §16.4), and creates-or-fetches the
// single org/monorepo/actor row this daemon instance uses, keyed by
// orgName.
func BootstrapPostgresStore(ctx context.Context, dsn, orgName, trunkRef string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("runkod: connect to postgres: %w", err)
	}
	// Schema first (§16.4 "schema upgrades"): a fresh database gets the
	// full embedded migration set, an existing one gets only what's new,
	// a current one is a no-op. Stage 14's compose smoke found this
	// missing - nothing outside the test harnesses had ever applied DDL.
	ran, err := ApplyMigrations(ctx, pool)
	if err != nil {
		return nil, err
	}
	if len(ran) > 0 {
		log.Printf("runkod: applied schema migrations: %s", strings.Join(ran, ", "))
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

func (s *PostgresStore) CreateOrUpdateChange(ctx context.Context, changeKey, baseSHA, headSHA, gitRef, title, authoredBy, originWorkspace, originBranch string) (Change, error) {
	authorID, err := s.actorIDFor(ctx, authoredBy)
	if err != nil {
		return Change{}, err
	}
	decision := receive.Decision{Accepted: true, ChangeID: changeKey}
	c, err := receive.CreateOrUpdateChange(ctx, s.Pool, s.Queries, s.MonorepoID, authorID, decision, baseSHA, headSHA, gitRef, title, originWorkspace, originBranch)
	if err != nil {
		return Change{}, err
	}
	return s.hydrateChange(ctx, c)
}

func (s *PostgresStore) GetChange(ctx context.Context, changeKey string) (Change, bool, error) {
	c, err := s.Queries.GetChangeByKey(ctx, s.Pool, dbgen.GetChangeByKeyParams{MonorepoID: s.MonorepoID, ChangeKey: changeKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return Change{}, false, nil
	}
	if err != nil {
		return Change{}, false, err
	}
	ch, err := s.hydrateChange(ctx, c)
	if err != nil {
		return Change{}, false, err
	}
	return ch, true, nil
}

// Ping delegates to pgx's own pool ping - a real round-trip to Postgres,
// which is exactly what /readyz wants to know about (§9.4).
func (s *PostgresStore) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

func (s *PostgresStore) ListChanges(ctx context.Context, state string) ([]Change, error) {
	var rows []*dbgen.Change
	var err error
	if state == "" {
		rows, err = s.Queries.ListAllChanges(ctx, s.Pool, s.MonorepoID)
	} else {
		rows, err = s.Queries.ListChangesByState(ctx, s.Pool, dbgen.ListChangesByStateParams{
			MonorepoID: s.MonorepoID, State: dbgen.ChangeState(state),
		})
	}
	if err != nil {
		return nil, err
	}
	out := make([]Change, len(rows))
	for i, r := range rows {
		if out[i], err = s.hydrateChange(ctx, r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *PostgresStore) MarkChangeAbandoned(ctx context.Context, changeKey string) (Change, error) {
	existing, ok, err := s.GetChange(ctx, changeKey)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	switch existing.State {
	case "landed":
		return Change{}, fmt.Errorf("runkod: change %q already landed - landed is terminal", changeKey)
	case "abandoned":
		return existing, nil // idempotent
	}
	id, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Change{}, err
	}
	c, err := s.Queries.AbandonChange(ctx, s.Pool, id)
	if err != nil {
		return Change{}, err
	}
	return s.hydrateChange(ctx, c)
}

// actorIDFor maps a principal name to an actors row (§15.1 interim
// registry, same upsert-by-external_ref approvals already use). "" - the
// anonymous deploy token - maps to the bootstrap placeholder actor.
func (s *PostgresStore) actorIDFor(ctx context.Context, name string) (uuid.UUID, error) {
	if name == "" {
		return s.AuthorActorID, nil
	}
	actor, err := s.Queries.UpsertActor(ctx, s.Pool, dbgen.UpsertActorParams{
		OrgID: s.OrgID, Type: dbgen.ActorTypeUser, ExternalRef: name, Metadata: []byte("{}"),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("runkod: upsert actor %q: %w", name, err)
	}
	return actor.ID, nil
}

// actorName is actorIDFor's inverse for reads; the bootstrap placeholder
// reads back as "" (anonymous), matching MemStore.
func (s *PostgresStore) actorName(ctx context.Context, id uuid.UUID) (string, error) {
	if id == s.AuthorActorID {
		return "", nil
	}
	actor, err := s.Queries.GetActor(ctx, s.Pool, id)
	if err != nil {
		return "", fmt.Errorf("runkod: resolve actor %s: %w", id, err)
	}
	return actor.ExternalRef, nil
}

func (s *PostgresStore) hydrateChange(ctx context.Context, c *dbgen.Change) (Change, error) {
	ch := Change{
		ChangeKey: c.ChangeKey, State: string(c.State),
		BaseSHA: c.BaseSha, HeadSHA: c.HeadSha, GitRef: c.GitRef, Title: c.Title,
		OriginWorkspace: c.OriginWorkspace, OriginBranch: c.OriginBranch,
	}
	if c.LandedSha != nil {
		ch.LandedSHA = *c.LandedSha
	}
	var err error
	if ch.AuthoredBy, err = s.actorName(ctx, c.AuthoredByActorID); err != nil {
		return Change{}, err
	}
	if c.LandedByActorID.Valid {
		if ch.LandedBy, err = s.actorName(ctx, uuid.UUID(c.LandedByActorID.Bytes)); err != nil {
			return Change{}, err
		}
	}
	return ch, nil
}

// MarkChangeLanded uses dbgen's LandChange query, generated straight from
// db/queries/changes.sql back in stage 2 - this stage is the first caller,
// but the query was already there waiting, since the schema always modeled
// landing as a first-class Change state transition.
func (s *PostgresStore) MarkChangeLanded(ctx context.Context, changeKey, landedSHA, landedBy string) (Change, error) {
	id, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Change{}, err
	}
	landedByID := pgtype.UUID{}
	if landedBy != "" {
		actorID, err := s.actorIDFor(ctx, landedBy)
		if err != nil {
			return Change{}, err
		}
		landedByID = pgtype.UUID{Bytes: actorID, Valid: true}
	}
	c, err := s.Queries.LandChange(ctx, s.Pool, dbgen.LandChangeParams{ID: id, LandedSha: &landedSHA, LandedByActorID: landedByID})
	if err != nil {
		return Change{}, err
	}
	return s.hydrateChange(ctx, c)
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

// RecordApproval persists an approval via stage 2's change_owner_requirements
// table (its first caller, like LandChange was at 11b). The approver becomes a
// real actors row (UpsertActor by external_ref) rather than dropping the name
// on the floor - break-glass and approvals must stay audited (§7.3), and the
// schema already modeled that with satisfied_by_actor_id.
func (s *PostgresStore) RecordApproval(ctx context.Context, changeKey, ownerRef, approvedBy, headSHA string) error {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return err
	}
	actor, err := s.Queries.UpsertActor(ctx, s.Pool, dbgen.UpsertActorParams{
		OrgID: s.OrgID, Type: dbgen.ActorTypeUser, ExternalRef: approvedBy, Metadata: []byte("{}"),
	})
	if err != nil {
		return fmt.Errorf("runkod: upsert approver actor %q: %w", approvedBy, err)
	}
	if err := s.Queries.SetChangeOwnerRequirement(ctx, s.Pool, dbgen.SetChangeOwnerRequirementParams{
		ChangeID: changeID, OwnerRef: ownerRef,
	}); err != nil {
		return err
	}
	var headPtr *string
	if headSHA != "" {
		headPtr = &headSHA
	}
	return s.Queries.SatisfyChangeOwnerRequirement(ctx, s.Pool, dbgen.SatisfyChangeOwnerRequirementParams{
		ChangeID: changeID, OwnerRef: ownerRef,
		SatisfiedByActorID:  pgtype.UUID{Bytes: actor.ID, Valid: true},
		SatisfiedForHeadSha: headPtr,
	})
}

func (s *PostgresStore) ListApprovals(ctx context.Context, changeKey string) ([]Approval, error) {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.Queries.ListChangeOwnerRequirements(ctx, s.Pool, changeID)
	if err != nil {
		return nil, err
	}
	var out []Approval
	for _, r := range rows {
		if !r.Satisfied {
			continue
		}
		a := Approval{OwnerRef: r.OwnerRef}
		if r.SatisfiedForHeadSha != nil {
			a.HeadSHA = *r.SatisfiedForHeadSha
		}
		if r.SatisfiedByActorID.Valid {
			actor, err := s.Queries.GetActor(ctx, s.Pool, uuid.UUID(r.SatisfiedByActorID.Bytes))
			if err != nil {
				return nil, fmt.Errorf("runkod: resolve approver actor for %s: %w", r.OwnerRef, err)
			}
			a.ApprovedBy = actor.ExternalRef
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OwnerRef < out[j].OwnerRef })
	return out, nil
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
	// Target the LATEST attempt (stage 12c-③): a result posted after a
	// rerun must complete the rerun's attempt, not resurrect attempt 1 and
	// strand the rerun as forever-queued.
	attempt := int32(1)
	latest, err := s.Queries.GetLatestCheckRunAttempt(ctx, s.Pool, dbgen.GetLatestCheckRunAttemptParams{
		ChangeID: changeID, HeadSha: headSHA, Name: run.Name,
	})
	switch {
	case err == nil:
		attempt = latest.Attempt
	case errors.Is(err, pgx.ErrNoRows):
		// First run for this name - insert attempt 1.
	default:
		return err
	}
	_, err = s.Queries.UpsertCheckRunByName(ctx, s.Pool, dbgen.UpsertCheckRunByNameParams{
		ChangeID: changeID, HeadSha: headSHA, Name: run.Name,
		ExternalID: run.Name, Status: dbgen.CheckStatus(run.Status), Conclusion: conclusion,
		Reporter: "unknown", TtlSeconds: checks.DefaultTTLSeconds, Attempt: attempt,
	})
	return err
}

func (s *PostgresStore) RerunCheck(ctx context.Context, changeKey, checkName, requestedBy string) (checks.CheckRunView, error) {
	change, ok, err := s.GetChange(ctx, changeKey)
	if err != nil {
		return checks.CheckRunView{}, err
	}
	if !ok {
		return checks.CheckRunView{}, fmt.Errorf("runkod: no such change %q", changeKey)
	}
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return checks.CheckRunView{}, err
	}
	if requestedBy == "" {
		requestedBy = "unknown"
	}
	run, err := checks.RerunCheck(ctx, s.Pool, s.Queries, changeID, change.HeadSHA, checkName, "", requestedBy)
	if err != nil {
		return checks.CheckRunView{}, err
	}
	return checkRunViewFromRow(run.Name, run.Status, run.Conclusion, run.LastSeenAt.Time, run.TtlSeconds), nil
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
	// One view per NAME, latest attempt wins (the merge gate's view,
	// §14.4.2): rows arrive ordered by (name, attempt), so a later row for
	// the same name is always the higher attempt.
	byName := map[string]int{}
	out := make([]checks.CheckRunView, 0, len(rows))
	for _, r := range rows {
		view := checkRunViewFromRow(r.Name, r.Status, r.Conclusion, r.LastSeenAt.Time, r.TtlSeconds)
		if i, seen := byName[r.Name]; seen {
			out[i] = view
			continue
		}
		byName[r.Name] = len(out)
		out = append(out, view)
	}
	return out, nil
}

func checkRunViewFromRow(name string, status dbgen.CheckStatus, conclusion *dbgen.CheckConclusion, lastSeen time.Time, ttl int32) checks.CheckRunView {
	view := checks.CheckRunView{
		Name: name, Status: checks.CheckStatus(status),
		LastSeenAt: lastSeen, TTLSeconds: int(ttl),
	}
	if conclusion != nil {
		view.Conclusion = checks.CheckConclusion(*conclusion)
	}
	return view
}

// CreateWorkspace persists a registry row via stage 2's workspaces table
// (§12.2). The human workspace ID isn't a separate column - it lives inside
// snapshot_ref (refs/workspaces/<id>/head), the one place it's load-bearing,
// and lookups go through GetWorkspaceBySnapshotRef. The owner becomes a real
// actors row (principal_actor_id), same attribution pattern as approvals.
func (s *PostgresStore) CreateWorkspace(ctx context.Context, ws Workspace) (Workspace, error) {
	if _, taken, err := s.GetWorkspace(ctx, ws.ID); err != nil {
		return Workspace{}, err
	} else if taken {
		return Workspace{}, fmt.Errorf("runkod: workspace %q already exists", ws.ID)
	}
	actor, err := s.Queries.UpsertActor(ctx, s.Pool, dbgen.UpsertActorParams{
		OrgID: s.OrgID, Type: dbgen.ActorTypeUser, ExternalRef: ws.Owner, Metadata: []byte("{}"),
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("runkod: upsert workspace owner %q: %w", ws.Owner, err)
	}
	row, err := s.Queries.CreateWorkspace(ctx, s.Pool, dbgen.CreateWorkspaceParams{
		OrgID: s.OrgID, MonorepoID: s.MonorepoID, PrincipalActorID: actor.ID,
		BaseRevision: ws.BaseRevision,
		// nil slices become SQL NULL under pgx, violating NOT NULL - the
		// exact stage-9a index.Sync bug; normalize at the boundary.
		ProjectAffinity: nonNilStrings(ws.ProjectAffinity),
		WriteAllowlist:  nonNilStrings(ws.WriteAllowlist),
		SnapshotRef:     ws.SnapshotRef,
		Mode:            dbgen.WorkspaceModeSparseLocal,
		Status:          dbgen.WorkspaceStatusActive,
	})
	if err != nil {
		return Workspace{}, err
	}
	return s.dbWorkspaceToWorkspace(ctx, row)
}

func (s *PostgresStore) GetWorkspace(ctx context.Context, id string) (Workspace, bool, error) {
	row, err := s.Queries.GetWorkspaceBySnapshotRef(ctx, s.Pool, dbgen.GetWorkspaceBySnapshotRefParams{
		MonorepoID: s.MonorepoID, SnapshotRef: "refs/workspaces/" + id + "/head",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, err
	}
	ws, err := s.dbWorkspaceToWorkspace(ctx, row)
	return ws, err == nil, err
}

func (s *PostgresStore) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.Queries.ListWorkspacesByMonorepo(ctx, s.Pool, s.MonorepoID)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(rows))
	for _, row := range rows {
		ws, err := s.dbWorkspaceToWorkspace(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *PostgresStore) UpdateWorkspaceBase(ctx context.Context, id, baseRevision string) (Workspace, error) {
	row, err := s.Queries.GetWorkspaceBySnapshotRef(ctx, s.Pool, dbgen.GetWorkspaceBySnapshotRefParams{
		MonorepoID: s.MonorepoID, SnapshotRef: "refs/workspaces/" + id + "/head",
	})
	if err != nil {
		return Workspace{}, fmt.Errorf("runkod: no such workspace %q: %w", id, err)
	}
	updated, err := s.Queries.UpdateWorkspaceBase(ctx, s.Pool, dbgen.UpdateWorkspaceBaseParams{
		ID: row.ID, BaseRevision: baseRevision,
	})
	if err != nil {
		return Workspace{}, err
	}
	return s.dbWorkspaceToWorkspace(ctx, updated)
}

func (s *PostgresStore) dbWorkspaceToWorkspace(ctx context.Context, row *dbgen.Workspace) (Workspace, error) {
	id, ok := SnapshotRefWorkspaceID(row.SnapshotRef)
	if !ok {
		return Workspace{}, fmt.Errorf("runkod: workspace row %s has malformed snapshot_ref %q", row.ID, row.SnapshotRef)
	}
	actor, err := s.Queries.GetActor(ctx, s.Pool, row.PrincipalActorID)
	if err != nil {
		return Workspace{}, fmt.Errorf("runkod: resolve workspace owner: %w", err)
	}
	return Workspace{
		ID: id, Owner: actor.ExternalRef,
		BaseRevision:    row.BaseRevision,
		ProjectAffinity: row.ProjectAffinity,
		WriteAllowlist:  row.WriteAllowlist,
		SnapshotRef:     row.SnapshotRef,
		Status:          string(row.Status),
	}, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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

func (s *PostgresStore) CreatePrincipal(ctx context.Context, name, credentialHash string) error {
	_, err := s.Queries.CreatePrincipal(ctx, s.Pool, dbgen.CreatePrincipalParams{
		OrgID: s.OrgID, Name: name, CredentialHash: credentialHash,
	})
	if err != nil {
		return fmt.Errorf("runkod: create principal %q: %w", name, err)
	}
	return nil
}

func (s *PostgresStore) GetStoredPrincipal(ctx context.Context, name string) (StoredPrincipal, bool, error) {
	row, err := s.Queries.GetPrincipalByName(ctx, s.Pool, dbgen.GetPrincipalByNameParams{OrgID: s.OrgID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPrincipal{}, false, nil
	}
	if err != nil {
		return StoredPrincipal{}, false, fmt.Errorf("runkod: get principal %q: %w", name, err)
	}
	return StoredPrincipal{Name: row.Name, CredentialHash: row.CredentialHash}, true, nil
}
