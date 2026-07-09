// Package gitstore is a core.MonorepoStore implementation that shells out to
// the system git binary (docs/design.md §11.3 mandates matching real upstream
// Git behavior; §28.2 rule 4 forbids a Git-in-Go library).
//
// It operates entirely through plumbing commands against a scratch index file,
// so it never assumes or touches a checked-out working tree - CommitOverlay
// builds a new tree from a base revision plus an overlay of file changes purely
// in Git's object database, the same shape workspace snapshots and change refs
// both need (§11.5, §12.2).
package gitstore

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/saxocellphone/runko/platform/core"
)

// Store is rooted at an existing git repository directory (the repo root for
// a working-tree checkout, or a bare repo's directory - both work since every
// operation here is plumbing).
type Store struct {
	Dir string
	// Ref is the default ref ListHistory walks when opts.Since is unset.
	Ref string
	// ExtraEnv is appended to os.Environ() for every call that doesn't
	// already build its own explicit env (CommitOverlay does, for its
	// scratch index file). Needed for git's object quarantine: a
	// pre-receive hook's OWN process gets GIT_OBJECT_DIRECTORY/
	// GIT_ALTERNATE_OBJECT_DIRECTORIES pointing at incoming-but-not-yet-
	// committed objects, but a Store used from a DIFFERENT process (e.g. a
	// daemon the hook forwards to over HTTP, runkod's Processor) has no way
	// to see them unless those vars are explicitly forwarded and set here.
	ExtraEnv []string
}

// New returns a Store rooted at an existing git repository directory, walking
// "HEAD" by default for ListHistory.
func New(dir string) *Store {
	return &Store{Dir: dir, Ref: "HEAD"}
}

// command builds a git invocation with the Store's env fallback applied: an
// explicit per-call env wins outright, otherwise ExtraEnv is appended to the
// ambient environment. Every subprocess in this package must be built here -
// a raw exec.Command silently drops ExtraEnv's quarantine forwarding
// (GIT_OBJECT_DIRECTORY et al.), which is invisible until the object being
// read exists only in a push's quarantine directory.
func (s *Store) command(env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.Dir
	switch {
	case env != nil:
		cmd.Env = env
	case s.ExtraEnv != nil:
		cmd.Env = append(os.Environ(), s.ExtraEnv...)
	}
	return cmd
}

func (s *Store) run(env []string, args ...string) (string, error) {
	cmd := s.command(env, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// ResolveRef implements core.MonorepoStore.
func (s *Store) ResolveRef(name string) (core.Revision, error) {
	out, err := s.run(nil, "rev-parse", name)
	if err != nil {
		return "", err
	}
	return core.Revision(out), nil
}

// GetTree implements core.MonorepoStore, listing the immediate children of
// path (or the tree root, if path is "" or ".") at rev.
func (s *Store) GetTree(rev core.Revision, path string) ([]core.TreeEntry, error) {
	spec := string(rev)
	if path != "" && path != "." {
		spec = fmt.Sprintf("%s:%s", rev, path)
	}
	out, err := s.run(nil, "ls-tree", spec)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var entries []core.TreeEntry
	for _, line := range strings.Split(out, "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("gitstore: unexpected ls-tree line %q", line)
		}
		meta := strings.Fields(line[:tab])
		if len(meta) != 3 {
			return nil, fmt.Errorf("gitstore: unexpected ls-tree metadata %q", line[:tab])
		}
		entries = append(entries, core.TreeEntry{
			Mode: meta[0],
			Type: meta[1],
			SHA:  meta[2],
			Path: line[tab+1:],
		})
	}
	return entries, nil
}

// GetBlob implements core.MonorepoStore. Content is read via `cat-file -p`
// directly to stdout so binary blobs are never mangled by line-oriented
// trimming.
func (s *Store) GetBlob(rev core.Revision, path string) (core.Blob, error) {
	sha, err := s.run(nil, "rev-parse", fmt.Sprintf("%s:%s", rev, path))
	if err != nil {
		return core.Blob{}, err
	}
	cmd := s.command(nil, "cat-file", "-p", sha)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return core.Blob{}, fmt.Errorf("git cat-file -p %s: %w: %s", sha, err, strings.TrimSpace(errBuf.String()))
	}
	content := out.Bytes()
	return core.Blob{SHA: sha, Size: int64(len(content)), Content: content}, nil
}

// CommitOverlay implements core.MonorepoStore. It builds the new tree in a
// throwaway index file (GIT_INDEX_FILE) rather than the repository's real
// index, so it is safe to call concurrently and never disturbs a working tree.
func (s *Store) CommitOverlay(base core.Revision, overlay core.Overlay, meta core.CommitMeta) (core.Revision, error) {
	idx, err := os.CreateTemp("", "runko-gitstore-index-*")
	if err != nil {
		return "", fmt.Errorf("gitstore: create scratch index: %w", err)
	}
	idxPath := idx.Name()
	idx.Close()
	// git treats an existing-but-empty index file as corrupt ("index file
	// smaller than expected"); remove it so the first `git` invocation below
	// creates a fresh index, and clean up whatever git leaves behind after.
	os.Remove(idxPath)
	defer os.Remove(idxPath)

	env := append(os.Environ(), "GIT_INDEX_FILE="+idxPath)

	if base != "" {
		if _, err := s.run(env, "read-tree", string(base)); err != nil {
			return "", err
		}
	}

	for _, ch := range overlay.Changes {
		if ch.Delete {
			if _, err := s.run(env, "update-index", "--force-remove", "--", ch.Path); err != nil {
				return "", err
			}
			continue
		}
		blobSHA, err := s.hashObjectWrite(env, ch.Content)
		if err != nil {
			return "", err
		}
		cacheinfo := fmt.Sprintf("100644,%s,%s", blobSHA, ch.Path)
		if _, err := s.run(env, "update-index", "--add", "--cacheinfo", cacheinfo); err != nil {
			return "", err
		}
	}

	treeSHA, err := s.run(env, "write-tree")
	if err != nil {
		return "", err
	}

	authorName := orDefault(meta.AuthorName, "Runko")
	authorEmail := orDefault(meta.AuthorEmail, "runko@localhost")
	commitEnv := append(append([]string{}, env...),
		"GIT_AUTHOR_NAME="+authorName, "GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName, "GIT_COMMITTER_EMAIL="+authorEmail,
	)
	args := []string{"commit-tree", treeSHA, "-m", meta.Message}
	if base != "" {
		args = append(args, "-p", string(base))
	}
	commitSHA, err := s.run(commitEnv, args...)
	if err != nil {
		return "", err
	}
	return core.Revision(commitSHA), nil
}

func (s *Store) hashObjectWrite(env []string, content []byte) (string, error) {
	cmd := s.command(env, "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(content)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git hash-object -w --stdin: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// UpdateRef implements core.MonorepoStore. When expected is non-nil, this is
// a compare-and-swap (git update-ref's optional oldvalue argument); when nil,
// the ref is updated (or created) unconditionally.
func (s *Store) UpdateRef(name string, rev core.Revision, expected *core.Revision) error {
	args := []string{"update-ref", name, string(rev)}
	if expected != nil {
		args = append(args, string(*expected))
	}
	_, err := s.run(nil, args...)
	return err
}

// ListHistory implements core.MonorepoStore, walking s.Ref (default "HEAD"),
// or opts.Since..s.Ref when opts.Since is set.
func (s *Store) ListHistory(path string, opts core.HistoryOptions) ([]core.HistoryEntry, error) {
	ref := s.Ref
	if opts.Since != "" {
		ref = string(opts.Since) + ".." + ref
	}
	args := []string{"log", "--format=%H%x09%s"}
	if opts.Limit > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Limit))
	}
	args = append(args, ref)
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := s.run(nil, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var entries []core.HistoryEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, core.HistoryEntry{Revision: core.Revision(parts[0]), Message: parts[1]})
	}
	return entries, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var _ core.MonorepoStore = (*Store)(nil)
