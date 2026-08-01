package main

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/index"
)

func TestPromptWorkspaceNameAcceptsDefault(t *testing.T) {
	var out strings.Builder
	got, err := promptWorkspaceName(bufio.NewReader(strings.NewReader("\n")), &out, "20260801")
	if err != nil {
		t.Fatalf("promptWorkspaceName: %v", err)
	}
	if got != "20260801" {
		t.Fatalf("empty input should accept default, got %q", got)
	}
	if !strings.Contains(out.String(), "Workspace name") || !strings.Contains(out.String(), "20260801") {
		t.Fatalf("prompt should show label + default, got %q", out.String())
	}
}

func TestPromptWorkspaceNameTypedOverride(t *testing.T) {
	got, err := promptWorkspaceName(bufio.NewReader(strings.NewReader("my-fix\n")), &strings.Builder{}, "20260801")
	if err != nil || got != "my-fix" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestParseProjectSelectionNumbersAndNames(t *testing.T) {
	ordered := []index.IndexedProject{
		{Name: "repo", Path: ""},
		{Name: "cli", Path: "cli"},
		{Name: "docs", Path: "docs"},
	}
	got, err := parseProjectSelection("1,3", ordered)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Join(got, ",") != "repo,docs" {
		t.Fatalf("numbers: got %v", got)
	}
	got, err = parseProjectSelection("cli docs", ordered)
	if err != nil {
		t.Fatalf("parse names: %v", err)
	}
	if strings.Join(got, ",") != "cli,docs" {
		t.Fatalf("names: got %v", got)
	}
	got, err = parseProjectSelection("2, docs", ordered)
	if err != nil {
		t.Fatalf("mixed: %v", err)
	}
	if strings.Join(got, ",") != "cli,docs" {
		t.Fatalf("mixed: got %v", got)
	}
}

func TestParseProjectSelectionRejectsUnknown(t *testing.T) {
	ordered := []index.IndexedProject{{Name: "cli", Path: "cli"}}
	_, err := parseProjectSelection("nope", ordered)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "invalid_input" {
		t.Fatalf("want invalid_input, got %v", err)
	}
	_, err = parseProjectSelection("9", ordered)
	if !errors.As(err, &ce) || ce.Code != "invalid_input" {
		t.Fatalf("out-of-range: want invalid_input, got %v", err)
	}
}

func TestPromptProjectSelectMultiSelect(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "cli", Path: "cli"},
		{Name: "docs", Path: "docs"},
		{Name: "repo", Path: ""},
	}
	var out strings.Builder
	got, err := promptProjectSelect(bufio.NewReader(strings.NewReader("2,3\n")), &out, projects)
	if err != nil {
		t.Fatalf("promptProjectSelect: %v", err)
	}
	// rootProjectFirst puts repo first, then cli, docs → 2=cli, 3=docs
	if strings.Join(got, ",") != "cli,docs" {
		t.Fatalf("got %v; listing was:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "1. repo (root)") {
		t.Fatalf("expected numbered root listing, got:\n%s", out.String())
	}
}

func TestMaybePromptIncludeRootDefaultYes(t *testing.T) {
	projects := []index.IndexedProject{
		{Name: "root", Path: ""}, // rootness is the path, not the name
		{Name: "cli", Path: "cli"},
	}
	var out strings.Builder
	got, err := maybePromptIncludeRoot(bufio.NewReader(strings.NewReader("\n")), &out, []string{"cli"}, projects)
	if err != nil {
		t.Fatalf("maybePromptIncludeRoot: %v", err)
	}
	if strings.Join(got, ",") != "cli,root" {
		t.Fatalf("default Yes should append root project name, got %v", got)
	}
	if !strings.Contains(out.String(), `Also include root project "root"?`) {
		t.Fatalf("prompt text: %q", out.String())
	}
}

func TestMaybePromptIncludeRootDecline(t *testing.T) {
	projects := []index.IndexedProject{{Name: "repo", Path: ""}, {Name: "cli", Path: "cli"}}
	got, err := maybePromptIncludeRoot(bufio.NewReader(strings.NewReader("n\n")), &strings.Builder{}, []string{"cli"}, projects)
	if err != nil || strings.Join(got, ",") != "cli" {
		t.Fatalf("got %v err=%v", got, err)
	}
}

func TestMaybePromptIncludeRootSkippedWhenAbsentOrSelected(t *testing.T) {
	got, err := maybePromptIncludeRoot(bufio.NewReader(strings.NewReader("")), &strings.Builder{},
		[]string{"cli"}, []index.IndexedProject{{Name: "cli", Path: "cli"}})
	if err != nil || strings.Join(got, ",") != "cli" {
		t.Fatalf("no root project: got %v err=%v", got, err)
	}
	got, err = maybePromptIncludeRoot(bufio.NewReader(strings.NewReader("")), &strings.Builder{},
		[]string{"repo", "cli"}, []index.IndexedProject{{Name: "repo", Path: ""}, {Name: "cli", Path: "cli"}})
	if err != nil || strings.Join(got, ",") != "repo,cli" {
		t.Fatalf("already selected: got %v err=%v", got, err)
	}
}

func TestMaybePromptIncludeRootIgnoresNonRootNamedRepo(t *testing.T) {
	// A leaf project named "repo" must not trigger the root-affinity nudge.
	got, err := maybePromptIncludeRoot(bufio.NewReader(strings.NewReader("")), &strings.Builder{},
		[]string{"cli"}, []index.IndexedProject{{Name: "repo", Path: "packages/repo"}, {Name: "cli", Path: "cli"}})
	if err != nil || strings.Join(got, ",") != "cli" {
		t.Fatalf("non-root named repo: got %v err=%v", got, err)
	}
}

func TestRequireWorkspaceCreateFlagsMissingField(t *testing.T) {
	err := requireWorkspaceCreateFlags("", "alice", nil, nil)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "missing_field" || ce.Field != "name" {
		t.Fatalf("name: want missing_field/name, got %#v", err)
	}
	err = requireWorkspaceCreateFlags("ws", "alice", nil, nil)
	if !errors.As(err, &ce) || ce.Code != "missing_field" || ce.Field != "project" {
		t.Fatalf("project: want missing_field/project, got %#v", err)
	}
	err = requireWorkspaceCreateFlags("ws", "", []string{"cli"}, nil)
	if !errors.As(err, &ce) || ce.Code != "missing_field" || ce.Field != "by" {
		t.Fatalf("by: want missing_field/by, got %#v", err)
	}
	if err := requireWorkspaceCreateFlags("ws", "alice", []string{"cli"}, nil); err != nil {
		t.Fatalf("complete flags: %v", err)
	}
	if err := requireWorkspaceCreateFlags("ws", "alice", nil, []string{"services/x"}); err != nil {
		t.Fatalf("new-path alone: %v", err)
	}
}
