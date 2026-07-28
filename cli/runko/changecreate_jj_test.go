package main

// Tests for createChangeJJ / amendChangeJJ defect fixes. All create/amend
// regression coverage for this workstream lives here (not in jj_test.go),
// including the plain-git AmendChange call-site pins that belong with the
// same Change-Id re-stamp contract.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// dedupChangeIDTrailers must keep the FIRST Change-Id, leave other trailers
// and the body alone, and be a no-op when there is already at most one.
func TestDedupChangeIDTrailers(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "no trailer",
			in:   "just a body\n",
			want: "just a body\n",
		},
		{
			name: "single trailer",
			in:   "body\n\nChange-Id: Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
			want: "body\n\nChange-Id: Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
		},
		{
			name: "duplicate keeps first, preserves other trailers",
			in: "body line\n\n" +
				"Signed-off-by: Alice <a@example.com>\n" +
				"Change-Id: Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
				"Co-Authored-By: Bob <b@example.com>\n" +
				"Change-Id: Icccccccccccccccccccccccccccccccccccccccc\n",
			want: "body line\n\n" +
				"Signed-off-by: Alice <a@example.com>\n" +
				"Change-Id: Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
				"Co-Authored-By: Bob <b@example.com>\n",
		},
		{
			name: "three trailers collapse to first",
			in: "x\n\n" +
				"Change-Id: I1111111111111111111111111111111111111111\n" +
				"Change-Id: I2222222222222222222222222222222222222222\n" +
				"Change-Id: I3333333333333333333333333333333333333333\n",
			want: "x\n\n" +
				"Change-Id: I1111111111111111111111111111111111111111\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dedupChangeIDTrailers(tc.in); got != tc.want {
				t.Fatalf("dedupChangeIDTrailers:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// messageWithPreservedChangeID strips any pasted Change-Id and re-stamps the
// established one — a foreign trailer must never win ParseChangeID.
func TestMessageWithPreservedChangeID(t *testing.T) {
	est := "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	foreign := "Icccccccccccccccccccccccccccccccccccccccc"
	cases := []struct {
		name, msg string
	}{
		{"plain body", "clearer wording"},
		{"body with foreign trailer", "clearer wording\n\nChange-Id: " + foreign + "\n"},
		{"body with same trailer", "clearer wording\n\nChange-Id: " + est + "\n"},
		{"body keeps other trailers", "clearer wording\n\nSigned-off-by: Alice <a@example.com>\nChange-Id: " + foreign + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := messageWithPreservedChangeID(tc.msg, est)
			if n := strings.Count(got, "Change-Id:"); n != 1 {
				t.Fatalf("want exactly one Change-Id, got %d in:\n%s", n, got)
			}
			if id, ok := receive.ParseChangeID(got); !ok || id != est {
				t.Fatalf("ParseChangeID: got %q ok=%v, want %s\nmsg:\n%s", id, ok, est, got)
			}
			if strings.Contains(got, foreign) {
				t.Fatalf("foreign Change-Id leaked into result:\n%s", got)
			}
			if !strings.Contains(got, "clearer wording") {
				t.Fatalf("body lost:\n%s", got)
			}
		})
	}
}

// DEFECT 1: squash into a parent whose description already carries a
// git-minted Change-Id APPENDS the jj-derived trailer. amend must leave
// exactly one (the first) and keep unrelated trailers intact.
func TestAmendChangeJJDedupsDuplicateChangeIDTrailers(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	if _, err := CreateChange(dir, "first cut", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	gitMinted := "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	planted := "first cut\n\n" +
		"Signed-off-by: Alice <alice@example.com>\n" +
		"Change-Id: " + gitMinted + "\n" +
		"Co-Authored-By: Bob <bob@example.com>\n"
	if _, err := runJJ(dir, "--config", "templates.commit_trailers=\"\"",
		"describe", "-r", "@-", "-m", planted); err != nil {
		t.Fatalf("plant git-minted description: %v", err)
	}
	before, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(before, "Change-Id:"); n != 1 {
		t.Fatalf("setup should plant exactly one Change-Id, got %d in:\n%s", n, before)
	}

	writeTestFile(t, dir, "proj/a.txt", "v2\n")
	id, err := AmendChange(dir, "", false)
	if err != nil {
		t.Fatalf("AmendChange: %v", err)
	}
	if id != gitMinted {
		t.Fatalf("amend must keep the FIRST (git-minted) Change-Id: got %s want %s", id, gitMinted)
	}

	after, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(after, "Change-Id:"); n != 1 {
		t.Fatalf("want exactly one Change-Id trailer after amend, got %d in:\n%s", n, after)
	}
	if got, ok := receive.ParseChangeID(after); !ok || got != gitMinted {
		t.Fatalf("ParseChangeID after amend: got %q ok=%v, want %s", got, ok, gitMinted)
	}
	for _, keep := range []string{
		"Signed-off-by: Alice <alice@example.com>",
		"Co-Authored-By: Bob <bob@example.com>",
		"first cut",
	} {
		if !strings.Contains(after, keep) {
			t.Fatalf("dedup mangled non-Change-Id content; missing %q in:\n%s", keep, after)
		}
	}
}

// DEFECT 1 (reword path): `jj describe -m` discards the old Change-Id and the
// template stamps a jj-derived one. A git-minted id must survive reword, with
// exactly one trailer.
func TestAmendChangeJJRewordPreservesGitMintedChangeID(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	if _, err := CreateChange(dir, "original wording", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	gitMinted := "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	planted := "original wording\n\n" +
		"Signed-off-by: Alice <alice@example.com>\n" +
		"Change-Id: " + gitMinted + "\n" +
		"Co-Authored-By: Bob <bob@example.com>\n"
	if _, err := runJJ(dir, "--config", "templates.commit_trailers=\"\"",
		"describe", "-r", "@-", "-m", planted); err != nil {
		t.Fatalf("plant git-minted description: %v", err)
	}
	before, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := receive.ParseChangeID(before); !ok || got != gitMinted {
		t.Fatalf("setup: want planted id %s, got %q ok=%v", gitMinted, got, ok)
	}

	// Message-only reword (empty @) — the path that previously rewrote the id.
	amended, err := AmendChange(dir, "clearer wording", false)
	if err != nil {
		t.Fatalf("AmendChange reword: %v", err)
	}
	if amended != gitMinted {
		t.Fatalf("reword CHANGED the Change-Id: %s -> %s (must preserve git-minted identity)", gitMinted, amended)
	}

	after, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(after, "Change-Id:"); n != 1 {
		t.Fatalf("want exactly one Change-Id after reword, got %d in:\n%s", n, after)
	}
	if got, ok := receive.ParseChangeID(after); !ok || got != gitMinted {
		t.Fatalf("ParseChangeID after reword: got %q ok=%v, want %s\n%s", got, ok, gitMinted, after)
	}
	if !strings.Contains(after, "clearer wording") {
		t.Fatalf("reword did not take; description was:\n%s", after)
	}
	// A full -m reword replaces the body (same as git commit --amend -m);
	// prior Signed-off-by lines are not carried unless the caller re-supplies
	// them. Identity is what this defect protects.
}

// DEFECT 1 (foreign trailer in -m): a pasted Change-Id different from the
// established one must not win. Silently honouring it opens a duplicate Change.
func TestAmendChangeJJRewordIgnoresForeignChangeIDInMessage(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	established, err := CreateChange(dir, "original wording", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	foreign := "Icccccccccccccccccccccccccccccccccccccccc"
	if foreign == established {
		t.Fatal("test setup: foreign id collided with established")
	}

	msg := "clearer wording\n\nChange-Id: " + foreign + "\n"
	amended, err := AmendChange(dir, msg, false)
	if err != nil {
		t.Fatalf("AmendChange reword with foreign trailer: %v", err)
	}
	if amended != established {
		t.Fatalf("reword must keep established id, not foreign: got %s want %s (foreign was %s)",
			amended, established, foreign)
	}
	after, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(after, "Change-Id:"); n != 1 {
		t.Fatalf("want exactly one Change-Id, got %d in:\n%s", n, after)
	}
	if strings.Contains(after, foreign) {
		t.Fatalf("foreign Change-Id must not appear in description:\n%s", after)
	}
	if got, ok := receive.ParseChangeID(after); !ok || got != established {
		t.Fatalf("ParseChangeID: got %q ok=%v, want %s", got, ok, established)
	}
}

// Same foreign-id rule on a git-minted parent (reword path that also plants
// the established id via messageWithPreservedChangeID).
func TestAmendChangeJJRewordGitMintedIgnoresForeignChangeID(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	if _, err := CreateChange(dir, "original", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	gitMinted := "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := runJJ(dir, "--config", "templates.commit_trailers=\"\"",
		"describe", "-r", "@-", "-m", "original\n\nChange-Id: "+gitMinted+"\n"); err != nil {
		t.Fatalf("plant: %v", err)
	}
	foreign := "Icccccccccccccccccccccccccccccccccccccccc"
	amended, err := AmendChange(dir, "reworded\n\nChange-Id: "+foreign+"\n", false)
	if err != nil {
		t.Fatalf("AmendChange: %v", err)
	}
	if amended != gitMinted {
		t.Fatalf("got %s, want established git-minted %s", amended, gitMinted)
	}
}

// DEFECT 2: mid-stack jj edit leaves @ as an established Change. change
// create must refuse (not reword + jj new, which forks the stack and drops
// descendants from the tip push). Suggestion must NOT name `change amend`
// (that would squash this Change into its parent and destroy it).
func TestCreateChangeJJRefusesEstablishedWorkingCopy(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	// Linear 3-change stack via create (the happy path that must keep working).
	for i, name := range []string{"change 1", "change 2", "change 3"} {
		writeTestFile(t, dir, "proj/f"+string(rune('1'+i))+".txt", name+"\n")
		if _, err := CreateChange(dir, name, false); err != nil {
			t.Fatalf("CreateChange %q: %v", name, err)
		}
	}
	headsBefore, err := runJJ(dir, "log", "--no-graph", "-r", "heads(all())", "-T", `change_id.short() ++ "\n"`)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(headsBefore)); n != 1 {
		t.Fatalf("setup: want one head after linear creates, got %d: %q", n, headsBefore)
	}

	// Surgical mid-stack edit: @ becomes change 2 (described, has children).
	if _, err := runJJ(dir, "edit", `description(glob:"change 2*")`); err != nil {
		t.Fatalf("jj edit change 2: %v", err)
	}
	writeTestFile(t, dir, "proj/f2.txt", "change 2 reworked\n")

	// Capture change 2's identity before the refused create — must survive.
	id2Before, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	change2ID, ok := receive.ParseChangeID(id2Before)
	if !ok {
		t.Fatalf("change 2 should carry a Change-Id, description:\n%s", id2Before)
	}

	_, err = CreateChange(dir, "change 2 reworked", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_a_change" {
		t.Fatalf("want already_a_change, got %v", err)
	}
	if strings.Contains(ce.Suggestion, "runko change amend") {
		t.Fatalf("suggestion must NOT recommend amend (destructive mid-stack); got %q", ce.Suggestion)
	}
	if !strings.Contains(ce.Suggestion, "runko change push") {
		t.Fatalf("suggestion must recommend push (edits already snapshotted); got %q", ce.Suggestion)
	}

	// Stack must still be one head; change 2's description and id untouched.
	headsAfter, err := runJJ(dir, "log", "--no-graph", "-r", "heads(all())", "-T", `change_id.short() ++ "\n"`)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(headsAfter)); n != 1 {
		t.Fatalf("refused create must not fork the stack; heads=%d: %q", n, headsAfter)
	}
	desc2, err := runJJ(dir, "log", "--no-graph", "-r", `description(glob:"change 2*")`, "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(desc2, "reworked") {
		t.Fatalf("refused create must not reword change 2; description was:\n%s", desc2)
	}
	if got, ok := receive.ParseChangeID(desc2); !ok || got != change2ID {
		t.Fatalf("change 2 identity must survive refusal: got %q want %s", got, change2ID)
	}
	// change 3 still reachable as a descendant of change 2.
	if _, err := runJJ(dir, "log", "--no-graph", "-r", `description(glob:"change 3*")`, "-T", "description"); err != nil {
		t.Fatalf("change 3 should still exist after the refusal: %v", err)
	}
}

// DEFECT 2 (amend side): mid-stack amend must refuse rather than squash the
// established @ into its parent (which annihilates the upper Change).
func TestAmendChangeJJRefusesEstablishedWorkingCopy(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	ids := make([]string, 0, 3)
	for i, name := range []string{"change 1", "change 2", "change 3"} {
		writeTestFile(t, dir, "proj/f"+string(rune('1'+i))+".txt", name+"\n")
		id, err := CreateChange(dir, name, false)
		if err != nil {
			t.Fatalf("CreateChange %q: %v", name, err)
		}
		ids = append(ids, id)
	}

	if _, err := runJJ(dir, "edit", `description(glob:"change 2*")`); err != nil {
		t.Fatalf("jj edit change 2: %v", err)
	}
	writeTestFile(t, dir, "proj/f2.txt", "change 2 reworked\n")

	_, err := AmendChange(dir, "", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_a_change" {
		t.Fatalf("want already_a_change from amend mid-stack, got %v", err)
	}
	if strings.Contains(ce.Suggestion, "runko change amend") {
		t.Fatalf("suggestion must not re-recommend amend; got %q", ce.Suggestion)
	}
	if !strings.Contains(ce.Suggestion, "runko change push") {
		t.Fatalf("suggestion must recommend push; got %q", ce.Suggestion)
	}

	// All three Changes still present with their original ids.
	for i, name := range []string{"change 1", "change 2", "change 3"} {
		desc, err := runJJ(dir, "log", "--no-graph", "-r", `description(glob:"`+name+`*")`, "-T", "description")
		if err != nil {
			t.Fatalf("%s missing after refused amend: %v", name, err)
		}
		got, ok := receive.ParseChangeID(desc)
		if !ok || got != ids[i] {
			t.Fatalf("%s id after refused amend: got %q want %s", name, got, ids[i])
		}
	}
	// Edits remain on change 2 (jj auto-snapshot) — nothing was folded away.
	summary, err := runJJ(dir, "diff", "-r", `description(glob:"change 2*")`, "--summary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "f2.txt") {
		t.Fatalf("change 2 should still hold the reworked file; diff: %q", summary)
	}
}

// Happy-path amend (empty undescribed @) must stay non-interactive when every
// editor channel is broken — agent containers and CI have no usable TTY.
//
// --use-destination-message on squash is defensive: a described source would
// open an editor, but already_a_change refuses that shape first, so removing
// the flag still leaves this test green. This test therefore pins the allowed
// path under EDITOR=false, not the flag in isolation.
func TestAmendChangeJJNeverOpensEditor(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	id, err := CreateChange(dir, "step 1", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	// Break every editor channel jj consults.
	t.Setenv("EDITOR", "false")
	t.Setenv("VISUAL", "false")
	t.Setenv("JJ_EDITOR", "false")

	writeTestFile(t, dir, "proj/a.txt", "v2\n")
	amended, err := AmendChange(dir, "", false)
	if err != nil {
		t.Fatalf("AmendChange with EDITOR=false must not fail (would mean an editor was invoked): %v", err)
	}
	if amended != id {
		t.Fatalf("identity drifted: %s -> %s", id, amended)
	}

	// Message-only reword also non-interactive.
	amended, err = AmendChange(dir, "step 1 reworded", false)
	if err != nil {
		t.Fatalf("reword with EDITOR=false: %v", err)
	}
	if amended != id {
		t.Fatalf("reword identity drifted: %s -> %s", id, amended)
	}
}

// A clean linear create/create/create stack still builds one head - the
// refusal must not fire on the empty undescribed @ that create parks.
func TestCreateChangeJJLinearStackOneHead(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	ids := make([]string, 0, 3)
	for i, name := range []string{"step one", "step two", "step three"} {
		writeTestFile(t, dir, "proj/s"+string(rune('1'+i))+".txt", name+"\n")
		id, err := CreateChange(dir, name, false)
		if err != nil {
			t.Fatalf("CreateChange %q: %v", name, err)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatalf("each create must mint a distinct Change-Id: %v", ids)
	}
	heads, err := runJJ(dir, "log", "--no-graph", "-r", "heads(all())", "-T", `change_id.short() ++ "\n"`)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(heads)); n != 1 {
		t.Fatalf("linear creates must leave one head, got %d: %q", n, heads)
	}
	// @ is empty and undescribed - ready for the next create or a push.
	if empty, _ := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty"); strings.TrimSpace(empty) != "true" {
		t.Fatal("after create, @ should be empty")
	}
	if desc, _ := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "description"); strings.TrimSpace(desc) != "" {
		t.Fatalf("after create, @ should be undescribed, got %q", desc)
	}
}

// DEFECT 3: change amend must accept --allow-large and actually land a file
// over jj's 1MiB snapshot cap (not merely skip the guard). The load-bearing
// claim is that the file is IN the amended commit; which intermediate jj
// call first raises the cap is an implementation detail (once snapshotted,
// later commands see the file without the raised cap).
func TestAmendChangeJJAllowLargeLandsFile(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "seed\n")
	id, err := CreateChange(dir, "step 1", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}

	// Over jj's 1MiB default, under runko's 5MiB artifact heuristic.
	writeTestFile(t, dir, "fixture.bin", strings.Repeat("x", 2<<20))

	_, err = AmendChange(dir, "", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "suspect_artifact" {
		t.Fatalf("without --allow-large want suspect_artifact, got %v", err)
	}
	if !strings.Contains(ce.Message, "fixture.bin") {
		t.Fatalf("error should name fixture.bin, got %q", ce.Message)
	}
	if !strings.Contains(ce.Suggestion, "--allow-large") {
		t.Fatalf("suggestion should name --allow-large, got %q", ce.Suggestion)
	}

	amended, err := AmendChange(dir, "", true)
	if err != nil {
		t.Fatalf("AmendChange --allow-large: %v", err)
	}
	if amended != id {
		t.Fatalf("allow-large amend must keep Change-Id: %s -> %s", id, amended)
	}
	// Prove the file is IN the amended commit, not merely past the guard.
	summary, err := runJJ(dir, "--config", "snapshot.max-new-file-size=1073741824",
		"diff", "-r", "@-", "--summary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "fixture.bin") {
		t.Fatalf("--allow-large did not land fixture.bin in the change; diff was %q", summary)
	}
	// Size check via git (colocated) so we don't fight jj fileset path rules.
	tip, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatal(err)
	}
	sizeOut, err := runGit(dir, "cat-file", "-s", tip+":fixture.bin")
	if err != nil {
		t.Fatalf("fixture.bin missing from amended commit: %v", err)
	}
	var size int
	if _, err := fmt.Sscan(strings.TrimSpace(sizeOut), &size); err != nil || size != 2<<20 {
		t.Fatalf("fixture.bin size in commit: got %q (%v), want %d", sizeOut, err, 2<<20)
	}
}

// plantLocalOriginMain points refs/remotes/origin/main at sha — the offline
// trunk tip refuseAmendOnTrunk / statusStack consult. No network.
func plantLocalOriginMain(t *testing.T, dir, sha string) {
	t.Helper()
	if _, err := runGit(dir, "update-ref", "refs/remotes/origin/main", sha); err != nil {
		t.Fatalf("plant refs/remotes/origin/main: %v", err)
	}
}

// DEFECT 1: post-attach shape is empty undescribed @ on the landed trunk tip.
// @- still carries a Change-Id, so no_change_id does not fire — amend must
// still refuse rather than rewrite shared trunk history.
func TestAmendChangeJJRefusesTrunkParent(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "README.md", "landed\n")
	// Landed trunk commit with a Change-Id (what a landed Change leaves).
	if _, err := runJJ(dir, "commit", "-m", "landed trunk commit"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}
	trunkSHA, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatal(err)
	}
	trunkSHA = strings.TrimSpace(trunkSHA)
	trunkDesc, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	trunkID, ok := receive.ParseChangeID(trunkDesc)
	if !ok {
		t.Fatalf("setup: trunk commit needs a Change-Id, got:\n%s", trunkDesc)
	}
	plantLocalOriginMain(t, dir, trunkSHA)

	// @ is empty undescribed on trunk — normal post-attach state.
	if empty, _ := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty"); strings.TrimSpace(empty) != "true" {
		t.Fatal("setup: @ should be empty")
	}
	if desc, _ := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "description"); strings.TrimSpace(desc) != "" {
		t.Fatalf("setup: @ should be undescribed, got %q", desc)
	}

	_, err = AmendChange(dir, "oops I meant create", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_on_trunk" {
		t.Fatalf("want already_on_trunk, got %v", err)
	}
	if !strings.Contains(ce.Suggestion, "runko change create") {
		t.Fatalf("suggestion must name change create (safe in this state); got %q", ce.Suggestion)
	}
	if strings.Contains(ce.Suggestion, "runko change amend") {
		t.Fatalf("suggestion must not re-recommend amend; got %q", ce.Suggestion)
	}

	// Trunk commit must be untouched (message and id).
	after, err := runJJ(dir, "log", "--no-graph", "-r", trunkSHA, "-T", "description")
	if err != nil {
		// commit_id may have moved if amend rewrote; fall back to origin/main tip.
		after, err = runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
		if err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(after, "oops I meant create") {
		t.Fatalf("amend rewrote the trunk commit message:\n%s", after)
	}
	if got, ok := receive.ParseChangeID(after); !ok || got != trunkID {
		t.Fatalf("trunk Change-Id must survive refused amend: got %q want %s", got, trunkID)
	}
}

// DEFECT 1 (happy path still works): open work above trunk amends fine when
// origin/main is planted at the real trunk base.
func TestAmendChangeJJAboveTrunkStillAmends(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "README.md", "trunk\n")
	if _, err := runJJ(dir, "commit", "-m", "trunk base"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	trunkSHA, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatal(err)
	}
	plantLocalOriginMain(t, dir, strings.TrimSpace(trunkSHA))

	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	id, err := CreateChange(dir, "open work", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	writeTestFile(t, dir, "proj/a.txt", "v2\n")
	amended, err := AmendChange(dir, "open work, clearer", false)
	if err != nil {
		t.Fatalf("AmendChange above trunk must succeed: %v", err)
	}
	if amended != id {
		t.Fatalf("identity drifted: %s -> %s", id, amended)
	}
}

// DEFECT 2: plain-git AmendChange must re-stamp via messageWithPreservedChangeID
// at the call site. A pure-helper test is not enough — mutating the git path
// to `message+"\n\nChange-Id: "+id` still passes every other test.
func TestAmendChangePlainGitPreservesChangeIDOnReword(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("proj/a.go", "package proj\n")
	repo.Commit("seed")

	repo.WriteFile("proj/a.go", "package proj // v1\n")
	established, err := CreateChange(repo.Dir, "feature", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}

	// Plain -m: identity preserved, exactly one trailer.
	got, err := AmendChange(repo.Dir, "feature reworded", false)
	if err != nil {
		t.Fatalf("AmendChange plain -m: %v", err)
	}
	if got != established {
		t.Fatalf("plain -m must keep Change-Id: got %s want %s", got, established)
	}
	msg := mustGit(t, repo.Dir, "log", "-1", "--format=%B")
	if n := strings.Count(msg, "Change-Id:"); n != 1 {
		t.Fatalf("want exactly one Change-Id after plain -m, got %d in:\n%s", n, msg)
	}
	if id, ok := receive.ParseChangeID(msg); !ok || id != established {
		t.Fatalf("ParseChangeID after plain -m: got %q ok=%v, want %s", id, ok, established)
	}
	if !strings.Contains(msg, "feature reworded") {
		t.Fatalf("reword body missing:\n%s", msg)
	}

	// -m carrying a foreign Change-Id: established must win (first trailer
	// after strip+re-stamp), not the pasted one.
	foreign := "Icccccccccccccccccccccccccccccccccccccccc"
	if foreign == established {
		t.Fatal("test setup: foreign id collided with established")
	}
	got, err = AmendChange(repo.Dir, "feature again\n\nChange-Id: "+foreign+"\n", false)
	if err != nil {
		t.Fatalf("AmendChange foreign -m: %v", err)
	}
	if got != established {
		t.Fatalf("foreign -m must keep established id, not foreign: got %s want %s (foreign %s)",
			got, established, foreign)
	}
	msg = mustGit(t, repo.Dir, "log", "-1", "--format=%B")
	if n := strings.Count(msg, "Change-Id:"); n != 1 {
		t.Fatalf("want exactly one Change-Id after foreign -m, got %d in:\n%s", n, msg)
	}
	if strings.Contains(msg, foreign) {
		t.Fatalf("foreign Change-Id leaked into amended message:\n%s", msg)
	}
	if id, ok := receive.ParseChangeID(msg); !ok || id != established {
		t.Fatalf("ParseChangeID after foreign -m: got %q ok=%v, want %s", id, ok, established)
	}
}

// DEFECT 2 (plain-git trunk): same landed-tip footgun as the jj path.
func TestAmendChangePlainGitRefusesTrunkHEAD(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "landed\n")
	// Commit with a Change-Id trailer so headChangeID would otherwise pass.
	landedID := "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	repo.Commit("landed trunk\n\nChange-Id: " + landedID + "\n")
	head := repo.Head()
	plantLocalOriginMain(t, repo.Dir, head)

	_, err := AmendChange(repo.Dir, "oops I meant create", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_on_trunk" {
		t.Fatalf("want already_on_trunk, got %v", err)
	}
	if !strings.Contains(ce.Suggestion, "runko change create") {
		t.Fatalf("suggestion must name change create; got %q", ce.Suggestion)
	}
	msg := mustGit(t, repo.Dir, "log", "-1", "--format=%B")
	if strings.Contains(msg, "oops I meant create") {
		t.Fatalf("amend rewrote trunk HEAD message:\n%s", msg)
	}
	if repo.Head() != head {
		t.Fatalf("amend rewrote trunk HEAD: %s -> %s", head, repo.Head())
	}
}

// DEFECT 3: createChangeJJ must strip a Change-Id from the user's -m so the
// template stamps exactly one trailer (the jj-derived id), not two.
func TestCreateChangeJJStripsForeignChangeIDInMessage(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	writeTestFile(t, dir, "proj/a.txt", "work\n")
	foreign := "Idddddddddddddddddddddddddddddddddddddddd"
	id, err := CreateChange(dir, "new work\n\nChange-Id: "+foreign+"\n", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if id == foreign {
		t.Fatalf("create must not adopt the foreign Change-Id; got %s", id)
	}
	desc, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(desc, "Change-Id:"); n != 1 {
		t.Fatalf("want exactly one Change-Id after create, got %d in:\n%s", n, desc)
	}
	if strings.Contains(desc, foreign) {
		t.Fatalf("foreign Change-Id must not appear in description:\n%s", desc)
	}
	if got, ok := receive.ParseChangeID(desc); !ok || got != id {
		t.Fatalf("ParseChangeID: got %q ok=%v, want %s", got, ok, id)
	}
	if !strings.Contains(desc, "new work") {
		t.Fatalf("body lost:\n%s", desc)
	}
}

// DEFECT 4: described @ whose parent has no Change-Id must report
// already_a_change (push), not no_change_id (create) — create would then
// report already_a_change and close a suggestion loop.
func TestAmendChangeJJDescribedAtBeatsNoChangeID(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)

	// Trunk commit with NO Change-Id.
	writeTestFile(t, dir, "README.md", "trunk\n")
	if _, err := runJJ(dir, "--config", `templates.commit_trailers=""`,
		"commit", "-m", "trunk no id"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}
	// Describe @ with work — established Change sitting on a no-id parent.
	writeTestFile(t, dir, "proj/a.txt", "edit\n")
	if _, err := runJJ(dir, "describe", "-m", "my work"); err != nil {
		t.Fatalf("describe @: %v", err)
	}

	_, err := AmendChange(dir, "", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_a_change" {
		t.Fatalf("want already_a_change (not no_change_id), got %v", err)
	}
	if !strings.Contains(ce.Suggestion, "runko change push") {
		t.Fatalf("suggestion must recommend push; got %q", ce.Suggestion)
	}
	if strings.Contains(ce.Suggestion, "runko change create") {
		t.Fatalf("suggestion must not recommend create (loops with already_a_change); got %q", ce.Suggestion)
	}
}

// The build-artifact guard resolved jj's root-relative diff paths against a
// caller-supplied repoDir, so from a SUBDIRECTORY every os.Stat missed, every
// candidate was skipped, and the guard silently reported a clean tree. Third
// instance of that path-normalization class in this migration, and the only
// one that shipped with no test at all - hence this one.
func TestSuspectArtifactsJJFromSubdirectory(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "sub/deep/keep.txt", "source\n")
	// Executable + binary, and comfortably UNDER jj's 1MiB snapshot cap, so
	// only this guard can catch it - jjSnapshotRefusals cannot.
	writeTestFile(t, dir, "tools/prog", "ELF\x00\x00\x00binary junk\n")
	if err := os.Chmod(filepath.Join(dir, "tools/prog"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The subdirectory is the whole point: repoDir is NOT the repo root.
	sub := filepath.Join(dir, "sub", "deep")
	_, err := CreateChange(sub, "should refuse the binary", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "suspect_artifact" {
		t.Fatalf("want suspect_artifact from a subdirectory, got %v", err)
	}
	if !strings.Contains(ce.Message, "tools/prog") {
		t.Fatalf("error should name the artifact, got %q", ce.Message)
	}
}

// `change amend` runs the same artifact heuristic as create. Guarding only
// create let a stray binary fold into the change on the second step, which is
// the step agents reach for most.
func TestAmendChangeJJRefusesArtifacts(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	if _, err := CreateChange(dir, "step 1", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	writeTestFile(t, dir, "tools/prog", "ELF\x00\x00\x00binary junk\n")
	if err := os.Chmod(filepath.Join(dir, "tools/prog"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := AmendChange(dir, "", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "suspect_artifact" {
		t.Fatalf("want suspect_artifact from amend, got %v", err)
	}
}

// refuseAmendOnTrunk read a hardcoded refs/remotes/origin/main, so any repo
// whose trunk is not main or whose remote is not origin got no guard at all -
// while the contract promised <remote>/<trunk>. Both axes are covered here.
func TestAmendChangeJJRefusesTrunkParentNonDefaultRemoteAndTrunk(t *testing.T) {
	requireJJ(t)
	for _, tc := range []struct{ name, remote, trunk string }{
		{"non-default trunk", "origin", "master"},
		{"non-default remote", "upstream", "main"},
		{"both non-default", "upstream", "develop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := jjTrailerRepo(t)
			writeTestFile(t, dir, "proj/a.txt", "landed\n")
			if _, err := CreateChange(dir, "landed work", false); err != nil {
				t.Fatalf("seed: %v", err)
			}
			landed, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
			if err != nil {
				t.Fatal(err)
			}
			// Plant the tracking ref the checkout's binding names.
			if _, err := runGit(dir, "update-ref",
				"refs/remotes/"+tc.remote+"/"+tc.trunk, strings.TrimSpace(landed)); err != nil {
				t.Fatal(err)
			}
			if _, err := runGit(dir, "config", "runko.trunk", tc.trunk); err != nil {
				t.Fatal(err)
			}
			_, err = AmendChange(dir, "rewrite a landed commit", false)
			var ce *clierr.Error
			if !errors.As(err, &ce) || ce.Code != "already_on_trunk" {
				t.Fatalf("want already_on_trunk for %s/%s, got %v", tc.remote, tc.trunk, err)
			}
		})
	}
}

// A -m consisting only of a pasted trailer strips to an empty body. Without a
// guard jj stamps no trailer and the failure surfaces as "jj stamped no
// Change-Id trailer" in a repo whose template is fine - a diagnosis that sends
// the user to fix something that is not broken.
func TestCreateChangeJJRefusesTrailerOnlyMessage(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "work\n")
	_, err := CreateChange(dir, "Change-Id: Idddddddddddddddddddddddddddddddddddddddd", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "missing_message" {
		t.Fatalf("want missing_message, got %v", err)
	}
}
