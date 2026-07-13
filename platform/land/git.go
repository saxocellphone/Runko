package land

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// Identity is a git name+email pair stamped as a commit's author or
// committer.
type Identity struct {
	Name  string
	Email string
}

// DefaultIdentity is the landing machine's identity when a deployment
// configures none - a deployment-agnostic placeholder. A real deployment
// sets it (--land-identity) to its own host so the mirror carries a
// routable address rather than "localhost".
var DefaultIdentity = Identity{Name: "Runko", Email: "runko@localhost"}

// ParseIdentity parses git's own "Name <email>" author format.
func ParseIdentity(s string) (Identity, error) {
	s = strings.TrimSpace(s)
	open := strings.LastIndex(s, "<")
	closeIdx := strings.LastIndex(s, ">")
	if open < 0 || closeIdx < 0 || closeIdx < open {
		return Identity{}, fmt.Errorf("identity %q: want \"Name <email>\"", s)
	}
	name := strings.TrimSpace(s[:open])
	email := strings.TrimSpace(s[open+1 : closeIdx])
	if name == "" || email == "" {
		return Identity{}, fmt.Errorf("identity %q: both a name and an email are required", s)
	}
	return Identity{Name: name, Email: email}, nil
}

// CommitTree wraps `git commit-tree`, turning a tree object into a real
// commit with the given parent (empty = a root commit), message, author and
// committer. It is the single mint point for every commit Runko itself
// writes: the rebase-land path, the fast-forward re-stamp (both in Land),
// and server-side stack sync all route through here (§7.4). Exported so
// runkod's SyncChange mints rebased Change heads from the same primitive.
//
// Landed history carries a single canonical identity (§7.5; changelog
// 2026-07-13). Git's author/committer fields are a CLIENT artifact - a
// commit arrived stamped with whatever identity (or synthetic fallback:
// "Runko", "Runko Workspace", an unconfigured VM's git default) happened to
// be active on the machine that wrote it, so the outbound mirror showed the
// same person under several names and bots as authors. Per-author
// attribution is a Runko concept (authored_by/landed_by), surfaced in the
// UI/CLI, not smuggled through git identity fields; the land path passes
// the landing identity for BOTH author and committer. An empty author
// field defaults to the committer (preserving stack sync's "fall back to
// the machine when the original author is unreadable" behavior).
func CommitTree(repoDir, treeSHA, parent, message string, author, committer Identity) (string, error) {
	if author.Name == "" {
		author.Name = committer.Name
	}
	if author.Email == "" {
		author.Email = committer.Email
	}

	args := []string{"commit-tree", treeSHA}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	args = append(args, "-m", message)

	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+author.Name, "GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_COMMITTER_NAME="+committer.Name, "GIT_COMMITTER_EMAIL="+committer.Email,
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git commit-tree: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// treeOf resolves a commit-ish to its tree SHA - the fast-forward land
// re-stamps by re-committing the change head's tree under the landing
// identity.
func treeOf(repoDir, rev string) (string, error) {
	cmd := exec.Command("git", "rev-parse", rev+"^{tree}")
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse %s^{tree}: %w: %s", rev, err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}
