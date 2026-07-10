package runkod

import (
	"os"
	"testing"
)

// TestComputeAffectedMemoizedBySHAPair pins the merge-requirements hot path's
// memoization (stage 15: the changes inbox reads merge requirements once per
// listed change, and every read re-walked the whole tree at head - one git
// subprocess per directory). computeAffected is a pure function of the
// (base_sha, head_sha) pair, so a repeat call must not touch git at all: the
// test proves it by deleting the repository between calls. A DIFFERENT pair
// must still go to the repo (and here, fail loudly) - the cache never answers
// for inputs it hasn't seen.
func TestComputeAffectedMemoizedBySHAPair(t *testing.T) {
	srv, bare, changeID, store := newLandTestServer(t)
	defer srv.Close()

	change, ok, err := store.GetChange(t.Context(), changeID)
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store}
	first, firstIndexed, err := server.computeAffected(change)
	if err != nil {
		t.Fatalf("computeAffected (cold): %v", err)
	}
	if len(firstIndexed) == 0 || len(first.Paths) == 0 {
		t.Fatalf("fixture sanity: expected a project and touched paths, got %+v / %+v", first, firstIndexed)
	}

	if err := os.RemoveAll(bare); err != nil {
		t.Fatalf("remove repo: %v", err)
	}

	second, secondIndexed, err := server.computeAffected(change)
	if err != nil {
		t.Fatalf("computeAffected (warm, repo gone - must be served from cache): %v", err)
	}
	if second.ComputationID != first.ComputationID || len(secondIndexed) != len(firstIndexed) {
		t.Fatalf("cached result drifted: %+v vs %+v", second, first)
	}

	// A head the cache has never seen must reach for the repo, not be
	// answered from someone else's entry.
	miss := change
	miss.HeadSHA = "0123456789abcdef0123456789abcdef01234567"
	if _, _, err := server.computeAffected(miss); err == nil {
		t.Fatalf("expected a cache miss on an unseen head to hit the (deleted) repo and fail")
	}
}
