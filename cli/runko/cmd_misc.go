// doctor, agents-md, and mcp serve - the checkout-wiring and
// agent-teaching surfaces (§6.9, §8.8, §8.3). Command wiring only; the
// mechanics live in doctor.go, jj.go, and platform/{agentsmd,mcp}.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/platform/agentsmd"
	"github.com/saxocellphone/runko/platform/mcp"
)

func newDoctorCmd() *cobra.Command {
	var (
		repoDir, trunk       string
		installHook, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Check this checkout's wiring; print the cheat-sheet",
		GroupID: "start",
		Long: `Reports how this checkout is wired (§6.9): remotes, hooks, git
version, jj/workspace binding - then prints the cheat-sheet.
--install-hook wires the checkout: the commit-msg Change-Id hook, the
advisory pre-commit verb nudge, jj Change-Id trailers (in a jj
workspace), and the agent skill.`,
		Example: `  runko doctor
  runko doctor --install-hook   # wire a fresh clone`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if installHook {
				if err := InstallChangeIDHook(repoDir); err != nil {
					return err
				}
				// The verb nudge answers raw `git commit` with the native verbs
				// (jj-aware, advisory, never blocks - doctor.go). A foreign
				// pre-commit hook wins: say so instead of clobbering it.
				if installed, err := InstallVerbNudgeHook(repoDir); err != nil {
					return err
				} else if !installed {
					fmt.Fprintln(os.Stderr, "note: a pre-commit hook already exists; leaving it alone (the verb nudge is optional)")
				}
				// A jj workspace gets its Change-Id identity from the trailer
				// template, not the hook (jj runs no git hooks) - one flag sets up
				// whichever worlds are present; colocated repos get both.
				if isJJWorkspace(repoDir) {
					if err := SetupJJChangeIDs(repoDir); err != nil {
						return err
					}
				}
				// Wiring a checkout means wiring it for its agents too (§8.8): a
				// harness finds the workflow at agentsmd.SkillPath or nowhere. The
				// tree's own skill is never touched - see ensureAgentSkill.
				if path, outcome, err := ensureAgentSkill(repoDir); err != nil {
					return err
				} else if outcome == "local" {
					fmt.Fprintf(os.Stderr, "note: wrote the runko agent skill to %s (local to this checkout, excluded from changes)\n", path)
				}
			}
			report, err := RunDoctor(repoDir, trunk)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(report)
			}
			PrintCheatSheet(os.Stdout, report)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&trunk, "trunk", "main", "trunk ref name")
	fl.BoolVar(&installHook, "install-hook", false, "wire this checkout: the commit-msg Change-Id hook, the advisory pre-commit verb nudge, and the agent skill")
	fl.BoolVar(&jsonOut, "json", false, "emit the doctor report as JSON instead of the cheat-sheet")
	return cmd
}

// newAgentsMDCmd (re)writes every generated agent teaching surface -
// AGENTS.md at the repo root and each skill in agentsmd.Skills() - from
// the same command inventory (§8.8's "reference prompts / skill files ...
// generated per monorepo", stage 11's (§28.3) done-when bar; org genesis
// seeds the identical set). Overwrites unconditionally, matching how
// sqlc/oapi-codegen generated files in this repo are treated: regenerate,
// don't hand-edit.
func newAgentsMDCmd() *cobra.Command {
	var (
		repoDir, out string
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:     "agents-md",
		Short:   "(Re)generate AGENTS.md and the agent skills",
		GroupID: "agents",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := out
			if !filepath.IsAbs(path) {
				path = filepath.Join(repoDir, path)
			}
			if err := os.WriteFile(path, []byte(agentsmd.Generate()), 0o644); err != nil {
				return fmt.Errorf("agents-md: write %s: %w", path, err)
			}
			var skillPaths []string
			for _, s := range agentsmd.Skills() {
				p := filepath.Join(repoDir, filepath.FromSlash(s.Path))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return fmt.Errorf("agents-md: mkdir %s: %w", filepath.Dir(p), err)
				}
				if err := os.WriteFile(p, []byte(s.Content), 0o644); err != nil {
					return fmt.Errorf("agents-md: write %s: %w", p, err)
				}
				skillPaths = append(skillPaths, p)
			}
			if jsonOut {
				// skill_path stays the reference skill for callers written against
				// the single-skill shape; skill_paths is the whole set.
				return json.NewEncoder(os.Stdout).Encode(map[string]any{
					"path":        path,
					"skill_path":  filepath.Join(repoDir, filepath.FromSlash(agentsmd.SkillPath)),
					"skill_paths": skillPaths,
				})
			}
			fmt.Printf("generated %s and %s\n", path, strings.Join(skillPaths, ", "))
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&out, "out", "AGENTS.md", "output path, relative to --repo unless absolute")
	fl.BoolVar(&jsonOut, "json", false, "emit {path,skill_path} as JSON instead of a human summary")
	return cmd
}

// newMCPCmd serves the MCP stdio adapter (§8.3, §17.4, §28.3 stage 12)
// for clients that can't shell out to this CLI. It speaks
// newline-delimited JSON-RPC on stdin/stdout until EOF - run it from an
// MCP client's server config, not interactively. Log output (none today)
// would go to stderr; stdout is exclusively protocol.
func newMCPCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mcp",
		Short:   "MCP adapter for tool-calling clients",
		GroupID: "agents",
		Args:    cobra.ArbitraryArgs,
		RunE:    groupRunE,
	}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the seven read-only MCP tools on stdio",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			srv := &mcp.Server{Client: &mcp.Client{BaseURL: cred.URL, Token: cred.AuthHeader()}}
			return srv.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
	cmd.AddCommand(serve)
	return cmd
}
