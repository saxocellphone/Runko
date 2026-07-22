// Command runko-ci is the portable CI-facing CLI (docs/design.md §14.6):
// checkout, affected, checks, report verbs - the core that native plugins
// for GitHub Actions/Buildkite/etc. wrap (§14.7). The command tree and
// flag parsing are cobra/pflag, matching runko (the clig.dev redesign,
// 2026-07-22); affected/checks/images are JSON-always by design - they
// are executor contracts, not human surfaces.
//
// Exit codes (docs/cli-contract.md): 0 success, 1 a recognized command
// failed (structured error printed to stderr), 2 usage error (unknown
// command, unparseable flags). test-impacted additionally passes bazel's
// own exit code through verbatim.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// usageError marks the exit-2 class (unknown command keyword, unparseable
// flags); everything else exits 1. The empty usageError means help
// already went to stderr.
type usageError string

func (e usageError) Error() string { return string(e) }

var errUsageShown = usageError("")

func main() {
	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	var ue usageError
	if errors.As(err, &ue) {
		if ue != "" {
			fmt.Fprintln(os.Stderr, "runko-ci: "+string(ue))
		}
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "runko-ci: %v\n", err)
	os.Exit(1)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "runko-ci",
		Short: "The CI-facing Runko CLI",
		Long: `runko-ci is the generic-executor side of Runko's CI contract (§14.9):
resolve what a base..head range affects (affected, checks, images),
materialize a sparse checkout (checkout), and report results back
(report-check, report-image). affected/checks/images always emit JSON -
they exist to be consumed by pipelines, not read.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ArbitraryArgs,
		RunE:          groupRunE,
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError(fmt.Sprintf("%v\nRun '%s --help' for usage.", err, cmd.CommandPath()))
	})
	root.AddCommand(
		newAffectedCmd(), newChecksCmd(), newImagesCmd(),
		newCheckoutCmd(), newReportCheckCmd(), newReportImageCmd(),
		newTestImpactedCmd(),
	)
	return root
}

// groupRunE handles the root run bare (help to stderr, exit 2) or with an
// unknown command keyword (suggestions, exit 2) - the runko convention.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "unknown command %q for %q", args[0], cmd.CommandPath())
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
			b.WriteString("\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t"))
		}
		fmt.Fprintf(&b, "\n\nRun '%s --help' for usage.", cmd.CommandPath())
		return usageError(b.String())
	}
	cmd.SetOut(cmd.ErrOrStderr())
	_ = cmd.Help()
	return errUsageShown
}

func newAffectedCmd() *cobra.Command {
	var (
		repoDir, base, head, rootPatterns, engine, universe string
		engineTimeout                                       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "affected --base <rev>",
		Short: "Compute the affected project set for a base..head range (JSON always)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if base == "" {
				return fmt.Errorf("affected: --base is required")
			}
			result, err := Affected(repoDir, base, head, splitNonEmpty(rootPatterns), engine, universe, engineTimeout)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&base, "base", "", "base revision")
	fl.StringVar(&head, "head", "HEAD", "head revision")
	fl.StringVar(&rootPatterns, "root-invalidation", "", "comma-separated root-invalidation glob patterns")
	fl.StringVar(&engine, "engine", "", "optional build-graph adapter engine to refine with, e.g. bazel (docs/spec/build-adapter/README.md)")
	fl.StringVar(&universe, "universe", "", "build-graph universe pattern, e.g. //... (default when --engine is set)")
	fl.DurationVar(&engineTimeout, "engine-timeout", 60*time.Second, "timeout for the build-graph engine query")
	return cmd
}

func newChecksCmd() *cobra.Command {
	var (
		repoDir, base, head, rootPatterns, engine, universe string
		engineTimeout                                       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "checks --base <rev>",
		Short: "Resolve affected projects' manifest-declared checks (JSON always)",
		Long: `The §14.9 executor contract: the affected closure's manifest-declared
ci.checks, deduped by name - each project OWNS its check commands, this
verb only resolves them for a generic executor to run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if base == "" {
				return fmt.Errorf("checks: --base is required")
			}
			result, err := Checks(repoDir, base, head, splitNonEmpty(rootPatterns), engine, universe, engineTimeout)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&base, "base", "", "base revision")
	fl.StringVar(&head, "head", "HEAD", "head revision")
	fl.StringVar(&rootPatterns, "root-invalidation", "", "comma-separated root-invalidation glob patterns (additive to the tree's)")
	fl.StringVar(&engine, "engine", "", "optional build-graph adapter engine: enables §14.5.8 snapshot-diff narrowing of refinable-only escalations - pass ONLY where nothing gates on the output (post-land CI)")
	fl.StringVar(&universe, "universe", "", "build-graph universe pattern, e.g. //... (default when --engine is set)")
	fl.DurationVar(&engineTimeout, "engine-timeout", 10*time.Minute, "timeout for the build-graph engine query")
	return cmd
}

func newImagesCmd() *cobra.Command {
	var repoDir, base, head, rootPatterns string
	cmd := &cobra.Command{
		Use:   "images --base <rev>",
		Short: "Resolve which deployable images a range must rebuild (JSON always)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if base == "" {
				return fmt.Errorf("images: --base is required")
			}
			result, err := Images(repoDir, base, head, splitNonEmpty(rootPatterns))
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&base, "base", "", "base revision")
	fl.StringVar(&head, "head", "HEAD", "head revision")
	fl.StringVar(&rootPatterns, "root-invalidation", "", "comma-separated root-invalidation glob patterns (additive to the tree's)")
	return cmd
}

func newCheckoutCmd() *cobra.Command {
	var (
		remote, dest, rev, projects string
		jsonOut                     bool
	)
	cmd := &cobra.Command{
		Use:   "checkout --remote <url> --dest <dir> --rev <sha>",
		Short: "Partial-clone + sparse-checkout a rev for CI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if remote == "" || dest == "" || rev == "" {
				return fmt.Errorf("checkout: --remote, --dest, and --rev are required")
			}
			if err := Checkout(remote, dest, rev, splitNonEmpty(projects)); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"rev": rev, "dest": dest})
			}
			fmt.Printf("checked out %s into %s\n", rev, dest)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&remote, "remote", "", "remote to clone from")
	fl.StringVar(&dest, "dest", "", "destination directory")
	fl.StringVar(&rev, "rev", "", "revision to check out")
	fl.StringVar(&projects, "projects", "", "comma-separated project paths for the sparse cone")
	fl.BoolVar(&jsonOut, "json", false, "emit {rev, dest} as JSON instead of a human summary")
	return cmd
}

func newReportCheckCmd() *cobra.Command {
	var (
		checksURL, token, name, externalID, status, conclusion, detailsURL, reporter string
		jsonOut                                                                      bool
	)
	cmd := &cobra.Command{
		Use:   "report-check --url <url> --name <check> --external-id <id> --reporter <id>",
		Short: "POST a CheckRun result to the platform's Checks API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if checksURL == "" || name == "" || externalID == "" || reporter == "" {
				return fmt.Errorf("report-check: --url, --name, --external-id, and --reporter are required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			report := CheckRunReport{
				Name: name, ExternalID: externalID, Status: status,
				Conclusion: conclusion, DetailsURL: detailsURL, Reporter: reporter,
			}
			if err := ReportCheck(ctx, http.DefaultClient, checksURL, token, report); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"name": name, "status": status, "external_id": externalID,
				})
			}
			fmt.Printf("reported %s (%s) for %s\n", name, status, externalID)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&checksURL, "url", "", "Checks API URL to POST to")
	fl.StringVar(&token, "token", "", "bearer token")
	fl.StringVar(&name, "name", "", "check name")
	fl.StringVar(&externalID, "external-id", "", "CI system's job id")
	fl.StringVar(&status, "status", "queued", "queued|in_progress|completed")
	fl.StringVar(&conclusion, "conclusion", "", "success|failure|cancelled|skipped|timed_out|action_required|neutral")
	fl.StringVar(&detailsURL, "details-url", "", "deep link to the CI job")
	fl.StringVar(&reporter, "reporter", "", "reporter id, e.g. github-actions")
	fl.BoolVar(&jsonOut, "json", false, "emit {name, status, external_id} as JSON instead of a human summary")
	return cmd
}

func newReportImageCmd() *cobra.Command {
	var (
		deployURL, token, image, imageRef, digest, runURL, reporter string
		jsonOut                                                     bool
	)
	cmd := &cobra.Command{
		Use:   "report-image --url <url> --image <name> --digest <sha256>",
		Short: "POST a built image's digest to the platform's deploy API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if deployURL == "" || image == "" || digest == "" {
				return fmt.Errorf("report-image: --url, --image, and --digest are required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			report := ImageReport{
				Image: image, ImageRef: imageRef, Digest: digest,
				RunURL: runURL, Reporter: reporter,
			}
			if err := ReportImage(ctx, http.DefaultClient, deployURL, token, report); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"image": image, "digest": digest,
				})
			}
			fmt.Printf("reported image %s -> %s\n", image, digest)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&deployURL, "url", "", "deploy API URL to POST to, e.g. $RUNKO_URL/api/deploys/$SHA/images")
	fl.StringVar(&token, "token", "", "bearer token")
	fl.StringVar(&image, "image", "", "logical image name (runkod|web|webadmin)")
	fl.StringVar(&imageRef, "image-ref", "", "full pushed reference sans digest, e.g. ghcr.io/saxocellphone/runko/runkod")
	fl.StringVar(&digest, "digest", "", "image digest, e.g. sha256:...")
	fl.StringVar(&runURL, "run-url", "", "deep link to the CI run that built the image")
	fl.StringVar(&reporter, "reporter", "", "reporter id, e.g. github-actions")
	fl.BoolVar(&jsonOut, "json", false, "emit {image, digest} as JSON instead of a human summary")
	return cmd
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
