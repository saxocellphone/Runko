package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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

// reportCheckAttempts and reportCheckBackoff bound the retry loop below.
// Exposed as vars for the tests' clock.
var (
	reportCheckAttempts = 5
	reportCheckBackoff  = 2 * time.Second
)

// ReportCheck implements `runko-ci report-check` (§14.6): posts a CheckRun
// result to the platform's Checks API, bearer-token authenticated (§14.11's
// "deploy tokens" pattern - full CI OIDC federation is out of scope for this
// CLI-wiring session).
//
// Transient failures (connection errors, 5xx, 429) are retried with
// exponential backoff (migration-findings #33): the daemon deploys as a
// single-replica Recreate pod, so EVERY deploy is a brief 503 window, and a
// report that dies in it leaves the check "has not reported yet" forever -
// invisible to the gate, unrecoverable without a rerun. The POST is an
// upsert keyed (change, head, name), so retrying is safe. 4xx (other than
// 429) stays fatal: a malformed report will not become well-formed by
// retrying.
func ReportCheck(ctx context.Context, client *http.Client, checksURL, token string, report CheckRunReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal check run: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < reportCheckAttempts; attempt++ {
		if attempt > 0 {
			delay := reportCheckBackoff << (attempt - 1)
			fmt.Printf("runko-ci: report-check attempt %d/%d failed (%v); retrying in %s\n", attempt, reportCheckAttempts, lastErr, delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, checksURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("post check run: %w", err)
			continue
		}
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("checks endpoint returned %d", resp.StatusCode)
			continue
		default:
			return fmt.Errorf("checks endpoint returned %d", resp.StatusCode)
		}
	}
	return lastErr
}
