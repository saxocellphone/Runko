package main

import (
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
	return postReport(ctx, client, checksURL, token, payload, reportCheckAttempts, reportCheckBackoff, "report-check")
}
