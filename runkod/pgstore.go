package runkod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
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

// ListChanges orders landed listings by landed_at (landing order - the one
// clock that matches trunk, finding #43/#45); every other listing stays on
// number (creation order).
func (s *PostgresStore) ListChanges(ctx context.Context, state string) ([]Change, error) {
	var rows []*dbgen.Change
	var err error
	switch state {
	case "":
		rows, err = s.Queries.ListAllChanges(ctx, s.Pool, s.MonorepoID)
	case "landed":
		rows, err = s.Queries.ListLandedChanges(ctx, s.Pool, s.MonorepoID)
	default:
		rows, err = s.Queries.ListChangesByState(ctx, s.Pool, dbgen.ListChangesByStateParams{
			MonorepoID: s.MonorepoID, State: dbgen.ChangeState(state),
		})
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateChanges(ctx, rows)
}

// ListChangesPage pages at the SQL layer - LIMIT/OFFSET riding migration
// 0010's (monorepo_id, state, number DESC) index (0018's landed_at index
// for the landed listing) - so serving one page of an unbounded landed
// history never materializes (or hydrates) the rest.
func (s *PostgresStore) ListChangesPage(ctx context.Context, state string, limit, offset int) ([]Change, error) {
	if limit < 0 {
		limit = 0 // dbgen's LIMIT NULLIF(x, 0): 0 means unbounded
	}
	if offset < 0 {
		offset = 0
	}
	var rows []*dbgen.Change
	var err error
	switch state {
	case "":
		rows, err = s.Queries.ListAllChangesPage(ctx, s.Pool, dbgen.ListAllChangesPageParams{
			MonorepoID: s.MonorepoID, PageLimit: int32(limit), PageOffset: int32(offset),
		})
	case "landed":
		rows, err = s.Queries.ListLandedChangesPage(ctx, s.Pool, dbgen.ListLandedChangesPageParams{
			MonorepoID: s.MonorepoID, PageLimit: int32(limit), PageOffset: int32(offset),
		})
	default:
		rows, err = s.Queries.ListChangesByStatePage(ctx, s.Pool, dbgen.ListChangesByStatePageParams{
			MonorepoID: s.MonorepoID, State: dbgen.ChangeState(state), PageLimit: int32(limit), PageOffset: int32(offset),
		})
	}
	if err != nil {
		return nil, err
	}
	return s.hydrateChanges(ctx, rows)
}

func (s *PostgresStore) SetChangeAutomerge(ctx context.Context, changeKey string, enabled bool, by string) (Change, error) {
	id, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Change{}, err
	}
	row, err := s.Queries.SetChangeAutomerge(ctx, s.Pool, dbgen.SetChangeAutomergeParams{
		ID: id, Automerge: enabled, AutomergeBy: by,
	})
	if err != nil {
		return Change{}, err
	}
	return s.hydrateChange(ctx, row)
}

func (s *PostgresStore) UpdateChangeDescription(ctx context.Context, changeKey, description, testPlan string) (Change, error) {
	id, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Change{}, err
	}
	row, err := s.Queries.UpdateChangeDescription(ctx, s.Pool, dbgen.UpdateChangeDescriptionParams{
		ID: id, Description: description, TestPlan: testPlan,
	})
	if err != nil {
		return Change{}, err
	}
	return s.hydrateChange(ctx, row)
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

// actorNamesFor resolves every authored_by/landed_by actor a set of change
// rows references in ONE GetActorsByIDs query. Hydrating row-by-row did a
// GetActor round-trip per name, which made ListChanges O(rows) in database
// round-trips - the landed tab paid ~300ms at 44 changes for what is one
// indexed lookup (stage 15 dogfood: "landed/open tabs load slowly"). The
// bootstrap placeholder actor is never fetched; it reads back as ""
// (anonymous), matching MemStore.
func (s *PostgresStore) actorNamesFor(ctx context.Context, rows []*dbgen.Change) (map[uuid.UUID]string, error) {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	add := func(id uuid.UUID) {
		if id != s.AuthorActorID && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, r := range rows {
		add(r.AuthoredByActorID)
		if r.LandedByActorID.Valid {
			add(uuid.UUID(r.LandedByActorID.Bytes))
		}
	}
	names := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	actors, err := s.Queries.GetActorsByIDs(ctx, s.Pool, ids)
	if err != nil {
		return nil, fmt.Errorf("runkod: resolve actors: %w", err)
	}
	for _, a := range actors {
		names[a.ID] = a.ExternalRef
	}
	return names, nil
}

func (s *PostgresStore) hydrateChanges(ctx context.Context, rows []*dbgen.Change) ([]Change, error) {
	names, err := s.actorNamesFor(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]Change, len(rows))
	for i, r := range rows {
		out[i] = hydrateChangeNamed(r, names)
	}
	return out, nil
}

func (s *PostgresStore) hydrateChange(ctx context.Context, c *dbgen.Change) (Change, error) {
	list, err := s.hydrateChanges(ctx, []*dbgen.Change{c})
	if err != nil {
		return Change{}, err
	}
	return list[0], nil
}

func hydrateChangeNamed(c *dbgen.Change, names map[uuid.UUID]string) Change {
	ch := Change{
		ChangeKey: c.ChangeKey, State: string(c.State),
		BaseSHA: c.BaseSha, HeadSHA: c.HeadSha, GitRef: c.GitRef, Title: c.Title,
		Description: c.Description, TestPlan: c.TestPlan,
		OriginWorkspace: c.OriginWorkspace, OriginBranch: c.OriginBranch,
		LandedForced: c.LandedForced,
		Automerge:    c.Automerge, AutomergeBy: c.AutomergeBy,
	}
	if c.LandedSha != nil {
		ch.LandedSHA = *c.LandedSha
	}
	if c.LandedAt.Valid {
		ch.LandedAt = c.LandedAt.Time
	}
	ch.AuthoredBy = names[c.AuthoredByActorID]
	if c.LandedByActorID.Valid {
		ch.LandedBy = names[uuid.UUID(c.LandedByActorID.Bytes)]
	}
	return ch
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

// commentFromRow maps a dbgen row to the Store's Comment, resolving the
// author actor (name + agent badge) the way ListApprovals resolves
// approvers - comments must stay attributed (§8.6).
func (s *PostgresStore) commentFromRow(ctx context.Context, row *dbgen.ChangeComment) (Comment, error) {
	c := Comment{
		ID:       row.ID.String(),
		Body:     row.Body,
		Resolved: row.Resolved,
	}
	if row.Path != nil {
		c.Path = *row.Path
	}
	if row.Side != nil {
		c.Side = *row.Side
	}
	if row.Line != nil {
		c.Line = int(*row.Line)
	}
	if row.HeadSha != nil {
		c.HeadSHA = *row.HeadSha
	}
	if row.ParentID.Valid {
		c.ParentID = uuid.UUID(row.ParentID.Bytes).String()
	}
	if row.CreatedAt.Valid {
		c.CreatedAt = row.CreatedAt.Time
	}
	actor, err := s.Queries.GetActor(ctx, s.Pool, row.AuthorActorID)
	if err != nil {
		return Comment{}, fmt.Errorf("runkod: resolve comment author for %s: %w", c.ID, err)
	}
	c.Author = actor.ExternalRef
	c.AuthorIsAgent = actor.Type == dbgen.ActorTypeAgent
	return c, nil
}

func (s *PostgresStore) CreateComment(ctx context.Context, changeKey string, c Comment) (Comment, error) {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Comment{}, err
	}
	actorType := dbgen.ActorTypeUser
	if c.AuthorIsAgent {
		actorType = dbgen.ActorTypeAgent
	}
	actor, err := s.Queries.UpsertActor(ctx, s.Pool, dbgen.UpsertActorParams{
		OrgID: s.OrgID, Type: actorType, ExternalRef: c.Author, Metadata: []byte("{}"),
	})
	if err != nil {
		return Comment{}, fmt.Errorf("runkod: upsert comment author %q: %w", c.Author, err)
	}
	params := dbgen.CreateChangeCommentParams{
		ChangeID:      changeID,
		AuthorActorID: actor.ID,
		Body:          c.Body,
	}
	if c.Path != "" {
		params.Path = &c.Path
	}
	if c.Side != "" {
		params.Side = &c.Side
	}
	if c.Line != 0 {
		line := int32(c.Line)
		params.Line = &line
	}
	if c.HeadSHA != "" {
		params.HeadSha = &c.HeadSHA
	}
	if c.ParentID != "" {
		parent, err := uuid.Parse(c.ParentID)
		if err != nil {
			return Comment{}, fmt.Errorf("runkod: bad parent comment id %q: %w", c.ParentID, err)
		}
		params.ParentID = pgtype.UUID{Bytes: parent, Valid: true}
	}
	row, err := s.Queries.CreateChangeComment(ctx, s.Pool, params)
	if err != nil {
		return Comment{}, err
	}
	return s.commentFromRow(ctx, row)
}

func (s *PostgresStore) ListComments(ctx context.Context, changeKey string, limit, offset int) ([]Comment, error) {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return nil, err
	}
	lim := int32(limit)
	if limit <= 0 {
		lim = math.MaxInt32 // ListChangesPage's "unbounded" convention, SQL LIMIT needs a value
	}
	rows, err := s.Queries.ListChangeComments(ctx, s.Pool, dbgen.ListChangeCommentsParams{
		ChangeID: changeID, Limit: lim, Offset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(rows))
	for _, row := range rows {
		c, err := s.commentFromRow(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *PostgresStore) GetComment(ctx context.Context, changeKey, commentID string) (Comment, bool, error) {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return Comment{}, false, err
	}
	id, err := uuid.Parse(commentID)
	if err != nil {
		return Comment{}, false, nil // not a UUID -> cannot exist
	}
	row, err := s.Queries.GetChangeComment(ctx, s.Pool, dbgen.GetChangeCommentParams{ID: id, ChangeID: changeID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, false, nil
		}
		return Comment{}, false, err
	}
	c, err := s.commentFromRow(ctx, row)
	if err != nil {
		return Comment{}, false, err
	}
	return c, true, nil
}

func (s *PostgresStore) SetCommentResolved(ctx context.Context, changeKey, commentID string, resolved bool) error {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(commentID)
	if err != nil {
		return fmt.Errorf("runkod: no such comment %q on change %q", commentID, changeKey)
	}
	n, err := s.Queries.ResolveChangeComment(ctx, s.Pool, dbgen.ResolveChangeCommentParams{
		ID: id, ChangeID: changeID, Resolved: resolved,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("runkod: no such comment %q on change %q", commentID, changeKey)
	}
	return nil
}

func (s *PostgresStore) UpsertReviewRequest(ctx context.Context, changeKey, reviewer, requestedBy string) error {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return err
	}
	return s.Queries.UpsertChangeReviewRequest(ctx, s.Pool, dbgen.UpsertChangeReviewRequestParams{
		ChangeID: changeID, Reviewer: reviewer, RequestedBy: requestedBy,
	})
}

func (s *PostgresStore) ListReviewRequests(ctx context.Context, changeKey string) ([]ReviewRequest, error) {
	changeID, err := s.resolveChangeID(ctx, changeKey)
	if err != nil {
		return nil, err
	}
	rows, err := s.Queries.ListChangeReviewRequests(ctx, s.Pool, changeID)
	if err != nil {
		return nil, err
	}
	out := make([]ReviewRequest, 0, len(rows))
	for _, r := range rows {
		rr := ReviewRequest{Reviewer: r.Reviewer, RequestedBy: r.RequestedBy}
		if r.CreatedAt.Valid {
			rr.CreatedAt = r.CreatedAt.Time
		}
		out = append(out, rr)
	}
	return out, nil
}

func releaseFromRow(row *dbgen.Release) Release {
	r := Release{
		ProjectName: row.ProjectName, ProjectPath: row.ProjectPath,
		Version: row.Version, TagRef: row.TagRef, TagSHA: row.TagSha,
		TargetSHA: row.TargetSha, HeadChangeKey: row.HeadChangeKey,
		Changelog: row.Changelog, CreatedBy: row.CreatedBy,
	}
	if row.CreatedAt.Valid {
		r.CreatedAt = row.CreatedAt.Time
	}
	return r
}

func (s *PostgresStore) CreateRelease(ctx context.Context, r Release) (Release, error) {
	row, err := s.Queries.CreateRelease(ctx, s.Pool, dbgen.CreateReleaseParams{
		MonorepoID: s.MonorepoID, ProjectName: r.ProjectName, ProjectPath: r.ProjectPath,
		Version: r.Version, TagRef: r.TagRef, TagSha: r.TagSHA, TargetSha: r.TargetSHA,
		HeadChangeKey: r.HeadChangeKey, Changelog: r.Changelog, CreatedBy: r.CreatedBy,
	})
	if err != nil {
		// The UNIQUE(monorepo_id, project_name, version) violation is the
		// concurrent-create race; keep the message shape MemStore uses so
		// the core's version_exists mapping matches both stores.
		return Release{}, fmt.Errorf("runkod: release %s %s already exists or failed: %w", r.ProjectName, r.Version, err)
	}
	return releaseFromRow(row), nil
}

func (s *PostgresStore) ListReleases(ctx context.Context, projectName string, limit, offset int) ([]Release, error) {
	lim := int32(limit)
	if limit <= 0 {
		lim = math.MaxInt32
	}
	rows, err := s.Queries.ListReleasesByProject(ctx, s.Pool, dbgen.ListReleasesByProjectParams{
		MonorepoID: s.MonorepoID, ProjectName: projectName, Limit: lim, Offset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(rows))
	for _, row := range rows {
		out = append(out, releaseFromRow(row))
	}
	return out, nil
}

func (s *PostgresStore) GetLatestRelease(ctx context.Context, projectName string) (Release, bool, error) {
	row, err := s.Queries.GetLatestReleaseByProject(ctx, s.Pool, dbgen.GetLatestReleaseByProjectParams{
		MonorepoID: s.MonorepoID, ProjectName: projectName,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, false, nil
	}
	if err != nil {
		return Release{}, false, err
	}
	return releaseFromRow(row), true, nil
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
	var detailsURL *string
	if run.DetailsURL != "" {
		detailsURL = &run.DetailsURL
	}
	_, err = s.Queries.UpsertCheckRunByName(ctx, s.Pool, dbgen.UpsertCheckRunByNameParams{
		ChangeID: changeID, HeadSha: headSHA, Name: run.Name,
		ExternalID: run.Name, Status: dbgen.CheckStatus(run.Status), Conclusion: conclusion,
		Reporter: "unknown", TtlSeconds: checks.DefaultTTLSeconds, Attempt: attempt,
		DetailsUrl: detailsURL,
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
	return checkRunViewFromRow(run.Name, run.Status, run.Conclusion, run.LastSeenAt.Time, run.TtlSeconds, run.DetailsUrl), nil
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
		view := checkRunViewFromRow(r.Name, r.Status, r.Conclusion, r.LastSeenAt.Time, r.TtlSeconds, r.DetailsUrl)
		if i, seen := byName[r.Name]; seen {
			out[i] = view
			continue
		}
		byName[r.Name] = len(out)
		out = append(out, view)
	}
	return out, nil
}

func checkRunViewFromRow(name string, status dbgen.CheckStatus, conclusion *dbgen.CheckConclusion, lastSeen time.Time, ttl int32, detailsURL *string) checks.CheckRunView {
	view := checks.CheckRunView{
		Name: name, Status: checks.CheckStatus(status),
		LastSeenAt: lastSeen, TTLSeconds: int(ttl),
	}
	if conclusion != nil {
		view.Conclusion = checks.CheckConclusion(*conclusion)
	}
	if detailsURL != nil {
		view.DetailsURL = *detailsURL
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

// Agent principals (agentprincipal.go): ephemeral per-task identities,
// org-scoped rows. Mint sweeps long-expired rows opportunistically so the
// table never grows with history (attribution lives in actors, not here).
func (s *PostgresStore) MintAgentPrincipal(ctx context.Context, ap AgentPrincipal) (AgentPrincipal, error) {
	_ = s.Queries.DeleteExpiredAgentPrincipals(ctx, s.Pool, dbgen.DeleteExpiredAgentPrincipalsParams{
		OrgID: s.OrgID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-agentSweepGrace), Valid: true},
	})
	row, err := s.Queries.MintAgentPrincipal(ctx, s.Pool, dbgen.MintAgentPrincipalParams{
		OrgID: s.OrgID, Name: ap.Name, Task: ap.Task, TokenHash: ap.TokenHash,
		CreatedBy: ap.CreatedBy, ExpiresAt: pgtype.Timestamptz{Time: ap.ExpiresAt, Valid: true},
	})
	if err != nil {
		return AgentPrincipal{}, err
	}
	return agentPrincipalFromRow(row), nil
}

func (s *PostgresStore) GetAgentPrincipalByName(ctx context.Context, name string) (AgentPrincipal, bool, error) {
	row, err := s.Queries.GetAgentPrincipalByName(ctx, s.Pool, dbgen.GetAgentPrincipalByNameParams{OrgID: s.OrgID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPrincipal{}, false, nil
	}
	if err != nil {
		return AgentPrincipal{}, false, err
	}
	return agentPrincipalFromRow(row), true, nil
}

func (s *PostgresStore) GetAgentPrincipalByTokenHash(ctx context.Context, hash string) (AgentPrincipal, bool, error) {
	row, err := s.Queries.GetAgentPrincipalByTokenHash(ctx, s.Pool, dbgen.GetAgentPrincipalByTokenHashParams{OrgID: s.OrgID, TokenHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPrincipal{}, false, nil
	}
	if err != nil {
		return AgentPrincipal{}, false, err
	}
	return agentPrincipalFromRow(row), true, nil
}

func (s *PostgresStore) ListAgentPrincipals(ctx context.Context) ([]AgentPrincipal, error) {
	rows, err := s.Queries.ListAgentPrincipals(ctx, s.Pool, s.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]AgentPrincipal, len(rows))
	for i, r := range rows {
		out[i] = agentPrincipalFromRow(r)
	}
	return out, nil
}

func (s *PostgresStore) RevokeAgentPrincipal(ctx context.Context, name string) error {
	return s.Queries.RevokeAgentPrincipal(ctx, s.Pool, dbgen.RevokeAgentPrincipalParams{OrgID: s.OrgID, Name: name})
}

func agentPrincipalFromRow(r *dbgen.AgentPrincipal) AgentPrincipal {
	return AgentPrincipal{
		Name: r.Name, Task: r.Task, TokenHash: r.TokenHash, CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt.Time, ExpiresAt: r.ExpiresAt.Time, Revoked: r.Revoked,
	}
}

// SetWorkspaceStatus is UpdateWorkspaceStatus's first caller// SetWorkspaceStatus is UpdateWorkspaceStatus's first caller (generated at
// stage 2; the status column was decorative until single-use agent
// workspaces made "closed" load-bearing at receive time).
func (s *PostgresStore) SetWorkspaceStatus(ctx context.Context, id, status string) error {
	row, err := s.Queries.GetWorkspaceBySnapshotRef(ctx, s.Pool, dbgen.GetWorkspaceBySnapshotRefParams{
		MonorepoID: s.MonorepoID, SnapshotRef: "refs/workspaces/" + id + "/head",
	})
	if err != nil {
		return fmt.Errorf("runkod: no such workspace %q: %w", id, err)
	}
	_, err = s.Queries.UpdateWorkspaceStatus(ctx, s.Pool, dbgen.UpdateWorkspaceStatusParams{
		ID: row.ID, Status: dbgen.WorkspaceStatus(status),
	})
	return err
}

// DeleteWorkspace hard-deletes the registry row (metadata only, §12.2);
// the id lives inside snapshot_ref, so resolve the row through the same
// lookup every other workspace verb uses.
func (s *PostgresStore) DeleteWorkspace(ctx context.Context, id string) error {
	row, err := s.Queries.GetWorkspaceBySnapshotRef(ctx, s.Pool, dbgen.GetWorkspaceBySnapshotRefParams{
		MonorepoID: s.MonorepoID, SnapshotRef: "refs/workspaces/" + id + "/head",
	})
	if err != nil {
		return fmt.Errorf("runkod: no such workspace %q: %w", id, err)
	}
	return s.Queries.DeleteWorkspace(ctx, s.Pool, row.ID)
}

func (s *PostgresStore) RecordWorkspaceEvent(ctx context.Context, ev WorkspaceEvent) (WorkspaceEvent, error) {
	row, err := s.Queries.InsertWorkspaceEvent(ctx, s.Pool, dbgen.InsertWorkspaceEventParams{
		OrgID: s.OrgID, MonorepoID: s.MonorepoID,
		WorkspaceID: ev.WorkspaceID, Branch: ev.Branch, EventType: ev.Type,
		Actor: ev.Actor, Sha: ev.SHA, ChangeKey: ev.ChangeKey,
		FilesChanged: int32(ev.FilesChanged), Additions: int32(ev.Additions), Deletions: int32(ev.Deletions),
	})
	if err != nil {
		return WorkspaceEvent{}, err
	}
	// Prune best-effort after insert (§12.6 retention): a failed prune
	// costs bounded extra rows, never the event.
	_ = s.Queries.PruneWorkspaceEvents(ctx, s.Pool, dbgen.PruneWorkspaceEventsParams{
		MonorepoID: s.MonorepoID, WorkspaceID: ev.WorkspaceID, Limit: workspaceEventRetentionCap,
	})
	return workspaceEventFromRow(row), nil
}

func (s *PostgresStore) ListWorkspaceEvents(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceEvent, error) {
	lim := int32(limit)
	if limit <= 0 {
		lim = math.MaxInt32
	}
	rows, err := s.Queries.ListWorkspaceEvents(ctx, s.Pool, dbgen.ListWorkspaceEventsParams{
		MonorepoID: s.MonorepoID, WorkspaceID: workspaceID, Limit: lim, Offset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, workspaceEventFromRow(row))
	}
	return out, nil
}

func (s *PostgresStore) DeleteWorkspaceEvents(ctx context.Context, workspaceID string) error {
	return s.Queries.DeleteWorkspaceEvents(ctx, s.Pool, dbgen.DeleteWorkspaceEventsParams{
		MonorepoID: s.MonorepoID, WorkspaceID: workspaceID,
	})
}

func (s *PostgresStore) RecordWorkspaceActivity(ctx context.Context, events []WorkspaceActivity) ([]WorkspaceActivity, error) {
	out := make([]WorkspaceActivity, 0, len(events))
	for _, ev := range events {
		row, err := s.Queries.InsertWorkspaceActivity(ctx, s.Pool, dbgen.InsertWorkspaceActivityParams{
			OrgID: s.OrgID, MonorepoID: s.MonorepoID,
			WorkspaceID: ev.WorkspaceID, Actor: ev.Actor, Kind: ev.Kind,
			Detail: ev.Detail, SessionID: ev.SessionID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, workspaceActivityFromRow(row))
	}
	// Prune best-effort once per batch (§12.6.1 retention): a failed
	// prune costs bounded extra rows, never the batch. Ingest builds
	// batches per workspace, so the first event's id covers them all.
	if len(events) > 0 {
		_ = s.Queries.PruneWorkspaceActivity(ctx, s.Pool, dbgen.PruneWorkspaceActivityParams{
			MonorepoID: s.MonorepoID, WorkspaceID: events[0].WorkspaceID, Limit: workspaceActivityRetentionCap,
		})
	}
	return out, nil
}

func (s *PostgresStore) ListWorkspaceActivity(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceActivity, error) {
	lim := int32(limit)
	if limit <= 0 {
		lim = math.MaxInt32
	}
	rows, err := s.Queries.ListWorkspaceActivity(ctx, s.Pool, dbgen.ListWorkspaceActivityParams{
		MonorepoID: s.MonorepoID, WorkspaceID: workspaceID, Limit: lim, Offset: int32(offset),
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceActivity, 0, len(rows))
	for _, row := range rows {
		out = append(out, workspaceActivityFromRow(row))
	}
	return out, nil
}

func (s *PostgresStore) LatestWorkspaceActivity(ctx context.Context, workspaceIDs []string) (map[string]WorkspaceActivity, error) {
	if len(workspaceIDs) == 0 {
		return map[string]WorkspaceActivity{}, nil
	}
	rows, err := s.Queries.LatestWorkspaceActivity(ctx, s.Pool, dbgen.LatestWorkspaceActivityParams{
		MonorepoID: s.MonorepoID, WorkspaceIds: workspaceIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]WorkspaceActivity, len(rows))
	for _, row := range rows {
		ev := workspaceActivityFromRow(row)
		out[ev.WorkspaceID] = ev
	}
	return out, nil
}

func (s *PostgresStore) DeleteWorkspaceActivity(ctx context.Context, workspaceID string) error {
	return s.Queries.DeleteWorkspaceActivity(ctx, s.Pool, dbgen.DeleteWorkspaceActivityParams{
		MonorepoID: s.MonorepoID, WorkspaceID: workspaceID,
	})
}

func workspaceActivityFromRow(row *dbgen.WorkspaceActivity) WorkspaceActivity {
	ev := WorkspaceActivity{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Actor: row.Actor,
		Kind: row.Kind, Detail: row.Detail, SessionID: row.SessionID,
	}
	if row.OccurredAt.Valid {
		ev.OccurredAt = row.OccurredAt.Time
	}
	return ev
}

func workspaceEventFromRow(row *dbgen.WorkspaceEvent) WorkspaceEvent {
	ev := WorkspaceEvent{
		ID: row.ID, Type: row.EventType,
		WorkspaceID: row.WorkspaceID, Branch: row.Branch,
		Actor: row.Actor, SHA: row.Sha, ChangeKey: row.ChangeKey,
		FilesChanged: int(row.FilesChanged), Additions: int(row.Additions), Deletions: int(row.Deletions),
	}
	if row.OccurredAt.Valid {
		ev.OccurredAt = row.OccurredAt.Time
	}
	return ev
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

// inviteRequestFromRow maps a dbgen row to the daemon view. Deliberately
// no OrgID anywhere in these methods: invite requests are deployment-wide
// (a request precedes any account), unlike the per-org webhook rows.
func inviteRequestFromRow(r *dbgen.InviteRequest) InviteRequest {
	out := InviteRequest{
		ID: r.ID.String(), Name: r.Name, Email: r.Email, Message: r.Message,
		Status: string(r.Status), Attempt: int(r.Attempt),
		NextAttemptAt: r.NextAttemptAt.Time, CreatedAt: r.CreatedAt.Time,
	}
	if r.LastError != nil {
		out.LastError = *r.LastError
	}
	return out
}

func (s *PostgresStore) CreateInviteRequest(ctx context.Context, name, email, message string) (InviteRequest, bool, error) {
	row, err := s.Queries.CreateInviteRequest(ctx, s.Pool, dbgen.CreateInviteRequestParams{
		Name: name, Email: email, Message: message,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING against the live-email partial unique
		// index: a live request already holds this address.
		return InviteRequest{}, false, nil
	}
	if err != nil {
		return InviteRequest{}, false, err
	}
	return inviteRequestFromRow(row), true, nil
}

func (s *PostgresStore) ListDueInviteRequests(ctx context.Context, now time.Time) ([]InviteRequest, error) {
	rows, err := s.Queries.ListDueInviteRequests(ctx, s.Pool, 100)
	if err != nil {
		return nil, err
	}
	out := make([]InviteRequest, len(rows))
	for i, r := range rows {
		out[i] = inviteRequestFromRow(r)
	}
	return out, nil
}

func (s *PostgresStore) RecordInviteSendResult(ctx context.Context, id, sendErr string, backoffBase, backoffMax time.Duration, now time.Time) (InviteRequest, error) {
	reqID, err := uuid.Parse(id)
	if err != nil {
		return InviteRequest{}, errNoInviteRequest
	}
	if sendErr == "" {
		row, err := s.Queries.MarkInviteRequestSent(ctx, s.Pool, reqID)
		if errors.Is(err, pgx.ErrNoRows) {
			return InviteRequest{}, errNoInviteRequest
		}
		if err != nil {
			return InviteRequest{}, err
		}
		return inviteRequestFromRow(row), nil
	}
	current, err := s.Queries.GetInviteRequest(ctx, s.Pool, reqID)
	if errors.Is(err, pgx.ErrNoRows) {
		return InviteRequest{}, errNoInviteRequest
	}
	if err != nil {
		return InviteRequest{}, err
	}
	attempt := int(current.Attempt) + 1
	status := dbgen.InviteRequestStatusFailed
	if attempt >= checks.MaxDeliveryAttempts {
		status = dbgen.InviteRequestStatusDeadLetter
	}
	row, err := s.Queries.MarkInviteRequestFailed(ctx, s.Pool, dbgen.MarkInviteRequestFailedParams{
		ID: reqID, Status: status,
		NextAttemptAt: pgtype.Timestamptz{Time: now.Add(checks.NextBackoff(attempt, backoffBase, backoffMax)), Valid: true},
		LastError:     &sendErr,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return InviteRequest{}, errNoInviteRequest
	}
	if err != nil {
		return InviteRequest{}, err
	}
	return inviteRequestFromRow(row), nil
}

func (s *PostgresStore) CountLiveInviteRequests(ctx context.Context) (int, error) {
	n, err := s.Queries.CountLiveInviteRequests(ctx, s.Pool)
	return int(n), err
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) CreatePrincipal(ctx context.Context, org, name, credentialHash string) error {
	// Per-org since migration 0017: the row binds to its org; every org's
	// PostgresStore shares the pool, so any of them answers for any org's
	// rows given the org name.
	_, err := s.Queries.CreatePrincipal(ctx, s.Pool, dbgen.CreatePrincipalParams{
		OrgName: org, Name: name, CredentialHash: credentialHash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The INSERT..SELECT matched no org row - a missing org must fail
		// loudly, not insert nothing.
		return fmt.Errorf("runkod: create principal %q: org %q has no directory row", name, org)
	}
	if err != nil {
		return fmt.Errorf("runkod: create principal %q in org %q: %w", name, org, err)
	}
	return nil
}

func (s *PostgresStore) GetStoredPrincipal(ctx context.Context, org, name string) (StoredPrincipal, bool, error) {
	row, err := s.Queries.GetPrincipalByOrgAndName(ctx, s.Pool, dbgen.GetPrincipalByOrgAndNameParams{
		OrgName: org, Name: name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPrincipal{}, false, nil
	}
	if err != nil {
		return StoredPrincipal{}, false, fmt.Errorf("runkod: get principal %q in org %q: %w", name, org, err)
	}
	return StoredPrincipal{Org: org, Name: row.Name, CredentialHash: row.CredentialHash}, true, nil
}

func (s *PostgresStore) ListPrincipalOrgs(ctx context.Context, name string) ([]StoredPrincipal, error) {
	rows, err := s.Queries.ListPrincipalOrgsByName(ctx, s.Pool, name)
	if err != nil {
		return nil, fmt.Errorf("runkod: list principal orgs for %q: %w", name, err)
	}
	out := make([]StoredPrincipal, len(rows))
	for i, r := range rows {
		out[i] = StoredPrincipal{Org: r.OrgName, Name: name, CredentialHash: r.CredentialHash}
	}
	return out, nil
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
