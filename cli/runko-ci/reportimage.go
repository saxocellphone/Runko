package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ImageReport mirrors docs/spec/webhooks/image-report.schema.json - the
// POST /api/deploys/{sha}/images body. The post-land image-build workflow
// reports one of these per image it built (the full pushed reference plus
// its digest); runkod records them against the landed commit and drives the
// GitOps rollout once every affected image has reported. This is the
// inverted CD trigger: GitHub only builds and reports, Runko rolls.
type ImageReport struct {
	// Image is the logical image name (runkod|web|webadmin), the kustomize
	// images: key runkod pins the digest under.
	Image string `json:"image"`
	// ImageRef is the full pushed reference sans digest
	// (e.g. ghcr.io/saxocellphone/runko/runkod) - reported so the deployer
	// is registry-agnostic and nothing hardcodes ghcr.io.
	ImageRef string `json:"image_ref,omitempty"`
	Digest   string `json:"digest"`
	// RunURL deep-links the CI run that built the image (provenance).
	RunURL   string `json:"run_url,omitempty"`
	Reporter string `json:"reporter,omitempty"`
}

// reportImageAttempts and reportImageBackoff bound the retry loop.
// Exposed as vars for the tests' clock, matching report-check.
var (
	reportImageAttempts = 5
	reportImageBackoff  = 2 * time.Second
)

// ReportImage implements `runko-ci report-image`: posts a built image's
// digest to the platform's deploy API, bearer-token authenticated (the same
// deploy-token pattern report-check uses). The POST is an idempotent upsert
// keyed (landed sha, image), so the single-replica 503-window retry that
// report-check documents (migration-findings #33) applies unchanged.
func ReportImage(ctx context.Context, client *http.Client, url, token string, report ImageReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal image report: %w", err)
	}
	return postReport(ctx, client, url, token, payload, reportImageAttempts, reportImageBackoff, "report-image")
}

// postReport is the shared report-check/report-image POST-with-retry:
// transient failures (connection errors, 5xx, 429) retried with exponential
// backoff, other 4xx fatal (a malformed report will not become well-formed
// by retrying). Both endpoints are idempotent upserts, so retrying is safe.
func postReport(ctx context.Context, client *http.Client, url, token string, payload []byte, attempts int, backoff time.Duration, label string) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := backoff << (attempt - 1)
			fmt.Printf("runko-ci: %s attempt %d/%d failed (%v); retrying in %s\n", label, attempt, attempts, lastErr, delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("post %s: %w", label, err)
			continue
		}
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("%s endpoint returned %d", label, resp.StatusCode)
			continue
		default:
			return fmt.Errorf("%s endpoint returned %d", label, resp.StatusCode)
		}
	}
	return lastErr
}
