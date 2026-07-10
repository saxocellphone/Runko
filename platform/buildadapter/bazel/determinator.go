package bazel

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/saxocellphone/runko/platform/buildadapter"
)

// DefaultDeterminatorBin is the binary SnapshotDiff shells out to when
// Engine.DeterminatorBin is unset: bazel-contrib's target-determinator, a
// static Go binary (an external process, per the lean-deps rule - never a
// go.mod import). Anything argv-compatible may stand in (tests use a
// scripted fake).
const DefaultDeterminatorBin = "target-determinator"

var _ buildadapter.SnapshotDiffer = Engine{}

// SnapshotDiff implements buildadapter.SnapshotDiffer (§14.5.8) by shelling
// out to target-determinator, which evaluates the Bazel graph at BaseRev
// and HeadRev and prints every target affected between them - including by
// configuration changes (MODULE.bazel, .bazelrc, rule edits) that no
// rdeps-over-changed-files query can see.
//
// The determinator CHECKS REVISIONS OUT while it works, so it never runs
// against req.RepoDir itself: a disposable `git clone --shared` (object
// store borrowed, working tree fresh) is created at HeadRev and discarded
// after, keeping the caller's checkout - a developer worktree or a CI
// checkout mid-job - untouched.
func (e Engine) SnapshotDiff(ctx context.Context, req buildadapter.SnapshotDiffRequest) (buildadapter.QueryResult, error) {
	if req.BaseRev == "" || req.HeadRev == "" {
		return buildadapter.QueryResult{}, fmt.Errorf("snapshot diff: base and head revisions are both required")
	}

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	scratch, err := os.MkdirTemp("", "runko-snapshot-diff-*")
	if err != nil {
		return buildadapter.QueryResult{}, fmt.Errorf("snapshot diff: scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	clone := scratch + "/repo"
	if out, err := exec.CommandContext(runCtx, "git", "clone", "--quiet", "--shared", "--no-checkout", req.RepoDir, clone).CombinedOutput(); err != nil {
		return buildadapter.QueryResult{}, fmt.Errorf("snapshot diff: clone: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(runCtx, "git", "-C", clone, "checkout", "--quiet", req.HeadRev).CombinedOutput(); err != nil {
		return buildadapter.QueryResult{}, fmt.Errorf("snapshot diff: checkout %s: %w: %s", req.HeadRev, err, strings.TrimSpace(string(out)))
	}

	bazelBin := e.Bin
	if bazelBin == "" {
		bazelBin = "bazel"
	}
	universe := req.UniversePattern
	if universe == "" {
		universe = "//..."
	}
	determinator := e.DeterminatorBin
	if determinator == "" {
		determinator = DefaultDeterminatorBin
	}

	cmd := exec.CommandContext(runCtx, determinator,
		"-working-directory", clone,
		"-bazel", bazelBin,
		"-targets", universe,
		req.BaseRev)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	// Same rationale as Query: bound Wait()'s post-kill blocking - the
	// determinator spawns bazel servers whose grandchildren can hold the
	// pipes open past a context kill.
	cmd.WaitDelay = 500 * time.Millisecond

	if err := cmd.Run(); err != nil {
		return buildadapter.QueryResult{}, fmt.Errorf("target-determinator: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}

	var targets []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			targets = append(targets, line)
		}
	}
	return buildadapter.QueryResult{Targets: targets}, nil
}
