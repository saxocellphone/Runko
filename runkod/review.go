// Review conversation REST handlers + webhook builders + the derived
// attention set (§13.4.1-13.4.2, DAG stage 16). Decision logic lives in
// actions.go's cores (the anti-drift doctrine); this file is the REST
// encoder half, exactly as workspace.go is for workspaces. rpc.go is the
// Connect encoder over the same cores.
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/checks"
)

// actorWire is docs/spec/mcp-tools/common.schema.json#/$defs/Actor.
type actorWire struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// commentWire is #/$defs/ChangeComment - the one wire shape comments have
// everywhere (REST, MCP passthrough, and the proto message mirrors it).
type commentWire struct {
	ID        string    `json:"id"`
	Author    actorWire `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path,omitempty"`
	Side      string    `json:"side,omitempty"`
	Line      int       `json:"line,omitempty"`
	HeadSHA   string    `json:"head_sha,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
	Resolved  bool      `json:"resolved,omitempty"`
}

func commentToWire(c Comment) commentWire {
	author := actorWire{Type: "user", ID: c.Author}
	if c.AuthorIsAgent {
		author.Type = "agent"
	}
	return commentWire{
		ID: c.ID, Author: author, Body: c.Body, CreatedAt: c.CreatedAt.UTC(),
		Path: c.Path, Side: c.Side, Line: c.Line,
		HeadSHA: c.HeadSHA, ParentID: c.ParentID, Resolved: c.Resolved,
	}
}

// listCommentsResponse matches list_change_comments' output_schema in the
// MCP catalog, so the MCP tool is a passthrough of this endpoint (§8.3's
// single-contract rule, the MergeRequirements precedent).
type listCommentsResponse struct {
	Comments      []commentWire `json:"comments"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, ok, err := s.Store.GetChange(r.Context(), key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	limit, ok := queryInt(w, r, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(w, r, "offset")
	if !ok {
		return
	}
	comments, err := s.Store.ListComments(r.Context(), key, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := listCommentsResponse{Comments: make([]commentWire, len(comments))}
	for i, c := range comments {
		resp.Comments[i] = commentToWire(c)
	}
	if limit > 0 && len(comments) == limit {
		resp.NextPageToken = fmt.Sprintf("%d", offset+limit)
	}
	writeJSON(w, http.StatusOK, resp)
}

type createCommentRequest struct {
	Body     string `json:"body"`
	Path     string `json:"path,omitempty"`
	Side     string `json:"side,omitempty"`
	Line     int    `json:"line,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Author   string `json:"author,omitempty"`
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	change, ok, err := s.Store.GetChange(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	var req createCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with at least a body field",
		})
		return
	}
	comment, apiErr := s.commentChangeCore(r.Context(), key, change, commentInput{
		Body: req.Body, Path: req.Path, Side: req.Side, Line: req.Line,
		ParentID: req.ParentID, Author: req.Author,
	}, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, commentToWire(comment))
}

type resolveCommentRequest struct {
	Resolved *bool `json:"resolved"`
}

func (s *Server) handleResolveComment(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	change, ok, err := s.Store.GetChange(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	resolved := true // POST .../resolve with an empty body means "resolve"
	var req resolveCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Resolved != nil {
		resolved = *req.Resolved
	}
	comment, apiErr := s.resolveCommentCore(r.Context(), key, change, r.PathValue("id"), resolved, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, commentToWire(comment))
}

type requestReviewRequest struct {
	Reviewer string `json:"reviewer"`
}

func (s *Server) handleRequestReview(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	change, ok, err := s.Store.GetChange(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	var req requestReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: `request body must be JSON: {"reviewer": "..."}`,
		})
		return
	}
	rr, apiErr := s.requestReviewCore(r.Context(), key, change, req.Reviewer, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviewer": rr.Reviewer, "requested_by": rr.RequestedBy})
}

func (s *Server) handleListReviewRequests(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if _, ok, err := s.Store.GetChange(r.Context(), key); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	requests, err := s.Store.ListReviewRequests(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, len(requests))
	for i, rr := range requests {
		out[i] = map[string]any{"reviewer": rr.Reviewer, "requested_by": rr.RequestedBy}
	}
	writeJSON(w, http.StatusOK, map[string]any{"review_requests": out})
}

// principalOwnsAnchor reports whether name is a required owner for the
// commented path (resolveCommentCore's third resolver identity). A
// change-level comment (path "") falls back to any of the change's
// required owners. Only user:<name> refs can match a principal - group
// membership isn't resolvable in the interim registry (§15.1), so a group
// owner resolves threads via the change author or admin instead.
func (s *Server) principalOwnsAnchor(ctx context.Context, change Change, path, name string) (bool, error) {
	result, indexed, err := s.computeAffected(change)
	if err != nil {
		return false, err
	}
	userRef := "user:" + name
	if path != "" {
		project, ok := owningProject(indexed, path)
		if !ok {
			return false, nil
		}
		for _, o := range project.Owners {
			if o.Ref == userRef {
				return true, nil
			}
		}
		return false, nil
	}
	owners, err := s.ownerRequirements(ctx, change.ChangeKey, change.HeadSHA, change.AuthoredBy, result, indexed)
	if err != nil {
		return false, err
	}
	for _, o := range owners {
		if o.OwnerRef == userRef {
			return true, nil
		}
	}
	return false, nil
}

// reviewActor types a reviewer/author name for webhook payloads: group:
// refs are groups, names matching an agent principal are agents, everything
// else is a user. Config-registry lookup only - a wrong "user" for an
// unknown stored agent is harmless (the schema's enum is the contract, the
// type is advisory routing metadata).
func (s *Server) reviewActor(name string) checks.WebhookActor {
	if strings.HasPrefix(name, "group:") {
		return checks.WebhookActor{Type: "group", ID: strings.TrimPrefix(name, "group:")}
	}
	for i := range s.Principals {
		if s.Principals[i].Name == name && s.Principals[i].IsAgent {
			return checks.WebhookActor{Type: "agent", ID: name}
		}
	}
	return checks.WebhookActor{Type: "user", ID: name}
}

// enqueueCommentWebhook emits change.commented (§13.4.1). Ids and the
// anchor only, never the body - consumers fetch bodies via the API so CI
// logs don't accumulate review text (the envelope schema's own rule). No
// affected block: nothing fans out jobs on a comment.
func (s *Server) enqueueCommentWebhook(ctx context.Context, change Change, comment Comment) {
	author := checks.WebhookActor{Type: "user", ID: comment.Author}
	if comment.AuthorIsAgent {
		author.Type = "agent"
	}
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  change.ChangeKey + "@comment@" + comment.ID,
		Type:        "change.commented",
		OccurredAt:  s.clock(),
		OrgID:       s.SettingsOrg,
		Change: checks.WebhookChange{
			ID: change.ChangeKey, State: change.State,
			BaseSHA: change.BaseSHA, HeadSHA: change.HeadSHA, GitRef: change.GitRef,
			Title: change.Title, Actor: author,
		},
		Comment: &checks.WebhookComment{
			ID: comment.ID, ParentID: comment.ParentID,
			Path: comment.Path, Side: comment.Side, Line: comment.Line,
			Resolved: comment.Resolved, Author: author,
		},
	}
	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		log.Printf("runkod: %s: marshal comment webhook: %v", change.ChangeKey, err)
		return
	}
	if _, err := s.Store.EnqueueWebhook(ctx, env.Type, payload); err != nil {
		log.Printf("runkod: %s: enqueue comment webhook: %v", change.ChangeKey, err)
	}
}

// enqueueReviewRequestWebhook emits change.review_requested (§13.4.2).
func (s *Server) enqueueReviewRequestWebhook(ctx context.Context, change Change, reviewer, requestedBy string) {
	by := checks.WebhookActor{Type: "user", ID: requestedBy}
	if requestedBy == "" {
		by.ID = "unknown"
	}
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  change.ChangeKey + "@review-request@" + reviewer + "@" + s.clock().UTC().Format(time.RFC3339),
		Type:        "change.review_requested",
		OccurredAt:  s.clock(),
		OrgID:       s.SettingsOrg,
		Change: checks.WebhookChange{
			ID: change.ChangeKey, State: change.State,
			BaseSHA: change.BaseSHA, HeadSHA: change.HeadSHA, GitRef: change.GitRef,
			Title: change.Title, Actor: by,
		},
		ReviewRequest: &checks.WebhookReviewRequest{
			Reviewer:    s.reviewActor(reviewer),
			RequestedBy: by,
		},
	}
	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		log.Printf("runkod: %s: marshal review-request webhook: %v", change.ChangeKey, err)
		return
	}
	if _, err := s.Store.EnqueueWebhook(ctx, env.Type, payload); err != nil {
		log.Printf("runkod: %s: enqueue review-request webhook: %v", change.ChangeKey, err)
	}
}

// requireResolvedThreads reads the org knob (§13.4.1, default off - the
// ceremony budget). Same read-with-degrade posture as effectiveGlobalChecks:
// a directory failure logs and reads as off, retried on the next request.
func (s *Server) requireResolvedThreads(ctx context.Context) bool {
	if s.SettingsOrg == "" || s.Directory == nil {
		return false
	}
	settings, err := s.Directory.GetOrgSettings(ctx, s.SettingsOrg)
	if err != nil {
		log.Printf("runkod: org %q settings unavailable for require_resolved_threads (reads as off): %v", s.SettingsOrg, err)
		return false
	}
	return settings.RequireResolvedThreads
}

// attentionSet derives §13.4.2's "whose turn is it" - a pure function of
// facts the store already holds, never stored itself: requested reviewers
// and required owners who have neither approved nor commented at the
// current head, plus the author once any reviewer has responded to the
// current version. An amend moves head_sha and the whole set re-derives.
func attentionSet(change Change, owners []checks.OwnerRequirement, requests []ReviewRequest, comments []Comment) []string {
	commented := map[string]bool{}
	reviewerResponded := false
	for _, c := range comments {
		if c.HeadSHA != change.HeadSHA {
			continue // outdated - a response to an older version isn't one to this version
		}
		commented[c.Author] = true
		if c.Author != change.AuthoredBy {
			reviewerResponded = true
		}
	}

	set := map[string]bool{}
	for _, r := range requests {
		if r.Reviewer == change.AuthoredBy {
			continue // self-requests never put the author on both sides
		}
		if commented[r.Reviewer] {
			continue
		}
		set[r.Reviewer] = true
	}
	for _, o := range owners {
		if o.Satisfied {
			reviewerResponded = true // an approval at the current head is a response
			continue
		}
		// user:<name> owners who commented at the current head have
		// responded; group refs can't be matched to comment authors
		// (§15.1 - no membership resolution) and stay until approved.
		if name, ok := strings.CutPrefix(o.OwnerRef, "user:"); ok {
			if name == change.AuthoredBy {
				continue
			}
			if commented[name] {
				continue
			}
		}
		set[o.OwnerRef] = true
	}
	if reviewerResponded && change.AuthoredBy != "" {
		set[change.AuthoredBy] = true
	}

	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
