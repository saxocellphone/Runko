package runkod

import (
	"context"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/affected"
)

func TestImagesForAffected(t *testing.T) {
	refs := func(names ...string) affected.Result {
		var r affected.Result
		for _, n := range names {
			r.Projects = append(r.Projects, affected.ProjectRef{Name: n})
		}
		return r
	}
	cases := []struct {
		name string
		in   affected.Result
		want []string
	}{
		{"a runkod dep (db) rebuilds runkod", refs("db"), []string{"runkod"}},
		{"web", refs("web"), []string{"web"}},
		{"proto feeds both runkod and web", refs("proto"), []string{"runkod", "web"}},
		{"webadmin only", refs("webadmin"), []string{"webadmin"}},
		{"docs-only affects no image", refs("docs"), nil},
		{"run everything -> every image (fail closed)", affected.Result{RunEverything: true}, []string{"runkod", "web", "webadmin"}},
		{"order is deployImages order, not input order", refs("webadmin", "runkod"), []string{"runkod", "webadmin"}},
	}
	for _, c := range cases {
		got := imagesForAffected(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// TestOpenDeployRecordOnLand: landing a change that touches a deployable
// project opens a deploy record naming the affected images (§14.10) - the
// server-of-record report-image later completes to fire the rollout.
func TestOpenDeployRecordOnLand(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	// A project named "platform" maps to the runkod image (imageProjects).
	repo.WriteFile("platform/PROJECT.yaml", "schema: project/v1\nname: platform\ntype: library\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("platform/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push rejected: %+v", res)
	}

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, AllowUnpolicedLand: true}
	chA, _, _ := store.GetChange(ctx, stackIDA)
	dec, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, &Principal{Name: "val", Stored: true}, false)
	if apiErr != nil || !dec.Landed {
		t.Fatalf("land: dec=%+v err=%+v", dec, apiErr)
	}

	rec, ok, err := store.GetDeployRecord(ctx, dec.LandedSHA)
	if err != nil || !ok {
		t.Fatalf("a deployable land must open a deploy record: ok=%v err=%v", ok, err)
	}
	if len(rec.Expected) != 1 || rec.Expected[0] != "runkod" {
		t.Fatalf("a platform change expects the runkod image, got %v", rec.Expected)
	}
	if rec.State != "pending" || rec.ChangeKey != stackIDA {
		t.Fatalf("record: %+v", rec)
	}
}
