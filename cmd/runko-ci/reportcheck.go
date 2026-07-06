package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CheckRunReport mirrors docs/spec/webhooks/checkrun.schema.json#/$defs/CheckRunCreateRequest -
// the POST /changes/{id}/checks body.
type CheckRunReport struct {
	Name       string `json:"name"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	DetailsURL string `json:"details_url,omitempty"`
	Reporter   string `json:"reporter"`
}

// ReportCheck implements `runko-ci report-check` (§14.6): posts a CheckRun
// result to the platform's Checks API, bearer-token authenticated (§14.11's
// "deploy tokens" pattern - full CI OIDC federation is out of scope for this
// CLI-wiring session).
func ReportCheck(ctx context.Context, client *http.Client, checksURL, token string, report CheckRunReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal check run: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, checksURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post check run: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("checks endpoint returned %d", resp.StatusCode)
	}
	return nil
}
