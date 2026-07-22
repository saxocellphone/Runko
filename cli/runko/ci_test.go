package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

func readWorkflow(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestCIInitScaffoldsChecks(t *testing.T) {
	dir := t.TempDir()
	if err := execCLI("ci", "init", "--dir", dir); err != nil {
		t.Fatalf("ci init: %v", err)
	}
	checks := readWorkflow(t, dir, "runko-checks.yml")

	// Generic dispatch trigger the daemon fires (the whole point of the file).
	if !strings.Contains(checks, "repository_dispatch") || !strings.Contains(checks, "types: [runko-change]") {
		t.Error("runko-checks.yml missing the runko-change repository_dispatch trigger")
	}
	// S1 regression guards: the executor is a downloaded binary, never an
	// in-repo `go run`, and no mandatory (uncommented) setup-go.
	if strings.Contains(checks, "go run ") {
		t.Error("runko-checks.yml still contains `go run` (must download the runko-ci binary)")
	}
	if !strings.Contains(checks, "install runko-ci") {
		t.Error("runko-checks.yml missing the install runko-ci step")
	}
	// The property most likely to be silently lost in later edits: the
	// install/report path must keep reporting even after a failed fetch, or
	// the merge gate wedges pending forever (Fable r3 #N3).
	if !strings.Contains(checks, "if: success() || failure()") {
		t.Error("runko-checks.yml lost the `if: success() || failure()` guard")
	}

	// Without --images, only the checks workflow is written.
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "runko-images.yml")); !os.IsNotExist(err) {
		t.Error("runko-images.yml written without --images")
	}
}

func TestCIInitImages(t *testing.T) {
	dir := t.TempDir()
	if err := execCLI("ci", "init", "--dir", dir, "--images"); err != nil {
		t.Fatalf("ci init --images: %v", err)
	}
	images := readWorkflow(t, dir, "runko-images.yml")
	if !strings.Contains(images, "types: [runko-image-build]") {
		t.Error("runko-images.yml missing the runko-image-build trigger")
	}
	if strings.Contains(images, "go run ") {
		t.Error("runko-images.yml still contains `go run`")
	}
}

func TestCIInitNoClobberThenForce(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := "# do not overwrite me\n"
	if err := os.WriteFile(filepath.Join(dst, "runko-checks.yml"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	// No --force: refuse, structured, and leave the file untouched.
	err := execCLI("ci", "init", "--dir", dir)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "workflow_exists" {
		t.Fatalf("want workflow_exists clierr, got %v", err)
	}
	if got := readWorkflow(t, dir, "runko-checks.yml"); got != sentinel {
		t.Error("existing workflow was overwritten despite no --force")
	}

	// --force: overwrite.
	if err := execCLI("ci", "init", "--dir", dir, "--force"); err != nil {
		t.Fatalf("ci init --force: %v", err)
	}
	if got := readWorkflow(t, dir, "runko-checks.yml"); got == sentinel {
		t.Error("--force did not overwrite the existing workflow")
	}
}

func TestCIInitUnsupportedExecutor(t *testing.T) {
	err := execCLI("ci", "init", "--dir", t.TempDir(), "--executor", "gitlab")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unsupported_executor" {
		t.Fatalf("want unsupported_executor clierr, got %v", err)
	}
}
