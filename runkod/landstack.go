package runkod

import (
	"context"
	"fmt"
	"net/http"

	"github.com/saxocellphone/runko/internal/clierr"
)

// Land-stack (§13.5, 2026-07-15): one verb for "land everything from this
// Change down to the bottom of its stack" - the Gerrit "submit including
// parents" analog, and the server-side form of the bottom-up loop every
// client hand-rolled (land parent, wait, land child). Each member lands
// through the exact same landChangeCore as a single land: same
// per-principal merge gate, same attribution, webhooks, automerge kick,
// and workspace bookkeeping. Force is deliberately not offered - the
// §13.5 override stays a single-change, eyes-on verb.

// landStackDecision is landStackCore's outcome: the members that landed
// (bottom-up) plus, when the sweep could not get through the requested
// Change, the member it stopped at and why. Per-member failures are
// encoded here rather than returned as apiErrors: every landed member is
// already durable trunk state, and an error response would bury that
// partial progress.
type landStackDecision struct {
	Landed     []landedStackMember
	StoppedKey string // "" when the sweep landed through the requested Change
	// Exactly one of the following explains a non-empty StoppedKey,
	// mirroring landDecision's non-landed outcomes.
	Blockers             []string
	RequiresRevalidation bool
	Conflicts            []string
	RaceRetryExhausted   bool
}

type landedStackMember struct {
	ChangeKey string
	LandedSHA string
}

// landStackCore lands the ancestor chain through key, bottom-up. The
// chain follows stackForChange's aliveness rule - parent is the OPEN
// Change whose head is the child's recorded base - so a landed, abandoned,
// or unknown base ends the walk, and landChangeCore's own stacked-ordering
// gate (refuseUnlandedParent) stays the authority on whether the
// bottom-most member may actually land.
func (s *Server) landStackCore(ctx context.Context, key string, change Change, lane *BotLane, principal *Principal) (landStackDecision, *apiError) {
	if change.State == "abandoned" {
		return landStackDecision{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    fmt.Sprintf("change %s is abandoned", key),
			Suggestion: "an abandoned change cannot land; push it again to reopen it",
		})
	}

	open, err := s.Store.ListChanges(ctx, "open")
	if err != nil {
		return landStackDecision{}, internalErr(err)
	}
	byHead := make(map[string]Change, len(open))
	for _, c := range open {
		if c.HeadSHA != "" {
			byHead[c.HeadSHA] = c
		}
	}
	chain := []Change{change}
	seen := map[string]bool{change.ChangeKey: true}
	for {
		parent, ok := byHead[chain[0].BaseSHA]
		if !ok || seen[parent.ChangeKey] {
			break
		}
		seen[parent.ChangeKey] = true
		chain = append([]Change{parent}, chain...)
	}

	var dec landStackDecision
	for _, member := range chain {
		// Refetch: an earlier iteration's land - or a concurrent caller -
		// may have moved this member since the chain was derived.
		cur, ok, err := s.Store.GetChange(ctx, member.ChangeKey)
		if err != nil {
			return landStackDecision{}, internalErr(err)
		}
		if !ok || cur.State == "landed" {
			continue // already there - LandChange's own idempotence rule
		}
		res, apiErr := s.landChangeCore(ctx, cur.ChangeKey, cur, lane, principal, false)
		if apiErr != nil {
			dec.StoppedKey = cur.ChangeKey
			if apiErr.Err.Code == "not_mergeable" {
				// The member's own merge gate: report the real blocker
				// list, the same strings its merge-requirements view shows.
				if mr, mrErr := s.mergeRequirements(ctx, cur.ChangeKey, cur, lane); mrErr == nil {
					dec.Blockers = mr.Blockers
					return dec, nil
				}
			}
			dec.Blockers = []string{apiErr.Err.Message}
			return dec, nil
		}
		switch {
		case res.Landed:
			dec.Landed = append(dec.Landed, landedStackMember{ChangeKey: cur.ChangeKey, LandedSHA: res.LandedSHA})
		case res.RequiresRevalidation:
			dec.StoppedKey = cur.ChangeKey
			dec.RequiresRevalidation = true
			return dec, nil
		case len(res.Conflicts) > 0:
			dec.StoppedKey = cur.ChangeKey
			dec.Conflicts = res.Conflicts
			return dec, nil
		default: // exhausted maxLandRaceRetries
			dec.StoppedKey = cur.ChangeKey
			dec.RaceRetryExhausted = true
			return dec, nil
		}
	}
	return dec, nil
}

// landStackResponse is POST .../land-stack's wire shape, mirroring
// changes.proto's LandStackResponse. One deliberate divergence from this
// API's one-clierr-per-outcome convention (handleLandChange): a stopped
// sweep still answers 200 with the full report, because the members that
// DID land are durable trunk state a 409 would bury.
type landStackResponse struct {
	Landed               []landStackLandedMember
	StoppedChangeID      string   `json:",omitempty"`
	Blockers             []string `json:",omitempty"`
	RequiresRevalidation bool     `json:",omitempty"`
	Conflicts            []string `json:",omitempty"`
	RaceRetry            bool     `json:",omitempty"`
}

type landStackLandedMember struct {
	ChangeID  string
	LandedSHA string
}

func (s *Server) handleLandStack(w http.ResponseWriter, r *http.Request) {
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
	dec, apiErr := s.landStackCore(r.Context(), key, change, s.laneFor(r), s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	resp := landStackResponse{
		Landed:               make([]landStackLandedMember, 0, len(dec.Landed)),
		StoppedChangeID:      dec.StoppedKey,
		Blockers:             dec.Blockers,
		RequiresRevalidation: dec.RequiresRevalidation,
		Conflicts:            dec.Conflicts,
		RaceRetry:            dec.RaceRetryExhausted,
	}
	for _, m := range dec.Landed {
		resp.Landed = append(resp.Landed, landStackLandedMember{ChangeID: m.ChangeKey, LandedSHA: m.LandedSHA})
	}
	writeJSON(w, http.StatusOK, resp)
}
