package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/land"
	"github.com/saxocellphone/runko/platform/receive"
)

// newLandTestServer seeds a bare repo with one project and one open Change
// (pushed for real via Processor.Process, exactly like newTestServer in
// api_test.go), and reports zero checks - so the Change starts out
// trivially mergeable (checks/requirements.go: an empty requiredCheckNames
// list blocks on nothing) and every test below can focus on land.Land's own
// wiring, not on merge-requirements gating mechanics already covered by
// TestAPIPostCheckAndMergeRequirementsRoundTrip. Every Server in this file
// sets AllowUnpolicedLand (the §9.3 eval profile): these fixtures declare
// no owners and no ci.checks, which the stage-11c default-deny posture
// would otherwise refuse - see policy_gate_test.go for the tests of that
// posture itself.
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

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
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

	// Even a fast-forward land re-mints the commit under the canonical
	// landing identity (§7.5): the landed SHA differs from the pushed head,
	// and both author and committer are the machine, never the client's git
	// config.
	if got.LandedSHA == change.HeadSHA {
		t.Fatalf("fast-forward land must re-stamp identity (a new SHA), not reuse the pushed head %s", change.HeadSHA)
	}
	ident, err := gitfixtureRunGit(bare, "log", "-1", "--format=%an%x00%cn", got.LandedSHA)
	if err != nil {
		t.Fatalf("read landed identity: %v", err)
	}
	if parts := strings.Split(ident, "\x00"); len(parts) != 2 || parts[0] != "Runko" || parts[1] != "Runko" {
		t.Fatalf("fast-forward land must stamp author+committer = Runko, got %q", ident)
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
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
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

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
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
	if !got.Landed || got.LandedSHA == "" {
		t.Fatalf("expected the first-ever land to bootstrap trunk, got %+v", got)
	}

	tip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil || tip != got.LandedSHA {
		t.Fatalf("expected refs/heads/main == landed SHA %s, got %s (err %v)", got.LandedSHA, tip, err)
	}
	// The bootstrap land re-mints a root commit under the canonical landing
	// identity (§7.5): a new SHA distinct from the pushed head, but the same
	// tree (content preserved), authored and committed by the machine.
	if got.LandedSHA == headSHA {
		t.Fatalf("bootstrap land must re-stamp identity (a new SHA), not reuse the pushed head %s", headSHA)
	}
	wantTree, _ := gitfixtureRunGit(bare, "rev-parse", headSHA+"^{tree}")
	gotTree, _ := gitfixtureRunGit(bare, "rev-parse", got.LandedSHA+"^{tree}")
	if wantTree == "" || wantTree != gotTree {
		t.Fatalf("bootstrap land must preserve content: tree %s != %s", gotTree, wantTree)
	}
	ident, _ := gitfixtureRunGit(bare, "log", "-1", "--format=%an%x00%cn", got.LandedSHA)
	if parts := strings.Split(ident, "\x00"); len(parts) != 2 || parts[0] != "Runko" || parts[1] != "Runko" {
		t.Fatalf("bootstrap land must stamp author+committer = Runko, got %q", ident)
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

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
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
// in the eval profile (AllowUnpolicedLand), a project with NO ci.checks
// declared (the common case, anti-Boq §6.2) must stay mergeable with zero
// posted checks. This is the exact scenario the review finding described:
// previously this was "mergeable" for the wrong reason (required :=
// whatever was posted, and nothing was posted); now it is "mergeable" for
// the right reason (nothing is actually required, and the eval profile
// explicitly permits unpoliced lands - outside it, the default-deny
// posture in policy_gate_test.go applies).
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

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
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

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
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

func mustRepoGit(t *testing.T, repo *gitfixture.Repo, args ...string) {
	t.Helper()
	// Identity flags: gitfixture's own Commit injects identity per call;
	// raw commands (rebase creates commits) need the same.
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)
	if _, err := gitfixtureRunGit(repo.Dir, full...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// TestRevalidationRebaseRepushLands is §13.5's own escape hatch as a
// regression test, found by the compose edge suite (E7): a gated Change
// 409s requires_revalidation when trunk's delta intersects its affected
// set, and the PRESCRIBED way out - rebase onto trunk, re-push the same
// Change-Id, re-gate - must then land. Pre-fix it could not, ever: the
// amend path updated head_sha but froze base_sha at creation time, so the
// trunk delta was computed from the stale base forever and revalidation
// became a dead end for the Change.
func TestRevalidationRebaseRepushLands(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	_, trunkTip := pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Change A and Change B, both based on the same trunk tip, same project.
	repo.WriteFile("commerce/checkout/a.go", "package main // a\n")
	repo.Commit("change A\n\nChange-Id: Iaaaa000000000000000000000000000000000001")
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	resA := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headA, Ref: "refs/for/main"}, nil)
	if !resA.Accepted {
		t.Fatalf("push A rejected: %+v", resA)
	}

	// B: sibling of A (branch from trunk, not from A).
	mustRepoGit(t, repo, "checkout", "-q", trunkTip)
	mustRepoGit(t, repo, "checkout", "-q", "-b", "changeB")
	repo.WriteFile("commerce/checkout/b.go", "package main // b\n")
	repo.Commit("change B\n\nChange-Id: Ibbbb000000000000000000000000000000000002")
	_, headB := pushCommit(t, repo, bare, "refs/for/main")
	resB := processor.Process(context.Background(), RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, nil)
	if !resB.Accepted {
		t.Fatalf("push B rejected: %+v", resB)
	}

	// Land A: trunk moves, its delta touches B's affected project.
	landResp := postLand(t, srv, resA.ChangeID, "sekret")
	if landResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(landResp.Body)
		t.Fatalf("land A: %d: %s", landResp.StatusCode, body)
	}
	landResp.Body.Close()

	// Land B: correctly refused - revalidation required.
	landResp = postLand(t, srv, resB.ChangeID, "sekret")
	body, _ := io.ReadAll(landResp.Body)
	landResp.Body.Close()
	if landResp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "requires_revalidation") {
		t.Fatalf("expected 409 requires_revalidation for B, got %d: %s", landResp.StatusCode, body)
	}

	// §13.5's way out: rebase B onto the new trunk, re-push the same
	// Change-Id. The amend must move base_sha to the new merge-base.
	newTrunk, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("rev-parse trunk: %v", err)
	}
	mustRepoGit(t, repo, "fetch", "-q", bare, "refs/heads/main")
	mustRepoGit(t, repo, "rebase", "-q", newTrunk)
	rebasedHead, err := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse rebased head: %v", err)
	}
	pushCommit(t, repo, bare, "refs/for/main")
	resB2 := processor.Process(context.Background(), RefUpdate{OldSHA: headB, NewSHA: rebasedHead, Ref: "refs/for/main"}, nil)
	if !resB2.Accepted || resB2.ChangeID != resB.ChangeID {
		t.Fatalf("re-push of rebased B not accepted as the same Change: %+v", resB2)
	}

	change, ok, err := store.GetChange(context.Background(), resB.ChangeID)
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if change.BaseSHA != newTrunk {
		t.Fatalf("amend must move base_sha to the new merge-base: base=%s want=%s (stale base makes revalidation a permanent dead end)", change.BaseSHA, newTrunk)
	}

	// And now B lands.
	landResp = postLand(t, srv, resB.ChangeID, "sekret")
	body, _ = io.ReadAll(landResp.Body)
	landResp.Body.Close()
	if landResp.StatusCode != http.StatusOK {
		t.Fatalf("expected the rebased re-push to land, got %d: %s", landResp.StatusCode, body)
	}
}

// TestLandNormalizesIdentity: every landed commit carries the canonical
// landing identity as BOTH author and committer (§7.5, changelog
// 2026-07-13), so trunk and the outbound mirror are uniform regardless of
// the client's git config - which historically leaked "Runko",
// "Runko Workspace", and the same person under two emails onto the mirror.
// The full message (body + Change-Id) must still survive the re-mint; only
// the identity is normalized, not the content.
func TestLandNormalizesIdentity(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}

	// Trunk with one commit; a change based on it; then trunk advances so
	// the land MUST rebase (non-fast-forward), touching disjoint paths so
	// revalidation stays quiet.
	work := t.TempDir()
	mustGitLand(t, work, "clone", bare, ".")
	mustGitLand(t, work, "-c", "user.name=Base", "-c", "user.email=base@x.dev", "commit", "--allow-empty", "-m", "root")
	mustGitLand(t, work, "push", "origin", "HEAD:refs/heads/main")

	// The change: authored by Alice with a multi-line message.
	change := t.TempDir()
	mustGitLand(t, change, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(change, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitLand(t, change, "add", "a.txt")
	{
		// Env beats -c for identity, so author Alice needs explicit env.
		cmd := exec.Command("git", "commit", "-m", "feat: the change\n\nA body line that must survive.\n\nChange-Id: Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		cmd.Dir = change
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@x.dev",
			"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@x.dev")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("alice commit: %v\n%s", err, out)
		}
	}
	headSHA := strings.TrimSpace(mustGitLand(t, change, "rev-parse", "HEAD"))
	baseSHA := strings.TrimSpace(mustGitLand(t, change, "rev-parse", "HEAD~1"))
	mustGitLand(t, change, "push", "origin", "HEAD:refs/changes/Iaaa/head")

	// Trunk moves on (disjoint path).
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitLand(t, work, "add", "b.txt")
	mustGitLand(t, work, "-c", "user.name=Base", "-c", "user.email=base@x.dev", "commit", "-m", "trunk moved")
	mustGitLand(t, work, "push", "origin", "HEAD:refs/heads/main")

	if _, err := store.CreateOrUpdateChange(context.Background(), "Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		baseSHA, headSHA, "refs/changes/Iaaa/head", "feat: the change", "", "", ""); err != nil {
		t.Fatal(err)
	}
	changeRow, _, _ := store.GetChange(context.Background(), "Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	outcome, err := srv.attemptLand(context.Background(), changeRow, land.RevalidationNever)
	if err != nil || !outcome.Landed {
		t.Fatalf("land: %v %+v", err, outcome)
	}

	mustGitLand(t, work, "fetch", "origin", "main")
	got := strings.TrimSpace(mustGitLand(t, work, "log", "-1", "--format=%an%x00%ae%x00%cn%x00%B", "FETCH_HEAD"))
	parts := strings.SplitN(got, "\x00", 4)
	if len(parts) != 4 {
		t.Fatalf("unexpected log output: %q", got)
	}
	if parts[0] != "Runko" || parts[1] != "runko@localhost" {
		t.Fatalf("landed author must be the canonical landing identity, not the client's (Alice); got %q <%q>", parts[0], parts[1])
	}
	if parts[2] != "Runko" {
		t.Fatalf("committer should be the landing machine, got %q", parts[2])
	}
	if !strings.Contains(parts[3], "A body line that must survive.") || !strings.Contains(parts[3], "Change-Id: Iaaa") {
		t.Fatalf("landing must preserve the full message, got:\n%s", parts[3])
	}
}

func mustGitLand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.dev", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.dev", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
