// Shared action cores behind both API transports (§28.3 stage 13, server
// half). The REST handlers (api.go, workspace.go) and the Connect RPC
// handlers (rpc.go) are thin encoders over the functions here, so the two
// surfaces cannot drift on semantics - the same stance mergeRequirements
// already takes for GET .../merge-requirements vs POST .../land, extended
// across transports. One construction site per failure; each transport
// maps apiError onto its own wire shape.
package runkod

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/land"
)

// apiError pairs a failure with the HTTP status the REST surface reports it
// as; rpc.go maps the same status onto the equivalent Connect code. A zero
// Err.Code means "plain text" - the REST layer's historical http.Error
// convention for not-found and internal errors - while a non-zero Code is a
// structured §6.5 clierr.Error written as JSON.
type apiError struct {
	Status int
	Err    clierr.Error
}

func plainErr(status int, msg string) *apiError {
	return &apiError{Status: status, Err: clierr.Error{Message: msg}}
}

func typedErr(status int, e clierr.Error) *apiError {
	return &apiError{Status: status, Err: e}
}

func internalErr(err error) *apiError {
	return plainErr(http.StatusInternalServerError, err.Error())
}

// writeAPIError encodes an apiError exactly as the pre-refactor REST
// handlers did: plain http.Error for code-less failures, JSON clierr
// otherwise.
func writeAPIError(w http.ResponseWriter, e *apiError) {
	if e.Err.Code == "" {
		http.Error(w, e.Err.Message, e.Status)
		return
	}
	writeJSON(w, e.Status, e.Err)
}

// approveChangeCore is POST .../approve's decision core (§13.5's "required
// human owners approved" gate, §28.3 stage 11c): principal attribution and
// the agent/self-approval denials, owner_ref validation against what the
// tree currently requires, the approval record, and the refreshed merge
// requirements. change is the caller's already-fetched row (both transports
// 404 before reaching here).
// requireOpenChange is the state-machine guard for events only an OPEN
// Change may receive (docs/change-lifecycle.md): landed is terminal (§7.4)
// and abandoned's only exit is a re-push. Approving an abandoned Change was
// a real leak, not just untidiness - approvals bind to head_sha, so a
// reopen via re-push of the SAME commit would inherit an approval granted
// while the Change was abandoned.
func requireOpenChange(key string, change Change, verb string) *apiError {
	switch change.State {
	case "open":
		return nil
	case "landed":
		return typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    fmt.Sprintf("change %s has already landed", key),
			Suggestion: fmt.Sprintf("landed is terminal - nothing to %s; new work needs a new change", verb),
		})
	default: // abandoned
		return typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    fmt.Sprintf("change %s is abandoned", key),
			Suggestion: fmt.Sprintf("push the change again to reopen it, then %s", verb),
		})
	}
}

func (s *Server) approveChangeCore(ctx context.Context, key string, change Change, ownerRef, approvedBy string, principal *Principal) (checks.MergeRequirements, *apiError) {
	if apiErr := requireOpenChange(key, change, "approve"); apiErr != nil {
		return checks.MergeRequirements{}, apiErr
	}
	// Attribution (§15.1 interim principals, stage 12c): a named principal
	// approves as itself - the client-asserted approved_by is only trusted
	// from the anonymous deploy token (the documented v1 eval boundary).
	if principal != nil {
		if principal.IsAgent {
			// §13.5's gate table: "Agent-only approval: No" - a hard rule,
			// not policy. An agent's review can inform; it cannot satisfy
			// the human-owner gate.
			return checks.MergeRequirements{}, typedErr(http.StatusForbidden, clierr.Error{
				Code:       "agent_approval_denied",
				Field:      "approved_by",
				Message:    fmt.Sprintf("%q is an agent principal - agents cannot approve changes (§13.5)", principal.Name),
				Suggestion: "a human owner must approve; agents may run checks and request review",
			})
		}
		if approvedBy != "" && approvedBy != principal.Name {
			return checks.MergeRequirements{}, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "approved_by_mismatch", Field: "approved_by",
				Message:    fmt.Sprintf("authenticated as %q but approved_by says %q", principal.Name, approvedBy),
				Suggestion: "drop approved_by - your token already says who you are",
			})
		}
		approvedBy = principal.Name
	}
	if ownerRef == "" || approvedBy == "" {
		return checks.MergeRequirements{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "owner_ref",
			Message:    "both owner_ref and approved_by are required",
			Suggestion: `POST {"owner_ref": "group:...", "approved_by": "<you>"}`,
		})
	}

	// §8.7's "no self-approval", enforceable now that Changes record who
	// pushed their current head. This also catches an honest deploy-token
	// caller naming the author in approved_by; a DISHONEST anonymous
	// caller can still lie, which is exactly the boundary the named-token
	// registry exists to retire.
	if change.AuthoredBy != "" && approvedBy == change.AuthoredBy {
		return checks.MergeRequirements{}, typedErr(http.StatusForbidden, clierr.Error{
			Code:       "self_approval_denied",
			Field:      "approved_by",
			Message:    fmt.Sprintf("%q pushed this change's current head and cannot approve it (§8.7)", approvedBy),
			Suggestion: "another required owner must approve",
		})
	}

	result, indexed, err := s.computeAffected(change)
	if err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	owners, err := s.ownerRequirements(ctx, key, change.HeadSHA, result, indexed)
	if err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	isRequired := false
	var requiredRefs []string
	for _, o := range owners {
		requiredRefs = append(requiredRefs, o.OwnerRef)
		if o.OwnerRef == ownerRef {
			isRequired = true
		}
	}
	if !isRequired {
		suggestion := "this change has no owner requirements at all - nothing to approve"
		if len(requiredRefs) > 0 {
			suggestion = "required owners for this change: " + strings.Join(requiredRefs, ", ")
		}
		return checks.MergeRequirements{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "not_a_required_owner", Field: "owner_ref",
			Message:    fmt.Sprintf("%q is not a required owner for change %s", ownerRef, key),
			Suggestion: suggestion,
		})
	}

	if err := s.Store.RecordApproval(ctx, key, ownerRef, approvedBy, change.HeadSHA); err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	reqs, err := s.mergeRequirements(ctx, key, change, nil)
	if err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	return reqs, nil
}

// commentInput is commentChangeCore's request shape across both transports
// (§13.4.1). Author is only honored from the anonymous deploy token - the
// same v1 trust boundary approve's approved_by lives with; a named
// principal always comments as itself.
type commentInput struct {
	Body     string
	Path     string
	Side     string
	Line     int
	ParentID string
	Author   string
}

// commentChangeCore is POST .../comments' decision core (§13.4.1): principal
// attribution (agents ALLOWED, unlike approve - review output is exactly
// what agent principals are for, the badge rides AuthorIsAgent), anchor
// validation, the one-level thread rule, the server-side head_sha stamp,
// and the change.commented webhook.
func (s *Server) commentChangeCore(ctx context.Context, key string, change Change, in commentInput, principal *Principal) (Comment, *apiError) {
	if apiErr := requireOpenChange(key, change, "comment"); apiErr != nil {
		return Comment{}, apiErr
	}
	author := in.Author
	isAgent := false
	if principal != nil {
		if in.Author != "" && in.Author != principal.Name {
			return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "author_mismatch", Field: "author",
				Message:    fmt.Sprintf("authenticated as %q but author says %q", principal.Name, in.Author),
				Suggestion: "drop author - your token already says who you are",
			})
		}
		author = principal.Name
		isAgent = principal.IsAgent
	}
	if author == "" {
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "author",
			Message:    "author is required",
			Suggestion: `POST {"body": "...", "author": "<you>"} - or authenticate as a named principal`,
		})
	}
	if strings.TrimSpace(in.Body) == "" {
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "body",
			Message:    "a comment needs a body",
			Suggestion: `POST {"body": "..."}`,
		})
	}

	// Anchor validation (§13.4.1): change-level (no path), file-level (path
	// only), or line-level (path+side+line). Fail loud on shapes the model
	// doesn't have rather than storing something no view can render.
	if in.Line < 0 {
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_anchor", Field: "line", Message: "line must be positive",
		})
	}
	if in.Line > 0 && in.Path == "" {
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_anchor", Field: "path",
			Message:    "a line anchor needs a path",
			Suggestion: "pass path with line, or drop line for a change-level comment",
		})
	}
	side := in.Side
	switch {
	case side != "" && side != "base" && side != "head":
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_anchor", Field: "side",
			Message:    fmt.Sprintf("side must be \"base\" or \"head\", got %q", side),
			Suggestion: "head = the change's version of the file (the usual case), base = the version it started from",
		})
	case side != "" && in.Line == 0:
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_anchor", Field: "side",
			Message: "side only applies to a line anchor - pass line too",
		})
	case side == "" && in.Line > 0:
		side = "head" // the diff side a reviewer nearly always means
	}

	if in.ParentID != "" {
		parent, ok, err := s.Store.GetComment(ctx, key, in.ParentID)
		if err != nil {
			return Comment{}, internalErr(err)
		}
		if !ok {
			return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "parent_not_found", Field: "parent_id",
				Message:    fmt.Sprintf("no comment %q on change %s", in.ParentID, key),
				Suggestion: "list the change's comments to find the thread root's id",
			})
		}
		if parent.ParentID != "" {
			// One-level threads (§13.4.1, the GitHub model): the thread is
			// the root plus its replies, never a tree.
			return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "thread_depth_exceeded", Field: "parent_id",
				Message:    "threads are one level deep - reply to the thread root",
				Suggestion: fmt.Sprintf("use parent_id %q", parent.ParentID),
			})
		}
		if in.Path != "" || in.Line > 0 || in.Side != "" {
			return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "invalid_anchor", Field: "parent_id",
				Message:    "replies inherit the thread root's anchor",
				Suggestion: "drop path/line/side when replying",
			})
		}
	}

	comment, err := s.Store.CreateComment(ctx, key, Comment{
		Author: author, AuthorIsAgent: isAgent,
		Body: in.Body, Path: in.Path, Side: side, Line: in.Line,
		HeadSHA:  change.HeadSHA, // the binding: an amend outdates this comment (§13.4.1)
		ParentID: in.ParentID,
	})
	if err != nil {
		return Comment{}, internalErr(err)
	}
	s.enqueueCommentWebhook(ctx, change, comment)
	return comment, nil
}

// resolveCommentCore is POST .../comments/{id}/resolve's core (§13.4.1):
// resolved lives on the thread root and may be flipped by the thread
// author, the Change author, an owner of the anchored path, or an admin.
// The anonymous deploy token may always resolve - the documented v1 eval
// boundary, same as approve's client-asserted identity.
func (s *Server) resolveCommentCore(ctx context.Context, key string, change Change, commentID string, resolved bool, principal *Principal) (Comment, *apiError) {
	if apiErr := requireOpenChange(key, change, "resolve review threads"); apiErr != nil {
		return Comment{}, apiErr
	}
	comment, ok, err := s.Store.GetComment(ctx, key, commentID)
	if err != nil {
		return Comment{}, internalErr(err)
	}
	if !ok {
		return Comment{}, plainErr(http.StatusNotFound, "comment not found")
	}
	if comment.ParentID != "" {
		return Comment{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "not_a_thread_root", Field: "comment_id",
			Message:    "resolved lives on the thread root, not on replies",
			Suggestion: fmt.Sprintf("resolve %q instead", comment.ParentID),
		})
	}
	if principal != nil && !principal.Admin &&
		principal.Name != comment.Author && principal.Name != change.AuthoredBy {
		allowed, err := s.principalOwnsAnchor(ctx, change, comment.Path, principal.Name)
		if err != nil {
			return Comment{}, internalErr(err)
		}
		if !allowed {
			return Comment{}, typedErr(http.StatusForbidden, clierr.Error{
				Code: "resolve_denied", Field: "comment_id",
				Message:    fmt.Sprintf("%q may not resolve this thread", principal.Name),
				Suggestion: "the thread author, the change author, or an owner of the commented path can resolve it",
			})
		}
	}
	if err := s.Store.SetCommentResolved(ctx, key, commentID, resolved); err != nil {
		return Comment{}, internalErr(err)
	}
	comment.Resolved = resolved
	return comment, nil
}

// requestReviewCore is POST .../request-review's core (§13.4.2): records
// the request (idempotent upsert) and emits change.review_requested; the
// reviewer enters the DERIVED attention set by existing, nothing else is
// stored.
func (s *Server) requestReviewCore(ctx context.Context, key string, change Change, reviewer string, principal *Principal) (ReviewRequest, *apiError) {
	if apiErr := requireOpenChange(key, change, "request review"); apiErr != nil {
		return ReviewRequest{}, apiErr
	}
	reviewer = strings.TrimSpace(reviewer)
	if reviewer == "" {
		return ReviewRequest{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "reviewer",
			Message:    "reviewer is required",
			Suggestion: `POST {"reviewer": "<principal or group:name>"}`,
		})
	}
	requestedBy := ""
	if principal != nil {
		requestedBy = principal.Name
	}
	if err := s.Store.UpsertReviewRequest(ctx, key, reviewer, requestedBy); err != nil {
		return ReviewRequest{}, internalErr(err)
	}
	s.enqueueReviewRequestWebhook(ctx, change, reviewer, requestedBy)
	return ReviewRequest{Reviewer: reviewer, RequestedBy: requestedBy}, nil
}

// createReleaseCore is POST /api/projects/{name}/releases' decision core
// (§14.10.3, stage 17b): resolve the project + its release capability at
// trunk tip, authorize against the SAME tag-policy decision the receive
// funnel enforces (server-minted tags bypass nothing), derive
// version + changelog, write the annotated tag, record the immutable row,
// trigger the mirror, and emit release.created.
func (s *Server) createReleaseCore(ctx context.Context, projectName, explicitVersion string, principal *Principal, lane *BotLane) (Release, *apiError) {
	proj, cfg, trunkTip, apiErr := s.resolveReleaseProject(projectName)
	if apiErr != nil {
		return Release{}, apiErr
	}

	version, apiErr := func() (string, *apiError) {
		latest, hasLatest, err := s.Store.GetLatestRelease(ctx, projectName)
		if err != nil {
			return "", internalErr(err)
		}
		return nextVersion(explicitVersion, cfg, latest, hasLatest)
	}()
	if apiErr != nil {
		return Release{}, apiErr
	}
	tagName := cfg.TagPrefix + version

	// The same decision function the funnel runs on a raw `git push
	// origin <tag>` (tags.go): when the org enforces tag policy, cutting a
	// release requires the same operator/admin/releaser/lane-in-namespace
	// standing - only the rendering differs (clierr here, the §6.9 script
	// over the wire).
	if s.Processor != nil && s.Processor.tagPolicyEnforced(ctx) {
		author, laneName := "", ""
		if principal != nil {
			author = principal.Name
		}
		if lane != nil {
			laneName = lane.Name
		}
		if reject := s.Processor.authorizeTagWrite(ctx, author, laneName, tagName); reject != "" {
			return Release{}, typedErr(http.StatusForbidden, clierr.Error{
				Code: "release_denied", Field: "project",
				Message:    fmt.Sprintf("this org enforces tag policy - %s may not cut releases for %q", callerLabel(principal, lane), projectName),
				Suggestion: `ask an org admin for the "releaser" role (release automation gets a bot lane with tags=<glob>)`,
				DocURL:     "docs/design.md#14103-tags-and-releases-decided-2026-07-10-resolves-the-223-tag-governance-question",
			})
		}
	}

	changelog, headChangeKey := "", ""
	if cfg.Changelog == "from-changes" {
		since := ""
		if latest, hasLatest, err := s.Store.GetLatestRelease(ctx, projectName); err == nil && hasLatest {
			since = latest.TargetSHA
		}
		commits, err := s.commitsSince(trunkTip, since, proj.Path, changelogMaxCommits)
		if err != nil {
			return Release{}, internalErr(err)
		}
		changelog, headChangeKey = s.deriveChangelog(ctx, version, commits)
	}

	message := fmt.Sprintf("Release %s %s\n\n%s", projectName, version, changelog)
	tagSHA, err := gitstore.New(s.RepoDir).CreateAnnotatedTag(tagName, trunkTip, message)
	if err != nil {
		if gitstore.IsTagExists(err) {
			return Release{}, typedErr(http.StatusConflict, clierr.Error{
				Code: "tag_exists", Field: "version",
				Message:    fmt.Sprintf("tag %q already exists", tagName),
				Suggestion: "pick a different version - existing tags are never re-pointed (§14.10.3)",
			})
		}
		return Release{}, internalErr(err)
	}

	createdBy := ""
	if principal != nil {
		createdBy = principal.Name
	} else if lane != nil {
		createdBy = lane.Name
	}
	release, err := s.Store.CreateRelease(ctx, Release{
		ProjectName: projectName, ProjectPath: proj.Path,
		Version: version, TagRef: "refs/tags/" + tagName,
		TagSHA: string(tagSHA), TargetSHA: string(trunkTip),
		HeadChangeKey: headChangeKey, Changelog: changelog, CreatedBy: createdBy,
	})
	if err != nil {
		// Lost the same-version race after the tag write: the tag stands
		// (immutable, correct content), the row loss surfaces loudly.
		return Release{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "version_exists", Field: "version",
			Message:    fmt.Sprintf("release %s %s already exists", projectName, version),
			Suggestion: "list the project's releases; a concurrent create may have won this version",
		})
	}
	s.Mirror.Trigger() // the mirror carries refs/tags/* - ship the new tag promptly
	s.enqueueReleaseWebhook(ctx, release)
	return release, nil
}

// callerLabel names the caller for release_denied messages.
func callerLabel(principal *Principal, lane *BotLane) string {
	switch {
	case principal != nil:
		return fmt.Sprintf("principal %q", principal.Name)
	case lane != nil:
		return fmt.Sprintf("bot lane %q", lane.Name)
	default:
		return "the anonymous deploy token"
	}
}

// landDecision is landChangeCore's outcome across both transports: exactly
// one of Landed / RequiresRevalidation / non-empty Conflicts /
// RaceRetryExhausted holds. REST encodes the non-landed cases as 409
// clierr.Errors (api.go's historical wire shape); the Connect surface
// returns them as LandChangeResponse fields, which is what the proto (and
// the web UI's banners) model.
type landDecision struct {
	Landed               bool
	LandedSHA            string
	RequiresRevalidation bool
	Conflicts            []string
	RaceRetryExhausted   bool
	// Forced echoes that this land bypassed the merge gates via the admin
	// override; the durable audit bit is Change.LandedForced.
	Forced bool
}

// authorizeForceLand gates the §13.5 force override: the anonymous deploy
// token (the documented v1 operator credential) and admin-flagged human
// principals may force; agents may NEVER (hard rule, same class as
// agent-approval denial) and neither may bot lanes (their entire design is
// scoped auto-land under their own checks - an ungated lane is exactly
// what §14.10.2 refuses to model).
// forceActor names the force-land caller for the audit log line.
func forceActor(principal *Principal) string {
	if principal == nil {
		return "the anonymous deploy token"
	}
	return fmt.Sprintf("admin principal %q", principal.Name)
}

func authorizeForceLand(principal *Principal, lane *BotLane) *apiError {
	if lane != nil {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "force_denied", Field: "force",
			Message:    fmt.Sprintf("bot lane %q may not force-land - lanes land only under their own required checks (§14.10.2)", lane.Name),
			Suggestion: "use an admin principal for manual overrides",
		})
	}
	if principal == nil {
		return nil // anonymous deploy token: the operator credential (v1 boundary)
	}
	if principal.IsAgent {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "force_denied", Field: "force",
			Message:    fmt.Sprintf("%q is an agent principal - agents may never bypass merge gates (§8.7, §13.5)", principal.Name),
			Suggestion: "a human admin must force-land",
		})
	}
	if !principal.Admin {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "force_denied", Field: "force",
			Message:    fmt.Sprintf("%q is not an admin principal", principal.Name),
			Suggestion: "org admins may force-land in their org; an operator can also grant a config principal: --principal 'name=...;token=...;admin'",
		})
	}
	return nil
}

// landChangeCore is POST .../land's decision core (§13.5, §28.3 stage 11b):
// terminal-state handling, the bot-lane path-allowlist refusal, the
// merge-requirements gate (the exact same Mergeable bool the caller's
// merge-requirements view reports, per-principal), and the land attempt
// itself with landed-state recording, webhook, and Zoekt trigger.
func (s *Server) landChangeCore(ctx context.Context, key string, change Change, lane *BotLane, principal *Principal, force bool) (landDecision, *apiError) {
	if force {
		if apiErr := authorizeForceLand(principal, lane); apiErr != nil {
			return landDecision{}, apiErr
		}
	}
	if change.State == "landed" {
		// Idempotent: a client retrying a land request after a dropped
		// response (or simply asking again) should see the same success,
		// not a confusing "not mergeable"/re-attempt error.
		return landDecision{Landed: true, LandedSHA: change.LandedSHA}, nil
	}
	if change.State == "abandoned" {
		// Stage 12c-③: abandoned became reachable, so land must refuse it
		// - gates are computed from the tree and would otherwise happily
		// pass an abandoned Change straight onto trunk.
		return landDecision{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    fmt.Sprintf("change %s is abandoned", key),
			Suggestion: "an abandoned change cannot land; push it again to reopen it",
		})
	}

	// Stacked-land ordering (§7.4): a Change whose recorded base is not on
	// trunk is stacked on another pending Change - land.Land would rebase
	// only base..head onto trunk, silently landing the child WITHOUT its
	// parent's content. Refuse until the parent lands (or the child is
	// rebased onto trunk and re-pushed).
	if apiErr := s.refuseUnlandedParent(ctx, key, change); apiErr != nil {
		return landDecision{}, apiErr
	}

	if lane != nil {
		// §14.10.2: the lane may land ONLY Changes fully inside its path
		// allowlist. Refused before gating - an out-of-scope Change is not
		// "not mergeable yet", it is something this principal may never
		// land, however green it is.
		result, _, err := s.computeAffected(change)
		if err != nil {
			return landDecision{}, internalErr(err)
		}
		if outside := lane.pathsOutsideAllowlist(result.Paths); len(outside) > 0 {
			return landDecision{}, typedErr(http.StatusForbidden, clierr.Error{
				Code:       "bot_lane_path_denied",
				Field:      "change",
				Message:    fmt.Sprintf("bot lane %q may not land changes touching: %s", lane.Name, strings.Join(outside, ", ")),
				Suggestion: "this change needs the normal owner/check gate - request a human land",
				DocURL:     "docs/design.md#14102-gitops-writers--the-bot-lane",
			})
		}
	}

	mr, err := s.mergeRequirements(ctx, key, change, lane)
	if err != nil {
		return landDecision{}, internalErr(err)
	}
	if !mr.Mergeable {
		if !force {
			return landDecision{}, typedErr(http.StatusConflict, clierr.Error{
				Code:       "not_mergeable",
				Field:      "change",
				Message:    fmt.Sprintf("change %s is not mergeable yet", key),
				Suggestion: strings.Join(mr.Blockers, "; "),
				DocURL:     "docs/design.md#136-merge-gates-and-landing",
			})
		}
		// The override is loud by design: every bypassed blocker is named
		// in the log, and the Change carries the durable landed_forced bit.
		log.Printf("runkod: FORCE land %s by %s - bypassing blockers: %s",
			key, forceActor(principal), strings.Join(mr.Blockers, "; "))
	}

	scope := land.RevalidationAffectedIntersection
	if force {
		// Force means "land NOW": the trunk-delta revalidation rule is a
		// gate too, and requires_revalidation would send the admin into
		// the rebase loop the override exists to skip. Conflicts still
		// fail - a conflicting tree cannot be forced into existence.
		scope = land.RevalidationNever
	}
	outcome, err := s.attemptLand(ctx, change, scope)
	if err != nil {
		return landDecision{}, plainErr(http.StatusInternalServerError, fmt.Sprintf("land: %v", err))
	}

	switch {
	case outcome.Landed:
		// §7.5 attribution via §15.1's interim principals (stage 12c): a
		// named principal or bot lane lands under its own name; the
		// anonymous deploy token stays anonymous ("").
		landedBy := ""
		if principal != nil {
			landedBy = principal.Name
		} else if lane != nil {
			landedBy = lane.Name
		}
		forced := force && !mr.Mergeable // a force that bypassed nothing is an ordinary land
		if _, err := s.Store.MarkChangeLanded(ctx, key, outcome.LandedSHA, landedBy, forced); err != nil {
			return landDecision{}, plainErr(http.StatusInternalServerError, fmt.Sprintf("land: record landed state: %v", err))
		}
		if lane != nil {
			log.Printf("runkod: change %s landed via bot lane %q", key, lane.Name)
		}
		s.enqueueLandedWebhook(ctx, change, outcome.LandedSHA)
		s.maybeCloseAgentWorkspace(ctx, change.OriginWorkspace)
		if s.Processor != nil {
			s.Processor.ZoektIndexWorker.Trigger()
		}
		s.Mirror.Trigger() // trunk moved - nil-safe like the zoekt trigger
		return landDecision{Landed: true, LandedSHA: outcome.LandedSHA, Forced: forced}, nil
	case outcome.RequiresRevalidation:
		return landDecision{RequiresRevalidation: true}, nil
	case len(outcome.Conflicts) > 0:
		return landDecision{Conflicts: outcome.Conflicts}, nil
	default: // exhausted maxLandRaceRetries
		return landDecision{RaceRetryExhausted: true}, nil
	}
}

// abandonChangeCore is POST .../abandon's core (§7.4's third state).
// Idempotent on an already-abandoned Change; refuses a landed one (terminal
// - trunk already has the code).
func (s *Server) abandonChangeCore(ctx context.Context, key string, principal *Principal) (Change, *apiError) {
	_, ok, err := s.Store.GetChange(ctx, key)
	if err != nil {
		return Change{}, internalErr(err)
	}
	if !ok {
		return Change{}, plainErr(http.StatusNotFound, "change not found")
	}
	change, err := s.Store.MarkChangeAbandoned(ctx, key)
	if err != nil {
		return Change{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    err.Error(),
			Suggestion: "a landed change cannot be abandoned; revert it with a new change instead",
		})
	}
	if principal != nil {
		log.Printf("runkod: change %s abandoned by %q", key, principal.Name)
	}
	s.maybeCloseAgentWorkspace(ctx, change.OriginWorkspace)
	return change, nil
}

// rerunCheckCore is POST .../checks/{name}/rerun's core (§14.4.2's re-run
// flow): only checks that actually gate this Change can be rerun - a rerun
// of an unknown name would queue a run nothing will ever report against.
// Responds with the refreshed merge requirements, per-principal like every
// merge-requirements read.
func (s *Server) rerunCheckCore(ctx context.Context, key string, change Change, name string, principal *Principal, lane *BotLane) (checks.MergeRequirements, *apiError) {
	if apiErr := requireOpenChange(key, change, "rerun checks"); apiErr != nil {
		return checks.MergeRequirements{}, apiErr
	}
	result, indexed, err := s.computeAffected(change)
	if err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	required := mergeCheckNames(requiredCheckNames(result, indexed), s.effectiveGlobalChecks(ctx))
	isRequired := false
	for _, n := range required {
		if n == name {
			isRequired = true
			break
		}
	}
	if !isRequired {
		suggestion := "this change requires no checks at all"
		if len(required) > 0 {
			suggestion = "required checks for this change: " + strings.Join(required, ", ")
		}
		return checks.MergeRequirements{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "unknown_check", Field: "name",
			Message:    fmt.Sprintf("%q is not a required check for change %s", name, key),
			Suggestion: suggestion,
		})
	}

	requestedBy := ""
	if principal != nil {
		requestedBy = principal.Name
	}
	if _, err := s.Store.RerunCheck(ctx, key, name, requestedBy); err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	s.enqueueRerunWebhook(ctx, change, name, requestedBy)

	reqs, err := s.mergeRequirements(ctx, key, change, lane)
	if err != nil {
		return checks.MergeRequirements{}, internalErr(err)
	}
	return reqs, nil
}
