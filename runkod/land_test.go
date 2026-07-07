package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
)

// newLandTestServer seeds a bare repo with one project and one open Change
// (pushed for real via Processor.Process, exactly like newTestServer in
// api_test.go), and reports zero checks - so the Change starts out
// trivially mergeable (checks/requirements.go: an empty requiredCheckNames
// list blocks on nothing) and every test below can focus on land.Land's own
// wiring, not on merge-requirements gating mechanics already covered by
// TestAPIPostCheckAndMergeRequirementsRoundTrip.
func newLandTestServer(t *testing.T) (srv *httptest.Server, bare string, changeID string, store Store) {
	t.Helper()
	bare = newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), bare, result.ChangeID, store
}

func postLand(t *testing.T, srv *httptest.Server, changeID, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/land", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST land: %v", err)
	}
	return resp
}

func TestHandleLandChangeRequiresAuth(t *testing.T) {
	srv, _, changeID, _ := newLandTestServer(t)
	defer srv.Close()

	resp := postLand(t, srv, changeID, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a token, got %d", resp.StatusCode)
	}
}

func TestHandleLandChangeNotFound(t *testing.T) {
	srv, _, _, _ := newLandTestServer(t)
	defer srv.Close()

	resp := postLand(t, srv, "no-such-change", "sekret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestHandleLandChangeFastForwards is the DAG stage 11b bar: trunk hasn't
// moved since the Change's base, so land.Land fast-forwards trunk straight
// to the Change's head - no rebase needed.
func TestHandleLandChangeFastForwards(t *testing.T) {
	srv, bare, changeID, store := newLandTestServer(t)
	defer srv.Close()

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var got landResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Landed || got.LandedSHA == "" {
		t.Fatalf("expected a landed response, got %+v", got)
	}

	tip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("rev-parse refs/heads/main: %v", err)
	}
	if tip != got.LandedSHA {
		t.Fatalf("expected trunk to advance to %s, got %s", got.LandedSHA, tip)
	}

	change, ok, err := store.GetChange(context.Background(), changeID)
	if err != nil || !ok {
		t.Fatalf("GetChange after land: ok=%v err=%v", ok, err)
	}
	if change.State != "landed" || change.LandedSHA != got.LandedSHA {
		t.Fatalf("expected Change marked landed with the landed SHA recorded, got %+v", change)
	}

	// A second land request against an already-landed Change is idempotent,
	// not an error or a re-attempt.
	resp2 := postLand(t, srv, changeID, "sekret")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on a repeat land request, got %d", resp2.StatusCode)
	}
	var got2 landResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	if got2.LandedSHA != got.LandedSHA {
		t.Fatalf("expected the same landed SHA on a repeat request, got %+v vs %+v", got2, got)
	}
}

// TestHandleLandChangeEnqueuesWebhookAndTriggersReindex proves the two
// side-effects a successful land must have: a change.landed webhook queued
// (checked via the real MemStore, not by reaching into private state), and
// the daemon's ZoektIndexWorker triggered - the one place stage 11's
// zoekt.go documented as "correctly placed... but currently unreachable in
// practice" (runkod/zoekt.go's doc comment). This test proves it is now
// reachable.
func TestHandleLandChangeEnqueuesWebhookAndTriggersReindex(t *testing.T) {
	// Built directly (not via newLandTestServer) so the Processor can carry
	// a ZoektIndexWorker - newLandTestServer's helper Processor doesn't wire
	// one, matching how most other tests don't need to observe it.
	store := NewMemStore()
	indexer := &countingIndexer{done: make(chan struct{}, 1)}

	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	processor := &Processor{
		RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store,
		ZoektIndexWorker: &ZoektIndexWorker{Indexer: indexer, RepoDir: bare, Debounce: time.Millisecond},
	}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv2 := httptest.NewServer(handler)
	defer srv2.Close()

	resp := postLand(t, srv2, result.ChangeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	select {
	case <-indexer.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected land to trigger a Zoekt reindex")
	}

	deliveries, err := store.ListDueWebhookDeliveries(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	found := false
	for _, d := range deliveries {
		if d.EventType == "change.landed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a change.landed webhook to be enqueued, got %+v", deliveries)
	}
}

// TestHandleLandChangeBootstrapsUnbornTrunk is a regression test for a real
// bug found by TestEndToEndDaemon (cmd/runkod): a brand-new daemon's bare
// repo has never had a commit on trunk (direct pushes are always rejected,
// §6.9), so the very first Change ever landed here must bootstrap trunk
// from nothing. attemptLand used to call index.Scan against
// refs/heads/main unconditionally, which fails outright when that ref
// doesn't exist yet - fixed by skipping the project scan (no projects
// exist without a first commit) rather than erroring.
func TestHandleLandChangeBootstrapsUnbornTrunk(t *testing.T) {
	bare := newBareRepo(t) // trunk never seeded - no refs/heads/main at all
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("first change ever\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp := postLand(t, srv, result.ChangeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 (trunk bootstrap), got %d: %s", resp.StatusCode, body)
	}
	var got landResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if !got.Landed || got.LandedSHA != headSHA {
		t.Fatalf("expected the first-ever land to bootstrap trunk to %s, got %+v", headSHA, got)
	}

	tip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil || tip != headSHA {
		t.Fatalf("expected refs/heads/main == %s, got %s (err %v)", headSHA, tip, err)
	}
}

// TestHandleLandChangeNotMergeableIsRejected proves the merge-requirements
// gate: a pending required check must block landing entirely - trunk must
// not move, and the Change must not be marked landed.
// TestHandleLandChangeNotMergeableIsRejected uses its own fixture (a
// PROJECT.yaml declaring a required "unit" check via ci.checks, §14.9)
// rather than newLandTestServer's - required check names now come from
// what's DECLARED, not from whatever happens to be posted (see
// requiredCheckNames' doc comment in api.go), so a shared fixture with no
// ci.checks at all would make posting "unit" as queued a no-op here.
func TestHandleLandChangeNotMergeableIsRejected(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}
	changeID := result.ChangeID

	if err := store.UpsertCheckRun(context.Background(), changeID, headSHA, checks.CheckRunView{Name: "unit", Status: checks.CheckStatus("queued")}); err != nil {
		t.Fatalf("UpsertCheckRun: %v", err)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	beforeTip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("rev-parse before land attempt: %v", err)
	}

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ce.Code != "not_mergeable" {
		t.Fatalf("expected code not_mergeable, got %+v", ce)
	}

	afterTip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("rev-parse after land attempt: %v", err)
	}
	if beforeTip != afterTip {
		t.Fatalf("expected trunk NOT to move on a blocked land, got %s -> %s", beforeTip, afterTip)
	}
}

// TestHandleLandChangeWithNoRequiredChecksIsMergeable is the counterpart -
// a project with NO ci.checks declared (the common case, anti-Boq §6.2)
// must stay mergeable with zero posted checks. This is the exact scenario
// the review finding described: previously this was "mergeable" for the
// wrong reason (required := whatever was posted, and nothing was posted);
// now it is "mergeable" for the right reason (nothing is actually
// required).
func TestHandleLandChangeWithNoRequiredChecksIsMergeable(t *testing.T) {
	srv, _, changeID, _ := newLandTestServer(t)
	defer srv.Close()

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 (nothing required), got %d: %s", resp.StatusCode, body)
	}
}

// TestHandleLandChangeRebasesAroundNonIntersectingTrunkAdvance proves the
// rebase-and-land path (§13.5): trunk moved in an unrelated project after
// the Change's base, so land.Land must rebase (producing a NEW trunk SHA,
// distinct from the Change's own head) rather than fast-forwarding or
// requiring revalidation.
func TestHandleLandChangeRebasesAroundNonIntersectingTrunkAdvance(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.WriteFile("libs/billing/PROJECT.yaml", "schema: project/v1\nname: billing-lib\ntype: library\n")
	initialSHA := repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("touch checkout\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	// Simulate a second, already-landed Change touching an unrelated
	// project - a direct ref update on the bare repo, standing in for
	// "some other change already landed here" without needing a second
	// full land round-trip.
	repo.Run("checkout -q " + initialSHA)
	repo.WriteFile("libs/billing/lib.go", "package billing\n// unrelated change\n")
	trunkTip := repo.Commit("touch billing")
	if _, err := gitfixtureRunGit(repo.Dir, "push", "-f", bare, trunkTip+":refs/heads/main"); err != nil {
		t.Fatalf("advance trunk directly: %v", err)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp := postLand(t, srv, result.ChangeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 (rebase-and-land), got %d: %s", resp.StatusCode, body)
	}
	var got landResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if !got.Landed {
		t.Fatalf("expected Landed=true, got %+v", got)
	}
	if got.LandedSHA == headSHA || got.LandedSHA == trunkTip {
		t.Fatalf("expected a NEW rebase commit SHA, got %s (same as an input)", got.LandedSHA)
	}
}

// TestHandleLandChangeRequiresRevalidationWhenTrunkDeltaIntersects proves
// the other §13.5 branch: trunk moved in the SAME project the Change
// touched, so landing must stop and ask for revalidation rather than
// silently rebasing past a change to code the Change's own (stale) checks
// never saw.
func TestHandleLandChangeRequiresRevalidationWhenTrunkDeltaIntersects(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	initialSHA := repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/other.go", "package main\n// change\n")
	repo.Commit("touch checkout (change)\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	// Advance trunk directly, touching the SAME project (commerce/checkout).
	repo.Run("checkout -q " + initialSHA)
	repo.WriteFile("commerce/checkout/main.go", "package main\n// trunk also changed this\n")
	trunkTip := repo.Commit("trunk touches checkout too")
	if _, err := gitfixtureRunGit(repo.Dir, "push", "-f", bare, trunkTip+":refs/heads/main"); err != nil {
		t.Fatalf("advance trunk directly: %v", err)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	beforeTip, _ := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")

	resp := postLand(t, srv, result.ChangeID, "sekret")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 requires_revalidation, got %d", resp.StatusCode)
	}
	var ce clierr.Error
	json.NewDecoder(resp.Body).Decode(&ce)
	if ce.Code != "requires_revalidation" {
		t.Fatalf("expected code requires_revalidation, got %+v", ce)
	}

	afterTip, _ := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if beforeTip != afterTip {
		t.Fatalf("expected trunk NOT to move when revalidation is required, got %s -> %s", beforeTip, afterTip)
	}

	change, _, _ := store.GetChange(context.Background(), result.ChangeID)
	if change.State == "landed" {
		t.Fatalf("expected the Change to remain open, not landed")
	}
}
