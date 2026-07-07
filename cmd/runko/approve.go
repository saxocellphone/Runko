package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
)

// ApproveChange implements `runko change approve` (§13.5's "required human
// owners approved" gate, §28.3 stage 11c): POST the daemon's .../approve
// endpoint (runkod/api.go's handleApproveChange) and decode the refreshed
// merge requirements it responds with, so the approver immediately sees what
// their approval covered and what still blocks. Like `change land`, this
// needs a live runkod instance.
func ApproveChange(ctx context.Context, client *http.Client, baseURL, token, changeID, ownerRef, approvedBy string) (checks.MergeRequirements, error) {
	body, err := json.Marshal(map[string]string{"owner_ref": ownerRef, "approved_by": approvedBy})
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change approve: encode request: %w", err)
	}
	url := strings.TrimSuffix(baseURL, "/") + "/api/changes/" + changeID + "/approve"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change approve: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change approve: contact %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		var ce clierr.Error
		if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
			return checks.MergeRequirements{}, fmt.Errorf("change approve: decode error response: %w", err)
		}
		return checks.MergeRequirements{}, &ce
	}
	if resp.StatusCode == http.StatusNotFound {
		return checks.MergeRequirements{}, &clierr.Error{
			Code:       "not_found",
			Field:      "change",
			Message:    fmt.Sprintf("no such change %q", changeID),
			Suggestion: "check the Change-Id, e.g. from `runko change push`'s output",
			DocURL:     "docs/design.md#74-change",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return checks.MergeRequirements{}, fmt.Errorf("change approve: %s returned %d", baseURL, resp.StatusCode)
	}

	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change approve: decode response: %w", err)
	}
	return reqs, nil
}
