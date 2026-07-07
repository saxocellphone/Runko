package clierr

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorRendersMessageSuggestionAndDocURL(t *testing.T) {
	e := &Error{
		Code:       "not_a_repo",
		Field:      "repo",
		Message:    "/tmp/x is not a git repository",
		Suggestion: "run `git init` first",
		DocURL:     "docs/design.md#67",
	}
	got := e.Error()
	for _, want := range []string{"/tmp/x is not a git repository", "run `git init` first", "docs/design.md#67"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, missing %q", got, want)
		}
	}
}

func TestErrorRendersWithoutSuggestionOrDocURL(t *testing.T) {
	e := &Error{Message: "something broke"}
	if got := e.Error(); got != "something broke" {
		t.Fatalf("Error() = %q, want exactly the message with no trailing lines", got)
	}
}

func TestWrapRevisionErrorRecognizesUnknownRevision(t *testing.T) {
	underlying := errors.New(`git diff --name-only badbase HEAD: exit status 128: fatal: ambiguous argument 'badbase': unknown revision or path not in the working tree.`)
	wrapped := WrapRevisionError(underlying, "--base", "badbase")

	var ce *Error
	if !errors.As(wrapped, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", wrapped, wrapped)
	}
	if ce.Code != "unknown_revision" || ce.Field != "--base" {
		t.Fatalf("unexpected fields: %+v", ce)
	}
	if !strings.Contains(ce.Message, "badbase") {
		t.Fatalf("expected the bad value in the message, got %q", ce.Message)
	}
}

func TestWrapRevisionErrorRecognizesPathspecFailure(t *testing.T) {
	underlying := errors.New(`git checkout --quiet nope: exit status 1: error: pathspec 'nope' did not match any file(s) known to git`)
	wrapped := WrapRevisionError(underlying, "--rev", "nope")

	var ce *Error
	if !errors.As(wrapped, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", wrapped, wrapped)
	}
}

func TestWrapRevisionErrorPassesThroughUnrelatedErrors(t *testing.T) {
	underlying := errors.New("connection refused")
	wrapped := WrapRevisionError(underlying, "--base", "main")
	if wrapped != underlying {
		t.Fatalf("expected the original error to pass through unchanged, got %v", wrapped)
	}
}

func TestWrapRevisionErrorNilIsNil(t *testing.T) {
	if WrapRevisionError(nil, "--base", "main") != nil {
		t.Fatalf("expected nil in, nil out")
	}
}
