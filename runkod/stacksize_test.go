package runkod

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// stackSizeFixture: trunk with one commit, an agent principal whose policy
// caps changes at maxFiles files (no affinity requirement - these tests
// isolate the size caps), and the REMOTE_USER env the funnel reads
// identity from.
func stackSizeFixture(t *testing.T, maxFiles int) (*Processor, *gitfixture.Repo, string, []string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	p := newTestProcessor(bare, NewMemStore())
	p.Principals = []Principal{{
		Name: "builder", IsAgent: true,
		Policy: receive.AgentPolicy{MaxChangedFiles: maxFiles},
	}}
	return p, repo, bare, []string{"REMOTE_USER=builder"}
}

func writeN(repo *gitfixture.Repo, prefix string, n int) {
	for i := 0; i < n; i++ {
		repo.WriteFile(fmt.Sprintf("%s/f%d.go", prefix, i), "package x\n")
	}
}

// TestAgentStackOfSmallChangesPassesWhereMonolithIsRefused is the
// incentive flip this feature exists for: with a 3-file cap, two stacked
// 2-file changes (4 files total - the OLD whole-push measurement refused
// this) are accepted, while one 4-file change is refused BY NAME with the
// split workflow in the message.
func TestAgentStackOfSmallChangesPassesWhereMonolithIsFlagged(t *testing.T) {
	p, repo, bare, env := stackSizeFixture(t, 3)
	ctx := context.Background()

	// A stack: two Change-bearing commits, 2 files each.
	writeN(repo, "a", 2)
	repo.Commit("step one\n\nChange-Id: I1111111111111111111111111111111111111111")
	writeN(repo, "b", 2)
	repo.Commit("step two\n\nChange-Id: I2222222222222222222222222222222222222222")
	oldSHA, tip := pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, env)
	if !result.Accepted {
		t.Fatalf("a stack of small changes must pass per-change caps (sum 4 > cap 3 is IRRELEVANT): %+v", result)
	}

	// The same volume as one change: refused, naming the change.
	p2, repo2, bare2, env2 := stackSizeFixture(t, 3)
	writeN(repo2, "c", 4)
	repo2.Commit("do everything at once\n\nChange-Id: I3333333333333333333333333333333333333333")
	oldSHA2, tip2 := pushCommit(t, repo2, bare2, "refs/for/main")

	result = p2.Process(ctx, RefUpdate{OldSHA: oldSHA2, NewSHA: tip2, Ref: "refs/for/main"}, env2)
	if !result.Accepted {
		t.Fatalf("since 2026-07-24 a cap overrun accepts and owes the agent-policy check, got %+v", result)
	}
	for _, want := range []string{"I3333333333333333333333333333333333333333", "changed 4 files", "split the work into a stack", "runko change ack-policy"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("push output must contain %q, got:\n%s", want, result.Message)
		}
	}
}

// TestAgentStackFindingNamesTheOversizedMember: in a stack where only the
// MIDDLE step is too big, the agent-policy finding lands on that member -
// the agent must know which step to split, not guess, and the small
// members' gates stay clean.
func TestAgentStackFindingNamesTheOversizedMember(t *testing.T) {
	p, repo, bare, env := stackSizeFixture(t, 3)
	ctx := context.Background()

	writeN(repo, "a", 1)
	repo.Commit("small step\n\nChange-Id: I1111111111111111111111111111111111111111")
	writeN(repo, "b", 4)
	repo.Commit("huge step\n\nChange-Id: I2222222222222222222222222222222222222222")
	writeN(repo, "c", 1)
	repo.Commit("small again\n\nChange-Id: I4444444444444444444444444444444444444444")
	oldSHA, tip := pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, env)
	if !result.Accepted {
		t.Fatalf("an oversized member accepts and owes the agent-policy check, got %+v", result)
	}
	if !strings.Contains(result.Message, "I2222222222222222222222222222222222222222") {
		t.Fatalf("the finding must name the oversized MEMBER, got:\n%s", result.Message)
	}
	if strings.Contains(result.Message, "agent-policy: change I1111111111111111111111111111111111111111") ||
		strings.Contains(result.Message, "agent-policy: change I4444444444444444444444444444444444444444") {
		t.Fatalf("small members must carry no finding, got:\n%s", result.Message)
	}
}

// TestAgentNearCapChangeGetsSplitNudge: over half the cap but under it -
// accepted, with the advisory note in the push output.
func TestAgentNearCapChangeGetsSplitNudge(t *testing.T) {
	p, repo, bare, env := stackSizeFixture(t, 4)
	ctx := context.Background()

	writeN(repo, "a", 3) // 3 > 4/2
	repo.Commit("chunky\n\nChange-Id: I1111111111111111111111111111111111111111")
	oldSHA, tip := pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, env)
	if !result.Accepted {
		t.Fatalf("under-cap change must be accepted: %+v", result)
	}
	if !strings.Contains(result.Message, "consider splitting into a stack") {
		t.Fatalf("accepted push must carry the split nudge, got:\n%s", result.Message)
	}

	// A comfortably small change earns no nudge - the note must stay rare
	// enough to mean something. (Fresh fixture: a stack that still CARRIES
	// the chunky change above keeps nagging, correctly - the series is
	// re-measured whole on every push.)
	p2, repo2, bare2, env2 := stackSizeFixture(t, 4)
	writeN(repo2, "b", 1)
	repo2.Commit("tiny\n\nChange-Id: I2222222222222222222222222222222222222222")
	prev, tip2 := pushCommit(t, repo2, bare2, "refs/for/main")
	result = p2.Process(ctx, RefUpdate{OldSHA: prev, NewSHA: tip2, Ref: "refs/for/main"}, env2)
	if !result.Accepted {
		t.Fatalf("tiny change: %+v", result)
	}
	if strings.Contains(result.Message, "consider splitting") {
		t.Fatalf("a small change must not be nagged, got:\n%s", result.Message)
	}
}

// TestHumanPushesNeverHitPerChangeCaps: caps are §8.7 agent policy - a
// human (or the anonymous deploy token) pushing a big change is governed
// by owners and review, not size caps.
func TestHumanPushesNeverHitPerChangeCaps(t *testing.T) {
	p, repo, bare, _ := stackSizeFixture(t, 3)
	ctx := context.Background()

	writeN(repo, "a", 10)
	repo.Commit("big human change\n\nChange-Id: I1111111111111111111111111111111111111111")
	oldSHA, tip := pushCommit(t, repo, bare, "refs/for/main")

	// No REMOTE_USER: the anonymous deploy token.
	if result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, nil); !result.Accepted {
		t.Fatalf("anonymous push must not hit agent caps: %+v", result)
	}
}

// TestAgentOrthogonalStackGetsParallelBranchNudge: two stacked steps
// touching disjoint top-level directories earn the DAG advisory on the
// accepted push; two steps sharing a directory stay quiet - file overlap
// (or shared area) reads as plausible dependence.
func TestAgentOrthogonalStackGetsParallelBranchNudge(t *testing.T) {
	p, repo, bare, env := stackSizeFixture(t, 10)
	ctx := context.Background()

	writeN(repo, "svc-a", 1)
	repo.Commit("touch service a\n\nChange-Id: I1111111111111111111111111111111111111111")
	writeN(repo, "svc-b", 1)
	repo.Commit("touch service b\n\nChange-Id: I2222222222222222222222222222222222222222")
	oldSHA, tip := pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, env)
	if !result.Accepted {
		t.Fatalf("orthogonal stack must still be ACCEPTED (the nudge is advisory): %+v", result)
	}
	for _, want := range []string{"PARALLEL branches", "I2222222222222222222222222222222222222222", "neither waits"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("accepted push must carry the parallel-branch nudge (%q), got:\n%s", want, result.Message)
		}
	}

	// Dependent shape: both steps inside one directory - no nudge.
	p2, repo2, bare2, env2 := stackSizeFixture(t, 10)
	writeN(repo2, "svc-a", 1)
	repo2.Commit("step one\n\nChange-Id: I1111111111111111111111111111111111111111")
	repo2.WriteFile("svc-a/more.go", "package x\n")
	repo2.Commit("step two, same area\n\nChange-Id: I2222222222222222222222222222222222222222")
	oldSHA2, tip2 := pushCommit(t, repo2, bare2, "refs/for/main")
	result = p2.Process(ctx, RefUpdate{OldSHA: oldSHA2, NewSHA: tip2, Ref: "refs/for/main"}, env2)
	if !result.Accepted {
		t.Fatalf("dependent stack: %+v", result)
	}
	if strings.Contains(result.Message, "PARALLEL branches") {
		t.Fatalf("same-directory steps must not be nagged toward parallelism, got:\n%s", result.Message)
	}
}
