package runkod

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	runkov1 "github.com/saxocellphone/runko/proto/gen/runko/v1"
)

// historyFixture builds a bare repo with three commits and one known
// Change row:
//
//	c1 "first"  (Change-Id Iaaa..., NO store row)  a.txt l1-l3, sub/keep.txt
//	c2 "second" (Change-Id Ibbb..., open Change)   a.txt l3 modified + l4
//	c3 "rename" (no trailer)                        a.txt -> renamed.txt
func historyFixture(t *testing.T) (*rpcServer, [3]string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)

	repo.WriteFile("a.txt", "l1\nl2\nl3\n")
	repo.WriteFile("sub/keep.txt", "kept\n")
	c1 := repo.Commit("first: add a.txt\n\nChange-Id: " + changeIDA)
	repo.WriteFile("a.txt", "l1\nl2\nl3-changed\nl4\n")
	c2 := repo.Commit("second: touch a.txt\n\nChange-Id: " + changeIDB)
	repo.Run("mv a.txt renamed.txt")
	c3 := repo.Commit("rename a.txt")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	if _, err := store.CreateOrUpdateChange(context.Background(), changeIDB,
		c1, c2, "refs/changes/x/head", "second: touch a.txt", "alice", "", ""); err != nil {
		t.Fatalf("seed change: %v", err)
	}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: newTestProcessor(bare, store), Token: "sekret"}
	return &rpcServer{s: srv}, [3]string{c1, c2, c3}
}

const (
	changeIDA = "Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	changeIDB = "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestListCommitsHistory(t *testing.T) {
	rpc, shas := historyFixture(t)
	ctx := context.Background()

	// Whole-repo history, newest first, Change enrichment on the row
	// whose Change exists (c2) and bare trailer on the one whose doesn't.
	resp, err := rpc.ListCommits(ctx, connect.NewRequest(&runkov1.ListCommitsRequest{}))
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	got := resp.Msg.Commits
	if len(got) != 3 || got[0].Sha != shas[2] || got[2].Sha != shas[0] {
		t.Fatalf("expected [c3 c2 c1], got %+v", got)
	}
	if got[1].ChangeId != changeIDB || got[1].ChangeState != runkov1.ChangeState_CHANGE_STATE_OPEN {
		t.Fatalf("c2 should carry its open Change: %+v", got[1])
	}
	if got[2].ChangeId != changeIDA || got[2].ChangeState != runkov1.ChangeState_CHANGE_STATE_UNSPECIFIED {
		t.Fatalf("c1 should carry a bare trailer, no state: %+v", got[2])
	}
	if got[0].ChangeId != "" {
		t.Fatalf("c3 has no trailer: %+v", got[0])
	}
	if got[1].AuthorName == "" || got[1].AuthoredAt == 0 {
		t.Fatalf("author metadata missing: %+v", got[1])
	}
	if got[1].CommittedAt == 0 {
		t.Fatalf("committer time missing: %+v", got[1])
	}
	if got[1].LandedAt != 0 {
		t.Fatalf("an OPEN change must not carry a landing time: %+v", got[1])
	}

	// Path scoping: sub/ was only ever touched by c1.
	resp, err = rpc.ListCommits(ctx, connect.NewRequest(&runkov1.ListCommitsRequest{Path: "sub"}))
	if err != nil || len(resp.Msg.Commits) != 1 || resp.Msg.Commits[0].Sha != shas[0] {
		t.Fatalf("sub/ history should be exactly c1: %v %+v", err, resp.Msg.Commits)
	}

	// Rename following: the renamed file's history reaches through c3
	// back to c1 (plain `git log -- renamed.txt` would stop at c3).
	resp, err = rpc.ListCommits(ctx, connect.NewRequest(&runkov1.ListCommitsRequest{Path: "renamed.txt"}))
	if err != nil {
		t.Fatalf("ListCommits(renamed.txt): %v", err)
	}
	if len(resp.Msg.Commits) != 3 {
		t.Fatalf("rename following should surface all 3 commits, got %d", len(resp.Msg.Commits))
	}

	// Pagination: one per page, token walks to the end.
	var pages [][]*runkov1.CommitInfo
	token := ""
	for {
		resp, err := rpc.ListCommits(ctx, connect.NewRequest(&runkov1.ListCommitsRequest{PageSize: 1, PageToken: token}))
		if err != nil {
			t.Fatalf("paged ListCommits: %v", err)
		}
		pages = append(pages, resp.Msg.Commits)
		if resp.Msg.NextPageToken == "" {
			break
		}
		token = resp.Msg.NextPageToken
	}
	if len(pages) != 3 || len(pages[0]) != 1 || pages[0][0].Sha != shas[2] {
		t.Fatalf("pagination should yield 3 single-commit pages: %+v", pages)
	}
}

// TestListCommitsLandedTime pins finding #43's fix: history rows carry the
// Change's server-clock landing time (the display time - author dates go
// backwards along a rebase-landed trunk, committer dates on fast-forward
// lands are client-stamped), and rows without a landed Change stay 0 so
// the client falls back to committer time.
func TestListCommitsLandedTime(t *testing.T) {
	rpc, shas := historyFixture(t)
	ctx := context.Background()

	if _, err := rpc.s.Store.MarkChangeLanded(ctx, changeIDB, shas[1], "alice", false); err != nil {
		t.Fatalf("MarkChangeLanded: %v", err)
	}
	resp, err := rpc.ListCommits(ctx, connect.NewRequest(&runkov1.ListCommitsRequest{}))
	if err != nil {
		t.Fatalf("ListCommits: %v", err)
	}
	got := resp.Msg.Commits
	if len(got) != 3 {
		t.Fatalf("want 3 commits, got %d", len(got))
	}
	if got[1].LandedAt == 0 {
		t.Fatalf("landed change's history row must carry landed_at: %+v", got[1])
	}
	if got[0].LandedAt != 0 || got[2].LandedAt != 0 {
		t.Fatalf("rows without a landed Change row must stay 0: %+v %+v", got[0], got[2])
	}
}

func TestBlameFileRegions(t *testing.T) {
	rpc, shas := historyFixture(t)
	resp, err := rpc.BlameFile(context.Background(), connect.NewRequest(&runkov1.BlameFileRequest{Path: "renamed.txt"}))
	if err != nil {
		t.Fatalf("BlameFile: %v", err)
	}
	msg := resp.Msg
	if len(msg.Lines) != 4 || msg.Lines[2] != "l3-changed" {
		t.Fatalf("blame lines wrong: %q", msg.Lines)
	}
	// l1-l2 from c1, l3-l4 from c2; the rename commit owns no lines.
	if len(msg.Regions) != 2 {
		t.Fatalf("expected 2 regions, got %+v", msg.Regions)
	}
	r1, r2 := msg.Regions[0], msg.Regions[1]
	if r1.Sha != shas[0] || r1.StartLine != 1 || r1.LineCount != 2 {
		t.Fatalf("region 1: %+v", r1)
	}
	if r2.Sha != shas[1] || r2.StartLine != 3 || r2.LineCount != 2 {
		t.Fatalf("region 2: %+v", r2)
	}
	if r1.ChangeId != changeIDA || r2.ChangeId != changeIDB {
		t.Fatalf("regions should carry Change-Ids: %+v %+v", r1, r2)
	}
	if r2.ChangeState != runkov1.ChangeState_CHANGE_STATE_OPEN {
		t.Fatalf("region 2 should carry the open Change state: %+v", r2)
	}
	if r1.Subject == "" || r1.AuthorName == "" || r1.AuthoredAt == 0 {
		t.Fatalf("region metadata missing: %+v", r1)
	}
}

func TestBlameBinaryFile(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("blob.bin", "PNG\x00\x01\x02")
	repo.Commit("binary")
	pushCommit(t, repo, bare, "refs/heads/main")
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: NewMemStore(), Processor: newTestProcessor(bare, NewMemStore()), Token: "sekret"}
	rpc := &rpcServer{s: srv}

	resp, err := rpc.BlameFile(context.Background(), connect.NewRequest(&runkov1.BlameFileRequest{Path: "blob.bin"}))
	if err != nil {
		t.Fatalf("BlameFile on binary: %v", err)
	}
	if !resp.Msg.Binary || len(resp.Msg.Regions) != 0 {
		t.Fatalf("binary blame should be a binary=true response: %+v", resp.Msg)
	}
}

func TestParseBlamePorcelainMergesGroups(t *testing.T) {
	// Two porcelain groups from the same sha, adjacent -> one region.
	sha := strings.Repeat("ab", 20)
	out := sha + " 1 1 1\nauthor Alice\nauthor-time 1700000000\nsummary do it\n\tline one\n" +
		sha + " 2 2 1\n\tline two\n"
	regions, lines, err := parseBlamePorcelain([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lines) != 2 || lines[1] != "line two" {
		t.Fatalf("lines: %q", lines)
	}
	if len(regions) != 1 || regions[0].StartLine != 1 || regions[0].LineCount != 2 {
		t.Fatalf("adjacent same-sha groups should merge: %+v", regions)
	}
	if regions[0].AuthorName != "Alice" || regions[0].Subject != "do it" || regions[0].AuthoredAt != 1700000000 {
		t.Fatalf("region meta: %+v", regions[0])
	}
}

func TestFirstChangeID(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"Iabc":          "Iabc",
		" Iabc \n":      "Iabc",
		"Iabc,Idef":     "Iabc",
		"  Iabc , Idef": "Iabc",
	} {
		if got := firstChangeID(in); got != want {
			t.Fatalf("firstChangeID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMergeRequirementsStackedBaseBlockers pins the "mergeable while
// unlandable" fix (2026-07-09, found live: abandon a stack's bottom and
// the pending child kept its green chip): a change whose base is not an
// ancestor of trunk is NOT mergeable, and the blocker names the parent
// and the way out per its state. protoChange's base_on_trunk carries the
// same fact to the UI's stack anchors.
func TestMergeRequirementsStackedBaseBlockers(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\n")
	trunkTip := repo.Commit("trunk")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("svc/a.go", "package svc\n")
	parentHead := repo.Commit("parent: add a\n\nChange-Id: " + changeIDA)
	repo.WriteFile("svc/b.go", "package svc\n")
	childHead := repo.Commit("child: add b\n\nChange-Id: " + changeIDB)
	pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	ctx := context.Background()
	seed := func(key, base, head, title string) {
		if _, err := store.CreateOrUpdateChange(ctx, key, base, head, "refs/changes/x", title, "alice", "", ""); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	seed(changeIDA, trunkTip, parentHead, "parent: add a")
	seed(changeIDB, parentHead, childHead, "child: add b")
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: newTestProcessor(bare, store), Token: "t", AllowUnpolicedLand: true}

	requirements := func(key string) checks.MergeRequirements {
		change, _, _ := store.GetChange(ctx, key)
		req, err := srv.mergeRequirements(ctx, key, change, nil)
		if err != nil {
			t.Fatalf("mergeRequirements(%s): %v", key, err)
		}
		return req
	}

	// Parent (trunk-based): mergeable, base_on_trunk true.
	if req := requirements(changeIDA); !req.Mergeable {
		t.Fatalf("trunk-based parent should be mergeable: %+v", req)
	}
	if !srv.protoChange(Change{BaseSHA: trunkTip}).BaseOnTrunk {
		t.Fatalf("trunk-based change should read base_on_trunk")
	}

	// Child of an OPEN parent: blocked, blocker names it + "land it first".
	req := requirements(changeIDB)
	if req.Mergeable {
		t.Fatalf("stacked child should not be mergeable: %+v", req)
	}
	if !strings.Contains(strings.Join(req.Blockers, " "), changeIDA) || !strings.Contains(strings.Join(req.Blockers, " "), "land it first") {
		t.Fatalf("blocker should name the open parent: %v", req.Blockers)
	}
	if srv.protoChange(Change{BaseSHA: parentHead}).BaseOnTrunk {
		t.Fatalf("stacked child must not read base_on_trunk")
	}

	// ABANDONED parent: blocker teaches reopen-or-rebase.
	if _, err := store.MarkChangeAbandoned(ctx, changeIDA); err != nil {
		t.Fatalf("abandon parent: %v", err)
	}
	req = requirements(changeIDB)
	if req.Mergeable || !strings.Contains(strings.Join(req.Blockers, " "), "abandoned") {
		t.Fatalf("abandoned-parent blocker wrong: %+v", req)
	}

	// LANDED (rebased) parent: base still not on trunk - blocker teaches
	// rebase-and-re-push. (Reopen, then mark landed at a DIFFERENT sha.)
	seed(changeIDA, trunkTip, parentHead, "parent: add a") // reopen
	if _, err := store.MarkChangeLanded(ctx, changeIDA, "0000000000000000000000000000000000000001", "alice", false); err != nil {
		t.Fatalf("land parent: %v", err)
	}
	req = requirements(changeIDB)
	if req.Mergeable || !strings.Contains(strings.Join(req.Blockers, " "), "landed as a different commit") {
		t.Fatalf("landed-parent blocker wrong: %+v", req)
	}

	// Unknown base (a real commit, but no Change owns it and trunk does
	// not contain it): generic stranded blocker.
	repo2 := gitfixture.New(t)
	repo2.Run("fetch " + bare + " refs/heads/main")
	repo2.Run("checkout FETCH_HEAD")
	repo2.WriteFile("svc/orphan.go", "package svc\n")
	orphanBase := repo2.Commit("dangling commit, not a change")
	pushCommit(t, repo2, bare, "refs/scratch/orphan")
	seed("Icccc222222222222222222222222222222222222", orphanBase, childHead, "orphan")
	req = requirements("Icccc222222222222222222222222222222222222")
	if req.Mergeable || !strings.Contains(strings.Join(req.Blockers, " "), "rebase onto trunk") {
		t.Fatalf("unknown-base blocker wrong: %+v", req)
	}
}
