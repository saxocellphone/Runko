package runkod

import (
	"context"
	"encoding/json"
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

	"github.com/saxocellphone/runko/internal/dbgen"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/receive"
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
	pool, err := OpenPostgresPool(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return NewOrgPostgresStore(ctx, pool, orgName, trunkRef)
}

// OpenPostgresPool connects and applies migrations - once per daemon; the
// per-org stores (NewOrgPostgresStore) all share the returned pool.
func OpenPostgresPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("runkod: connect to postgres: %w", err)
	}
	// Schema first (§16.4 "schema upgrades"): a fresh database gets the
	// full embedded migration set, an existing one gets only what's new,
	// a current one is a no-op. Stage 14's compose smoke found this
	// missing - nothing outside the test harnesses had ever applied DDL.
	//
	// Bounded retry on the initial connection: postgres:16's entrypoint
	// runs a TEMPORARY server during first-boot init, and pg_isready-style
	// healthchecks pass against it - a compose/k8s neighbor starting "after
	// postgres is healthy" can still hit "the database system is starting
	// up" (SQLSTATE 57P03) in the restart window. Seen flaking CI's
	// compose-smoke; a daemon that dies on a database mid-boot instead of
	// waiting a few seconds is wrong in every deployment shape.
	var ran []string
	deadline := time.Now().Add(30 * time.Second)
	for {
		ran, err = ApplyMigrations(ctx, pool)
		if err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Printf("runkod: postgres not ready yet (%v); retrying", err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(2 * time.Second):
		}
	}
	if len(ran) > 0 {
		log.Printf("runkod: applied schema migrations: %s", strings.Join(ran, ", "))
	}
	return pool, nil
}

// NewOrgPostgresStore binds one org's store onto an already-migrated
// shared pool, get-or-creating its org/monorepo/placeholder-actor rows.
func NewOrgPostgresStore(ctx context.Context, pool *pgxpool.Pool, orgName, trunkRef string) (*PostgresStore, error) {
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
		LandedForced: c.LandedForced,
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

func (s *PostgresStore) GetMirrorCursor(ctx context.Context, remote, ref string) (MirrorCursor, bool, error) {
	c, err := s.Queries.GetMirrorCursor(ctx, s.Pool, dbgen.GetMirrorCursorParams{MonorepoID: s.MonorepoID, RemoteName: remote, RefName: ref})
	if errors.Is(err, pgx.ErrNoRows) {
		return MirrorCursor{}, false, nil
	}
	if err != nil {
		return MirrorCursor{}, false, err
	}
	return mirrorCursorFromRow(c), true, nil
}

func (s *PostgresStore) ListMirrorCursors(ctx context.Context, remote string) ([]MirrorCursor, error) {
	rows, err := s.Queries.ListMirrorCursors(ctx, s.Pool, dbgen.ListMirrorCursorsParams{MonorepoID: s.MonorepoID, RemoteName: remote})
	if err != nil {
		return nil, err
	}
	out := make([]MirrorCursor, len(rows))
	for i, r := range rows {
		out[i] = mirrorCursorFromRow(r)
	}
	return out, nil
}

func (s *PostgresStore) UpsertMirrorCursor(ctx context.Context, remote, ref, lastSyncedSHA string) error {
	_, err := s.Queries.UpsertMirrorCursor(ctx, s.Pool, dbgen.UpsertMirrorCursorParams{
		MonorepoID: s.MonorepoID, RemoteName: remote, RefName: ref,
		LastSyncedSha: &lastSyncedSHA, Writer: "runko",
	})
	return err
}

func (s *PostgresStore) FreezeMirrorCursor(ctx context.Context, remote, ref string) error {
	_, err := s.Queries.FreezeMirrorCursor(ctx, s.Pool, dbgen.FreezeMirrorCursorParams{
		MonorepoID: s.MonorepoID, RemoteName: remote, RefName: ref, Writer: "runko",
	})
	return err
}

func mirrorCursorFromRow(c *dbgen.MirrorCursor) MirrorCursor {
	mc := MirrorCursor{Ref: c.RefName, Frozen: c.Frozen, UpdatedAt: c.UpdatedAt.Time}
	if c.LastSyncedSha != nil {
		mc.LastSyncedSHA = *c.LastSyncedSha
	}
	return mc
}

// MarkChangeLanded uses dbgen's LandChange query, generated straight from
// db/queries/changes.sql back in stage 2 - this stage is the first caller,
// but the query was already there waiting, since the schema always modeled
// landing as a first-class Change state transition.
func (s *PostgresStore) MarkChangeLanded(ctx context.Context, changeKey, landedSHA, landedBy string, forced bool) (Change, error) {
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
	c, err := s.Queries.LandChange(ctx, s.Pool, dbgen.LandChangeParams{ID: id, LandedSha: &landedSHA, LandedByActorID: landedByID, LandedForced: forced})
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
	rows, err := s.Queries.ListDueWebhookDeliveries(ctx, s.Pool, dbgen.ListDueWebhookDeliveriesParams{
		OrgID: s.OrgID, Limit: 100,
	})
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
	// Server-global since migration 0007 (one account, many orgs) - every
	// org's PostgresStore shares the pool, so any of them answers for the
	// same account rows.
	_, err := s.Queries.CreatePrincipal(ctx, s.Pool, dbgen.CreatePrincipalParams{
		Name: name, CredentialHash: credentialHash,
	})
	if err != nil {
		return fmt.Errorf("runkod: create principal %q: %w", name, err)
	}
	return nil
}

func (s *PostgresStore) GetStoredPrincipal(ctx context.Context, name string) (StoredPrincipal, bool, error) {
	row, err := s.Queries.GetPrincipalByName(ctx, s.Pool, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPrincipal{}, false, nil
	}
	if err != nil {
		return StoredPrincipal{}, false, fmt.Errorf("runkod: get principal %q: %w", name, err)
	}
	return StoredPrincipal{Name: row.Name, CredentialHash: row.CredentialHash}, true, nil
}

// Directory (orghub.go): global account + membership view. Backed by the
// shared pool, so the default org's store doubles as the hub's directory.
var _ Directory = (*PostgresStore)(nil)

func (s *PostgresStore) EnsureOrg(ctx context.Context, name string) error {
	_, err := s.Queries.GetOrgByName(ctx, s.Pool, name)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = s.Queries.CreateOrg(ctx, s.Pool, name)
	}
	if err != nil {
		return fmt.Errorf("runkod: ensure org %q: %w", name, err)
	}
	return nil
}

func (s *PostgresStore) OrgMemberRole(ctx context.Context, orgName, principal string) (string, bool, error) {
	role, err := s.Queries.GetOrgMemberRole(ctx, s.Pool, dbgen.GetOrgMemberRoleParams{
		OrgName: orgName, PrincipalName: principal,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("runkod: membership of %q in %q: %w", principal, orgName, err)
	}
	return role, true, nil
}

func (s *PostgresStore) UpsertOrgMember(ctx context.Context, orgName, principal, role string) error {
	err := s.Queries.UpsertOrgMember(ctx, s.Pool, dbgen.UpsertOrgMemberParams{
		OrgName: orgName, PrincipalName: principal, Role: role,
	})
	if err != nil {
		return fmt.Errorf("runkod: add %q to org %q: %w", principal, orgName, err)
	}
	return nil
}

func (s *PostgresStore) ListOrgMemberships(ctx context.Context, principal string) ([]OrgMembership, error) {
	rows, err := s.Queries.ListOrgMembershipsForPrincipal(ctx, s.Pool, principal)
	if err != nil {
		return nil, fmt.Errorf("runkod: memberships of %q: %w", principal, err)
	}
	out := make([]OrgMembership, 0, len(rows))
	for _, r := range rows {
		out = append(out, OrgMembership{Org: r.OrgName, Role: r.Role})
	}
	return out, nil
}

func (s *PostgresStore) RemoveOrgMember(ctx context.Context, orgName, principal string) error {
	err := s.Queries.DeleteOrgMember(ctx, s.Pool, dbgen.DeleteOrgMemberParams{
		OrgName: orgName, PrincipalName: principal,
	})
	if err != nil {
		return fmt.Errorf("runkod: remove %q from org %q: %w", principal, orgName, err)
	}
	return nil
}

func (s *PostgresStore) ListOrgMembers(ctx context.Context, orgName string) ([]OrgMember, error) {
	rows, err := s.Queries.ListOrgMembers(ctx, s.Pool, orgName)
	if err != nil {
		return nil, fmt.Errorf("runkod: members of %q: %w", orgName, err)
	}
	out := make([]OrgMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, OrgMember{Name: r.PrincipalName, Role: r.Role})
	}
	return out, nil
}

func (s *PostgresStore) GetOrgSettings(ctx context.Context, orgName string) (OrgSettings, error) {
	raw, err := s.Queries.GetOrgSettings(ctx, s.Pool, orgName)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgSettings{}, fmt.Errorf("runkod: no org named %q", orgName)
	}
	if err != nil {
		return OrgSettings{}, fmt.Errorf("runkod: settings of %q: %w", orgName, err)
	}
	var settings OrgSettings
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return OrgSettings{}, fmt.Errorf("runkod: decode settings of %q: %w", orgName, err)
		}
	}
	return settings, nil
}

func (s *PostgresStore) UpdateOrgSettings(ctx context.Context, orgName string, settings OrgSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("runkod: encode settings of %q: %w", orgName, err)
	}
	err = s.Queries.UpdateOrgSettings(ctx, s.Pool, dbgen.UpdateOrgSettingsParams{
		OrgName: orgName, Settings: raw,
	})
	if err != nil {
		return fmt.Errorf("runkod: update settings of %q: %w", orgName, err)
	}
	return nil
}

func (s *PostgresStore) ListOrgRecords(ctx context.Context) ([]OrgRecord, error) {
	rows, err := s.Queries.ListOrgs(ctx, s.Pool)
	if err != nil {
		return nil, fmt.Errorf("runkod: list orgs: %w", err)
	}
	out := make([]OrgRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, OrgRecord{Name: r.Name, Archived: r.ArchivedAt.Valid})
	}
	return out, nil
}

func (s *PostgresStore) SetOrgArchived(ctx context.Context, orgName string, archived bool) error {
	err := s.Queries.SetOrgArchived(ctx, s.Pool, dbgen.SetOrgArchivedParams{
		OrgName: orgName, Archived: archived,
	})
	if err != nil {
		return fmt.Errorf("runkod: set org %q archived=%v: %w", orgName, archived, err)
	}
	return nil
}

func (s *PostgresStore) ListOrgNames(ctx context.Context) ([]string, error) {
	rows, err := s.Queries.ListOrgs(ctx, s.Pool)
	if err != nil {
		return nil, fmt.Errorf("runkod: list orgs: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names, nil
}
