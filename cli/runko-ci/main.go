// Command runko-ci is the portable CI-facing CLI (docs/design.md §14.6):
// checkout, affected, report-check - the core that native plugins for
// GitHub Actions/Buildkite/etc. wrap (§14.7). Implemented this session
// (§28.3 stage 9) against the local repo and a bearer-token Checks API call;
// full CI OIDC federation and the sparse-checkout API (§14.4.4, served by a
// not-yet-built control plane) are out of scope here.
//
// Exit codes (docs/cli-contract.md, added in the §8.3 CLI-first audit):
// 0 success, 1 a recognized command failed (structured error printed to
// stderr), 2 usage error (unknown/missing command) - flag-parsing errors
// already exit 2 via stdlib flag.ExitOnError.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "affected":
		err = cmdAffected(os.Args[2:])
	case "checkout":
		err = cmdCheckout(os.Args[2:])
	case "report-check":
		err = cmdReportCheck(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "runko-ci: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "runko-ci: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: runko-ci <command> [flags]

commands:
  affected       compute the affected project set for a base..head range (JSON always)
  checkout       partial-clone + sparse-checkout a rev for CI [--json]
  report-check   POST a CheckRun result to the platform's Checks API [--json]

exit codes: 0 success, 1 command failed, 2 usage error (docs/cli-contract.md)`)
}

func cmdAffected(args []string) error {
	fs := flag.NewFlagSet("affected", flag.ExitOnError)
	repoDir := fs.String("repo", ".", "path to the local repo")
	base := fs.String("base", "", "base revision")
	head := fs.String("head", "HEAD", "head revision")
	rootPatterns := fs.String("root-invalidation", "", "comma-separated root-invalidation glob patterns")
	engine := fs.String("engine", "", "optional build-graph adapter engine to refine with, e.g. bazel (docs/spec/build-adapter/README.md)")
	universe := fs.String("universe", "", "build-graph universe pattern, e.g. //... (default when --engine is set)")
	engineTimeout := fs.Duration("engine-timeout", 60*time.Second, "timeout for the build-graph engine query")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" {
		return fmt.Errorf("affected: --base is required")
	}

	result, err := Affected(*repoDir, *base, *head, splitNonEmpty(*rootPatterns), *engine, *universe, *engineTimeout)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func cmdCheckout(args []string) error {
	fs := flag.NewFlagSet("checkout", flag.ExitOnError)
	remote := fs.String("remote", "", "remote to clone from")
	dest := fs.String("dest", "", "destination directory")
	rev := fs.String("rev", "", "revision to check out")
	projects := fs.String("projects", "", "comma-separated project paths for the sparse cone")
	jsonOut := fs.Bool("json", false, "emit {rev, dest} as JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *remote == "" || *dest == "" || *rev == "" {
		return fmt.Errorf("checkout: --remote, --dest, and --rev are required")
	}

	if err := Checkout(*remote, *dest, *rev, splitNonEmpty(*projects)); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"rev": *rev, "dest": *dest})
	}
	fmt.Printf("checked out %s into %s\n", *rev, *dest)
	return nil
}

func cmdReportCheck(args []string) error {
	fs := flag.NewFlagSet("report-check", flag.ExitOnError)
	checksURL := fs.String("url", "", "Checks API URL to POST to")
	token := fs.String("token", "", "bearer token")
	name := fs.String("name", "", "check name")
	externalID := fs.String("external-id", "", "CI system's job id")
	status := fs.String("status", "queued", "queued|in_progress|completed")
	conclusion := fs.String("conclusion", "", "success|failure|cancelled|skipped|timed_out|action_required|neutral")
	detailsURL := fs.String("details-url", "", "deep link to the CI job")
	reporter := fs.String("reporter", "", "reporter id, e.g. github-actions")
	jsonOut := fs.Bool("json", false, "emit {name, status, external_id} as JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *checksURL == "" || *name == "" || *externalID == "" || *reporter == "" {
		return fmt.Errorf("report-check: --url, --name, --external-id, and --reporter are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := CheckRunReport{
		Name: *name, ExternalID: *externalID, Status: *status,
		Conclusion: *conclusion, DetailsURL: *detailsURL, Reporter: *reporter,
	}
	if err := ReportCheck(ctx, http.DefaultClient, *checksURL, *token, report); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"name": *name, "status": *status, "external_id": *externalID,
		})
	}
	fmt.Printf("reported %s (%s) for %s\n", *name, *status, *externalID)
	return nil
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
