package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

// fakeRunkod is a scripted runkod REST surface: one open change with a
// configurable merge-requirements answer, recording every report/rerun.
type fakeRunkod struct {
	mu       sync.Mutex
	change   string
	head     string
	reqs     checks.MergeRequirements
	reports  []map[string]string
	reruns   []string
	lastAuth string
}

func (f *fakeRunkod) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/changes", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		fmt.Fprintf(w, `[{"ChangeKey":%q,"HeadSHA":%q}]`, f.change, f.head)
	})
	mux.HandleFunc("GET /api/changes/{key}/merge-requirements", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.reqs)
	})
	mux.HandleFunc("POST /api/changes/{key}/checks", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reports = append(f.reports, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /api/changes/{key}/checks/{name}/rerun", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reruns = append(f.reruns, r.PathValue("name"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ghaJob scripts one job of the discovery run.
type ghaJob struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	HTMLURL    string
}

// fakeGHA answers GET run-by-id with a scripted status/conclusion, plus the
// discovery pair (runs list + run jobs) with one scripted run; counts hits.
type fakeGHA struct {
	mu         sync.Mutex
	status     string
	conclusion string
	hits       int
	// discovery script: runTitle "" serves an empty runs list.
	runTitle string
	runID    int64
	jobs     []ghaJob
	listHits int
}

func (f *fakeGHA) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.listHits++
		if f.runTitle == "" {
			fmt.Fprint(w, `{"workflow_runs":[]}`)
			return
		}
		fmt.Fprintf(w, `{"workflow_runs":[{"id":%d,"display_title":%q}]}`, f.runID, f.runTitle)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{id}/jobs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		type j struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		}
		out := struct {
			Jobs []j `json:"jobs"`
		}{}
		for _, job := range f.jobs {
			out.Jobs = append(out.Jobs, j(job))
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		fmt.Fprintf(w, `{"status":%q,"conclusion":%q}`, f.status, f.conclusion)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newWatchdog(runkod *httptest.Server, gha *httptest.Server, grace time.Duration) *Watchdog {
	w := &Watchdog{
		Runkod: &RunkodClient{BaseURL: runkod.URL, Token: "tok", Client: runkod.Client()},
		GitHub: &GitHubClient{
			APIBase: "https://api.github.invalid", // tests that need it override
			Repo:    "acme/mono",
			Client:  http.DefaultClient,
		},
		Grace:       grace,
		EnableRerun: true,
	}
	if gha != nil {
		w.GitHub.APIBase = gha.URL
		w.GitHub.Client = gha.Client()
	}
	return w
}

func pendingReqs(name, detailsURL string) checks.MergeRequirements {
	r := checks.MergeRequirements{
		RequiredChecks: []string{name},
		PendingChecks:  []string{name},
	}
	if detailsURL != "" {
		r.CheckDetailsURLs = map[string]string{name: detailsURL}
	}
	return r
}

// TestSweepForceReportsCompletedRun is the observed incident ("CI failed
// on GitHub but Runko never picked it up"): the GHA run for a pending
// check already concluded - the watchdog reports the run's REAL
// conclusion, attributed to ci-watchdog, with the run link preserved.
func TestSweepForceReportsCompletedRun(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1",
		reqs: pendingReqs("platform-check", "https://github.com/acme/mono/actions/runs/777")}
	gha := &fakeGHA{status: "completed", conclusion: "failure"}
	w := newWatchdog(rk.server(t), gha.server(t), 0)

	res := w.Sweep(context.Background())
	if len(res.Errors) != 0 {
		t.Fatalf("sweep errors: %v", res.Errors)
	}
	if len(rk.reports) != 1 {
		t.Fatalf("want exactly one force-report, got %+v", rk.reports)
	}
	got := rk.reports[0]
	if got["name"] != "platform-check" || got["status"] != "completed" ||
		got["conclusion"] != "failure" || got["reporter"] != "ci-watchdog" ||
		got["external_id"] != "777" ||
		got["details_url"] != "https://github.com/acme/mono/actions/runs/777" {
		t.Fatalf("force-report body: %+v", got)
	}
	if len(rk.reruns) != 0 {
		t.Fatalf("a completed run must be reported, never rerun, got %v", rk.reruns)
	}
	if rk.lastAuth != "Bearer tok" {
		t.Fatalf("expected the deploy token on runkod calls, got %q", rk.lastAuth)
	}
}

// TestPrincipalCredentialRidesBasic: a name:secret token is a §15.1
// principal and must authenticate the way every other client does.
func TestPrincipalCredentialRidesBasic(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1", reqs: checks.MergeRequirements{}}
	srv := rk.server(t)
	w := newWatchdog(srv, nil, 0)
	w.Runkod.Token = "watchdog:s3cret"
	_ = w.Sweep(context.Background())
	if rk.lastAuth != "Basic "+base64.StdEncoding.EncodeToString([]byte("watchdog:s3cret")) {
		t.Fatalf("expected Basic auth for a name:secret token, got %q", rk.lastAuth)
	}
}

// TestSweepLeavesRunningRunsAlone: a run still in_progress is CI doing its
// job - the watchdog must not touch it, however long it takes.
func TestSweepLeavesRunningRunsAlone(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1",
		reqs: pendingReqs("platform-check", "https://github.com/acme/mono/actions/runs/777")}
	gha := &fakeGHA{status: "in_progress"}
	w := newWatchdog(rk.server(t), gha.server(t), 0)

	if res := w.Sweep(context.Background()); len(res.Reported)+len(res.Rescued) != 0 || len(res.Errors) != 0 {
		t.Fatalf("expected a no-op sweep, got %+v", res)
	}
	if len(rk.reports) != 0 || len(rk.reruns) != 0 {
		t.Fatalf("in-progress run must be left alone: reports=%v reruns=%v", rk.reports, rk.reruns)
	}
}

// TestSweepRescuesNeverReportedCheckExactlyOnce: no details_url means the
// dispatch never produced a report - one rescue rerun fires, and repeat
// sweeps never fire a second for the same (change, head, check).
func TestSweepRescuesNeverReportedCheckExactlyOnce(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1", reqs: pendingReqs("web-check", "")}
	w := newWatchdog(rk.server(t), (&fakeGHA{}).server(t), 0) // empty runs list: discovery finds nothing

	for i := 0; i < 3; i++ {
		if res := w.Sweep(context.Background()); len(res.Errors) != 0 {
			t.Fatalf("sweep %d errors: %v", i, res.Errors)
		}
	}
	if len(rk.reruns) != 1 || rk.reruns[0] != "web-check" {
		t.Fatalf("want exactly ONE rescue rerun across sweeps, got %v", rk.reruns)
	}

	// An amend moves the head: a fresh (change, head, check) key gets its
	// own rescue - the old head's spent rescue must not shadow it.
	rk.mu.Lock()
	rk.head = "h2"
	rk.mu.Unlock()
	_ = w.Sweep(context.Background())
	if len(rk.reruns) != 2 {
		t.Fatalf("a new head deserves a fresh rescue, got %v", rk.reruns)
	}
}

// TestSweepHonorsGraceWindow: nothing happens before the grace elapses -
// the watchdog is a backstop, not a race against a healthy CI.
func TestSweepHonorsGraceWindow(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1", reqs: pendingReqs("web-check", "")}
	w := newWatchdog(rk.server(t), (&fakeGHA{}).server(t), 10*time.Minute)
	base := time.Now()
	w.Now = func() time.Time { return base }

	_ = w.Sweep(context.Background())
	if len(rk.reruns) != 0 {
		t.Fatalf("acted inside the grace window: %v", rk.reruns)
	}
	w.Now = func() time.Time { return base.Add(11 * time.Minute) }
	_ = w.Sweep(context.Background())
	if len(rk.reruns) != 1 {
		t.Fatalf("want the rescue after grace elapsed, got %v", rk.reruns)
	}
}

// TestSweepBackfillsRunningJobURL is the user report ("I can't click on
// running ci checks"): a pending check with no details_url gets its run
// DISCOVERED via the run-name stamp and its running job reported as
// in_progress with the job's URL - immediately, no grace, no conclusion,
// no rerun. The link is what makes the running check clickable.
func TestSweepBackfillsRunningJobURL(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1", reqs: pendingReqs("platform-test", "")}
	gha := &fakeGHA{
		runTitle: "checks: Iabc@h1", runID: 900,
		jobs: []ghaJob{{ID: 7, Name: "platform-test", Status: "in_progress",
			HTMLURL: "https://github.com/acme/mono/actions/runs/900/job/7"}},
	}
	w := newWatchdog(rk.server(t), gha.server(t), 10*time.Minute) // grace must NOT gate backfill

	res := w.Sweep(context.Background())
	if len(res.Errors) != 0 || len(res.Backfilled) != 1 {
		t.Fatalf("want one backfill, got %+v", res)
	}
	if len(rk.reports) != 1 {
		t.Fatalf("want exactly one report, got %+v", rk.reports)
	}
	got := rk.reports[0]
	if got["name"] != "platform-test" || got["status"] != "in_progress" ||
		got["details_url"] != "https://github.com/acme/mono/actions/runs/900/job/7" ||
		got["external_id"] != "900-7" || got["reporter"] != "ci-watchdog" {
		t.Fatalf("backfill body: %+v", got)
	}
	if _, hasConclusion := got["conclusion"]; hasConclusion {
		t.Fatalf("an in_progress backfill must carry no conclusion: %+v", got)
	}
	if len(rk.reruns) != 0 {
		t.Fatalf("a discovered running job must never be rerun, got %v", rk.reruns)
	}
}

// TestSweepReportsDiscoveredCompletedJobInsteadOfRerun: discovery finding a
// FINISHED job states its real conclusion - strictly better than the old
// behavior of burning a rescue rerun on work CI already did.
func TestSweepReportsDiscoveredCompletedJobInsteadOfRerun(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1", reqs: pendingReqs("platform-test", "")}
	gha := &fakeGHA{
		runTitle: "checks: Iabc@h1", runID: 901,
		jobs: []ghaJob{{ID: 3, Name: "platform-test", Status: "completed", Conclusion: "failure",
			HTMLURL: "https://github.com/acme/mono/actions/runs/901/job/3"}},
	}
	w := newWatchdog(rk.server(t), gha.server(t), 0)

	res := w.Sweep(context.Background())
	if len(res.Errors) != 0 || len(res.Reported) != 1 {
		t.Fatalf("want one discovered force-report, got %+v", res)
	}
	got := rk.reports[0]
	if got["status"] != "completed" || got["conclusion"] != "failure" || got["external_id"] != "901-3" {
		t.Fatalf("discovered-completed body: %+v", got)
	}
	if len(rk.reruns) != 0 {
		t.Fatalf("a discovered completed job must be reported, never rerun, got %v", rk.reruns)
	}
}

// TestSweepDiscoveryMissFallsBackToRescue: a runs list with no matching
// stamp (foreign change, pre-stamp workflow) leaves the rescue rerun as
// the last resort - and per-job mismatches (run found, no same-named job)
// behave identically.
func TestSweepDiscoveryMissFallsBackToRescue(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1", reqs: pendingReqs("web-check", "")}
	gha := &fakeGHA{runTitle: "checks: Iother@h9", runID: 800,
		jobs: []ghaJob{{ID: 1, Name: "web-check", Status: "in_progress", HTMLURL: "x"}}}
	w := newWatchdog(rk.server(t), gha.server(t), 0)

	res := w.Sweep(context.Background())
	if len(res.Errors) != 0 {
		t.Fatalf("sweep errors: %v", res.Errors)
	}
	if gha.listHits == 0 {
		t.Fatalf("discovery must have been attempted")
	}
	if len(rk.reports) != 0 || len(rk.reruns) != 1 {
		t.Fatalf("miss must fall back to exactly one rescue: reports=%v reruns=%v", rk.reports, rk.reruns)
	}
}

// TestSweepRefusesForeignRepoLinks: details_url is reporter-controlled
// input - a link into any repo but the allowlisted one is not followed.
func TestSweepRefusesForeignRepoLinks(t *testing.T) {
	rk := &fakeRunkod{change: "Iabc", head: "h1",
		reqs: pendingReqs("platform-check", "https://github.com/evil/other/actions/runs/1")}
	gha := &fakeGHA{status: "completed", conclusion: "success"}
	w := newWatchdog(rk.server(t), gha.server(t), 0)

	if res := w.Sweep(context.Background()); len(res.Reported) != 0 || len(res.Errors) != 0 {
		t.Fatalf("foreign link must be a silent skip, got %+v", res)
	}
	if gha.hits != 0 {
		t.Fatalf("the GHA API must never be asked about a foreign repo's run")
	}
	if len(rk.reports) != 0 {
		t.Fatalf("no report for a link we can't vouch for, got %v", rk.reports)
	}
}

func TestMapConclusionFailsClosed(t *testing.T) {
	cases := map[string]checks.CheckConclusion{
		"success":         checks.ConclusionSuccess,
		"failure":         checks.ConclusionFailure,
		"cancelled":       checks.ConclusionCancelled,
		"timed_out":       checks.ConclusionTimedOut,
		"startup_failure": checks.ConclusionFailure, // unknown to runko -> fail closed
		"stale":           checks.ConclusionFailure,
		"":                checks.ConclusionFailure,
	}
	for gha, want := range cases {
		if got := mapConclusion(gha); got != want {
			t.Fatalf("mapConclusion(%q) = %q, want %q", gha, got, want)
		}
	}
}

// TestParseRunURL pins the allowlist and id extraction.
func TestParseRunURL(t *testing.T) {
	g := &GitHubClient{Repo: "acme/mono"}
	if id, ok := g.ParseRunURL("https://github.com/acme/mono/actions/runs/29127955511"); !ok || id != "29127955511" {
		t.Fatalf("want id extracted, got %q %v", id, ok)
	}
	for _, bad := range []string{
		"https://github.com/acme/other/actions/runs/1",
		"https://github.com/acme/mono/pull/5",
		"https://ci.example.com/runs/manifest-lint",
		"",
	} {
		if _, ok := g.ParseRunURL(bad); ok {
			t.Fatalf("ParseRunURL(%q) must refuse", bad)
		}
	}
}
