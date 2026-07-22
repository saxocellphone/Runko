// Package gitversion checks the system git binary's version against the
// minimum Runko requires. land.Rebase depends on `git merge-tree
// --merge-base`, which needs git >= 2.40 (docs/design.md §28.3 stage 9a item
// 4); older git either rejects the flag outright or silently falls back to
// its own merge-base search instead of the Change's recorded base_sha,
// breaking §7.4's "base_sha is the correct merge base by construction"
// invariant. Fail loud and specific at the point of use, not with a cryptic
// merge-tree error.
package gitversion

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
)

// Minimum is the lowest supported git version.
var Minimum = Version{2, 40, 0}

type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

var versionPattern = regexp.MustCompile(`git version (\d+)\.(\d+)(?:\.(\d+))?`)

// Detect shells out to `git --version` and parses the result. Distro
// suffixes (e.g. "2.39.2 (Apple Git-143)", "2.43.0.windows.1") are ignored -
// only the leading major.minor[.patch] is parsed.
func Detect() (Version, error) {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return Version{}, fmt.Errorf("gitversion: run `git --version`: %w", err)
	}
	m := versionPattern.FindSubmatch(out)
	if m == nil {
		return Version{}, fmt.Errorf("gitversion: unrecognized `git --version` output: %q", out)
	}
	patch := 0
	if len(m[3]) > 0 {
		patch, _ = strconv.Atoi(string(m[3]))
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	return Version{major, minor, patch}, nil
}

// Check fails with a specific, actionable error if the installed git is
// older than Minimum.
func Check() error {
	v, err := Detect()
	if err != nil {
		return err
	}
	if v.Less(Minimum) {
		return fmt.Errorf("gitversion: found git %s, need >= %s (`git merge-tree --merge-base`) - upgrade git", v, Minimum)
	}
	return nil
}
