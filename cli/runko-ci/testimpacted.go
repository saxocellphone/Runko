package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/buildadapter"
	"github.com/saxocellphone/runko/platform/buildadapter/bazel"
)

// newTestImpactedCmd implements `runko-ci test-impacted`
// (docs/spec/build-adapter/README.md §5c): target-scoped check execution.
// A manifest's bazel-test check command wraps itself in this verb to run
// only the targets the §14.5.8 snapshot diff proves impacted between base
// and head, instead of its whole universe pattern - the §14.5.4
// "check-set policies key on the refined target set" opt-in, made per
// check where §14.9.1 wants policy: inside the manifest-owned command.
// The merge gate is untouched - it keys on check NAMES, and this verb
// changes what a named check costs, never whether it runs and reports.
//
// Every scoping failure falls back to running the full universe pattern -
// exactly the command the manifest declared before wrapping - so the
// fail-closed floor is the status quo, never "run nothing".
func newTestImpactedCmd() *cobra.Command {
	var (
		repoDir, base, head, universe, bazelBin, determinatorBin string
		engineTimeout                                            time.Duration
	)
	cmd := &cobra.Command{
		Use:   "test-impacted --universe <pattern> [-- <bazel test args>]",
		Short: "Run a check's bazel tests scoped to the impacted targets",
		Long: `Runs a manifest check's bazel tests scoped to the §14.5.8
snapshot-diff-impacted targets between base and head; every scoping
failure falls back to the full universe pattern (fail closed). Bazel's
own exit code passes through verbatim.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if universe == "" {
				return &clierr.Error{
					Code:       "missing_field",
					Field:      "--universe",
					Message:    "test-impacted needs the bazel target pattern this check owns",
					Suggestion: "runko-ci test-impacted --universe //<project>/... -- <bazel test args>",
					DocURL:     "docs/spec/build-adapter/README.md",
				}
			}
			if base == "" {
				base = os.Getenv("BASE_SHA")
			}
			if head == "" {
				head = os.Getenv("HEAD_SHA")
			}
			if head == "" {
				head = "HEAD"
			}

			code, err := TestImpacted(TestImpactedOptions{
				RepoDir:         repoDir,
				Base:            base,
				Head:            head,
				Universe:        universe,
				EngineTimeout:   engineTimeout,
				BazelBin:        bazelBin,
				DeterminatorBin: determinatorBin,
				BazelArgs:       args,
			})
			if err != nil {
				return err
			}
			if code != 0 {
				// Bazel's own exit code, verbatim - the executor's shell sees the
				// same codes an unwrapped `bazel test` would have produced.
				os.Exit(code)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&base, "base", "", "base revision (default: $BASE_SHA, the §14.4 executor payload)")
	fl.StringVar(&head, "head", "", "head revision (default: $HEAD_SHA, then HEAD)")
	fl.StringVar(&universe, "universe", "", "bazel target pattern this check owns, e.g. //runkod/... (required)")
	fl.DurationVar(&engineTimeout, "engine-timeout", 10*time.Minute, "timeout for the snapshot diff")
	fl.StringVar(&bazelBin, "bazel-bin", "bazel", "bazel binary tests run with")
	fl.StringVar(&determinatorBin, "determinator-bin", bazel.DefaultDeterminatorBin, "target-determinator binary for the snapshot diff")
	return cmd
}

// TestImpactedOptions carries one resolved test-impacted invocation.
// BazelArgs are the manifest's own `bazel test` options (everything after
// `--`), passed through verbatim ahead of the target list.
type TestImpactedOptions struct {
	RepoDir         string
	Base            string
	Head            string
	Universe        string
	EngineTimeout   time.Duration
	BazelBin        string
	DeterminatorBin string
	BazelArgs       []string
}

// TestImpacted runs the check: snapshot-diff the universe between base and
// head, then `bazel test` exactly the impacted targets - or the full
// universe pattern when anything stops the diff from vouching (fail
// closed, docs/spec/build-adapter/README.md §5c's ladder). An empty
// impacted set succeeds without invoking bazel at all. Returns bazel's
// exit code; the error return is reserved for invocation-level failures
// (the bazel binary itself missing), not test outcomes.
func TestImpacted(o TestImpactedOptions) (int, error) {
	targets, fallback := impactedTargets(o)
	if fallback != "" {
		logTestImpacted("running the full universe %s: %s", o.Universe, fallback)
		return runBazelTest(o, []string{o.Universe}, false)
	}
	if len(targets) == 0 {
		logTestImpacted("no targets in %s impacted by %s..%s - nothing to test", o.Universe, short(o.Base), short(o.Head))
		return 0, nil
	}
	logTestImpacted("%d impacted target(s) in %s:\n%s", len(targets), o.Universe, indentTargets(targets))
	return runBazelTest(o, targets, true)
}

// impactedTargets is the fail-closed ladder: every rung that cannot vouch
// for a narrower set returns a non-empty fallback reason, and the caller
// runs the full universe. Order is cheapest-first so the common
// no-determinator environments (developer shells, post-land ci.yml jobs)
// fall back before any git traffic happens.
func impactedTargets(o TestImpactedOptions) (targets []string, fallback string) {
	if o.Base == "" {
		return nil, "no base revision (--base / $BASE_SHA unset)"
	}
	if _, err := exec.LookPath(o.DeterminatorBin); err != nil {
		return nil, fmt.Sprintf("%s not found", o.DeterminatorBin)
	}
	base, head, err := ensureRevs(o.RepoDir, o.Base, o.Head)
	if err != nil {
		return nil, err.Error()
	}
	fc, err := computeFloor(o.RepoDir, base, head, nil)
	if err != nil {
		return nil, fmt.Sprintf("affected floor: %v", err)
	}
	if fc.out.Result.RunEverything && !fc.out.Result.EscalationRefinableOnly {
		// A blunt root-invalidation pattern or an unowned path escalated:
		// out-of-graph by definition (§14.5.8), so no snapshot diff can
		// vouch for a narrower set - the whole universe must run.
		return nil, "out-of-graph run_everything escalation"
	}
	result, err := bazel.Engine{Bin: o.BazelBin, DeterminatorBin: o.DeterminatorBin}.SnapshotDiff(context.Background(), buildadapter.SnapshotDiffRequest{
		RepoDir:         o.RepoDir,
		BaseRev:         base,
		HeadRev:         head,
		UniversePattern: o.Universe,
		Timeout:         o.EngineTimeout,
	})
	if err != nil {
		return nil, fmt.Sprintf("snapshot diff: %v", err)
	}
	return result.Targets, ""
}

// ensureRevs resolves base and head to commit SHAs, first fetching what a
// CI checkout may be missing: the snapshot diff clones the repo
// (`git clone --shared`, which git refuses from a shallow source) and the
// affected floor diffs base..head (which needs both commits present), but
// executors typically materialize only the change head. Resolving to SHAs
// also keeps symbolic names like HEAD from re-resolving differently inside
// the determinator's disposable clone.
func ensureRevs(repoDir, base, head string) (baseSHA, headSHA string, err error) {
	if shallow, _ := runGit(repoDir, "rev-parse", "--is-shallow-repository"); shallow == "true" {
		if _, err := runGit(repoDir, "fetch", "--quiet", "--unshallow", "origin"); err != nil {
			return "", "", fmt.Errorf("unshallow for the snapshot diff: %w", err)
		}
	}
	if _, err := runGit(repoDir, "cat-file", "-e", base+"^{commit}"); err != nil {
		if _, err := runGit(repoDir, "fetch", "--quiet", "origin", base); err != nil {
			return "", "", fmt.Errorf("fetch base %s: %w", short(base), err)
		}
	}
	if baseSHA, err = runGit(repoDir, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return "", "", err
	}
	if headSHA, err = runGit(repoDir, "rev-parse", "--verify", head+"^{commit}"); err != nil {
		return "", "", err
	}
	return baseSHA, headSHA, nil
}

// runBazelTest runs `bazel test <manifest args> <targets>`, streaming
// output through, and returns bazel's exit code. In scoped mode exit 4
// ("no test targets were found") maps to success: bazel builds the
// requested targets before noticing none are tests, so the impacted
// targets compiled and no impacted test exists - the outcome scoping is
// for. The full-universe path keeps raw semantics: there, exit 4 means
// the manifest's own pattern matches no tests at all, a manifest bug
// worth failing on, and exactly what an unwrapped command would have done.
func runBazelTest(o TestImpactedOptions, targets []string, scoped bool) (int, error) {
	args := append([]string{"test"}, o.BazelArgs...)
	args = append(args, targets...)
	cmd := exec.Command(o.BazelBin, args...)
	cmd.Dir = o.RepoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		if scoped && exitErr.ExitCode() == 4 {
			logTestImpacted("impacted targets built; none are tests")
			return 0, nil
		}
		return exitErr.ExitCode(), nil
	}
	return 0, fmt.Errorf("%s test: %w", o.BazelBin, err)
}

func logTestImpacted(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "test-impacted: "+format+"\n", args...)
}

func indentTargets(targets []string) string {
	const maxShown = 20
	shown := targets
	var more string
	if len(shown) > maxShown {
		shown = shown[:maxShown]
		more = fmt.Sprintf("\n  ... and %d more", len(targets)-maxShown)
	}
	return "  " + strings.Join(shown, "\n  ") + more
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
