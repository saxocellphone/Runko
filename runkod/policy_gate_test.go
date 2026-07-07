package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
)

// newPolicyGateServer is the configurable fixture for the stage-11c policy
// tests: manifest controls what the tree declares, configure mutates the
// Server before it starts serving (global checks, eval opt-out, bot lanes).
func newPolicyGateServer(t *testing.T, manifest string, configure func(*Server)) (srv *httptest.Server, bare string, changeID string, store Store) {
	t.Helper()
	bare = newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", manifest)
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	if configure != nil {
		configure(server)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), bare, result.ChangeID, store
}

const unpolicedManifest = "schema: project/v1\nname: checkout-api\ntype: service\n"

// TestUnpolicedChangeIsNotMergeableByDefault is the stage-11c default-deny
// posture (§28.3): with the zero-value Server (no AllowUnpolicedLand), a
// Change whose touched paths resolve NO policy at all - no ci.checks, no
// org global checks, no owners - is not mergeable, with a blocker that says
// exactly why and what to do. A default-deny platform must not be
// default-allow just because nobody configured anything yet.
func TestUnpolicedChangeIsNotMergeableByDefault(t *testing.T) {
	srv, bare, changeID, _ := newPolicyGateServer(t, unpolicedManifest, nil)
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, changeID)
	if reqs.Mergeable {
		t.Fatalf("expected NOT mergeable with no resolvable policy, got %+v", reqs)
	}
	if len(reqs.Blockers) != 1 || !strings.Contains(reqs.Blockers[0], "no merge policy resolves") {
		t.Fatalf("expected the unpoliced-change blocker, got %v", reqs.Blockers)
	}

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409, got %d: %s", resp.StatusCode, body)
	}
	tip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if tip == "" {
		t.Fatalf("sanity: trunk should exist")
	}
}

// TestUnpolicedChangeLandsWithLoudOptOut is the other half: the SAME
// fixture with AllowUnpolicedLand set (eval profile / the
// --insecure-allow-unpoliced-land opt-out) lands fine - the posture is a
// deployment decision, not a hidden hard-coding.
func TestUnpolicedChangeLandsWithLoudOptOut(t *testing.T) {
	srv, _, changeID, _ := newPolicyGateServer(t, unpolicedManifest, func(s *Server) {
		s.AllowUnpolicedLand = true
	})
	defer srv.Close()

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 with the opt-out set, got %d: %s", resp.StatusCode, body)
	}
}

// TestGlobalRequiredChecksGateEveryChange is §14.9's org-level global
// checks ("e.g. secrets-scan always"): required on a project that declares
// nothing itself, pending until posted, satisfied by a green run. Note the
// global check alone also makes the change POLICED - the default-deny
// blocker must not fire once org config resolves a requirement.
func TestGlobalRequiredChecksGateEveryChange(t *testing.T) {
	srv, _, changeID, store := newPolicyGateServer(t, unpolicedManifest, func(s *Server) {
		s.GlobalRequiredChecks = []string{"secrets-scan"}
	})
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, changeID)
	if len(reqs.RequiredChecks) != 1 || reqs.RequiredChecks[0] != "secrets-scan" {
		t.Fatalf("expected secrets-scan required via org config, got %v", reqs.RequiredChecks)
	}
	if reqs.Mergeable {
		t.Fatalf("expected not mergeable while secrets-scan is unreported, got %+v", reqs)
	}
	for _, b := range reqs.Blockers {
		if strings.Contains(b, "no merge policy resolves") {
			t.Fatalf("the unpoliced blocker must not fire when org config requires a check, got %v", reqs.Blockers)
		}
	}

	change, _, _ := store.GetChange(context.Background(), changeID)
	if err := store.UpsertCheckRun(context.Background(), changeID, change.HeadSHA,
		checks.CheckRunView{Name: "secrets-scan", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess}); err != nil {
		t.Fatalf("UpsertCheckRun: %v", err)
	}
	reqs = getMergeRequirements(t, srv, changeID)
	if !reqs.Mergeable {
		t.Fatalf("expected mergeable with secrets-scan green, got %+v", reqs)
	}
}

const ownedManifest = "schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n"

func laneServer(t *testing.T, manifest string, lane BotLane) (*httptest.Server, string, string, Store) {
	t.Helper()
	return newPolicyGateServer(t, manifest, func(s *Server) {
		s.BotLanes = []BotLane{lane}
	})
}

// TestBotLaneWaivesOwnerApprovalWithinAllowlist is §14.10.2's core
// semantic: the change's touched paths sit inside the lane's allowlist and
// the lane's required check is green, so the LANE token lands it without
// any human approval - while the deploy token, gating the same Change,
// still requires the owner. Same Change, per-principal gates.
func TestBotLaneWaivesOwnerApprovalWithinAllowlist(t *testing.T) {
	lane := BotLane{Name: "image-bumper", Token: "lane-tok", PathAllowlist: []string{"commerce/**"}, RequiredChecks: []string{"manifest-lint"}}
	srv, bare, changeID, store := laneServer(t, ownedManifest, lane)
	defer srv.Close()

	// The deploy token's view: blocked on the human owner.
	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409 for the deploy token (owner outstanding), got %d: %s", resp.StatusCode, body)
	}

	// The lane's view: blocked only on ITS required check...
	resp = postLand(t, srv, changeID, "lane-tok")
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409 for the lane while manifest-lint is unreported, got %d: %s", resp.StatusCode, body)
	}
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ce.Code != "not_mergeable" || !strings.Contains(ce.Suggestion, "manifest-lint") {
		t.Fatalf("expected the lane blocked on manifest-lint, got %+v", ce)
	}

	// ...and green means land, no approval ever recorded.
	change, _, _ := store.GetChange(context.Background(), changeID)
	if err := store.UpsertCheckRun(context.Background(), changeID, change.HeadSHA,
		checks.CheckRunView{Name: "manifest-lint", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess}); err != nil {
		t.Fatalf("UpsertCheckRun: %v", err)
	}
	resp = postLand(t, srv, changeID, "lane-tok")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 lane land with manifest-lint green, got %d: %s", resp.StatusCode, body)
	}
	var got landResponse
	json.NewDecoder(resp.Body).Decode(&got)
	tip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil || tip != got.LandedSHA {
		t.Fatalf("expected trunk at %s, got %s (err %v)", got.LandedSHA, tip, err)
	}
}

// TestBotLanePathOutsideAllowlistIsDenied: a lane scoped to deploy/** may
// never land a Change touching commerce/**, however green - 403, not 409:
// out-of-scope is "this principal may not", not "not yet".
func TestBotLanePathOutsideAllowlistIsDenied(t *testing.T) {
	lane := BotLane{Name: "image-bumper", Token: "lane-tok", PathAllowlist: []string{"deploy/**"}, RequiredChecks: []string{"manifest-lint"}}
	srv, bare, changeID, _ := laneServer(t, ownedManifest, lane)
	defer srv.Close()

	beforeTip, _ := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")

	resp := postLand(t, srv, changeID, "lane-tok")
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
	}
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ce.Code != "bot_lane_path_denied" || !strings.Contains(ce.Message, "commerce/checkout/main.go") {
		t.Fatalf("expected bot_lane_path_denied naming the offending path, got %+v", ce)
	}

	afterTip, _ := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if beforeTip != afterTip {
		t.Fatalf("expected trunk NOT to move, got %s -> %s", beforeTip, afterTip)
	}
}

// TestBotLaneSeesItsOwnGateFromMergeRequirements pins the per-principal
// invariant: GET .../merge-requirements with the lane token reports the
// gate the lane will actually be held to (its check required, no owners),
// while the deploy token sees the human gate for the same Change.
func TestBotLaneSeesItsOwnGateFromMergeRequirements(t *testing.T) {
	lane := BotLane{Name: "image-bumper", Token: "lane-tok", PathAllowlist: []string{"commerce/**"}, RequiredChecks: []string{"manifest-lint"}}
	srv, _, changeID, _ := laneServer(t, ownedManifest, lane)
	defer srv.Close()

	get := func(token string) checks.MergeRequirements {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/changes/"+changeID+"/merge-requirements", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET merge-requirements: %v", err)
		}
		defer resp.Body.Close()
		var reqs checks.MergeRequirements
		if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return reqs
	}

	human := get("sekret")
	if len(human.RequiredOwners) != 1 || len(human.RequiredChecks) != 0 {
		t.Fatalf("deploy-token view: want the owner gate and no lane checks, got %+v", human)
	}
	laneView := get("lane-tok")
	if len(laneView.RequiredOwners) != 0 {
		t.Fatalf("lane view: owner approval must be waived, got %+v", laneView)
	}
	if len(laneView.RequiredChecks) != 1 || laneView.RequiredChecks[0] != "manifest-lint" {
		t.Fatalf("lane view: want manifest-lint required, got %+v", laneView)
	}
}

// TestBotLaneTokenIsAFullAPIClient: lane tokens authenticate everywhere
// (§8.8 "internal bots: same CLI/API surface"), not just at the land verb.
func TestBotLaneTokenIsAFullAPIClient(t *testing.T) {
	lane := BotLane{Name: "image-bumper", Token: "lane-tok", PathAllowlist: []string{"deploy/**"}, RequiredChecks: []string{"manifest-lint"}}
	srv, _, changeID, _ := laneServer(t, ownedManifest, lane)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/changes/"+changeID, nil)
	req.Header.Set("Authorization", "Bearer lane-tok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET change: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with the lane token, got %d", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/changes/"+changeID, nil)
	req2.Header.Set("Authorization", "Bearer wrong-token")
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatalf("GET change: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong token, got %d", resp2.StatusCode)
	}
}
