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

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/land"
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
			Suggestion: "an operator can grant it: --principal 'name=...;token=...;admin'",
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
	required := mergeCheckNames(requiredCheckNames(result, indexed), s.GlobalRequiredChecks)
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
