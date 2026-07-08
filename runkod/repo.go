// Read-only tree/blob reads behind RepoService (proto/runko/v1/repo.proto,
// §17.2's repository browser; §28.3 stage 13 server half). Barebones on
// purpose, mirroring the proto's own scope note: level-by-level directory
// listing plus whole-file reads at a revision - no recursion, no write
// verbs (the only write path is the receive funnel, §11.5).
package runkod

import (
	"bytes"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
)

// blobContentCap bounds GetBlob's returned content (repo.proto: "oversized
// files set truncated - the UI links to git"). 1 MiB matches the REST
// layer's request-body cap: nothing a code browser renders needs more.
const blobContentCap = 1 << 20

type repoTreeEntry struct {
	Name  string // base name within the directory
	Path  string // repo-root-relative
	IsDir bool
	Size  int64 // bytes; 0 for directories
}

// resolveRepoRev resolves a client-supplied revision ("" = trunk tip).
// ok=false with a nil error means the trunk is unborn - an empty repo, not
// a failure (the same stance handleListProjects takes).
func (s *Server) resolveRepoRev(rev string) (core.Revision, bool, *apiError) {
	gstore := gitstore.New(s.RepoDir)
	if rev == "" {
		tip, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
		if err != nil {
			return "", false, nil // unborn trunk
		}
		return tip, true, nil
	}
	resolved, err := gstore.ResolveRef(rev + "^{commit}")
	if err != nil {
		return "", false, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "unknown_revision", Field: "rev",
			Message: fmt.Sprintf("%q is not a commit this monorepo knows", rev),
		})
	}
	return resolved, true, nil
}

// repoTree lists the immediate children of path ("" = root) at rev, dirs
// before files, both alphabetical (repo.proto's GetTree contract). Sizes
// come from `ls-tree -l` - gitstore.GetTree deliberately doesn't carry
// them, and the browser needs them for its listing.
func (s *Server) repoTree(rev core.Revision, path string) ([]repoTreeEntry, *apiError) {
	spec := string(rev)
	path = strings.Trim(path, "/")
	if path != "" && path != "." {
		spec = fmt.Sprintf("%s:%s", rev, path)
	}
	cmd := exec.Command("git", "-c", "core.quotePath=false", "ls-tree", "-l", spec)
	cmd.Dir = s.RepoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, plainErr(http.StatusNotFound, fmt.Sprintf("directory not found: %s", path))
	}

	var dirs, files []repoTreeEntry
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			return nil, internalErr(fmt.Errorf("runkod: unexpected ls-tree line %q", line))
		}
		meta := strings.Fields(line[:tab])
		if len(meta) != 4 {
			return nil, internalErr(fmt.Errorf("runkod: unexpected ls-tree -l metadata %q", line[:tab]))
		}
		name := line[tab+1:]
		full := name
		if path != "" && path != "." {
			full = path + "/" + name
		}
		switch meta[1] {
		case "tree":
			dirs = append(dirs, repoTreeEntry{Name: name, Path: full, IsDir: true})
		case "blob":
			size, _ := strconv.ParseInt(meta[3], 10, 64)
			files = append(files, repoTreeEntry{Name: name, Path: full, Size: size})
		default:
			// Submodules/etc. - nothing the browser renders.
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return append(dirs, files...), nil
}

type repoBlob struct {
	Content   string // UTF-8 text; "" when binary
	Binary    bool
	Truncated bool
	Size      int64
}

// repoBlobAt reads one file at rev. Binary detection is git's own NUL-byte
// heuristic; oversized text is cut at blobContentCap on a rune boundary.
func (s *Server) repoBlobAt(rev core.Revision, path string) (repoBlob, *apiError) {
	blob, err := gitstore.New(s.RepoDir).GetBlob(rev, strings.Trim(path, "/"))
	if err != nil {
		return repoBlob{}, plainErr(http.StatusNotFound, fmt.Sprintf("file not found: %s", path))
	}
	if bytes.IndexByte(blob.Content, 0) >= 0 {
		return repoBlob{Binary: true, Size: blob.Size}, nil
	}
	content := blob.Content
	truncated := false
	if len(content) > blobContentCap {
		cut := blobContentCap
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		content = content[:cut]
		truncated = true
	}
	return repoBlob{Content: string(content), Truncated: truncated, Size: blob.Size}, nil
}
