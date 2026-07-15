// Connect RPC surface for the web frontend (proto/runko/v1; §17.4, §28.3
// stage 13's server half). Every RPC here is a thin encoder over the exact
// same cores the REST API uses (actions.go, mergeRequirements, index.Scan +
// affected.Compute) - two transports, one set of semantics. Connect was
// confirmed server-side by consumption: web/ is built on Connect-ES against
// these protos (proto/README.md item 1), and connect-go mounts on the
// daemon's existing net/http mux with no extra proxy process, matching the
// repo's no-heavyweight-infra posture (Envoy would be the odd one out).
//
// Auth is the same bearer-token gate as /api/* (rpcMiddleware); browser
// clients send it as an ordinary Authorization header, never cookies, which
// is also why the permissive CORS policy below is sound: a cross-origin
// page without the token can reach exactly nothing.
package runkod

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/project"
	"github.com/saxocellphone/runko/platform/search"
	runkov1 "github.com/saxocellphone/runko/proto/gen/runko/v1"
	"github.com/saxocellphone/runko/proto/gen/runko/v1/runkov1connect"
	"github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1/mailerv1connect"
)

// rpcServer implements all six runko.v1 service handler interfaces on one
// receiver (their method sets don't collide).
type rpcServer struct {
	s *Server
}

var (
	_ runkov1connect.ChangeServiceHandler    = (*rpcServer)(nil)
	_ runkov1connect.ProjectServiceHandler   = (*rpcServer)(nil)
	_ runkov1connect.WorkspaceServiceHandler = (*rpcServer)(nil)
	_ runkov1connect.SearchServiceHandler    = (*rpcServer)(nil)
	_ runkov1connect.RepoServiceHandler      = (*rpcServer)(nil)
)

// mountRPC attaches every Connect service to the mux behind rpcMiddleware.
// Called from Handler() so the RPC surface always ships with the REST one.
func (s *Server) mountRPC(mux *http.ServeMux) {
	rpc := &rpcServer{s: s}
	mount := func(path string, h http.Handler) { mux.Handle(path, s.rpcMiddleware(h)) }
	mount(runkov1connect.NewChangeServiceHandler(rpc))
	mount(runkov1connect.NewProjectServiceHandler(rpc))
	mount(runkov1connect.NewWorkspaceServiceHandler(rpc))
	mount(runkov1connect.NewSearchServiceHandler(rpc))
	mount(runkov1connect.NewRepoServiceHandler(rpc))
	// The invite feed (runkod/proto/mailer/v1, §13.3.1's first in-boundary
	// contract) is operator-gated on top of the ordinary auth middleware:
	// PII rows, write acks (invitefeed.go).
	feedPath, feedHandler := mailerv1connect.NewInviteFeedServiceHandler(rpc)
	mux.Handle(feedPath, s.rpcMiddleware(s.requireOperatorRPC(feedHandler)))
}

// publicReadProcedures is the §15.2 anonymous-read allowlist for the
// Connect surface: change/project/repo/search READS only. Workspace RPCs
// (owner metadata + write allowlists), preview/create, and every mutating
// procedure stay authenticated - anything not listed here behaves exactly
// as before. Keyed by the full procedure path the router serves.
var publicReadProcedures = map[string]bool{
	"/runko.v1.ChangeService/GetChange":            true,
	"/runko.v1.ChangeService/ListChanges":          true,
	"/runko.v1.ChangeService/GetChangeStack":       true,
	"/runko.v1.ChangeService/GetChangeDiff":        true,
	"/runko.v1.ChangeService/GetAffected":          true,
	"/runko.v1.ChangeService/GetMergeRequirements": true,
	"/runko.v1.ProjectService/ListProjects":        true,
	"/runko.v1.ProjectService/GetProject":          true,
	"/runko.v1.ProjectService/WhoOwns":             true,
	"/runko.v1.ProjectService/ListReleases":        true,
	"/runko.v1.RepoService/GetTree":                true,
	"/runko.v1.RepoService/GetBlob":                true,
	"/runko.v1.RepoService/ListCommits":            true,
	"/runko.v1.RepoService/BlameFile":              true,
	"/runko.v1.SearchService/SearchCode":           true,
}

// rpcMiddleware is requireAuth's Connect-route sibling plus browser CORS.
// Allow-Origin is deliberately "*": authentication rides in the
// Authorization header (never cookies), so a cross-origin request without
// the token gains nothing, and the web UI's dev server / any deploy origin
// can talk to the daemon without per-origin daemon config. The OPTIONS
// preflight must pass unauthenticated - browsers send it without headers.
func (s *Server) rpcMiddleware(next http.Handler) http.Handler {
	return s.rpcMiddlewareOpts(next, true)
}

// rpcMiddlewareGlobal authenticates WITHOUT this server's org-membership
// gate - the hub's global-account routes (org listing/creation, orghub.go)
// must serve any valid credential regardless of which orgs it belongs to.
func (s *Server) rpcMiddlewareGlobal(next http.Handler) http.Handler {
	return s.rpcMiddlewareOpts(next, false)
}

func (s *Server) rpcMiddlewareOpts(next http.Handler, gated bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			// PUT/DELETE cover the org settings/member routes (orghub.go),
			// which ride this same middleware; Connect itself needs only
			// GET/POST.
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms")
			h.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		c := s.callerForAuthHeaderOpts(r.Header.Get("Authorization"), gated)
		if c.deniedOrg {
			// Bare 403 -> CodePermissionDenied on Connect clients: valid
			// credential, not a member of this org (auth.go).
			http.Error(w, "forbidden: not a member of org "+s.OrgName, http.StatusForbidden)
			return
		}
		if !c.ok {
			// §15.2 public_read: a request with NO credentials at all may
			// call the read-procedure allowlist on an opted-in org.
			// Presented-but-wrong credentials still 401 - never a silent
			// downgrade to the anonymous view.
			if r.Header.Get("Authorization") == "" && publicReadProcedures[r.URL.Path] && s.publicReadEnabled(r.Context()) {
				if r.Body != nil {
					r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				}
				next.ServeHTTP(w, r)
				return
			}
			// A plain 401: connect clients map the bare HTTP status onto
			// CodeUnauthenticated without a Connect-framed body.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

// connectErr maps an apiError (the REST layer's status + §6.5 clierr shape)
// onto the equivalent Connect code, carrying the full structured error as a
// runko.v1.ErrorDetail detail (proto/README.md item 4: clients branch on
// detail.code, never parse message).
func connectErr(e *apiError) error {
	var code connect.Code
	switch {
	case e.Err.Code == "workspace_exists", e.Err.Code == "already_exists":
		code = connect.CodeAlreadyExists
	case e.Status == http.StatusBadRequest:
		code = connect.CodeInvalidArgument
	case e.Status == http.StatusUnauthorized:
		code = connect.CodeUnauthenticated
	case e.Status == http.StatusForbidden:
		code = connect.CodePermissionDenied
	case e.Status == http.StatusNotFound:
		code = connect.CodeNotFound
	case e.Status == http.StatusConflict:
		code = connect.CodeFailedPrecondition
	case e.Status == http.StatusServiceUnavailable:
		code = connect.CodeUnavailable
	default:
		code = connect.CodeInternal
	}
	msg := e.Err.Message
	if e.Err.Code != "" {
		msg = e.Err.Code + ": " + e.Err.Message
		if e.Err.Suggestion != "" {
			msg += " (" + e.Err.Suggestion + ")"
		}
	}
	cerr := connect.NewError(code, errors.New(msg))
	if e.Err.Code != "" {
		if detail, derr := connect.NewErrorDetail(&runkov1.ErrorDetail{
			Code: e.Err.Code, Field: e.Err.Field, Message: e.Err.Message,
			Suggestion: e.Err.Suggestion, DocUrl: e.Err.DocURL,
		}); derr == nil {
			cerr.AddDetail(detail)
		}
	}
	return cerr
}

// getChange is the RPC-side 404 helper every change-keyed RPC starts with.
func (r *rpcServer) getChange(ctx context.Context, key string) (Change, error) {
	change, ok, err := r.s.Store.GetChange(ctx, key)
	if err != nil {
		return Change{}, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return Change{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("change not found: %s", key))
	}
	return change, nil
}

// pageOffset decodes the plain offset page token every paginated read here
// uses (proto/README.md item 6).
func pageOffset(pageToken string) (int, error) {
	if pageToken == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(pageToken)
	if err != nil || v < 0 {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page_token %q", pageToken))
	}
	return v, nil
}

// pageWindow applies the draft's plain offset-token pagination (proto/
// README.md item 6) - adapter-level windowing for reads whose underlying
// store call is not paginated (ListChanges pages at the store instead).
func pageWindow[T any](items []T, pageSize int32, pageToken string) ([]T, string, error) {
	offset, err := pageOffset(pageToken)
	if err != nil {
		return nil, "", err
	}
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]
	if pageSize > 0 && int(pageSize) < len(items) {
		return items[:pageSize:pageSize], strconv.Itoa(offset + int(pageSize)), nil
	}
	return items, "", nil
}

// ---- ChangeService ----

func (r *rpcServer) GetChange(ctx context.Context, req *connect.Request[runkov1.GetChangeRequest]) (*connect.Response[runkov1.GetChangeResponse], error) {
	change, err := r.getChange(ctx, req.Msg.ChangeId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&runkov1.GetChangeResponse{Change: r.s.protoChange(change)}), nil
}

func (r *rpcServer) ListChanges(ctx context.Context, req *connect.Request[runkov1.ListChangesRequest]) (*connect.Response[runkov1.ListChangesResponse], error) {
	// Unspecified defaults to OPEN, the inbox view (changes.proto).
	state := changeStateString(req.Msg.State)

	// A positive page_size pages at the STORE (SQL LIMIT/OFFSET riding
	// migration 0010's index): serving one page of an unbounded landed
	// history must not materialize the rest (stage 15). One extra row is
	// fetched to learn whether a next page exists. page_size 0 keeps the
	// fetch-everything contract the stack views and CLI rely on.
	if size := int(req.Msg.PageSize); size > 0 {
		offset, err := pageOffset(req.Msg.PageToken)
		if err != nil {
			return nil, err
		}
		page, err := r.s.Store.ListChangesPage(ctx, state, size+1, offset)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		next := ""
		if len(page) > size {
			page, next = page[:size], strconv.Itoa(offset+size)
		}
		out := make([]*runkov1.ChangeSummary, len(page))
		for i, c := range page {
			out[i] = r.s.protoChange(c)
		}
		return connect.NewResponse(&runkov1.ListChangesResponse{Changes: out, NextPageToken: next}), nil
	}

	list, err := r.s.Store.ListChanges(ctx, state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*runkov1.ChangeSummary, len(list))
	for i, c := range list {
		out[i] = r.s.protoChange(c)
	}
	return connect.NewResponse(&runkov1.ListChangesResponse{Changes: out}), nil
}

func (r *rpcServer) GetChangeStack(ctx context.Context, req *connect.Request[runkov1.GetChangeStackRequest]) (*connect.Response[runkov1.GetChangeStackResponse], error) {
	change, err := r.getChange(ctx, req.Msg.ChangeId)
	if err != nil {
		return nil, err
	}
	all, err := r.s.Store.ListChanges(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	chain, position := stackForChange(all, change)
	out := make([]*runkov1.ChangeSummary, len(chain))
	for i, c := range chain {
		out[i] = r.s.protoChange(c)
	}
	return connect.NewResponse(&runkov1.GetChangeStackResponse{Changes: out, Position: int32(position)}), nil
}

func (r *rpcServer) GetChangeDiff(ctx context.Context, req *connect.Request[runkov1.GetChangeDiffRequest]) (*connect.Response[runkov1.GetChangeDiffResponse], error) {
	change, err := r.getChange(ctx, req.Msg.ChangeId)
	if err != nil {
		return nil, err
	}
	files, err := computeChangeDiff(r.s.RepoDir, change.BaseSHA, change.HeadSHA)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Project tagging by longest-prefix match at the Change's own tree
	// (§13.3) - a scan failure degrades to untagged files, never a failed
	// diff (the tag is presentation, the diff is the payload).
	var indexed []index.IndexedProject
	if scanned, serr := r.s.indexedProjectsAt(gitstore.New(r.s.RepoDir), core.Revision(change.HeadSHA)); serr == nil {
		indexed = scanned
	}
	out := make([]*runkov1.FileDiff, len(files))
	for i, f := range files {
		out[i] = protoFileDiff(f, indexed)
	}
	return connect.NewResponse(&runkov1.GetChangeDiffResponse{
		ChangeId: change.ChangeKey,
		BaseSha:  change.BaseSHA,
		HeadSha:  change.HeadSHA,
		Files:    out,
	}), nil
}

func (r *rpcServer) GetAffected(ctx context.Context, req *connect.Request[runkov1.GetAffectedRequest]) (*connect.Response[runkov1.GetAffectedResponse], error) {
	switch target := req.Msg.Target.(type) {
	case *runkov1.GetAffectedRequest_ChangeId:
		change, err := r.getChange(ctx, target.ChangeId)
		if err != nil {
			return nil, err
		}
		result, indexed, err := r.s.computeAffected(change)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&runkov1.GetAffectedResponse{Affected: protoAffected(result, indexed)}), nil

	case *runkov1.GetAffectedRequest_Paths:
		paths := target.Paths.GetPaths()
		if len(paths) == 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("paths must not be empty"))
		}
		// Paths mode computes at the current trunk tip, exactly like GET
		// /api/affected (handleAffectedByPaths).
		gstore := gitstore.New(r.s.RepoDir)
		trunkTip, err := gstore.ResolveRef("refs/heads/" + r.s.TrunkRef)
		if err != nil {
			return nil, connectErr(typedErr(http.StatusConflict, clierr.Error{
				Code: "trunk_unborn", Field: "monorepo",
				Message: fmt.Sprintf("trunk %s has no commits yet", r.s.TrunkRef),
			}))
		}
		indexed, err := r.s.indexedProjectsAt(gstore, trunkTip)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		projects := make([]affected.ProjectInfo, len(indexed))
		for i, p := range indexed {
			projects[i] = affected.ProjectInfo{Name: p.Name, Path: p.Path, DeclaredDependencies: p.DeclaredDependencies}
		}
		rootInvalidation := index.RootInvalidation(indexed)
		if r.s.Processor != nil {
			rootInvalidation = append(rootInvalidation, r.s.Processor.RootInvalidationPatterns...)
		}
		result := affected.Compute(projects, paths, affected.Options{
			RootInvalidationPatterns: rootInvalidation,
			ProsePatterns:            index.Prose(indexed),
		})
		return connect.NewResponse(&runkov1.GetAffectedResponse{Affected: protoAffected(result, indexed)}), nil

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("target is required: set paths or change_id"))
	}
}

func (r *rpcServer) GetMergeRequirements(ctx context.Context, req *connect.Request[runkov1.GetMergeRequirementsRequest]) (*connect.Response[runkov1.GetMergeRequirementsResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	// Per-principal, like the REST view: a bot-lane token sees the gate IT
	// will be held to (§14.10.2).
	reqs, rerr := r.s.mergeRequirements(ctx, key, change, r.s.laneForAuthHeader(req.Header().Get("Authorization")))
	if rerr != nil {
		return nil, connect.NewError(connect.CodeInternal, rerr)
	}
	return connect.NewResponse(&runkov1.GetMergeRequirementsResponse{Requirements: protoMergeRequirements(reqs)}), nil
}

func (r *rpcServer) ApproveChange(ctx context.Context, req *connect.Request[runkov1.ApproveChangeRequest]) (*connect.Response[runkov1.ApproveChangeResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	reqs, apiErr := r.s.approveChangeCore(ctx, key, change, req.Msg.OwnerRef, req.Msg.ApprovedBy, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.ApproveChangeResponse{Requirements: protoMergeRequirements(reqs)}), nil
}

func (r *rpcServer) LandChange(ctx context.Context, req *connect.Request[runkov1.LandChangeRequest]) (*connect.Response[runkov1.LandChangeResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	auth := req.Header().Get("Authorization")
	decision, apiErr := r.s.landChangeCore(ctx, key, change, r.s.laneForAuthHeader(auth), r.s.principalForAuthHeader(auth), req.Msg.Force)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	// Unlike REST (409 clierr per outcome), the proto models the non-landed
	// outcomes as response fields - which is also what the web UI's banners
	// render (changes.proto's LandChangeResponse).
	return connect.NewResponse(&runkov1.LandChangeResponse{
		Landed:               decision.Landed,
		LandedSha:            decision.LandedSHA,
		Forced:               decision.Forced,
		RequiresRevalidation: decision.RequiresRevalidation,
		Conflicts:            decision.Conflicts,
		RaceRetry:            decision.RaceRetryExhausted,
	}), nil
}

func (r *rpcServer) SyncChange(ctx context.Context, req *connect.Request[runkov1.SyncChangeRequest]) (*connect.Response[runkov1.SyncChangeResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	dec, apiErr := r.s.syncChangeCore(ctx, key, change, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	resp := &runkov1.SyncChangeResponse{
		Synced:           dec.Synced,
		AlreadyInSync:    dec.AlreadyInSync,
		ConflictChangeId: dec.ConflictChange,
		Conflicts:        dec.ConflictPaths,
	}
	for _, c := range dec.Stack {
		resp.Stack = append(resp.Stack, r.s.protoChange(c))
	}
	return connect.NewResponse(resp), nil
}

func (r *rpcServer) AbandonChange(ctx context.Context, req *connect.Request[runkov1.AbandonChangeRequest]) (*connect.Response[runkov1.AbandonChangeResponse], error) {
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	change, apiErr := r.s.abandonChangeCore(ctx, req.Msg.ChangeId, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.AbandonChangeResponse{Change: r.s.protoChange(change)}), nil
}

func (r *rpcServer) SetAutomerge(ctx context.Context, req *connect.Request[runkov1.SetAutomergeRequest]) (*connect.Response[runkov1.SetAutomergeResponse], error) {
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	change, apiErr := r.s.setAutomergeCore(ctx, req.Msg.ChangeId, req.Msg.Enabled, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.SetAutomergeResponse{Change: r.s.protoChange(change)}), nil
}

func (r *rpcServer) ListComments(ctx context.Context, req *connect.Request[runkov1.ListCommentsRequest]) (*connect.Response[runkov1.ListCommentsResponse], error) {
	key := req.Msg.ChangeId
	if _, err := r.getChange(ctx, key); err != nil {
		return nil, err
	}
	limit := int(req.Msg.PageSize)
	offset := 0
	if req.Msg.PageToken != "" {
		n, err := strconv.Atoi(req.Msg.PageToken)
		if err != nil || n < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_token must be a non-negative integer offset"))
		}
		offset = n
	}
	comments, err := r.s.Store.ListComments(ctx, key, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &runkov1.ListCommentsResponse{}
	for _, c := range comments {
		resp.Comments = append(resp.Comments, protoComment(c))
	}
	if limit > 0 && len(comments) == limit {
		resp.NextPageToken = strconv.Itoa(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

func (r *rpcServer) CreateComment(ctx context.Context, req *connect.Request[runkov1.CreateCommentRequest]) (*connect.Response[runkov1.CreateCommentResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	comment, apiErr := r.s.commentChangeCore(ctx, key, change, commentInput{
		Body: req.Msg.Body, Path: req.Msg.Path, Side: commentSideString(req.Msg.Side),
		Line: int(req.Msg.Line), ParentID: req.Msg.ParentId, Author: req.Msg.Author,
	}, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.CreateCommentResponse{Comment: protoComment(comment)}), nil
}

func (r *rpcServer) ResolveComment(ctx context.Context, req *connect.Request[runkov1.ResolveCommentRequest]) (*connect.Response[runkov1.ResolveCommentResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	comment, apiErr := r.s.resolveCommentCore(ctx, key, change, req.Msg.CommentId, req.Msg.Resolved, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.ResolveCommentResponse{Comment: protoComment(comment)}), nil
}

func (r *rpcServer) RequestReview(ctx context.Context, req *connect.Request[runkov1.RequestReviewRequest]) (*connect.Response[runkov1.RequestReviewResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	rr, apiErr := r.s.requestReviewCore(ctx, key, change, req.Msg.Reviewer, principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.RequestReviewResponse{Reviewer: rr.Reviewer, RequestedBy: rr.RequestedBy}), nil
}

func (r *rpcServer) RerunCheck(ctx context.Context, req *connect.Request[runkov1.RerunCheckRequest]) (*connect.Response[runkov1.RerunCheckResponse], error) {
	key := req.Msg.ChangeId
	change, err := r.getChange(ctx, key)
	if err != nil {
		return nil, err
	}
	auth := req.Header().Get("Authorization")
	reqs, apiErr := r.s.rerunCheckCore(ctx, key, change, req.Msg.CheckName, r.s.principalForAuthHeader(auth), r.s.laneForAuthHeader(auth))
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.RerunCheckResponse{Requirements: protoMergeRequirements(reqs)}), nil
}

// stackForChange derives the stack containing change (changes.proto's
// GetChangeStack relation): B is stacked on A iff B.base_sha == A.head_sha
// and both are OPEN; the requested Change itself always participates
// regardless of state ("always contains at least the requested Change").
// A stack is pending work only: a landed Change's head is (or was) a trunk
// commit, so letting it parent relations chains every independent Change
// based at that trunk tip into one false mega-stack of siblings - the
// 2026-07-08 dogfood review's "blob of every open change that shares that
// base". A child of a landed parent reads as based-on-trunk, which is what
// it now is. Trunk-most first; children are walked in ChangeKey order for
// a deterministic chain when two Changes share a base.
func stackForChange(all []Change, change Change) ([]Change, int) {
	alive := make([]Change, 0, len(all))
	for _, c := range all {
		if c.State == "open" || c.ChangeKey == change.ChangeKey {
			alive = append(alive, c)
		}
	}
	byHead := make(map[string]Change, len(alive))
	for _, c := range alive {
		if c.HeadSHA != "" {
			byHead[c.HeadSHA] = c
		}
	}
	children := make(map[string][]Change)
	for _, c := range alive {
		if parent, ok := byHead[c.BaseSHA]; ok && parent.ChangeKey != c.ChangeKey {
			children[parent.ChangeKey] = append(children[parent.ChangeKey], c)
		}
	}
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool { return children[k][i].ChangeKey < children[k][j].ChangeKey })
	}

	// Walk down to the trunk-most ancestor, then up child-by-child, with
	// separate cycle guards per phase (a shared one would truncate the
	// descend at the queried change for any mid-stack query).
	root := change
	upSeen := map[string]bool{root.ChangeKey: true}
	for {
		parent, ok := byHead[root.BaseSHA]
		if !ok || upSeen[parent.ChangeKey] {
			break
		}
		upSeen[parent.ChangeKey] = true
		root = parent
	}
	// The FULL tree below the root, not a first-child linearization: a
	// workspace's parallel branches (§12.2) fork a stack, and the client
	// rebuilds the tree from base/head relations - pre-order DFS keeps
	// every parent before its children (changes.proto GetChangeStack).
	chain := []Change{root}
	downSeen := map[string]bool{root.ChangeKey: true}
	var walk func(parent Change)
	walk = func(parent Change) {
		for _, c := range children[parent.ChangeKey] {
			if downSeen[c.ChangeKey] {
				continue
			}
			downSeen[c.ChangeKey] = true
			chain = append(chain, c)
			walk(c)
		}
	}
	walk(root)
	position := 0
	for i, c := range chain {
		if c.ChangeKey == change.ChangeKey {
			position = i
			break
		}
	}
	return chain, position
}

// ---- ProjectService ----

// trunkProjects scans the project index at the current trunk tip; an unborn
// trunk is an empty monorepo, not an error (handleListProjects' stance).
func (s *Server) trunkProjects() ([]index.IndexedProject, error) {
	gstore := gitstore.New(s.RepoDir)
	trunkTip, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		return nil, nil
	}
	return s.indexedProjectsAt(gstore, trunkTip)
}

func (r *rpcServer) ListProjects(ctx context.Context, req *connect.Request[runkov1.ListProjectsRequest]) (*connect.Response[runkov1.ListProjectsResponse], error) {
	indexed, err := r.s.trunkProjects()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	q := strings.ToLower(req.Msg.Query)
	var filtered []index.IndexedProject
	for _, p := range indexed {
		if q == "" || strings.Contains(strings.ToLower(p.Name), q) || strings.Contains(strings.ToLower(p.Path), q) {
			filtered = append(filtered, p)
		}
	}
	window, next, err := pageWindow(filtered, req.Msg.PageSize, req.Msg.PageToken)
	if err != nil {
		return nil, err
	}
	out := make([]*runkov1.ProjectSummary, len(window))
	for i, p := range window {
		out[i] = protoProjectSummary(p)
	}
	return connect.NewResponse(&runkov1.ListProjectsResponse{Projects: out, NextPageToken: next}), nil
}

func (r *rpcServer) GetProject(ctx context.Context, req *connect.Request[runkov1.GetProjectRequest]) (*connect.Response[runkov1.GetProjectResponse], error) {
	indexed, err := r.s.trunkProjects()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// id == name in v1 (common.proto's ProjectSummary note).
	for _, p := range indexed {
		if p.Name == req.Msg.Project {
			return connect.NewResponse(&runkov1.GetProjectResponse{Project: protoProjectDetail(p)}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found: %s", req.Msg.Project))
}

func (r *rpcServer) WhoOwns(ctx context.Context, req *connect.Request[runkov1.WhoOwnsRequest]) (*connect.Response[runkov1.WhoOwnsResponse], error) {
	indexed, err := r.s.trunkProjects()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	switch target := req.Msg.Target.(type) {
	case *runkov1.WhoOwnsRequest_Project:
		for _, p := range indexed {
			if p.Name == target.Project {
				return connect.NewResponse(&runkov1.WhoOwnsResponse{Owners: protoOwnersResult(p)}), nil
			}
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found: %s", target.Project))
	case *runkov1.WhoOwnsRequest_Path:
		if p, ok := owningProject(indexed, strings.Trim(target.Path, "/")); ok {
			return connect.NewResponse(&runkov1.WhoOwnsResponse{Owners: protoOwnersResult(p)}), nil
		}
		// An unowned path resolved through to the (empty) org default -
		// §7.3 "gaps visible", the same degradation mcp's ownersResult uses.
		return connect.NewResponse(&runkov1.WhoOwnsResponse{Owners: &runkov1.OwnersResult{
			Owners: []string{}, Source: runkov1.OwnersSource_OWNERS_SOURCE_ORG_DEFAULT,
		}}), nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("target is required: set path or project"))
	}
}

func intentFromProto(in *runkov1.CreateProjectIntent) project.Intent {
	if in == nil {
		return project.Intent{}
	}
	return project.Intent{
		Name:        in.Name,
		Type:        in.Type,
		Language:    in.Language,
		NoTemplate:  in.NoTemplate,
		BuildEngine: in.BuildEngine,
		Owners:      in.Owners,
		TemplateID:  in.TemplateId,
		Path:        in.Path,
		API:         in.Api,
	}
}

func (r *rpcServer) PreviewCreateProject(ctx context.Context, req *connect.Request[runkov1.PreviewCreateProjectRequest]) (*connect.Response[runkov1.PreviewCreateProjectResponse], error) {
	plan, apiErr := r.s.previewProjectCore(intentFromProto(req.Msg.Intent))
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	files := make([]*runkov1.PlannedFile, len(plan.Files))
	for i, f := range plan.Files {
		files[i] = &runkov1.PlannedFile{Path: f.Path, Action: f.Action, Content: f.Content}
	}
	return connect.NewResponse(&runkov1.PreviewCreateProjectResponse{Path: plan.Path, Files: files}), nil
}

func protoRelease(rel Release) *runkov1.Release {
	var created int64
	if !rel.CreatedAt.IsZero() {
		created = rel.CreatedAt.Unix()
	}
	return &runkov1.Release{
		Project:       &runkov1.ProjectSummary{Id: rel.ProjectName, Name: rel.ProjectName, Path: rel.ProjectPath},
		Version:       rel.Version,
		TagRef:        rel.TagRef,
		TagSha:        rel.TagSHA,
		TargetSha:     rel.TargetSHA,
		HeadChangeKey: rel.HeadChangeKey,
		Changelog:     rel.Changelog,
		CreatedBy:     rel.CreatedBy,
		CreatedAt:     created,
	}
}

func (r *rpcServer) ListReleases(ctx context.Context, req *connect.Request[runkov1.ListReleasesRequest]) (*connect.Response[runkov1.ListReleasesResponse], error) {
	limit := int(req.Msg.PageSize)
	offset := 0
	if req.Msg.PageToken != "" {
		n, err := strconv.Atoi(req.Msg.PageToken)
		if err != nil || n < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("page_token must be a non-negative integer offset"))
		}
		offset = n
	}
	releases, apiErr := r.s.listReleasesCore(ctx, req.Msg.Project, limit, offset)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	resp := &runkov1.ListReleasesResponse{}
	for _, rel := range releases {
		resp.Releases = append(resp.Releases, protoRelease(rel))
	}
	if limit > 0 && len(releases) == limit {
		resp.NextPageToken = strconv.Itoa(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

func (r *rpcServer) CreateRelease(ctx context.Context, req *connect.Request[runkov1.CreateReleaseRequest]) (*connect.Response[runkov1.CreateReleaseResponse], error) {
	auth := req.Header().Get("Authorization")
	release, apiErr := r.s.createReleaseCore(ctx, req.Msg.Project, req.Msg.Version,
		r.s.principalForAuthHeader(auth), r.s.laneForAuthHeader(auth))
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.CreateReleaseResponse{Release: protoRelease(release)}), nil
}

func (r *rpcServer) CreateProject(ctx context.Context, req *connect.Request[runkov1.CreateProjectRequest]) (*connect.Response[runkov1.CreateProjectResponse], error) {
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	change, apiErr := r.s.createProjectCore(ctx, intentFromProto(req.Msg.Intent), principal)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.CreateProjectResponse{Change: r.s.protoChange(change)}), nil
}

// ---- WorkspaceService ----

func (r *rpcServer) CreateWorkspace(ctx context.Context, req *connect.Request[runkov1.CreateWorkspaceRequest]) (*connect.Response[runkov1.CreateWorkspaceResponse], error) {
	ws, apiErr := r.s.createWorkspaceCore(ctx, req.Msg.Name, req.Msg.Owner, req.Msg.Projects)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.CreateWorkspaceResponse{Workspace: r.s.protoWorkspace(ws)}), nil
}

func (r *rpcServer) ListWorkspaces(ctx context.Context, req *connect.Request[runkov1.ListWorkspacesRequest]) (*connect.Response[runkov1.ListWorkspacesResponse], error) {
	list, err := r.s.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*runkov1.WorkspaceSummary, len(list))
	ids := make([]string, len(list))
	for i, ws := range list {
		out[i] = r.s.protoWorkspace(ws)
		ids[i] = ws.ID
	}
	// The §12.6.1 at-a-glance line: one batched read for the whole page.
	// Decoration, not payload - a failed lookup degrades to no presence
	// line, the protoFileDiff project-tagging rule.
	if latest, err := r.s.Store.LatestWorkspaceActivity(ctx, ids); err == nil {
		for i, ws := range list {
			if ev, ok := latest[ws.ID]; ok {
				out[i].LatestActivity = r.s.protoWorkspaceActivity(ctx, ev)
			}
		}
	}
	return connect.NewResponse(&runkov1.ListWorkspacesResponse{Workspaces: out}), nil
}

func (r *rpcServer) GetWorkspace(ctx context.Context, req *connect.Request[runkov1.GetWorkspaceRequest]) (*connect.Response[runkov1.GetWorkspaceResponse], error) {
	ws, ok, err := r.s.Store.GetWorkspace(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found: %s", req.Msg.Id))
	}
	return connect.NewResponse(&runkov1.GetWorkspaceResponse{Workspace: r.s.protoWorkspace(ws)}), nil
}

func (r *rpcServer) UpdateWorkspaceBase(ctx context.Context, req *connect.Request[runkov1.UpdateWorkspaceBaseRequest]) (*connect.Response[runkov1.UpdateWorkspaceBaseResponse], error) {
	ws, apiErr := r.s.updateWorkspaceBaseCore(ctx, req.Msg.Id, req.Msg.BaseRevision)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.UpdateWorkspaceBaseResponse{Workspace: r.s.protoWorkspace(ws)}), nil
}

func (r *rpcServer) DeleteWorkspace(ctx context.Context, req *connect.Request[runkov1.DeleteWorkspaceRequest]) (*connect.Response[runkov1.DeleteWorkspaceResponse], error) {
	principal := r.s.principalForAuthHeader(req.Header().Get("Authorization"))
	if apiErr := r.s.deleteWorkspaceCore(ctx, req.Msg.Id, principal); apiErr != nil {
		return nil, connectErr(apiErr)
	}
	return connect.NewResponse(&runkov1.DeleteWorkspaceResponse{}), nil
}

func (r *rpcServer) GetWorkspaceDiff(ctx context.Context, req *connect.Request[runkov1.GetWorkspaceDiffRequest]) (*connect.Response[runkov1.GetWorkspaceDiffResponse], error) {
	ws, ok, err := r.s.Store.GetWorkspace(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found: %s", req.Msg.Id))
	}
	branch := req.Msg.Branch
	if branch == "" {
		branch = "head" // the default branch every workspace starts with (§12.2)
	}
	// The branch names a ref segment - charset-validate BEFORE it is
	// interpolated into a ref path (the same rule the receive funnel
	// enforces on snapshot pushes, workspace.go's workspaceIDPattern).
	if !workspaceIDPattern.MatchString(branch) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid workspace branch %q", branch))
	}
	resp := &runkov1.GetWorkspaceDiffResponse{Id: req.Msg.Id, Branch: branch, BaseSha: ws.BaseRevision}
	out, err := exec.Command("git", "--git-dir", r.s.RepoDir, "rev-parse", "--verify", "--quiet", "refs/workspaces/"+req.Msg.Id+"/"+branch).Output()
	snapSHA := strings.TrimSpace(string(out))
	if err != nil || snapSHA == "" {
		// No snapshot pushed on this branch yet: an empty workspace is a
		// state, not an error (§12.6).
		return connect.NewResponse(resp), nil
	}
	resp.SnapshotSha = snapSHA
	files, err := computeChangeDiff(r.s.RepoDir, ws.BaseRevision, snapSHA)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Project tagging at the snapshot's own tree, same degradation rule as
	// GetChangeDiff: the tag is presentation, the diff is the payload.
	var indexed []index.IndexedProject
	if scanned, serr := r.s.indexedProjectsAt(gitstore.New(r.s.RepoDir), core.Revision(snapSHA)); serr == nil {
		indexed = scanned
	}
	resp.Files = make([]*runkov1.FileDiff, len(files))
	for i, f := range files {
		resp.Files[i] = protoFileDiff(f, indexed)
	}
	return connect.NewResponse(resp), nil
}

func (r *rpcServer) ListWorkspaceEvents(ctx context.Context, req *connect.Request[runkov1.ListWorkspaceEventsRequest]) (*connect.Response[runkov1.ListWorkspaceEventsResponse], error) {
	if _, ok, err := r.s.Store.GetWorkspace(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	} else if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found: %s", req.Msg.Id))
	}
	size := int(req.Msg.PageSize)
	if size <= 0 {
		size = 50
	}
	offset, err := pageOffset(req.Msg.PageToken)
	if err != nil {
		return nil, err
	}
	// One extra row to learn whether a next page exists (the ListChanges
	// store-paging convention; the §12.6 cap bounds the whole timeline).
	evs, err := r.s.Store.ListWorkspaceEvents(ctx, req.Msg.Id, size+1, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	next := ""
	if len(evs) > size {
		evs, next = evs[:size], strconv.Itoa(offset+size)
	}
	out := make([]*runkov1.WorkspaceEvent, len(evs))
	for i, ev := range evs {
		out[i] = r.s.protoWorkspaceEvent(ctx, ev)
	}
	return connect.NewResponse(&runkov1.ListWorkspaceEventsResponse{Events: out, NextPageToken: next}), nil
}

// ListWorkspaceActivity is the §12.6.1 harness-reported feed's read side,
// ListWorkspaceEvents' shape verbatim: 404-guarded (a deleted workspace
// answers NotFound, never 200-empty), offset paging, size+1 next-page.
func (r *rpcServer) ListWorkspaceActivity(ctx context.Context, req *connect.Request[runkov1.ListWorkspaceActivityRequest]) (*connect.Response[runkov1.ListWorkspaceActivityResponse], error) {
	if _, ok, err := r.s.Store.GetWorkspace(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	} else if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found: %s", req.Msg.Id))
	}
	size := int(req.Msg.PageSize)
	if size <= 0 {
		size = 50
	}
	offset, err := pageOffset(req.Msg.PageToken)
	if err != nil {
		return nil, err
	}
	evs, err := r.s.Store.ListWorkspaceActivity(ctx, req.Msg.Id, size+1, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	next := ""
	if len(evs) > size {
		evs, next = evs[:size], strconv.Itoa(offset+size)
	}
	out := make([]*runkov1.WorkspaceActivityEvent, len(evs))
	for i, ev := range evs {
		out[i] = r.s.protoWorkspaceActivity(ctx, ev)
	}
	return connect.NewResponse(&runkov1.ListWorkspaceActivityResponse{Events: out, NextPageToken: next}), nil
}

// watchKeepaliveInterval paces WatchWorkspace's empty frames: fast enough
// to hold proxy read-timeouts (nginx-ingress defaults to 60s) open, slow
// enough to cost nothing.
const watchKeepaliveInterval = 25 * time.Second

// WatchWorkspace is the surface's first server-streaming RPC (§12.6).
// Frames are pokes off the org's EventBus - the client refetches via the
// unary RPCs on every frame and every (re)connect; an empty frame is a
// keepalive. Exits promptly on client disconnect (ctx) and bus teardown
// (sub.Done), so graceful shutdown is never held hostage by a stream.
func (r *rpcServer) WatchWorkspace(ctx context.Context, req *connect.Request[runkov1.WatchWorkspaceRequest], stream *connect.ServerStream[runkov1.WatchWorkspaceResponse]) error {
	if _, ok, err := r.s.Store.GetWorkspace(ctx, req.Msg.Id); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	} else if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found: %s", req.Msg.Id))
	}
	sub, cancel := r.s.Events.Subscribe(req.Msg.Id)
	defer cancel()

	// An immediate first frame proves liveness: the client's "refetch on
	// (re)connect" rule keys on receiving it, not on transport open.
	if err := stream.Send(&runkov1.WatchWorkspaceResponse{}); err != nil {
		return nil
	}
	keepalive := time.NewTicker(watchKeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil // client gone or server draining
		case <-sub.Done():
			return nil // bus torn down (a nil bus is born done: stream ends honestly)
		case <-sub.Ready():
			ev, ok := sub.Take()
			if !ok {
				continue // a stale signal an earlier Take already drained
			}
			if err := stream.Send(&runkov1.WatchWorkspaceResponse{Event: r.s.protoWorkspaceEvent(ctx, ev)}); err != nil {
				return nil
			}
		case <-keepalive.C:
			if err := stream.Send(&runkov1.WatchWorkspaceResponse{}); err != nil {
				return nil
			}
		}
	}
}

// protoWorkspaceEvent maps a Store row onto the wire shape. The actor's
// agent badge resolves through isAgentPrincipalName (flag-config AND
// minted ephemeral agents) - protoChange's Principals-only scan predates
// minted agents and stays as-is for now.
func (s *Server) protoWorkspaceEvent(ctx context.Context, ev WorkspaceEvent) *runkov1.WorkspaceEvent {
	out := &runkov1.WorkspaceEvent{
		Id: ev.ID, Type: protoWorkspaceEventType(ev.Type),
		WorkspaceId: ev.WorkspaceID, Branch: ev.Branch,
		Sha: ev.SHA, ChangeId: ev.ChangeKey,
		FilesChanged: int32(ev.FilesChanged), Additions: int32(ev.Additions), Deletions: int32(ev.Deletions),
	}
	if !ev.OccurredAt.IsZero() {
		out.OccurredAt = ev.OccurredAt.Unix()
	}
	if ev.Actor != "" {
		t := runkov1.ActorType_ACTOR_TYPE_USER
		if s.isAgentPrincipalName(ctx, ev.Actor) {
			t = runkov1.ActorType_ACTOR_TYPE_AGENT
		}
		out.Actor = &runkov1.Actor{Type: t, Id: ev.Actor}
	}
	return out
}

// protoWorkspaceActivity maps a §12.6.1 activity row onto the wire shape,
// protoWorkspaceEvent's actor-badge rule included.
func (s *Server) protoWorkspaceActivity(ctx context.Context, ev WorkspaceActivity) *runkov1.WorkspaceActivityEvent {
	out := &runkov1.WorkspaceActivityEvent{
		Id: ev.ID, WorkspaceId: ev.WorkspaceID,
		Kind: ev.Kind, Detail: ev.Detail, SessionId: ev.SessionID,
	}
	if !ev.OccurredAt.IsZero() {
		out.OccurredAt = ev.OccurredAt.Unix()
	}
	if ev.Actor != "" {
		t := runkov1.ActorType_ACTOR_TYPE_USER
		if s.isAgentPrincipalName(ctx, ev.Actor) {
			t = runkov1.ActorType_ACTOR_TYPE_AGENT
		}
		out.Actor = &runkov1.Actor{Type: t, Id: ev.Actor}
	}
	return out
}

func protoWorkspaceEventType(t string) runkov1.WorkspaceEventType {
	switch t {
	case WorkspaceEventAgentActivity:
		return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_AGENT_ACTIVITY
	case WorkspaceEventSnapshotPushed:
		return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_SNAPSHOT_PUSHED
	case WorkspaceEventChangePushed:
		return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_CHANGE_PUSHED
	case WorkspaceEventChangeLanded:
		return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_CHANGE_LANDED
	case WorkspaceEventChangeAbandoned:
		return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_CHANGE_ABANDONED
	case WorkspaceEventWorkspaceClosed:
		return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_WORKSPACE_CLOSED
	}
	return runkov1.WorkspaceEventType_WORKSPACE_EVENT_TYPE_UNSPECIFIED
}

// ---- SearchService ----

func (r *rpcServer) SearchCode(ctx context.Context, req *connect.Request[runkov1.SearchCodeRequest]) (*connect.Response[runkov1.SearchCodeResponse], error) {
	if req.Msg.Query == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is required"))
	}
	result, err := r.s.searcher().Search(ctx, req.Msg.Query, search.SearchOptions{Num: int(req.Msg.PageSize)})
	if err != nil {
		var ce *clierr.Error
		if errors.As(err, &ce) {
			return nil, connectErr(typedErr(http.StatusServiceUnavailable, *ce))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Same project tagging as GET /api/search, same layering (search stays
	// a leaf package; the daemon joins in the index).
	gstore := gitstore.New(r.s.RepoDir)
	if trunkTip, rerr := gstore.ResolveRef("refs/heads/" + r.s.TrunkRef); rerr == nil {
		if indexed, ierr := r.s.indexedProjectsAt(gstore, trunkTip); ierr == nil {
			tagProjects(result, indexed)
		}
	}
	hits := make([]*runkov1.SearchHit, 0, len(result.Hits))
	for _, h := range result.Hits {
		// The project filter is a post-filter over tagged hits, the same
		// documented v1 approximation the MCP adapter makes (no per-project
		// Zoekt shards).
		if req.Msg.Project != "" && h.Project != req.Msg.Project {
			continue
		}
		hits = append(hits, &runkov1.SearchHit{
			Path:      h.Path,
			ProjectId: h.Project,
			Line:      int32(h.LineNumber),
			Preview:   h.Line,
		})
	}
	return connect.NewResponse(&runkov1.SearchCodeResponse{Hits: hits, NextPageToken: ""}), nil
}

// ---- RepoService ----

func (r *rpcServer) GetTree(ctx context.Context, req *connect.Request[runkov1.GetTreeRequest]) (*connect.Response[runkov1.GetTreeResponse], error) {
	rev, ok, apiErr := r.s.resolveRepoRev(req.Msg.Rev)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	if !ok {
		// Unborn trunk: the root of an empty monorepo lists empty; any
		// deeper path doesn't exist.
		if p := strings.Trim(req.Msg.Path, "/"); p == "" || p == "." {
			return connect.NewResponse(&runkov1.GetTreeResponse{Entries: []*runkov1.TreeEntry{}, Rev: ""}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("directory not found: %s", req.Msg.Path))
	}
	entries, apiErr := r.s.repoTree(rev, req.Msg.Path)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	var indexed []index.IndexedProject
	if scanned, serr := r.s.indexedProjectsAt(gitstore.New(r.s.RepoDir), rev); serr == nil {
		indexed = scanned
	}
	out := make([]*runkov1.TreeEntry, len(entries))
	for i, e := range entries {
		t := runkov1.TreeEntryType_TREE_ENTRY_TYPE_FILE
		if e.IsDir {
			t = runkov1.TreeEntryType_TREE_ENTRY_TYPE_DIR
		}
		project := ""
		if p, ok := owningProject(indexed, e.Path); ok && p.Path != "" {
			project = p.Name
		}
		out[i] = &runkov1.TreeEntry{Name: e.Name, Path: e.Path, Type: t, Size: e.Size, Project: project}
	}
	return connect.NewResponse(&runkov1.GetTreeResponse{Entries: out, Rev: string(rev)}), nil
}

func (r *rpcServer) GetBlob(ctx context.Context, req *connect.Request[runkov1.GetBlobRequest]) (*connect.Response[runkov1.GetBlobResponse], error) {
	rev, ok, apiErr := r.s.resolveRepoRev(req.Msg.Rev)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found: %s", req.Msg.Path))
	}
	blob, apiErr := r.s.repoBlobAt(rev, req.Msg.Path)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	project := ""
	if indexed, serr := r.s.indexedProjectsAt(gitstore.New(r.s.RepoDir), rev); serr == nil {
		if p, ok := owningProject(indexed, strings.Trim(req.Msg.Path, "/")); ok && p.Path != "" {
			project = p.Name
		}
	}
	return connect.NewResponse(&runkov1.GetBlobResponse{
		Path:      strings.Trim(req.Msg.Path, "/"),
		Rev:       string(rev),
		Content:   blob.Content,
		Binary:    blob.Binary,
		Truncated: blob.Truncated,
		Size:      blob.Size,
		Project:   project,
	}), nil
}

func (r *rpcServer) ListCommits(ctx context.Context, req *connect.Request[runkov1.ListCommitsRequest]) (*connect.Response[runkov1.ListCommitsResponse], error) {
	rev, ok, apiErr := r.s.resolveRepoRev(req.Msg.Rev)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	if !ok {
		// Unborn trunk: an empty repo has no history, not an error.
		return connect.NewResponse(&runkov1.ListCommitsResponse{}), nil
	}
	limit := int(req.Msg.PageSize)
	if limit <= 0 {
		limit = historyPageDefault
	}
	if limit > historyPageMax {
		limit = historyPageMax
	}
	offset := 0
	if req.Msg.PageToken != "" {
		v, err := strconv.Atoi(req.Msg.PageToken)
		if err != nil || v < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page_token %q", req.Msg.PageToken))
		}
		offset = v
	}
	commits, hasMore, err := r.s.listCommits(ctx, rev, req.Msg.Path, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &runkov1.ListCommitsResponse{Rev: string(rev)}
	for _, c := range commits {
		resp.Commits = append(resp.Commits, &runkov1.CommitInfo{
			Sha: c.SHA, Subject: c.Subject,
			AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail, AuthoredAt: c.AuthoredAt,
			CommittedAt: c.CommittedAt, LandedAt: c.LandedAt,
			ChangeId: c.ChangeID, ChangeState: protoChangeState(c.ChangeState),
		})
	}
	if hasMore {
		resp.NextPageToken = strconv.Itoa(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

func (r *rpcServer) BlameFile(ctx context.Context, req *connect.Request[runkov1.BlameFileRequest]) (*connect.Response[runkov1.BlameFileResponse], error) {
	rev, ok, apiErr := r.s.resolveRepoRev(req.Msg.Rev)
	if apiErr != nil {
		return nil, connectErr(apiErr)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found: %s", req.Msg.Path))
	}
	regions, lines, truncated, apiErr := r.s.blameFile(ctx, rev, req.Msg.Path)
	if apiErr != nil {
		if apiErr.Err.Code == "blame_binary" {
			// Not an error at the proto level: the response shape carries
			// binary=true so the UI can say so in place.
			return connect.NewResponse(&runkov1.BlameFileResponse{
				Path: strings.Trim(req.Msg.Path, "/"), Rev: string(rev), Binary: true,
			}), nil
		}
		return nil, connectErr(apiErr)
	}
	resp := &runkov1.BlameFileResponse{
		Path: strings.Trim(req.Msg.Path, "/"), Rev: string(rev),
		Lines: lines, Truncated: truncated,
	}
	for _, reg := range regions {
		resp.Regions = append(resp.Regions, &runkov1.BlameRegion{
			StartLine: int32(reg.StartLine), LineCount: int32(reg.LineCount),
			Sha: reg.SHA, Subject: reg.Subject, AuthorName: reg.AuthorName,
			AuthoredAt: reg.AuthoredAt, ChangeId: reg.ChangeID,
			ChangeState: protoChangeState(reg.ChangeState),
		})
	}
	return connect.NewResponse(resp), nil
}

// ---- proto transforms ----

// protoChange maps a Store Change onto common.proto's ChangeSummary.
// number/url stay unset - the proto marks them "not yet served" until the
// daemon exposes them (common.proto's field comments are the contract for
// that).
func (s *Server) protoChange(c Change) *runkov1.ChangeSummary {
	baseOnTrunk, baseBehind := s.baseTrunkRelation(c.BaseSHA)
	// Landed changes are history: their base is by definition behind
	// whatever landed after them, and no one is about to land them again -
	// the staleness signal is for OPEN changes only.
	if c.State != "open" {
		baseBehind = 0
	}
	out := &runkov1.ChangeSummary{
		Id:              c.ChangeKey,
		State:           protoChangeState(c.State),
		BaseSha:         c.BaseSHA,
		HeadSha:         c.HeadSHA,
		GitRef:          c.GitRef,
		Title:           c.Title,
		Description:     c.Description,
		LandedSha:       c.LandedSHA,
		LandedForced:    c.LandedForced,
		LandedAt:        landedAtUnix(c),
		OriginWorkspace: c.OriginWorkspace,
		OriginBranch:    c.OriginBranch,
		BaseOnTrunk:     baseOnTrunk,
		BaseBehindTrunk: baseBehind,
		Automerge:       c.Automerge,
		AutomergeBy:     c.AutomergeBy,
	}
	if c.AuthoredBy != "" {
		t := runkov1.ActorType_ACTOR_TYPE_USER
		for i := range s.Principals {
			if s.Principals[i].Name == c.AuthoredBy && s.Principals[i].IsAgent {
				t = runkov1.ActorType_ACTOR_TYPE_AGENT
			}
		}
		out.AuthoredBy = &runkov1.Actor{Type: t, Id: c.AuthoredBy}
	}
	return out
}

// landedAtUnix maps the zero Time to proto's 0 (field comment: "0 until
// state == LANDED") instead of a nonsense year-1 epoch offset.
func landedAtUnix(c Change) int64 {
	if c.LandedAt.IsZero() {
		return 0
	}
	return c.LandedAt.Unix()
}

func protoChangeState(state string) runkov1.ChangeState {
	switch state {
	case "open":
		return runkov1.ChangeState_CHANGE_STATE_OPEN
	case "landed":
		return runkov1.ChangeState_CHANGE_STATE_LANDED
	case "abandoned":
		return runkov1.ChangeState_CHANGE_STATE_ABANDONED
	}
	return runkov1.ChangeState_CHANGE_STATE_UNSPECIFIED
}

// changeStateString is protoChangeState's request-side inverse; UNSPECIFIED
// defaults to "open" (ListChangesRequest's documented default).
func changeStateString(state runkov1.ChangeState) string {
	switch state {
	case runkov1.ChangeState_CHANGE_STATE_LANDED:
		return "landed"
	case runkov1.ChangeState_CHANGE_STATE_ABANDONED:
		return "abandoned"
	}
	return "open"
}

func protoMergeRequirements(m checks.MergeRequirements) *runkov1.MergeRequirements {
	return &runkov1.MergeRequirements{
		ChangeId: m.ChangeID,
		Owners: &runkov1.OwnerGate{
			Required:    m.RequiredOwners,
			Satisfied:   m.SatisfiedOwners,
			Outstanding: m.OutstandingOwners,
		},
		Checks: &runkov1.CheckGate{
			Required:    m.RequiredChecks,
			Passing:     m.PassingChecks,
			Failing:     m.FailingChecks,
			Pending:     m.PendingChecks,
			DetailsUrls: m.CheckDetailsURLs,
		},
		Mergeable:    m.Mergeable,
		Blockers:     m.Blockers,
		AttentionSet: m.AttentionSet,
	}
}

// protoComment mirrors review.go's commentToWire onto the proto shape - the
// one ChangeComment contract in a third encoding (§17.4's single-contract
// rule).
func protoComment(c Comment) *runkov1.Comment {
	author := &runkov1.Actor{Type: runkov1.ActorType_ACTOR_TYPE_USER, Id: c.Author}
	if c.AuthorIsAgent {
		author.Type = runkov1.ActorType_ACTOR_TYPE_AGENT
	}
	side := runkov1.CommentSide_COMMENT_SIDE_UNSPECIFIED
	switch c.Side {
	case "base":
		side = runkov1.CommentSide_COMMENT_SIDE_BASE
	case "head":
		side = runkov1.CommentSide_COMMENT_SIDE_HEAD
	}
	var created int64
	if !c.CreatedAt.IsZero() {
		created = c.CreatedAt.Unix()
	}
	return &runkov1.Comment{
		Id: c.ID, Author: author, Body: c.Body, CreatedAt: created,
		Path: c.Path, Side: side, Line: int32(c.Line),
		HeadSha: c.HeadSHA, ParentId: c.ParentID, Resolved: c.Resolved,
	}
}

func commentSideString(s runkov1.CommentSide) string {
	switch s {
	case runkov1.CommentSide_COMMENT_SIDE_BASE:
		return "base"
	case runkov1.CommentSide_COMMENT_SIDE_HEAD:
		return "head"
	default:
		return ""
	}
}

func protoProjectType(t string) runkov1.ProjectType {
	switch t {
	case "library":
		return runkov1.ProjectType_PROJECT_TYPE_LIBRARY
	case "service":
		return runkov1.ProjectType_PROJECT_TYPE_SERVICE
	case "app":
		return runkov1.ProjectType_PROJECT_TYPE_APP
	case "job":
		return runkov1.ProjectType_PROJECT_TYPE_JOB
	case "other":
		return runkov1.ProjectType_PROJECT_TYPE_OTHER
	}
	return runkov1.ProjectType_PROJECT_TYPE_UNSPECIFIED
}

func ownerRefStrings(p index.IndexedProject) []string {
	refs := make([]string, len(p.Owners))
	for i, o := range p.Owners {
		refs[i] = o.Ref
	}
	return refs
}

func protoProjectSummary(p index.IndexedProject) *runkov1.ProjectSummary {
	return &runkov1.ProjectSummary{
		Id:            p.Name, // id == name in v1 (common.proto)
		Name:          p.Name,
		Type:          protoProjectType(p.Type),
		Path:          p.Path,
		OwnersSummary: ownerRefStrings(p),
	}
}

func protoProjectDetail(p index.IndexedProject) *runkov1.ProjectDetail {
	visibility := runkov1.Visibility_VISIBILITY_DEFAULT
	if p.Visibility == "restricted" {
		visibility = runkov1.Visibility_VISIBILITY_RESTRICTED
	}
	return &runkov1.ProjectDetail{
		Id:              p.Name,
		Name:            p.Name,
		Type:            protoProjectType(p.Type),
		Path:            p.Path,
		Visibility:      visibility,
		EffectiveOwners: ownerRefStrings(p),
		Capabilities:    p.Capabilities,
		Dependencies: &runkov1.Dependencies{
			Declared: p.DeclaredDependencies,
			Inferred: []string{}, // always empty in v1 (§13.3)
		},
	}
}

func protoOwnersResult(p index.IndexedProject) *runkov1.OwnersResult {
	// Every owner entry for one project shares one Source (§7.3's
	// precedence picks a single winning layer); no owners means the
	// project resolved through to the (empty) org default.
	source := runkov1.OwnersSource_OWNERS_SOURCE_ORG_DEFAULT
	if len(p.Owners) > 0 {
		switch p.Owners[0].Source {
		case "project_manifest":
			source = runkov1.OwnersSource_OWNERS_SOURCE_PROJECT_MANIFEST
		case "path_owners":
			source = runkov1.OwnersSource_OWNERS_SOURCE_PATH_OWNERS
		}
	}
	return &runkov1.OwnersResult{Owners: ownerRefStrings(p), Source: source}
}

// protoAffected joins an affected.Result with the project index so each
// affected project carries its full summary; a project in the result but
// absent from the index (possible in change mode, where the Change's own
// tree can contain a project trunk doesn't have yet) degrades to a summary
// built from the ref alone rather than being dropped - the affected SET is
// the load-bearing part (§13.3). Same stance as mcp's affectedComputation.
func protoAffected(result affected.Result, indexed []index.IndexedProject) *runkov1.AffectedComputation {
	byName := make(map[string]index.IndexedProject, len(indexed))
	for _, p := range indexed {
		byName[p.Name] = p
	}
	projects := make([]*runkov1.ProjectSummary, len(result.Projects))
	for i, ref := range result.Projects {
		if p, ok := byName[ref.Name]; ok {
			projects[i] = protoProjectSummary(p)
		} else {
			projects[i] = &runkov1.ProjectSummary{
				Id: ref.Name, Name: ref.Name, Path: ref.Path,
				Type: runkov1.ProjectType_PROJECT_TYPE_OTHER,
			}
		}
	}
	reasons := make([]runkov1.ReasonCode, 0, len(result.ReasonCodes))
	for _, rc := range result.ReasonCodes {
		switch rc {
		case affected.ReasonDirectPath:
			reasons = append(reasons, runkov1.ReasonCode_REASON_CODE_DIRECT_PATH)
		case affected.ReasonDependsOn:
			reasons = append(reasons, runkov1.ReasonCode_REASON_CODE_DEPENDS_ON)
		case affected.ReasonRootInvalidation:
			reasons = append(reasons, runkov1.ReasonCode_REASON_CODE_ROOT_INVALIDATION)
		}
	}
	return &runkov1.AffectedComputation{
		ComputationId: result.ComputationID,
		Projects:      projects,
		Paths:         result.Paths,
		ReasonCodes:   reasons,
		RunEverything: result.RunEverything,
	}
}

func protoFileDiff(f fileDiff, indexed []index.IndexedProject) *runkov1.FileDiff {
	status := runkov1.FileDiffStatus_FILE_DIFF_STATUS_MODIFIED
	switch f.Status {
	case "added":
		status = runkov1.FileDiffStatus_FILE_DIFF_STATUS_ADDED
	case "deleted":
		status = runkov1.FileDiffStatus_FILE_DIFF_STATUS_DELETED
	case "renamed":
		status = runkov1.FileDiffStatus_FILE_DIFF_STATUS_RENAMED
	}
	project := ""
	if p, ok := owningProject(indexed, f.Path); ok && p.Path != "" {
		project = p.Name
	}
	hunks := make([]*runkov1.DiffHunk, len(f.Hunks))
	for i, h := range f.Hunks {
		lines := make([]*runkov1.DiffLine, len(h.Lines))
		for j, l := range h.Lines {
			t := runkov1.DiffLineType_DIFF_LINE_TYPE_CONTEXT
			switch l.Type {
			case "added":
				t = runkov1.DiffLineType_DIFF_LINE_TYPE_ADDED
			case "removed":
				t = runkov1.DiffLineType_DIFF_LINE_TYPE_REMOVED
			}
			lines[j] = &runkov1.DiffLine{
				Type: t, Content: l.Content,
				OldLine: int32(l.OldLine), NewLine: int32(l.NewLine),
			}
		}
		hunks[i] = &runkov1.DiffHunk{
			OldStart: int32(h.OldStart), OldLines: int32(h.OldLines),
			NewStart: int32(h.NewStart), NewLines: int32(h.NewLines),
			Header: h.Header, Lines: lines,
		}
	}
	return &runkov1.FileDiff{
		Path:      f.Path,
		OldPath:   f.OldPath,
		Status:    status,
		Hunks:     hunks,
		Binary:    f.Binary,
		Additions: int32(f.Adds),
		Deletions: int32(f.Dels),
		Project:   project,
	}
}

func (s *Server) protoWorkspace(ws Workspace) *runkov1.WorkspaceSummary {
	status := runkov1.WorkspaceStatus_WORKSPACE_STATUS_UNSPECIFIED
	switch ws.Status {
	case "active":
		status = runkov1.WorkspaceStatus_WORKSPACE_STATUS_ACTIVE
	case "detached":
		status = runkov1.WorkspaceStatus_WORKSPACE_STATUS_DETACHED
	case "closed":
		status = runkov1.WorkspaceStatus_WORKSPACE_STATUS_CLOSED
	}
	return &runkov1.WorkspaceSummary{
		Id:              ws.ID,
		Owner:           ws.Owner,
		BaseRevision:    ws.BaseRevision,
		ProjectAffinity: ws.ProjectAffinity,
		WriteAllowlist:  ws.WriteAllowlist,
		SnapshotRef:     ws.SnapshotRef,
		Status:          status,
		Branches:        s.workspaceBranches(ws.ID),
		CreatedAt:       ws.CreatedAt,
	}
}
