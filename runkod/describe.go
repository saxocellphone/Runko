// Change description / test plan (§8.6 "Summaries": agents and humans are
// encouraged to say WHAT a change does and HOW it was verified; the UI
// prompts when empty). Explicitly-set control-plane metadata on the Change,
// never derived from the commit message and never a merge gate - it also
// feeds §14.10.3's release changelogs, which is why only OPEN changes take
// edits: once landed, the description is part of the record a release
// derives from.
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/saxocellphone/runko/internal/clierr"
)

// describeChangeCore is POST /api/changes/{key}/describe's decision core.
// A nil field means "leave as is" - so a caller can set the test plan
// without re-sending (or clobbering) the description, and vice versa. An
// explicit empty string clears the field.
func (s *Server) describeChangeCore(ctx context.Context, key string, description, testPlan *string) (Change, *apiError) {
	change, ok, err := s.Store.GetChange(ctx, key)
	if err != nil {
		return Change{}, internalErr(err)
	}
	if !ok {
		return Change{}, plainErr(http.StatusNotFound, "change not found")
	}
	if change.State != "open" {
		return Change{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_state", Field: "change",
			Message:    fmt.Sprintf("change %s is %s - only open changes take a description", key, change.State),
			Suggestion: "a landed change's description is part of the record its release changelog derives from; re-push to reopen an abandoned change first",
		})
	}
	desc, plan := change.Description, change.TestPlan
	if description != nil {
		desc = *description
	}
	if testPlan != nil {
		plan = *testPlan
	}
	updated, err := s.Store.UpdateChangeDescription(ctx, key, desc, plan)
	if err != nil {
		return Change{}, internalErr(err)
	}
	return updated, nil
}

func (s *Server) handleDescribeChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description *string `json:"description"`
		TestPlan    *string `json:"test_plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with description and/or test_plan (strings)",
		})
		return
	}
	if req.Description == nil && req.TestPlan == nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Field: "description",
			Message:    "nothing to set: provide description and/or test_plan",
			Suggestion: "pass at least one field, e.g. {\"description\": \"what this change does\"}",
		})
		return
	}
	change, apiErr := s.describeChangeCore(r.Context(), r.PathValue("key"), req.Description, req.TestPlan)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, change)
}
