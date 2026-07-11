// Sweep logic and the two thin HTTP clients (runkod REST + GitHub Actions
// API). Kept apart from main.go's flag/loop plumbing so tests drive Sweep
// directly against httptest stubs, the runko-bridge testing pattern.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

// Watchdog reconciles Runko's view of required checks with what their CI
// runs actually did. Two remedies, both bounded:
//
//   - A pending check whose details_url names a run that already COMPLETED
//     gets the run's real conclusion force-reported (reporter
//     "ci-watchdog") - the report the runner should have posted and didn't.
//   - A required check with NO details_url at all past the grace window
//     (the dispatch never reached CI, or the runner died before its first
//     report-check) gets exactly ONE rescue rerun per (change, head, name),
//     which re-fires the §14.4.1 rerun webhook through the bridge. Never a
//     second: an infrastructure that eats every dispatch must page a human,
//     not receive a dispatch storm.
//
// The grace window is measured from when THIS process first saw the check
// pending (merge requirements carry no timestamps); a restart resets the
// clock, which only delays rescue - fail slow, never fail loud twice.
type Watchdog struct {
	Runkod *RunkodClient
	GitHub *GitHubClient
	// Grace is how long a pending check may sit before the watchdog acts.
	Grace time.Duration
	// EnableRerun turns the never-reported rescue on (the completed-run
	// reconciliation is always on - it only states facts CI already knows).
	EnableRerun bool
	// Now is injectable for tests; nil means time.Now.
	Now func() time.Time

	firstSeen map[string]time.Time // change|head|check -> first observed pending
	rescued   map[string]bool      // keys already given their one rescue rerun
}

// SweepResult reports what one pass did, for logs and tests.
type SweepResult struct {
	Reported []string // "change/check=conclusion" force-reported from a completed run
	Rescued  []string // "change/check" given their one rescue rerun
	Errors   []string // per-item failures; the sweep continues past each
}

func (w *Watchdog) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Sweep runs one reconciliation pass over every open Change.
func (w *Watchdog) Sweep(ctx context.Context) SweepResult {
	var res SweepResult
	if w.firstSeen == nil {
		w.firstSeen = map[string]time.Time{}
	}
	if w.rescued == nil {
		w.rescued = map[string]bool{}
	}

	changes, err := w.Runkod.ListOpenChanges(ctx)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("list open changes: %v", err))
		return res
	}

	// Rebuild the bookkeeping maps from what is STILL pending, carrying
	// timestamps over - landed/abandoned changes and reported checks drop
	// out, so the maps never grow with history.
	liveFirstSeen := map[string]time.Time{}
	liveRescued := map[string]bool{}
	now := w.now()

	for _, c := range changes {
		reqs, err := w.Runkod.MergeRequirements(ctx, c.ChangeKey)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: merge requirements: %v", c.ChangeKey, err))
			continue
		}
		for _, name := range reqs.PendingChecks {
			key := c.ChangeKey + "|" + c.HeadSHA + "|" + name
			first, seen := w.firstSeen[key]
			if !seen {
				first = now
			}
			liveFirstSeen[key] = first
			if w.rescued[key] {
				liveRescued[key] = true
			}
			if now.Sub(first) < w.Grace {
				continue
			}

			url := reqs.CheckDetailsURLs[name]
			if url == "" {
				// Never reported: one rescue rerun, then hands off.
				if !w.EnableRerun || liveRescued[key] {
					continue
				}
				if err := w.Runkod.RerunCheck(ctx, c.ChangeKey, name); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: rerun %s: %v", c.ChangeKey, name, err))
					continue
				}
				liveRescued[key] = true
				res.Rescued = append(res.Rescued, c.ChangeKey+"/"+name)
				continue
			}

			runID, ok := w.GitHub.ParseRunURL(url)
			if !ok {
				// A link we can't vouch for (foreign repo, non-GHA CI) is
				// none of our business - the staleness blocker still names
				// the check for humans.
				continue
			}
			status, conclusion, err := w.GitHub.GetRun(ctx, runID)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: fetch run %s for %s: %v", c.ChangeKey, runID, name, err))
				continue
			}
			if status != "completed" {
				continue // CI is genuinely still running
			}
			mapped := mapConclusion(conclusion)
			if err := w.Runkod.ReportCheck(ctx, c.ChangeKey, name, runID, mapped, url); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: report %s=%s: %v", c.ChangeKey, name, mapped, err))
				continue
			}
			res.Reported = append(res.Reported, fmt.Sprintf("%s/%s=%s", c.ChangeKey, name, mapped))
		}
	}

	w.firstSeen = liveFirstSeen
	w.rescued = liveRescued
	return res
}

// mapConclusion maps a GitHub Actions run conclusion onto runko's
// CheckConclusion enum. Values both sides share pass through; anything
// unrecognized (startup_failure, stale, ...) fails closed to failure -
// a rescue that reported "success" for a state it doesn't understand
// would silently green a gate.
func mapConclusion(gha string) checks.CheckConclusion {
	switch c := checks.CheckConclusion(gha); c {
	case checks.ConclusionSuccess, checks.ConclusionFailure, checks.ConclusionCancelled,
		checks.ConclusionSkipped, checks.ConclusionTimedOut, checks.ConclusionActionRequired,
		checks.ConclusionNeutral:
		return c
	default:
		return checks.ConclusionFailure
	}
}

// ---------------------------------------------------------------- runkod

// RunkodClient is the minimal REST surface the watchdog needs, against one
// org's base URL (e.g. https://host/o/runko). Auth is the deploy token.
type RunkodClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

type openChange struct {
	// The REST /api/changes listing serializes runkod.Change's Go field
	// names verbatim (no json tags on that struct, by design).
	ChangeKey string
	HeadSHA   string
}

func (r *RunkodClient) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	// A token of the form name:secret is a §15.1 principal credential and
	// rides Basic, exactly like the CLI; a bare token is the deploy token.
	if name, secret, ok := strings.Cut(r.Token, ":"); ok {
		req.SetBasicAuth(name, secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, truncate(data, 200))
	}
	return data, nil
}

func (r *RunkodClient) ListOpenChanges(ctx context.Context) ([]openChange, error) {
	data, err := r.do(ctx, http.MethodGet, "/api/changes?state=open", nil)
	if err != nil {
		return nil, err
	}
	var out []openChange
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode changes: %w", err)
	}
	return out, nil
}

func (r *RunkodClient) MergeRequirements(ctx context.Context, changeKey string) (checks.MergeRequirements, error) {
	data, err := r.do(ctx, http.MethodGet, "/api/changes/"+changeKey+"/merge-requirements", nil)
	if err != nil {
		return checks.MergeRequirements{}, err
	}
	var reqs checks.MergeRequirements
	if err := json.Unmarshal(data, &reqs); err != nil {
		return checks.MergeRequirements{}, fmt.Errorf("decode merge requirements: %w", err)
	}
	return reqs, nil
}

// ReportCheck posts the completed conclusion the runner should have - the
// same body `runko-ci report-check` sends, attributed to "ci-watchdog" so
// the audit trail shows the reconciler acted, not CI.
func (r *RunkodClient) ReportCheck(ctx context.Context, changeKey, name, runID string, conclusion checks.CheckConclusion, detailsURL string) error {
	body, err := json.Marshal(map[string]string{
		"name":        name,
		"external_id": runID,
		"status":      "completed",
		"conclusion":  string(conclusion),
		"details_url": detailsURL,
		"reporter":    "ci-watchdog",
	})
	if err != nil {
		return err
	}
	_, err = r.do(ctx, http.MethodPost, "/api/changes/"+changeKey+"/checks", body)
	return err
}

func (r *RunkodClient) RerunCheck(ctx context.Context, changeKey, name string) error {
	_, err := r.do(ctx, http.MethodPost, "/api/changes/"+changeKey+"/checks/"+name+"/rerun", []byte("{}"))
	return err
}

// ---------------------------------------------------------------- github

// GitHubClient fetches Actions run status. Repo is an ALLOWLIST: only
// details_urls pointing into exactly this owner/name are followed - a
// reporter-controlled URL must never steer the watchdog's requests
// anywhere else. Token may be empty for public repos (rate limits apply).
type GitHubClient struct {
	APIBase string // https://api.github.com, or a test stub
	Repo    string // owner/name
	Token   string
	Client  *http.Client
}

var runURLPattern = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/actions/runs/(\d+)`)

// ParseRunURL extracts the run id from a GHA run URL, refusing URLs whose
// repo is not the configured allowlist entry.
func (g *GitHubClient) ParseRunURL(url string) (string, bool) {
	m := runURLPattern.FindStringSubmatch(url)
	if m == nil || m[1] != g.Repo {
		return "", false
	}
	return m[2], true
}

func (g *GitHubClient) GetRun(ctx context.Context, runID string) (status, conclusion string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.APIBase+"/repos/"+g.Repo+"/actions/runs/"+runID, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GET run %s: %s: %s", runID, resp.Status, truncate(data, 200))
	}
	var run struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(data, &run); err != nil {
		return "", "", fmt.Errorf("decode run %s: %w", runID, err)
	}
	return run.Status, run.Conclusion, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
