// Unified-diff computation for GetChangeDiff (proto/runko/v1/changes.proto,
// §17.2's per-Change scoped diff; §28.3 stage 13 server half). Shells out to
// `git diff` like every other git interaction in this repo (§28.2 rule 4)
// and parses the patch into the FileDiff/DiffHunk/DiffLine shapes the proto
// models - a Change's diff is exactly base_sha..head_sha, so a stacked
// Change's diff is already its own delta (changes.proto's GetChangeDiff doc).
package runkod

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type fileDiff struct {
	Path    string // repo-root-relative; the NEW path for renames
	OldPath string // set only when Status == "renamed"
	Status  string // "added" | "modified" | "deleted" | "renamed"
	Binary  bool
	Hunks   []diffHunk
	Adds    int
	Dels    int
}

type diffHunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Header             string // git's section heading (text after the second @@)
	Lines              []diffLine
}

type diffLine struct {
	Type    string // "context" | "added" | "removed"
	Content string // without the leading +/-/space marker
	OldLine int    // 0 when the line has no old-side number (added)
	NewLine int    // 0 when the line has no new-side number (removed)
}

// computeChangeDiff runs `git diff -M base head` in repoDir and parses it.
// base "" means the empty tree (a first Change on an unborn trunk).
// core.quotePath=false keeps non-ASCII paths literal; paths containing
// genuinely hostile characters (newlines, " b/") can still confuse the
// `diff --git` fallback parse, but the ---/+++ headers cover every normal
// case including renames - the same pragmatic stance gitDiffNamesOnly takes.
func computeChangeDiff(repoDir, base, head string) ([]fileDiff, error) {
	if base == "" {
		base = emptyTreeOID
	}
	cmd := exec.Command("git", "-c", "core.quotePath=false", "diff", "-M", "--no-color", "--no-ext-diff", base, head)
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff %s %s: %w: %s", base, head, err, strings.TrimSpace(errBuf.String()))
	}
	return parseUnifiedDiff(out.String())
}

// parseUnifiedDiff parses `git diff` patch output. It tolerates every
// extended header git emits (mode changes, similarity, index lines) by
// ignoring the ones that don't affect the FileDiff shape.
func parseUnifiedDiff(patch string) ([]fileDiff, error) {
	var files []fileDiff
	var cur *fileDiff
	var hunk *diffHunk
	oldNo, newNo := 0, 0

	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}

	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			cur = &fileDiff{Status: "modified"}
			// Fallback path from the header itself; overwritten by the
			// more reliable ---/+++/rename headers when they follow.
			if a, b, ok := splitDiffGitPaths(line); ok {
				cur.Path = b
				if a != b {
					cur.OldPath = a
				}
			}

		case cur == nil:
			// Preamble before the first file (shouldn't happen) - skip.

		case strings.HasPrefix(line, "new file mode "):
			cur.Status = "added"
		case strings.HasPrefix(line, "deleted file mode "):
			cur.Status = "deleted"
		case strings.HasPrefix(line, "rename from "):
			cur.Status = "renamed"
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.Status = "renamed"
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			cur.Binary = true

		case strings.HasPrefix(line, "--- "):
			// A deleted file has no "+++ b/" line - its path comes from the
			// old side. (Adds are the mirror image: "--- /dev/null" here.)
			if p, ok := strings.CutPrefix(line, "--- a/"); ok && cur.Status == "deleted" {
				cur.Path = p
			}
		case strings.HasPrefix(line, "+++ "):
			if p, ok := strings.CutPrefix(line, "+++ b/"); ok {
				cur.Path = p
			}

		case strings.HasPrefix(line, "@@ "):
			flushHunk()
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			hunk = &h
			oldNo, newNo = h.OldStart, h.NewStart

		case hunk != nil && strings.HasPrefix(line, "+"):
			cur.Adds++
			hunk.Lines = append(hunk.Lines, diffLine{Type: "added", Content: line[1:], NewLine: newNo})
			newNo++
		case hunk != nil && strings.HasPrefix(line, "-"):
			cur.Dels++
			hunk.Lines = append(hunk.Lines, diffLine{Type: "removed", Content: line[1:], OldLine: oldNo})
			oldNo++
		case hunk != nil && strings.HasPrefix(line, " "):
			hunk.Lines = append(hunk.Lines, diffLine{Type: "context", Content: line[1:], OldLine: oldNo, NewLine: newNo})
			oldNo++
			newNo++
		case hunk != nil && strings.HasPrefix(line, `\`):
			// "\ No newline at end of file" - metadata, not content.
		}
	}
	flushFile()
	return files, nil
}

// splitDiffGitPaths extracts (a, b) from `diff --git a/<a> b/<b>`. Ambiguous
// when a path itself contains " b/" - callers treat this as a fallback only.
func splitDiffGitPaths(line string) (string, string, bool) {
	rest, ok := strings.CutPrefix(line, "diff --git a/")
	if !ok {
		return "", "", false
	}
	i := strings.LastIndex(rest, " b/")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+len(" b/"):], true
}

// parseHunkHeader parses `@@ -oldStart[,oldLines] +newStart[,newLines] @@ header`.
func parseHunkHeader(line string) (diffHunk, error) {
	rest, ok := strings.CutPrefix(line, "@@ ")
	if !ok {
		return diffHunk{}, fmt.Errorf("runkod: not a hunk header: %q", line)
	}
	marks, header, ok := strings.Cut(rest, " @@")
	if !ok {
		return diffHunk{}, fmt.Errorf("runkod: malformed hunk header: %q", line)
	}
	oldPart, newPart, ok := strings.Cut(marks, " ")
	if !ok || !strings.HasPrefix(oldPart, "-") || !strings.HasPrefix(newPart, "+") {
		return diffHunk{}, fmt.Errorf("runkod: malformed hunk ranges: %q", line)
	}
	os, ol, err := parseHunkRange(oldPart[1:])
	if err != nil {
		return diffHunk{}, fmt.Errorf("runkod: hunk header %q: %w", line, err)
	}
	ns, nl, err := parseHunkRange(newPart[1:])
	if err != nil {
		return diffHunk{}, fmt.Errorf("runkod: hunk header %q: %w", line, err)
	}
	return diffHunk{
		OldStart: os, OldLines: ol, NewStart: ns, NewLines: nl,
		Header: strings.TrimPrefix(header, " "),
	}, nil
}

// parseHunkRange parses "start[,count]"; count defaults to 1 per the format.
func parseHunkRange(s string) (start, count int, err error) {
	startStr, countStr, has := strings.Cut(s, ",")
	start, err = strconv.Atoi(startStr)
	if err != nil {
		return 0, 0, err
	}
	count = 1
	if has {
		count, err = strconv.Atoi(countStr)
		if err != nil {
			return 0, 0, err
		}
	}
	return start, count, nil
}
