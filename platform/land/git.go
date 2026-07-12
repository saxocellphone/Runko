package land

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/saxocellphone/runko/platform/core"
)

// diffPaths returns the paths that differ between two revisions
// (`git diff --name-only`), used to compute what the trunk delta since a
// Change's base actually touched.
func diffPaths(repoDir, from, to string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", from, to)
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --name-only %s %s: %w: %s", from, to, err, strings.TrimSpace(errBuf.String()))
	}
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// CommitTree wraps `git commit-tree`, used to turn a merge-tree result (a
// tree object, not yet a commit) into a real commit with a single parent -
// the linear-trunk-history half of rebase-based landing (§7.4). Exported
// for runkod's server-side stack sync, which mints rebased Change heads
// from the same Rebase primitive.
//
// Identity follows the Gerrit/GitHub model: the AUTHOR is whoever wrote
// the change (meta, filled from the original commit by the caller - a
// rebase-land must not eat authorship the way a fast-forward land
// preserves it), the COMMITTER is the landing machine.
func CommitTree(repoDir, treeSHA, parent string, meta core.CommitMeta) (string, error) {
	authorName := orDefault(meta.AuthorName, "Runko")
	authorEmail := orDefault(meta.AuthorEmail, "runko@localhost")

	cmd := exec.Command("git", "commit-tree", treeSHA, "-p", parent, "-m", meta.Message)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName, "GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME=Runko", "GIT_COMMITTER_EMAIL=runko@localhost",
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git commit-tree: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
