package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/land"
)

// LandChange implements `runko change land` (§13.5, §28.3 stage 11b): POST
// the daemon's .../land endpoint (runkod/api.go's handleLandChange) and
// decode its success response into land.Outcome. The daemon actually
// marshals a smaller landResponse{Landed,LandedSHA} on 200 - decoding that
// into the larger land.Outcome works fine (the extra fields just stay
// zero-valued) and saves this CLI from defining a third copy of the same
// two fields, matching docs/cli-contract.md's convention of reusing a Go
// struct's own exported field names as the wire shape rather than hand-
// duplicating one.
//
// Unlike `change push` and `project create` (which operate on the local
// repo with no server involved), this genuinely needs a live control plane
// - the one this session's runkod IS, unlike auth/workspace/mcp which still
// have none to talk to.
func LandChange(ctx context.Context, client *http.Client, baseURL, token, changeID string, force bool) (land.Outcome, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/api/changes/" + changeID + "/land"
	var reqBody io.Reader
	if force {
		// The §13.5 admin override: bypasses owner/check gates and the
		// revalidation rule server-side, audited as landed_forced. The
		// daemon authorizes it (admin principals + the deploy token only).
		reqBody = strings.NewReader(`{"force": true}`)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return land.Outcome{}, fmt.Errorf("change land: build request: %w", err)
	}
	req.Header.Set("Authorization", authHeaderValue(token))
	if force {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return land.Outcome{}, fmt.Errorf("change land: contact %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return land.Outcome{}, &clierr.Error{
			Code:       "not_found",
			Field:      "change",
			Message:    fmt.Sprintf("no such change %q", changeID),
			Suggestion: "check the Change-Id, e.g. from `runko change push`'s output",
			DocURL:     "docs/design.md#74-change",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return land.Outcome{}, decodeAPIError(resp, "change land")
	}

	var outcome land.Outcome
	if err := json.NewDecoder(resp.Body).Decode(&outcome); err != nil {
		return land.Outcome{}, fmt.Errorf("change land: decode response: %w", err)
	}
	return outcome, nil
}

// LandWithSync drives the full §13.5 recovery loop client-side (the sync
// feature, 2026-07-10): land; on requires_revalidation, sync the local
// line onto trunk (SyncToTrunk - jj-aware), re-push, wait for the gates
// to re-green, and try again. Bounded by attempts AND a wall-clock
// deadline; every step prints progress so a human or agent watching the
// terminal knows the loop is alive. parent_change_not_landed is
// deliberately NOT retried - a parent that must land first is not
// fixable by rebasing this change.
func LandWithSync(ctx context.Context, client *http.Client, cred Credential, changeID, dir, remote, trunk string, timeout time.Duration, progress io.Writer) (land.Outcome, error) {
	deadline := time.Now().Add(timeout)
	var outcome land.Outcome
	var err error
	for attempt := 1; attempt <= 4; attempt++ {
		outcome, err = LandChange(ctx, client, cred.URL, cred.AuthHeader(), changeID, false)
		if err == nil && outcome.Landed {
			return outcome, nil
		}
		var ce *clierr.Error
		revalidate := outcome.RequiresRevalidation
		if !revalidate && asClierr(err, &ce) && ce.Code == "requires_revalidation" {
			revalidate = true
		}
		if !revalidate || time.Now().After(deadline) {
			return outcome, err
		}

		fmt.Fprintf(progress, "runko: trunk moved - syncing and re-pushing (attempt %d)\n", attempt)
		if _, syncErr := SyncToTrunk(dir, remote, trunk); syncErr != nil {
			return outcome, syncErr
		}
		if _, pushErr := pushChange(dir, remote, trunk, false); pushErr != nil {
			return outcome, pushErr
		}
		fmt.Fprintln(progress, "runko: waiting for required checks against the new base")
		for {
			if time.Now().After(deadline) {
				return outcome, &clierr.Error{
					Code: "sync_timeout", Field: "change",
					Message:    fmt.Sprintf("gates did not re-green within %s after syncing", timeout),
					Suggestion: "check CI, then retry: runko change land --change " + changeID,
				}
			}
			reqs, reqErr := ChangeRequirements(ctx, client, cred, changeID)
			if reqErr == nil {
				if len(reqs.FailingChecks) > 0 {
					return outcome, &clierr.Error{
						Code: "checks_failed", Field: "change",
						Message:    fmt.Sprintf("required checks failed after the sync: %s", strings.Join(reqs.FailingChecks, ", ")),
						Suggestion: "fix and push again, or rerun flaky checks: runko change rerun-check --change " + changeID + " --name <check>",
					}
				}
				if reqs.Mergeable {
					break
				}
			}
			time.Sleep(10 * time.Second)
		}
	}
	return outcome, err
}
