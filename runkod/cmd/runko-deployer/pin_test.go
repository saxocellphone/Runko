package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/platform/checks"
)

const sampleKustomization = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - runkod.yaml
  - web.yaml

generators:
  - ghcr-pull-secret-generator.yaml
`

func TestPinImagesAddUpdateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kustomization.yaml")
	if err := os.WriteFile(path, []byte(sampleKustomization), 0o644); err != nil {
		t.Fatal(err)
	}
	imgs := []checks.DeployImage{
		{Image: "runkod", ImageRef: "ghcr.io/saxocellphone/runko/runkod", Digest: "sha256:aaa"},
		{Image: "web", ImageRef: "ghcr.io/saxocellphone/runko/web", Digest: "sha256:bbb"},
	}
	changed, err := pinImages(path, imgs)
	if err != nil || !changed {
		t.Fatalf("pin: changed=%v err=%v", changed, err)
	}
	s := readFile(t, path)
	if !strings.Contains(s, "resources:") || !strings.Contains(s, "ghcr-pull-secret-generator.yaml") {
		t.Fatalf("preserved keys lost:\n%s", s)
	}
	for _, want := range []string{
		"name: ghcr.io/saxocellphone/runko/runkod", "digest: sha256:aaa",
		"name: ghcr.io/saxocellphone/runko/web", "digest: sha256:bbb",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}

	// Idempotent: same digests => no change (no needless GitOps commit).
	if changed, err := pinImages(path, imgs); err != nil || changed {
		t.Fatalf("re-pin same digests must be a no-op: changed=%v err=%v", changed, err)
	}

	// New digest updates in place, not a duplicate entry.
	imgs[0].Digest = "sha256:ccc"
	if changed, _ := pinImages(path, imgs); !changed {
		t.Fatal("digest change must be detected")
	}
	s = readFile(t, path)
	if !strings.Contains(s, "digest: sha256:ccc") || strings.Contains(s, "sha256:aaa") {
		t.Fatalf("update-in-place failed:\n%s", s)
	}
	if strings.Count(s, "name: ghcr.io/saxocellphone/runko/runkod") != 1 {
		t.Fatalf("digest update duplicated the entry:\n%s", s)
	}

	// An image reported without a ref/digest is skipped, not written blank.
	if changed, _ := pinImages(path, []checks.DeployImage{{Image: "webadmin"}}); changed {
		t.Fatal("an image without image_ref/digest must be skipped")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSetPushAuthResolvesPerPush pins the App-auth contract: the push
// credential is resolved at push time through tokenFn (App mode mints
// there, so a pin arriving hours after boot never rides an expired
// token), lands only in the ephemeral checkout's remote URL, and a
// non-https repo-url never resolves a credential at all (filesystem
// test remotes stay anonymous).
func TestSetPushAuthResolvesPerPush(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	mints := 0
	d := &deployer{
		repoURL: "https://github.com/acme/gitops.git",
		tokenFn: func() (string, error) { mints++; return fmt.Sprintf("tok-%d", mints), nil },
	}
	if err := d.git(ctx, "", "init", dir); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := d.git(ctx, dir, "remote", "add", "origin", d.repoURL); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if err := d.setPushAuth(ctx, dir); err != nil {
			t.Fatalf("setPushAuth %d: %v", i, err)
		}
		got, err := d.gitOut(ctx, dir, "remote", "get-url", "origin")
		if err != nil {
			t.Fatalf("get-url: %v", err)
		}
		want := fmt.Sprintf("https://x-access-token:tok-%d@github.com/acme/gitops.git", i)
		if strings.TrimSpace(got) != want {
			t.Fatalf("push %d remote: got %q, want %q", i, strings.TrimSpace(got), want)
		}
	}

	// A filesystem remote must not mint: the credential resolver is
	// never consulted for a non-https repo-url.
	local := &deployer{
		repoURL: filepath.Join(t.TempDir(), "gitops"),
		tokenFn: func() (string, error) { t.Fatal("non-https repo-url must not resolve a credential"); return "", nil },
	}
	if err := local.setPushAuth(ctx, dir); err != nil {
		t.Fatalf("setPushAuth local: %v", err)
	}
}
