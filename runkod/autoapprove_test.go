package runkod

// Auto-approve zones at the merge gate (2026-07-28, runkod/README.md
// Decisions): a subtree whose TRUNK manifest declares auto_approve stops
// waiting on humans - owner requirements read satisfied, agent-policy
// findings stop being required - while checks, descriptions and the zone
// boundary all keep working. These drive the real HTTP gate over a real bare
// repo, because the property that matters most (resolution comes from trunk,
// not from the change's own tree) is invisible to a unit test of the
// resolver.

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/receive"
)

const autoApproveChangeID = "Ibbbb1111cccc2222dddd3333eeee4444ffff5555"

// zoneManifest is a project manifest with an explicit auto_approve posture.
func zoneManifest(name, owners, autoApprove string) string {
	m := "schema: project/v1\nname: " + name + "\ntype: service\n"
	if owners != "" {
		m += "owners:\n  - " + owners + "\n"
	}
	if autoApprove != "" {
		m += "auto_approve: " + autoApprove + "\n"
	}
	return m
}

// newAutoApproveFixture seeds trunk from trunkFiles, then pushes one
// agent-authored change writing changeFiles, and serves the API over both.
// The agent's denylist covers sandbox/**, so an in-zone change also carries a
// minted agent-policy finding - the second thing a zone waives.
func newAutoApproveFixture(t *testing.T, policy receive.AgentPolicy, trunkFiles, changeFiles map[string]string) (*httptest.Server, *Server, *MemStore, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	for path, content := range trunkFiles {
		repo.WriteFile(path, content)
	}
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	for path, content := range changeFiles {
		repo.WriteFile(path, content)
	}
	repo.Commit("the change\n\nChange-Id: " + autoApproveChangeID)
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	principals := []Principal{
		{Name: "alice", Token: "alice-tok"},
		{Name: "builder", Token: "builder-tok", IsAgent: true, Policy: policy},
	}
	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: principals}
	result := processor.Process(context.Background(),
		RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"REMOTE_USER=builder"})
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", Principals: principals}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	hs := httptest.NewServer(handler)
	t.Cleanup(hs.Close)
	return hs, server, store, result.ChangeID
}

// agentPolicyWithSandboxDenylist is the fixtures' agent posture: everything
// content-shaped that could gate is off EXCEPT the denylist covering the
// zone, so each test isolates the one gate it is about.
func agentPolicyWithSandboxDenylist() receive.AgentPolicy {
	p := receive.DefaultAgentPolicy()
	p.RequireWorkspaceAffinity = false
	p.RequireDescription = false
	p.DenylistPaths = []string{"sandbox/**"}
	return p
}

var zonedTrunk = map[string]string{
	"sandbox/PROJECT.yaml":           zoneManifest("sandbox", "group:sandbox-eng", "true"),
	"commerce/checkout/PROJECT.yaml": zoneManifest("checkout-api", "group:commerce-eng", ""),
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestAutoApproveZoneWaivesOwnersAndPolicyFindings is the headline behavior:
// inside a declared zone an agent's change is ready to land with no human
// action - the owner requirement is still REPORTED (the tree still says who
// owns this) but reads satisfied, and the minted agent-policy finding stops
// being required.
func TestAutoApproveZoneWaivesOwnersAndPolicyFindings(t *testing.T) {
	hs, _, _, changeID := newAutoApproveFixture(t, agentPolicyWithSandboxDenylist(), zonedTrunk,
		map[string]string{"sandbox/app.go": "package sandbox\n"})

	reqs := getMergeRequirements(t, hs, changeID)
	if !hasName(reqs.RequiredOwners, "group:sandbox-eng") {
		t.Fatalf("the zone must not hide who owns the code, got required=%v", reqs.RequiredOwners)
	}
	if !hasName(reqs.SatisfiedOwners, "group:sandbox-eng") {
		t.Fatalf("an in-zone owner requirement must read satisfied, got outstanding=%v", reqs.OutstandingOwners)
	}
	if hasName(reqs.RequiredChecks, checks.AgentPolicyCheckName) {
		t.Fatalf("an in-zone change must not require the agent-policy ack, got %v", reqs.RequiredChecks)
	}
	if !reqs.AutoApproved {
		t.Fatal("a waived gate must report auto_approved - a green gate must never read as a reviewed one")
	}
	if !reqs.Mergeable {
		t.Fatalf("in-zone change should be ready to land, blockers: %v", reqs.Blockers)
	}
}

// TestAutoApproveStopsAtTheZoneBoundary: a change straddling the zone and a
// governed project is governed. The waiver is per-path, and the agent-policy
// finding - one verdict over the whole diff - comes back as required.
func TestAutoApproveStopsAtTheZoneBoundary(t *testing.T) {
	hs, _, _, changeID := newAutoApproveFixture(t, agentPolicyWithSandboxDenylist(), zonedTrunk,
		map[string]string{
			"sandbox/app.go":            "package sandbox\n",
			"commerce/checkout/main.go": "package main\n",
		})

	reqs := getMergeRequirements(t, hs, changeID)
	if !hasName(reqs.OutstandingOwners, "group:commerce-eng") {
		t.Fatalf("the governed project's owner must stay outstanding, got satisfied=%v", reqs.SatisfiedOwners)
	}
	if !hasName(reqs.SatisfiedOwners, "group:sandbox-eng") {
		t.Fatalf("the in-zone owner should still be waived, got outstanding=%v", reqs.OutstandingOwners)
	}
	if !hasName(reqs.RequiredChecks, checks.AgentPolicyCheckName) {
		t.Fatalf("a straddling change must still owe the agent-policy ack, got %v", reqs.RequiredChecks)
	}
	if reqs.Mergeable || !hasBlocker(reqs.Blockers, "waiting on approval from group:commerce-eng") {
		t.Fatalf("straddling change must stay blocked on the governed owner: mergeable=%v blockers=%v", reqs.Mergeable, reqs.Blockers)
	}
}

// TestAutoApproveCannotSelfGrant is the security property the whole design
// hangs on: the gate resolves zones from TRUNK, so a change that declares
// auto_approve in its own tree gains nothing from it - it lands (or not)
// under the policy that governed it before.
func TestAutoApproveCannotSelfGrant(t *testing.T) {
	governedTrunk := map[string]string{
		"PROJECT.yaml": zoneManifest("root", "group:platform-eng", "false"),
	}
	hs, _, _, changeID := newAutoApproveFixture(t, agentPolicyWithSandboxDenylist(), governedTrunk,
		map[string]string{
			// The change flips the root manifest's posture AND adds code.
			"PROJECT.yaml": zoneManifest("root", "group:platform-eng", "true"),
			"app.go":       "package app\n",
		})

	reqs := getMergeRequirements(t, hs, changeID)
	if reqs.AutoApproved {
		t.Fatal("a change may not approve itself by declaring a zone in its own tree")
	}
	if !hasName(reqs.OutstandingOwners, "group:platform-eng") {
		t.Fatalf("the owner requirement must stand, got satisfied=%v", reqs.SatisfiedOwners)
	}
	if reqs.Mergeable {
		t.Fatalf("the self-granting change must not be mergeable: %+v", reqs)
	}
}

// TestDisableAutoApproveVetoesTheTree: the org kill switch outranks every
// manifest, live - no restart, no manifest sweep - and clearing it hands the
// decision straight back to the tree.
func TestDisableAutoApproveVetoesTheTree(t *testing.T) {
	hs, srv, store, changeID := newAutoApproveFixture(t, agentPolicyWithSandboxDenylist(), zonedTrunk,
		map[string]string{"sandbox/app.go": "package sandbox\n"})
	srv.SettingsOrg = "acme"
	srv.Directory = store

	ctx := context.Background()
	if err := store.UpdateOrgSettings(ctx, "acme", OrgSettings{DisableAutoApprove: true}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	reqs := getMergeRequirements(t, hs, changeID)
	if reqs.AutoApproved || !hasName(reqs.OutstandingOwners, "group:sandbox-eng") {
		t.Fatalf("the org veto must restore the owner gate, got auto=%v outstanding=%v", reqs.AutoApproved, reqs.OutstandingOwners)
	}
	if !hasName(reqs.RequiredChecks, checks.AgentPolicyCheckName) {
		t.Fatalf("the org veto must restore the agent-policy ack, got %v", reqs.RequiredChecks)
	}

	if err := store.UpdateOrgSettings(ctx, "acme", OrgSettings{}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	if reqs = getMergeRequirements(t, hs, changeID); !reqs.AutoApproved || !reqs.Mergeable {
		t.Fatalf("clearing the veto must hand the decision back to the tree: %+v", reqs)
	}
}

// TestAutoApproveSatisfiesDefaultDeny is the bootstrap case the feature
// exists for: a brand-new project with no owners and no checks yet. Before
// the zone that change was refused as unpoliced; declaring the zone IS the
// policy that resolves for it.
func TestAutoApproveSatisfiesDefaultDeny(t *testing.T) {
	hs, _, _, changeID := newAutoApproveFixture(t, agentPolicyWithSandboxDenylist(),
		map[string]string{"sandbox/PROJECT.yaml": zoneManifest("sandbox", "", "true")},
		map[string]string{"sandbox/app.go": "package sandbox\n"})

	reqs := getMergeRequirements(t, hs, changeID)
	if hasBlocker(reqs.Blockers, "no merge policy resolves") {
		t.Fatalf("a declared zone IS a resolved policy: %v", reqs.Blockers)
	}
	if !reqs.Mergeable {
		t.Fatalf("an ownerless in-zone bootstrap change must be landable: %+v", reqs)
	}
}

// TestAutoApproveDoesNotWaiveTheDescription: the zone waives what HUMANS owe
// the change, never what the agent owes its reviewers - a change nobody can
// read without the diff stays blocked, zone or not.
func TestAutoApproveDoesNotWaiveTheDescription(t *testing.T) {
	policy := agentPolicyWithSandboxDenylist()
	policy.RequireDescription = true
	hs, _, _, changeID := newAutoApproveFixture(t, policy, zonedTrunk,
		map[string]string{"sandbox/app.go": "package sandbox\n"})

	reqs := getMergeRequirements(t, hs, changeID)
	if !reqs.AutoApproved {
		t.Fatal("the approval waiver still applies")
	}
	if reqs.Mergeable || !hasBlocker(reqs.Blockers, "no description") {
		t.Fatalf("the description gate must survive the zone: mergeable=%v blockers=%v", reqs.Mergeable, reqs.Blockers)
	}
}
