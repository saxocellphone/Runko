// `runko ci init` scaffolds the generic Runko CI/CD GitHub Actions workflows
// into a repo (§14.9.1). It is a LOCAL file-writer, not a server-authored
// change like `project create`: workflows are plain files the user reviews
// and commits through their normal change flow, so this needs no runkod
// call. The files it writes are embedded verbatim from templates/ci (see
// platform citemplates), which download the runko-ci binary and read all
// project/check/image/registry facts from the tree - so this command names
// no projects, checks, images, or registry either.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
	citemplates "github.com/saxocellphone/runko/templates/ci"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ci",
		Short:   "Scaffold the generic CI/CD workflows",
		GroupID: "agents",
		Args:    cobra.ArbitraryArgs,
		RunE:    groupRunE,
	}
	cmd.AddCommand(newCIInitCmd())
	return cmd
}

// ciInitResult is the --json shape: the files written and the manual
// follow-up steps (wiring the tree + org that ci init deliberately does not
// touch).
type ciInitResult struct {
	Written   []string `json:"written"`
	Images    bool     `json:"images"`
	NextSteps []string `json:"next_steps"`
}

func newCIInitCmd() *cobra.Command {
	var (
		dir, executor          string
		images, force, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write the generic Runko CI workflows into a repo",
		Long: `Scaffolds the generic Runko CI/CD GitHub Actions workflows into
.github/workflows/ - a LOCAL file-writer you review and
commit through your normal change flow. The files download runko-ci
and read all project/check/image/registry facts from the tree, so this
command names none of them. --images adds the post-land CD workflow.`,
		Example: `  runko ci init
  runko ci init --images --force`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if executor != "github" {
				return &clierr.Error{
					Code:       "unsupported_executor",
					Field:      "executor",
					Message:    fmt.Sprintf("executor %q is not supported; only \"github\" (GitHub Actions) is available today", executor),
					Suggestion: "runko ci init --executor github",
				}
			}

			wfDir := filepath.Join(dir, ".github", "workflows")
			if err := os.MkdirAll(wfDir, 0o755); err != nil {
				return fmt.Errorf("ci init: create %s: %w", wfDir, err)
			}

			names := []string{"runko-checks.yml"}
			if images {
				names = append(names, "runko-images.yml")
			}
			// Pre-check for clobber BEFORE writing anything, so a partial scaffold
			// never leaves one file new and another refused.
			if !force {
				for _, name := range names {
					dst := filepath.Join(wfDir, name)
					if _, err := os.Stat(dst); err == nil {
						return &clierr.Error{
							Code:       "workflow_exists",
							Field:      "force",
							Message:    fmt.Sprintf("%s already exists", dst),
							Suggestion: "rerun with --force to overwrite it",
						}
					}
				}
			}

			written := make([]string, 0, len(names))
			for _, name := range names {
				content, err := citemplates.FS.ReadFile(name)
				if err != nil {
					return fmt.Errorf("ci init: read embedded template %s: %w", name, err)
				}
				dst := filepath.Join(wfDir, name)
				if err := os.WriteFile(dst, content, 0o644); err != nil {
					return fmt.Errorf("ci init: write %s: %w", dst, err)
				}
				written = append(written, dst)
			}

			return ciInitReport(written, images, jsonOut)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&dir, "dir", ".", "target repo directory; its .github/workflows/ receives the files")
	fl.BoolVar(&images, "images", false, "also write the post-land image-build (CD) workflow")
	fl.BoolVar(&force, "force", false, "overwrite existing workflow files")
	fl.StringVar(&executor, "executor", "github", "CI executor to scaffold for (only \"github\" is supported today)")
	fl.BoolVar(&jsonOut, "json", false, "emit the result as JSON")
	return cmd
}

func ciInitReport(written []string, images, jsonOut bool) error {
	// The scaffolder writes the executor; these are the tree/org facts it
	// deliberately leaves to the user (§2.3 anti-Boq: opt-in, not required).
	// App install is listed BEFORE connect - connect verifies the install.
	steps := []string{
		"add ci.checks to each project's PROJECT.yaml (a name + command per check)",
		"adjust the RUNNER CONTRACT block in runko-checks.yml (setup-go/node/bazel, services)",
		"install the Runko GitHub App on this repo, then: runko github connect --repo <owner>/<name>",
		"set repo secrets RUNKO_URL (your org mount) and RUNKO_CI_TOKEN (the org's deploy token)",
	}
	if images {
		steps = append(steps,
			"declare capability_config.deploy.image on each deployable project + deploy_registry on the root PROJECT.yaml")
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(ciInitResult{Written: written, Images: images, NextSteps: steps})
	}

	fmt.Println("scaffolded Runko CI workflows:")
	for _, w := range written {
		fmt.Printf("  %s\n", w)
	}
	fmt.Println("next steps (ci init writes the executor; you wire the tree + org):")
	for _, s := range steps {
		fmt.Printf("  -> %s\n", s)
	}
	fmt.Println("see templates/ci/README.md for the full adoption guide")
	return nil
}
