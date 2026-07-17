package runkod

import (
	"context"
	"testing"
)

// TestDeployRecordLifecycle pins the inverted CD trigger's state machine
// (§14.10): open on land, fill by report-image, flip to ready exactly once
// when the expected image set is complete.
func TestDeployRecordLifecycle(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	sha := "abc123"

	if err := s.OpenDeployRecord(ctx, sha, "Ichange", "prov", []string{"runkod", "web"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	// Idempotent re-open (a duplicate land event) must NOT reset the expected
	// set or wipe reported digests.
	if err := s.OpenDeployRecord(ctx, sha, "Ichange", "prov", []string{"runkod"}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rec, ok, err := s.GetDeployRecord(ctx, sha)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if rec.State != "pending" || len(rec.Expected) != 2 {
		t.Fatalf("after open: %+v (expected 2 images from the FIRST open)", rec)
	}

	// One of two reported: not ready yet.
	rec, ok, nowReady, err := s.RecordDeployImage(ctx, sha, DeployImageRow{
		Image: "runkod", ImageRef: "ghcr.io/x/runkod", Digest: "sha256:aaa",
	})
	if err != nil || !ok {
		t.Fatalf("report runkod: ok=%v err=%v", ok, err)
	}
	if nowReady || rec.State != "pending" {
		t.Fatalf("one of two reported must not be ready: nowReady=%v state=%s", nowReady, rec.State)
	}

	// Completing the set flips ready exactly once.
	rec, _, nowReady, err = s.RecordDeployImage(ctx, sha, DeployImageRow{Image: "web", Digest: "sha256:bbb"})
	if err != nil {
		t.Fatalf("report web: %v", err)
	}
	if !nowReady || rec.State != "ready" || len(rec.Reported) != 2 {
		t.Fatalf("completing the set must flip ready once: nowReady=%v %+v", nowReady, rec)
	}

	// A further report on an already-ready record must NOT re-fire the trigger
	// (idempotent rollout - Argo must roll once).
	_, _, nowReady, err = s.RecordDeployImage(ctx, sha, DeployImageRow{Image: "runkod", Digest: "sha256:aaa2"})
	if err != nil {
		t.Fatalf("re-report: %v", err)
	}
	if nowReady {
		t.Fatalf("already-ready record must not re-fire deploy.images_ready")
	}

	// A report for an unknown sha (no record opened) is ok=false, not an error.
	_, ok, _, err = s.RecordDeployImage(ctx, "unknown", DeployImageRow{Image: "runkod", Digest: "z"})
	if err != nil || ok {
		t.Fatalf("unknown sha must be ok=false: ok=%v err=%v", ok, err)
	}
}
