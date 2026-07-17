package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

// TestWorkspaceCreateDefaultsToManagedHome pins §12.7 placement: with no
// --dir/--clone-dir the worktree and the shared store land under the
// managed home's <host>/<org>/<repo>/ layout - never in the caller's cwd
// (the pre-§12.7 "mono"-in-cwd default is how eighteen worktrees ended up
// inside the developing checkout's root) - and the registry records the
// materialization.
func TestWorkspaceCreateDefaultsToManagedHome(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	// cwd = a throwaway dir; nothing may appear here.
	cwd := t.TempDir()
	t.Chdir(cwd)

	info, dir, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"managed-ws", "alice", []string{"checkout-api"}, nil, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate with managed defaults: %v", err)
	}

	home := os.Getenv("RUNKO_WORKSPACE_HOME")
	u, _ := url.Parse(srv.URL)
	base := filepath.Join(home, strings.ReplaceAll(u.Host, ":", "_"), "default", "monorepo")
	if want := filepath.Join(base, "managed-ws"); dir != want {
		t.Fatalf("worktree dir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "commerce/checkout/main.go")); err != nil {
		t.Fatalf("managed worktree not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, ".store", ".git")); err != nil {
		t.Fatalf("shared store not at <base>/.store: %v", err)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("managed default must leave the caller's cwd untouched, found %v", entries)
	}

	rows, err := loadMaterializations()
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected exactly one registry row, got %v (%v)", rows, err)
	}
	r := rows[0]
	if r.Workspace != info.ID || r.Branch != "head" || r.Path != dir || r.Store != filepath.Join(base, ".store") {
		t.Fatalf("registry row mismatch: %+v", r)
	}
	if r.CreatedAt.IsZero() || r.LastUsedAt.IsZero() {
		t.Fatalf("registry row missing timestamps: %+v", r)
	}
}

// TestWorkspaceCreateRefusesNestedCheckout: materializing a workspace (or
// its store) inside another git working tree is refused with a structured
// error - the in-tree sprawl and the gazelle-walked-into-worktrees hazard
// (finding #49) - and --force-nested remains the deliberate escape.
func TestWorkspaceCreateRefusesNestedCheckout(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	host := t.TempDir()
	mustGit(t, host, "init", "-q")
	writeFile(t, host, "README.md", "a host checkout\n")
	mustGit(t, host, "add", "-A")

	_, _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"nested-ws", "alice", []string{"checkout-api"}, nil,
		MaterializeOptions{CloneDir: filepath.Join(t.TempDir(), "store"), Dir: filepath.Join(host, "nested-ws")})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "workspace_nested_checkout" {
		t.Fatalf("expected workspace_nested_checkout, got %T: %v", err, err)
	}

	if _, _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"nested-ws", "alice", []string{"checkout-api"}, nil,
		MaterializeOptions{CloneDir: filepath.Join(t.TempDir(), "store"), Dir: filepath.Join(host, "nested-ws"), ForceNested: true}); err != nil {
		t.Fatalf("--force-nested should override the guard: %v", err)
	}
}

// TestWorkspacePathLookup: the scripting glue behind `workspace path` -
// by name via the registry, and a structured miss for the untracked.
func TestWorkspacePathLookup(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	_, dir, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"path-ws", "alice", []string{"checkout-api"}, nil, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}

	m, err := workspacePathLookup("path-ws")
	if err != nil {
		t.Fatalf("workspacePathLookup: %v", err)
	}
	if m.Path != dir {
		t.Fatalf("path = %q, want %q", m.Path, dir)
	}

	_, err = workspacePathLookup("never-heard-of-it")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_materialized" {
		t.Fatalf("expected not_materialized, got %T: %v", err, err)
	}
}

// TestResolveWorkspaceDirRunsVerbsFromAnywhere pins the §12.7 transparency
// contract: -w/--workspace resolves a workspace's registered
// materialization, so checkout verbs run from ANY cwd - the repo root
// included - without cd'ing into the worktree. name@branch narrows to
// that branch's row, -w plus an explicit --dir is a refused
// contradiction, and an unmaterialized name gets the attach suggestion.
func TestResolveWorkspaceDirRunsVerbsFromAnywhere(t *testing.T) {
	srv, _ := startWorkspaceServer(t)
	_, dir, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"anywhere-ws", "alice", []string{"checkout-api"}, nil, MaterializeOptions{})
	if err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	t.Chdir(t.TempDir()) // an unrelated cwd: nothing here is a checkout

	got, err := resolveWorkspaceDir("anywhere-ws", ".")
	if err != nil || got != dir {
		t.Fatalf("resolveWorkspaceDir = %q, %v; want %q", got, err, dir)
	}

	// The whole verb through the flag wiring: change create -w from the
	// unrelated cwd commits IN the workspace's worktree.
	writeFile(t, dir, "commerce/checkout/anywhere.txt", "hi\n")
	if err := cmdChangeCreate([]string{"-m", "from anywhere", "-w", "anywhere-ws"}); err != nil {
		t.Fatalf("change create -w from an unrelated cwd: %v", err)
	}
	if subject := mustGit(t, dir, "log", "-1", "--format=%s"); subject != "from anywhere" {
		t.Fatalf("the commit landed somewhere else: HEAD subject = %q", subject)
	}

	// @branch narrows to that branch's materialization row.
	branchDir := t.TempDir()
	if err := recordMaterialization(Materialization{Workspace: "anywhere-ws", Branch: "fix", Path: branchDir}); err != nil {
		t.Fatalf("recordMaterialization: %v", err)
	}
	if got, err := resolveWorkspaceDir("anywhere-ws@fix", "."); err != nil || got != branchDir {
		t.Fatalf("resolveWorkspaceDir @branch = %q, %v; want %q", got, err, branchDir)
	}
	var ce *clierr.Error
	if _, err := resolveWorkspaceDir("anywhere-ws@nope", "."); !errors.As(err, &ce) || ce.Code != "not_materialized" {
		t.Fatalf("unknown branch: expected not_materialized, got %v", err)
	}

	// Contradiction and miss are structured refusals.
	if _, err := resolveWorkspaceDir("anywhere-ws", "/somewhere/else"); !errors.As(err, &ce) || ce.Code != "workspace_and_dir" {
		t.Fatalf("-w with --dir: expected workspace_and_dir, got %v", err)
	}
	if _, err := resolveWorkspaceDir("ghost", "."); !errors.As(err, &ce) || ce.Code != "not_materialized" {
		t.Fatalf("expected not_materialized, got %v", err)
	}
}
