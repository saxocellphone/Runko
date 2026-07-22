package main

// Regression tests from the fable review (2026-07-22): the root
// -v/--version alias keeps its --json composition, and stray positionals
// on flags-only commands are the exit-2 usage class, not a generic
// failure.

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRootVersionFlagJSON(t *testing.T) {
	out := captureStdout(t, func() {
		if err := execCLI("-v", "--json"); err != nil {
			t.Errorf("runko -v --json: %v", err)
		}
	})
	var id BuildIdentity
	if err := json.Unmarshal([]byte(out), &id); err != nil {
		t.Fatalf("expected BuildIdentity JSON, got %q: %v", out, err)
	}
	if id.Go == "" {
		t.Fatalf("expected the toolchain field, got %+v", id)
	}
}

func TestTrailingPositionalIsUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"version", "extra"},
		{"change", "push", "extra"},
		{"workspace", "path", "a", "b"},
	} {
		err := execCLI(args...)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Fatalf("%v: expected a usageError (exit 2), got %T: %v", args, err, err)
		}
	}
}
