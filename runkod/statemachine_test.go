package runkod

// TestChangeStateMachine is the executable form of
// docs/change-lifecycle.md: every cell of that document's transition
// matrix - (state × event) -> (outcome, resulting state) - is driven
// through the real entry points (Processor.Process for pushes, the
// action cores for approve/rerun/abandon/land) against a real bare repo,
// one fresh fixture per cell so cells cannot contaminate each other.
// If a transition changes, the doc's table, its diagram, and this test
// table change together.
//
// The richer WITHIN-open behaviors (gate ordering, revalidation recovery,
// stacked-parent refusal, race retries, approval head-binding) have their
// own suites (policy_gate_test.go, land_test.go, stack_test.go,
// lifecycle_test.go, cmd/runkod's e2e); this table pins the lifecycle
// skeleton those all hang off.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

const smChangeID = "Icccccccccccccccccccccccccccccccccccccccc"

// smFixture is one Change on one daemon, advanced to a starting state
// through the same paths production uses.
type smFixture struct {
	t     *testing.T
	bare  string
	repo  *gitfixture.Repo
	store *MemStore
	p     *Processor
	srv   *Server
}

func newSMFixture(t *testing.T) *smFixture {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	// The project declares BOTH gate sources (§13.5): owners and a
	// required check - so land-from-open exercises the real gates, not
	// the AllowUnpolicedLand eval bypass.
	repo.WriteFile("proj/PROJECT.yaml",
		"schema: project/v1\nname: alpha\ntype: library\nowners:\n  - group:eng\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	repo.WriteFile("proj/a.txt", "a\n")
	repo.Commit("the change\n\nChange-Id: " + smChangeID)
	_, head := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: head, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("seed push rejected: %+v", res)
	}

	return &smFixture{
		t: t, bare: bare, repo: repo, store: store, p: p,
		srv: &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p},
	}
}

func (f *smFixture) change() Change {
	f.t.Helper()
	c, ok, err := f.store.GetChange(context.Background(), smChangeID)
	if err != nil || !ok {
		f.t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	return c
}

// greenAndApprove satisfies both §13.5 gates at the current head.
func (f *smFixture) greenAndApprove() {
	f.t.Helper()
	ctx := context.Background()
	if err := f.store.UpsertCheckRun(ctx, smChangeID, f.change().HeadSHA,
		checks.CheckRunView{Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess}); err != nil {
		f.t.Fatalf("UpsertCheckRun: %v", err)
	}
	if _, apiErr := f.srv.approveChangeCore(ctx, smChangeID, f.change(), "group:eng", "reviewer", nil); apiErr != nil {
		f.t.Fatalf("approve during setup: %+v", apiErr)
	}
}

func (f *smFixture) toAbandoned() {
	f.t.Helper()
	if _, apiErr := f.srv.abandonChangeCore(context.Background(), smChangeID, nil); apiErr != nil {
		f.t.Fatalf("abandon during setup: %+v", apiErr)
	}
}

func (f *smFixture) toLanded() {
	f.t.Helper()
	f.greenAndApprove()
	dec, apiErr := f.srv.landChangeCore(context.Background(), smChangeID, f.change(), nil, nil, false)
	if apiErr != nil || !dec.Landed {
		f.t.Fatalf("land during setup: %+v, %+v", dec, apiErr)
	}
}

// eventPush pushes a NEW commit carrying the same Change-Id through the
// real funnel; returns "" when accepted, "push_rejected" plus the client-
// visible message otherwise.
func (f *smFixture) eventPush() (code, detail string) {
	f.t.Helper()
	f.repo.WriteFile("proj/a.txt", "grown "+f.change().State+"\n")
	f.repo.Commit("more work\n\nChange-Id: " + smChangeID)
	oldSHA, newSHA := pushCommit(f.t, f.repo, f.bare, "refs/for/main")
	res := f.p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: "refs/for/main"}, nil)
	if res.Accepted {
		return "", ""
	}
	return "push_rejected", res.Message
}

func codeOf(apiErr *apiError) string {
	if apiErr == nil {
		return ""
	}
	if apiErr.Err.Code != "" {
		return apiErr.Err.Code
	}
	return "internal"
}

func TestChangeStateMachine(t *testing.T) {
	cases := []struct {
		state, event string
		prep         func(f *smFixture) // extra setup after reaching state
		wantCode     string
		wantState    string
	}{
		// --- open ---
		{state: "open", event: "push", wantCode: "", wantState: "open"},
		{state: "open", event: "approve", wantCode: "", wantState: "open"},
		{state: "open", event: "rerun", wantCode: "", wantState: "open"},
		{state: "open", event: "abandon", wantCode: "", wantState: "abandoned"},
		{state: "open", event: "land", prep: (*smFixture).greenAndApprove, wantCode: "", wantState: "landed"},
		{state: "open", event: "land_blocked", wantCode: "not_mergeable", wantState: "open"},

		// --- abandoned: parked, only exit is re-push ---
		{state: "abandoned", event: "push", wantCode: "", wantState: "open"}, // change.reopened, §7.4
		{state: "abandoned", event: "approve", wantCode: "invalid_state", wantState: "abandoned"},
		{state: "abandoned", event: "rerun", wantCode: "invalid_state", wantState: "abandoned"},
		{state: "abandoned", event: "abandon", wantCode: "", wantState: "abandoned"}, // idempotent
		{state: "abandoned", event: "land", wantCode: "invalid_state", wantState: "abandoned"},

		// --- landed: terminal, §7.4 ---
		{state: "landed", event: "push", wantCode: "push_rejected", wantState: "landed"},
		{state: "landed", event: "approve", wantCode: "invalid_state", wantState: "landed"},
		{state: "landed", event: "rerun", wantCode: "invalid_state", wantState: "landed"},
		{state: "landed", event: "abandon", wantCode: "invalid_state", wantState: "landed"},
		{state: "landed", event: "land", wantCode: "", wantState: "landed"}, // idempotent replay
	}

	for _, tc := range cases {
		t.Run(tc.state+"/"+tc.event, func(t *testing.T) {
			f := newSMFixture(t)
			switch tc.state {
			case "abandoned":
				f.toAbandoned()
			case "landed":
				f.toLanded()
			}
			if tc.prep != nil {
				tc.prep(f)
			}
			before := f.change()
			ctx := context.Background()

			var code, detail string
			switch tc.event {
			case "push":
				code, detail = f.eventPush()
			case "approve":
				_, apiErr := f.srv.approveChangeCore(ctx, smChangeID, before, "group:eng", "reviewer", nil)
				code = codeOf(apiErr)
			case "rerun":
				_, apiErr := f.srv.rerunCheckCore(ctx, smChangeID, before, "unit", nil, nil)
				code = codeOf(apiErr)
			case "abandon":
				_, apiErr := f.srv.abandonChangeCore(ctx, smChangeID, nil)
				code = codeOf(apiErr)
			case "land", "land_blocked":
				dec, apiErr := f.srv.landChangeCore(ctx, smChangeID, before, nil, nil, false)
				code = codeOf(apiErr)
				if code == "" && !dec.Landed {
					t.Fatalf("land neither errored nor landed: %+v", dec)
				}
			default:
				t.Fatalf("unknown event %q", tc.event)
			}

			if code != tc.wantCode {
				t.Fatalf("outcome: want %q, got %q (%s)", tc.wantCode, code, detail)
			}
			after := f.change()
			if after.State != tc.wantState {
				t.Fatalf("state: want %q, got %q", tc.wantState, after.State)
			}

			// Cell-specific invariants the matrix calls out.
			switch {
			case tc.state == "landed" && tc.event == "push":
				if !strings.Contains(detail, "already landed") {
					t.Fatalf("rejection must explain terminality, got %q", detail)
				}
				if after.HeadSHA != before.HeadSHA {
					t.Fatalf("a rejected push must not move a landed head: %s -> %s", before.HeadSHA, after.HeadSHA)
				}
			case tc.state == "landed" && tc.event == "land":
				if after.LandedSHA != before.LandedSHA {
					t.Fatalf("idempotent land replay changed landed_sha: %s -> %s", before.LandedSHA, after.LandedSHA)
				}
			case tc.state == "open" && tc.event == "push":
				if after.HeadSHA == before.HeadSHA {
					t.Fatal("an accepted push must move the open head")
				}
			case tc.state == "abandoned" && tc.event == "push":
				if after.HeadSHA == before.HeadSHA {
					t.Fatal("the reopening push must move the head")
				}
			}
		})
	}
}

// TestReopenedChangeDoesNotInheritAbandonedEraApprovals would have caught
// the approve-while-abandoned leak requireOpenChange closes: approvals bind
// to head_sha, so one granted while abandoned would count again after a
// same-head reopen. With the guard, the approval can never be recorded.
func TestReopenedChangeDoesNotInheritAbandonedEraApprovals(t *testing.T) {
	f := newSMFixture(t)
	ctx := context.Background()
	f.toAbandoned()

	_, apiErr := f.srv.approveChangeCore(ctx, smChangeID, f.change(), "group:eng", "reviewer", nil)
	if codeOf(apiErr) != "invalid_state" {
		t.Fatalf("approve on abandoned: want invalid_state, got %+v", apiErr)
	}

	// Reopen by re-pushing the SAME commit (the exact inheritance vector:
	// same head, so a recorded approval would still bind).
	head := f.change().HeadSHA
	res := f.p.Process(ctx, RefUpdate{OldSHA: head, NewSHA: head, Ref: "refs/for/main"}, nil)
	if !res.Accepted {
		t.Fatalf("same-commit reopen push rejected: %+v", res)
	}
	if got := f.change(); got.State != "open" || got.HeadSHA != head {
		t.Fatalf("reopen: want open at %s, got %s at %s", head, got.State, got.HeadSHA)
	}

	approvals, err := f.store.ListApprovals(ctx, smChangeID)
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	for _, a := range approvals {
		if a.HeadSHA == head {
			t.Fatalf("an abandoned-era approval leaked into the reopened change: %+v", a)
		}
	}
}

// TestForceLandOverride pins the §13.5 admin override (2026-07-08, "add a
// force approve/merge option so owner can merge changes"): WHO may force
// (deploy token + admin humans; never agents, never bot lanes, never
// non-admin humans), WHAT it bypasses (owner/check gates), what it NEVER
// bypasses (terminal states, stacked-parent ordering - integrity, not
// policy), and that the override is durably audited (landed_forced).
func TestForceLandOverride(t *testing.T) {
	admin := &Principal{Name: "boss", Token: "t1", Admin: true}
	human := &Principal{Name: "pleb", Token: "t2"}
	agentAdmin := &Principal{Name: "bot", Token: "t3", IsAgent: true, Admin: true}
	lane := &BotLane{Name: "bumper", Token: "t4"}

	t.Run("deploy token forces past both gates and is audited", func(t *testing.T) {
		f := newSMFixture(t) // owner + required check, neither satisfied
		dec, apiErr := f.srv.landChangeCore(context.Background(), smChangeID, f.change(), nil, nil, true)
		if apiErr != nil || !dec.Landed || !dec.Forced {
			t.Fatalf("force by deploy token: %+v, %+v", dec, apiErr)
		}
		if got := f.change(); !got.LandedForced || got.State != "landed" {
			t.Fatalf("landed_forced audit bit not recorded: %+v", got)
		}
	})

	t.Run("admin principal forces and is attributed", func(t *testing.T) {
		f := newSMFixture(t)
		dec, apiErr := f.srv.landChangeCore(context.Background(), smChangeID, f.change(), nil, admin, true)
		if apiErr != nil || !dec.Landed || !dec.Forced {
			t.Fatalf("force by admin: %+v, %+v", dec, apiErr)
		}
		if got := f.change(); got.LandedBy != "boss" || !got.LandedForced {
			t.Fatalf("attribution: %+v", got)
		}
	})

	t.Run("non-admin, agent, and bot lane are all refused", func(t *testing.T) {
		f := newSMFixture(t)
		for name, tc := range map[string]struct {
			principal *Principal
			lane      *BotLane
		}{"non-admin human": {human, nil}, "agent even with admin flag": {agentAdmin, nil}, "bot lane": {nil, lane}} {
			_, apiErr := f.srv.landChangeCore(context.Background(), smChangeID, f.change(), tc.lane, tc.principal, true)
			if apiErr == nil || apiErr.Err.Code != "force_denied" || apiErr.Status != http.StatusForbidden {
				t.Fatalf("%s: want 403 force_denied, got %+v", name, apiErr)
			}
		}
		if got := f.change(); got.State != "open" {
			t.Fatalf("refused forces must not move state: %+v", got)
		}
	})

	t.Run("force never bypasses terminal states", func(t *testing.T) {
		f := newSMFixture(t)
		f.toAbandoned()
		if _, apiErr := f.srv.landChangeCore(context.Background(), smChangeID, f.change(), nil, admin, true); apiErr == nil || apiErr.Err.Code != "invalid_state" {
			t.Fatalf("force on abandoned: want invalid_state, got %+v", apiErr)
		}
	})

	t.Run("a force that bypassed nothing is an ordinary land", func(t *testing.T) {
		f := newSMFixture(t)
		f.greenAndApprove()
		dec, apiErr := f.srv.landChangeCore(context.Background(), smChangeID, f.change(), nil, admin, true)
		if apiErr != nil || !dec.Landed || dec.Forced {
			t.Fatalf("green force: %+v, %+v", dec, apiErr)
		}
		if got := f.change(); got.LandedForced {
			t.Fatalf("nothing was bypassed - landed_forced must stay false: %+v", got)
		}
	})
}

// TestForceLandNeverBypassesStackOrdering: the parent guard is integrity,
// not policy - forcing a child onto trunk without its parent's content
// would corrupt the tree, so even admins are refused.
func TestForceLandNeverBypassesStackOrdering(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	_, _, _, _, _ = pushStackedPair(t, bare, store)
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}
	admin := &Principal{Name: "boss", Token: "t1", Admin: true}

	chB, _, _ := store.GetChange(context.Background(), stackIDB)
	_, apiErr := srv.landChangeCore(context.Background(), stackIDB, chB, nil, admin, true)
	if apiErr == nil || apiErr.Err.Code != "parent_change_not_landed" {
		t.Fatalf("force must not skip stack ordering: %+v", apiErr)
	}
}
