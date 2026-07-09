// Package bazel is the v1 buildadapter.Engine implementation
// (docs/spec/build-adapter/README.md §5): shells out to `bazel query`'s
// rdeps recipe. Runner-side only - this is invoked by `runko-ci`, never by
// the platform daemon.
package bazel

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/saxocellphone/runko/platform/buildadapter"
)

// Engine shells out to the `bazel` binary. The zero value uses "bazel" from
// PATH; set Bin to point at a specific binary (tests use this to point at a
// scripted fake).
type Engine struct {
	Bin string
}

var _ buildadapter.Engine = Engine{}

// Query implements buildadapter.Engine via the rdeps recipe in
// docs/spec/build-adapter/README.md §5: changed paths become Bazel
// source-file labels, then `bazel query "rdeps(<universe>, set(<labels>))"`
// resolves the reverse-dependency closure in one shot.
func (e Engine) Query(ctx context.Context, req buildadapter.QueryRequest) (buildadapter.QueryResult, error) {
	if len(req.ChangedPaths) == 0 {
		// Nothing changed within the engine's view - not a query failure,
		// just nothing to ask about (docs/spec/build-adapter/README.md §5).
		return buildadapter.QueryResult{}, nil
	}

	bin := e.Bin
	if bin == "" {
		bin = "bazel"
	}
	universe := req.UniversePattern
	if universe == "" {
		universe = "//..."
	}

	labels := make([]string, len(req.ChangedPaths))
	for i, p := range req.ChangedPaths {
		labels[i] = fileLabel(p)
	}
	query := fmt.Sprintf("rdeps(%s, set(%s))", universe, strings.Join(labels, " "))

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, bin, "query",
		"--output=label", "--noshow_progress", "--order_output=no", query)
	cmd.Dir = req.RepoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	// Bound how long Wait() will block on I/O after the process is killed
	// (context timeout) or exits: bazel can leave grandchildren holding the
	// stdout/stderr pipes open, which would otherwise make Wait() block
	// until THEY exit too, defeating the timeout entirely.
	cmd.WaitDelay = 500 * time.Millisecond

	if err := cmd.Run(); err != nil {
		return buildadapter.QueryResult{}, fmt.Errorf("bazel query: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}

	var targets []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" {
			targets = append(targets, line)
		}
	}
	return buildadapter.QueryResult{Targets: targets}, nil
}

// fileLabel converts a repo-relative changed-file path to a Bazel
// source-file label: a source file's label is always "//<dir>:<basename>",
// with the root package spelled "//:<basename>".
func fileLabel(changedPath string) string {
	dir := path.Dir(changedPath)
	base := path.Base(changedPath)
	if dir == "." || dir == "" {
		return "//:" + base
	}
	return "//" + dir + ":" + base
}
