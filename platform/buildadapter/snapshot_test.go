package buildadapter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubDiffer struct {
	targets []string
	err     error
	got     SnapshotDiffRequest
}

func (s *stubDiffer) SnapshotDiff(_ context.Context, req SnapshotDiffRequest) (QueryResult, error) {
	s.got = req
	if s.err != nil {
		return QueryResult{}, s.err
	}
	return QueryResult{Targets: s.targets}, nil
}

func TestRefineSnapshotFailsClosedOnError(t *testing.T) {
	d := &stubDiffer{err: errors.New("determinator binary missing")}
	ref := RefineSnapshot(context.Background(), d, "bazel", SnapshotDiffRequest{BaseRev: "a", HeadRev: "b"}, nil)
	if !ref.RunEverything {
		t.Fatalf("engine error must fail closed: %+v", ref)
	}
	if ref.Strategy != "snapshot_diff" || !strings.Contains(ref.FailureReason, "missing") {
		t.Fatalf("want snapshot_diff strategy + reason, got %+v", ref)
	}
}

func TestRefineSnapshotMapsAndDedupes(t *testing.T) {
	d := &stubDiffer{targets: []string{"//svc:test", "//svc:test", "//lib/deep:t"}}
	projects := []ProjectInfo{{Name: "svc", Path: "svc"}, {Name: "lib", Path: "lib"}}
	ref := RefineSnapshot(context.Background(), d, "bazel", SnapshotDiffRequest{BaseRev: "a", HeadRev: "b"}, projects)
	if ref.RunEverything {
		t.Fatalf("clean diff must not escalate: %+v", ref)
	}
	if len(ref.Targets) != 2 {
		t.Fatalf("duplicate targets must dedupe, got %v", ref.Targets)
	}
	if ref.TargetProjects["//svc:test"] != "svc" || ref.TargetProjects["//lib/deep:t"] != "lib" {
		t.Fatalf("mapping wrong: %v", ref.TargetProjects)
	}
	if ref.UniversePattern != "//..." {
		t.Fatalf("universe should default: %+v", ref)
	}
}

// The gating distinction from Refine (§14.5.8): snapshot-diff output STANDS
// IN for a run_everything escalation, so a target no project can own would
// silently drop its checks - that is a failure, not a shrug.
func TestRefineSnapshotFailsClosedOnUnmappedTarget(t *testing.T) {
	d := &stubDiffer{targets: []string{"//svc:test", "//scripts:stray"}}
	projects := []ProjectInfo{{Name: "svc", Path: "svc"}}
	ref := RefineSnapshot(context.Background(), d, "bazel", SnapshotDiffRequest{BaseRev: "a", HeadRev: "b"}, projects)
	if !ref.RunEverything {
		t.Fatalf("unmapped target must fail closed: %+v", ref)
	}
	if !strings.Contains(ref.FailureReason, "outside every project boundary") {
		t.Fatalf("want the attribution failure reason, got %+v", ref)
	}
}

// An empty diff is a real answer (nothing impacted - e.g. a comment-only
// MODULE.bazel edit), not a failure: de-escalation proceeds with zero
// additional projects.
func TestRefineSnapshotEmptyDiffSucceeds(t *testing.T) {
	d := &stubDiffer{}
	ref := RefineSnapshot(context.Background(), d, "bazel", SnapshotDiffRequest{BaseRev: "a", HeadRev: "b"}, nil)
	if ref.RunEverything {
		t.Fatalf("empty diff must not escalate: %+v", ref)
	}
	if len(ref.Targets) != 0 || len(ref.TargetProjects) != 0 {
		t.Fatalf("want empty sets, got %+v", ref)
	}
}
