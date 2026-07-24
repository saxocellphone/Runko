package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/checks"
)

// AckPolicy implements `runko change ack-policy` (the 2026-07-24
// enforcement split's "extra button"): POST the daemon's .../ack-policy
// endpoint to complete the reserved agent-policy check - the human
// acknowledgement of an agent change's policy findings (denylisted paths,
// size-cap overruns, owners edits) - and decode the refreshed merge
// requirements. Approve-rights humans only; agents are refused
// server-side. Needs a live runkod instance.
func AckPolicy(ctx context.Context, client *http.Client, baseURL, authHeader, changeID, ackedBy string) (checks.MergeRequirements, error) {
	body, err := json.Marshal(map[string]string{"acked_by": ackedBy})
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change ack-policy: encode request: %w", err)
	}
	url := strings.TrimSuffix(baseURL, "/") + "/api/changes/" + changeID + "/ack-policy"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change ack-policy: build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change ack-policy: contact %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return checks.MergeRequirements{}, &clierr.Error{
			Code:       "not_found",
			Field:      "change",
			Message:    fmt.Sprintf("no such change %q", changeID),
			Suggestion: "check the Change-Id, e.g. from `runko change push`'s output",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return checks.MergeRequirements{}, decodeAPIError(resp, "change ack-policy")
	}

	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("change ack-policy: decode response: %w", err)
	}
	return reqs, nil
}
