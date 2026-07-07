package land

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/saxocellphone/runko/internal/gitversion"
)

// RebaseResult is the outcome of attempting to rebase a Change onto a new
// trunk tip.
type RebaseResult struct {
	Clean         bool
	NewTreeSHA    string   // valid only when Clean
	ConflictPaths []string // populated when !Clean
}

// conflictPathPattern matches git merge-tree's "CONFLICT (<kind>): ... in
// <path>" informational lines.
var conflictPathPattern = regexp.MustCompile(`^CONFLICT \([^)]+\): .* in (.+)$`)

// Rebase computes what rebasing a single-commit Change onto newBase would
// produce, via `git merge-tree --write-tree` - a 3-way merge of
// (oldBase, newBase, changeHead) with oldBase passed explicitly as the merge
// base (never git's own history search), because oldBase is exactly the
// base_sha the Change was created against (§7.4) - the correct merge base by
// construction, not something to rediscover.
//
// This is git's own merge engine applied to a tree, not a bespoke merge
// algorithm (§28.2 rule 4: shell out to git, never reimplement its
// behavior). A Change is represented as a single commit relative to its
// base (built via gitstore.CommitOverlay), so a 3-way merge here is
// equivalent to what `git rebase --onto` would produce for that one commit.
func Rebase(repoDir, oldBase, newBase, changeHead string) (RebaseResult, error) {
	if err := gitversion.Check(); err != nil {
		return RebaseResult{}, fmt.Errorf("land: %w", err)
	}

	cmd := exec.Command("git", "merge-tree", "--write-tree", "--merge-base="+oldBase, newBase, changeHead)
	cmd.Dir = repoDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()

	text := strings.TrimRight(out.String(), "\n")
	lines := strings.Split(text, "\n")
	if lines[0] == "" {
		return RebaseResult{}, fmt.Errorf("land: git merge-tree produced no output: %w", runErr)
	}
	treeSHA := lines[0]

	if runErr == nil {
		return RebaseResult{Clean: true, NewTreeSHA: treeSHA}, nil
	}

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
		return RebaseResult{}, fmt.Errorf("land: git merge-tree: %w: %s", runErr, text)
	}

	seen := map[string]bool{}
	var conflicts []string
	for _, l := range lines[1:] {
		m := conflictPathPattern.FindStringSubmatch(l)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		conflicts = append(conflicts, m[1])
	}
	return RebaseResult{Clean: false, ConflictPaths: conflicts}, nil
}
