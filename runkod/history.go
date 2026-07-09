// Path history + per-line blame behind RepoService (proto/runko/v1/
// repo.proto, §17.2's repository browser). Gerrit-inspired at the data
// level - path-scoped log, rename following, per-line provenance - with
// one Runko-specific enrichment: every commit's Change-Id trailer is
// resolved against the Store, so the browser links history rows to the
// REVIEW that landed them (§7.4's change-centric stance), not just to raw
// commits. Commits without a resolvable Change (pre-Runko history,
// imports) degrade to plain rows, never errors.
package runkod

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/core"
)

// blameLineCap bounds how much of a file gets blamed - blame is O(history)
// per line and nothing a browser renders needs more. Matches the spirit of
// blobContentCap (repo.go).
const blameLineCap = 5000

// historyPageDefault/Max bound ListCommits pages.
const (
	historyPageDefault = 30
	historyPageMax     = 100
)

type commitInfo struct {
	SHA         string
	Subject     string
	AuthorName  string
	AuthorEmail string
	AuthoredAt  int64 // unix seconds
	ChangeID    string
	ChangeState string // "" when no Change row exists for ChangeID
}

type blameRegion struct {
	StartLine   int // 1-based
	LineCount   int
	SHA         string
	Subject     string
	AuthorName  string
	AuthoredAt  int64
	ChangeID    string
	ChangeState string
}

// gitOut runs one git command against the bare repo and returns stdout.
func (s *Server) gitOut(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.RepoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// logFormat renders one record per commit: unit-separated fields, record-
// separated entries. %(trailers:key=Change-Id,valueonly,separator=%x2C)
// yields the trailer value(s) with no "Change-Id:" prefix.
const logFormat = "%H%x1f%an%x1f%ae%x1f%at%x1f%s%x1f%(trailers:key=Change-Id,valueonly,separator=%x2C)%x1e"

// listCommits reads up to limit+1 commits touching path (repo-wide when
// "") starting at offset, newest first. The +1 is the has-more probe.
// Single-file paths follow renames.
func (s *Server) listCommits(ctx context.Context, rev core.Revision, path string, limit, offset int) ([]commitInfo, bool, error) {
	path = strings.Trim(path, "/")
	args := []string{"log", "--format=" + logFormat,
		"--skip=" + strconv.Itoa(offset), "--max-count=" + strconv.Itoa(limit+1)}
	if path != "" && s.isBlobAt(rev, path) {
		// --follow is only valid (and only meaningful) for one file.
		args = append(args, "--follow")
	}
	args = append(args, string(rev))
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := s.gitOut(args...)
	if err != nil {
		return nil, false, err
	}
	commits := parseLogRecords(out)
	hasMore := len(commits) > limit
	if hasMore {
		commits = commits[:limit]
	}
	s.resolveChangeStates(ctx, commits)
	return commits, hasMore, nil
}

func (s *Server) isBlobAt(rev core.Revision, path string) bool {
	out, err := s.gitOut("cat-file", "-t", fmt.Sprintf("%s:%s", rev, path))
	return err == nil && strings.TrimSpace(string(out)) == "blob"
}

func parseLogRecords(out []byte) []commitInfo {
	var commits []commitInfo
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 6 {
			continue
		}
		at, _ := strconv.ParseInt(f[3], 10, 64)
		commits = append(commits, commitInfo{
			SHA: f[0], AuthorName: f[1], AuthorEmail: f[2], AuthoredAt: at,
			Subject:  f[4],
			ChangeID: firstChangeID(f[5]),
		})
	}
	return commits
}

// firstChangeID: a commit legitimately carries at most one Change-Id, but
// the trailers formatter joins duplicates - take the first, trimmed.
func firstChangeID(v string) string {
	v, _, _ = strings.Cut(strings.TrimSpace(v), ",")
	return strings.TrimSpace(v)
}

// resolveChangeStates fills ChangeState for every commit whose Change-Id
// has a row on this control plane. Lookup failures leave the field empty -
// history must render even if the store is having a bad day.
func (s *Server) resolveChangeStates(ctx context.Context, commits []commitInfo) {
	states := map[string]string{}
	for i := range commits {
		id := commits[i].ChangeID
		if id == "" {
			continue
		}
		state, seen := states[id]
		if !seen {
			if change, ok, err := s.Store.GetChange(ctx, id); err == nil && ok {
				state = change.State
			}
			states[id] = state
		}
		commits[i].ChangeState = state
	}
}

// blameFile computes contiguous same-commit regions for path at rev, plus
// the blamed lines themselves (one response, one revision - content and
// attribution can never disagree).
func (s *Server) blameFile(ctx context.Context, rev core.Revision, path string) ([]blameRegion, []string, bool, *apiError) {
	path = strings.Trim(path, "/")
	blob, apiErr := s.repoBlobAt(rev, path)
	if apiErr != nil {
		return nil, nil, false, apiErr
	}
	if blob.Binary {
		return nil, nil, false, typedErr(http.StatusUnprocessableEntity, clierr.Error{
			Code: "blame_binary", Field: "path",
			Message: fmt.Sprintf("%s is binary at this revision - nothing to blame", path),
		})
	}

	truncated := false
	args := []string{"blame", "--porcelain"}
	if lineCount := strings.Count(blob.Content, "\n") + 1; blob.Truncated || lineCount > blameLineCap {
		truncated = true
		args = append(args, "-L", "1,"+strconv.Itoa(blameLineCap))
	}
	args = append(args, string(rev), "--", path)
	out, err := s.gitOut(args...)
	if err != nil {
		return nil, nil, false, internalErr(err)
	}
	regions, lines, err := parseBlamePorcelain(out)
	if err != nil {
		return nil, nil, false, internalErr(err)
	}

	// Change-Id trailers aren't in porcelain output; one batch log call
	// covers every distinct commit in the file.
	if err := s.attachBlameChanges(ctx, regions); err != nil {
		return nil, nil, false, internalErr(err)
	}
	return regions, lines, truncated, nil
}

// parseBlamePorcelain parses `git blame --porcelain`: each group opens with
// "<sha> <origLine> <finalLine> [<count>]", carries header tags (author,
// author-time, summary - only the first time a sha appears), and prefixes
// every content line with a TAB. Consecutive groups from the same commit
// merge into one region.
func parseBlamePorcelain(out []byte) ([]blameRegion, []string, error) {
	type meta struct {
		author  string
		at      int64
		subject string
	}
	metas := map[string]*meta{}
	var regions []blameRegion
	var lines []string
	var cur *blameRegion // group being accumulated

	flush := func() {
		if cur != nil {
			regions = append(regions, *cur)
			cur = nil
		}
	}

	var groupSHA string
	for _, raw := range strings.Split(string(out), "\n") {
		if raw == "" {
			continue
		}
		if raw[0] == '\t' {
			lines = append(lines, raw[1:])
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) >= 3 && len(fields[0]) == 40 && isHex(fields[0]) {
			sha := fields[0]
			finalLine, err := strconv.Atoi(fields[2])
			if err != nil {
				return nil, nil, fmt.Errorf("runkod: unexpected blame group header %q", raw)
			}
			groupSHA = sha
			if metas[sha] == nil {
				metas[sha] = &meta{}
			}
			if cur != nil && cur.SHA == sha && cur.StartLine+cur.LineCount == finalLine {
				cur.LineCount++
			} else {
				flush()
				cur = &blameRegion{StartLine: finalLine, LineCount: 1, SHA: sha}
			}
			continue
		}
		// Header tag for the current group's commit.
		m := metas[groupSHA]
		if m == nil {
			continue
		}
		switch {
		case strings.HasPrefix(raw, "author "):
			m.author = strings.TrimPrefix(raw, "author ")
		case strings.HasPrefix(raw, "author-time "):
			m.at, _ = strconv.ParseInt(strings.TrimPrefix(raw, "author-time "), 10, 64)
		case strings.HasPrefix(raw, "summary "):
			m.subject = strings.TrimPrefix(raw, "summary ")
		}
	}
	flush()

	for i := range regions {
		if m := metas[regions[i].SHA]; m != nil {
			regions[i].AuthorName = m.author
			regions[i].AuthoredAt = m.at
			regions[i].Subject = m.subject
		}
	}
	return regions, lines, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// attachBlameChanges resolves each distinct blamed commit's Change-Id
// trailer (one batch `git log --no-walk` call) and its Change state.
func (s *Server) attachBlameChanges(ctx context.Context, regions []blameRegion) error {
	seen := map[string]bool{}
	var shas []string
	for _, r := range regions {
		if !seen[r.SHA] {
			seen[r.SHA] = true
			shas = append(shas, r.SHA)
		}
	}
	if len(shas) == 0 {
		return nil
	}
	args := append([]string{"log", "--no-walk=unsorted", "--format=" + logFormat}, shas...)
	out, err := s.gitOut(args...)
	if err != nil {
		return err
	}
	commits := parseLogRecords(out)
	s.resolveChangeStates(ctx, commits)
	bySHA := map[string]commitInfo{}
	for _, c := range commits {
		bySHA[c.SHA] = c
	}
	for i := range regions {
		if c, ok := bySHA[regions[i].SHA]; ok {
			regions[i].ChangeID = c.ChangeID
			regions[i].ChangeState = c.ChangeState
		}
	}
	return nil
}
