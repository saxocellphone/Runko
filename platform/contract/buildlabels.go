package contract

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// labelRef matches an in-repo Bazel label inside a BUILD file: "//pkg/dir"
// or "//pkg/dir:target". External repos (@repo//...), relative labels
// (":target") and the pseudo-packages below are deliberately out of scope -
// this rule is about edges between PROJECTS in this tree.
var labelRef = regexp.MustCompile(`"//([A-Za-z0-9_/.+-]*)(?::[A-Za-z0-9_/.+-]+)?"`)

// pseudoPackages are the //-prefixed names Bazel reserves; they name no
// package in the tree and own no project.
var pseudoPackages = map[string]bool{"visibility": true, "conditions": true, "command_line_option": true}

// LabelEdge is one cross-project reference found in a build file.
type LabelEdge struct {
	File     string // the BUILD file declaring the reference
	From     string // project owning that BUILD file
	To       string // project owning the referenced package
	Label    string // the raw label, e.g. //docs:contracts
	Declared bool   // whether a declared edge covers it
}

// CheckBuildLabels reports cross-project Bazel references that no declared
// manifest edge covers - the guard for the defect class this repo hit on
// 2026-07-24, where four of thirteen projects' workspaces could not build
// because real edges (an embedded template package, a runfiles binary, a
// schema filegroup) existed only in the build graph.
//
// Why build labels rather than Go imports: gazelle keeps BUILD deps in sync
// with imports and a repo-wide check enforces that, so labels see every
// import PLUS the edges no language-level analysis can - `data` runfiles,
// binaries a test execs, filegroups of schema artifacts. A tree with no
// BUILD files yields nothing, which is the honest answer rather than a
// false pass.
//
// The rule is strict-deps, matching checkImports: the referencing project
// must declare the referenced one DIRECTLY, as dependencies (shipped code),
// test_dependencies (its own checks only) or consumes (contract client).
// Transitive reachability is deliberately not enough - an edge you use is
// an edge you declare, or the closure cannot be read off the manifests.
func CheckBuildLabels(projects []Project, files []File) []Violation {
	var out []Violation
	for _, e := range BuildLabelEdges(projects, files) {
		if e.Declared {
			continue
		}
		out = append(out, Violation{
			Code: "undeclared_project_dependency",
			Path: e.File,
			Message: fmt.Sprintf("%s references %s (%s) without a declared edge on %s",
				e.From, e.To, e.Label, e.To),
			Suggestion: fmt.Sprintf("declare %q in %s's PROJECT.yaml dependencies (or test_dependencies when only this project's own checks need it)",
				e.To, e.From),
		})
	}
	return out
}

// BuildLabelEdges returns every cross-project label reference in files, each
// marked with whether a declared edge covers it. Exported for the reporting
// verb, which prints the whole edge set - the covered ones are how you read
// the graph the manifests claim.
func BuildLabelEdges(projects []Project, files []File) []LabelEdge {
	var out []LabelEdge
	for _, f := range files {
		if path.Base(f.Path) != "BUILD.bazel" && path.Base(f.Path) != "BUILD" {
			continue
		}
		from := ownerOf(projects, f.Path)
		if from == nil {
			continue
		}
		seen := map[string]bool{}
		for _, m := range labelRef.FindAllStringSubmatch(string(f.Content), -1) {
			pkg := m[1]
			if pkg == "" || pseudoPackages[strings.SplitN(pkg, "/", 2)[0]] {
				continue
			}
			to := ownerOf(projects, pkg)
			if to == nil || to.Name == from.Name {
				continue
			}
			label := "//" + pkg
			if seen[to.Name+label] {
				continue
			}
			seen[to.Name+label] = true
			out = append(out, LabelEdge{
				File: f.Path, From: from.Name, To: to.Name, Label: label,
				Declared: declaresDirect(from.Dependencies, to.Name) ||
					declaresDirect(from.TestDependencies, to.Name) ||
					declaresDirect(from.Consumes, to.Name),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Label < out[j].Label
	})
	return out
}
