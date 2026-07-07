package search

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Indexer builds or refreshes a code-search index for a repo. Separate from
// CodeSearcher: a zoekt-webserver serves queries against whatever a
// zoekt-git-index run last produced, so indexing and searching are two
// independent processes/seams, matching how the real Zoekt deployment works
// (§9.3's k8s scaffold already runs both as separate containers).
type Indexer interface {
	Index(ctx context.Context, repoDir string) error
}

// ZoektIndexer shells out to zoekt-git-index (see doc.go: a process, not a
// library) to (re)build a shard for repoDir into IndexDir - the same
// directory a zoekt-webserver started with -index IndexDir then serves.
type ZoektIndexer struct {
	// Bin is the zoekt-git-index executable; defaults to "zoekt-git-index"
	// on PATH, mirroring GitleaksScanner.Bin / buildadapter/bazel's binary
	// discovery.
	Bin string
	// IndexDir is the shard output directory (zoekt-git-index's -index
	// flag). Required.
	IndexDir string
	// Branches is passed as zoekt-git-index's -branches flag (comma-joined);
	// defaults to "HEAD" - the branch a bare repo's HEAD points at (trunk,
	// per EnsureBareRepo), matching what a search over "trunk" should mean
	// (§13.3's tree-as-truth: index what's actually landed, not in-flight
	// Changes).
	Branches []string
}

func (z ZoektIndexer) Index(ctx context.Context, repoDir string) error {
	if z.IndexDir == "" {
		return fmt.Errorf("zoekt indexer: IndexDir is required")
	}
	if err := os.MkdirAll(z.IndexDir, 0o755); err != nil {
		return fmt.Errorf("zoekt indexer: create index dir %s: %w", z.IndexDir, err)
	}

	bin := z.Bin
	if bin == "" {
		bin = "zoekt-git-index"
	}
	branches := "HEAD"
	if len(z.Branches) > 0 {
		branches = strings.Join(z.Branches, ",")
	}

	cmd := exec.CommandContext(ctx, bin, "-index", z.IndexDir, "-branches", branches, repoDir)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("zoekt-git-index: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

var _ Indexer = ZoektIndexer{}
