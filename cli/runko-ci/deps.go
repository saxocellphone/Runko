// deps: does the manifest graph cover the build graph? A monorepo's
// PROJECT.yaml edges are what the merge gate and the workspace cone read;
// an edge that exists only in BUILD files is invisible to both, which is
// how this repo shipped four projects whose fresh workspaces could not
// build (2026-07-24). This verb reads both graphs off the working tree and
// reports the difference, so the drift fails a check instead of an agent's
// first build.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/contract"
	"github.com/saxocellphone/runko/platform/project"
)

// newDepsCmd implements `runko-ci deps`: the manifest-vs-build-graph
// audit. Exit 1 (with the structured refusal) when an edge is undeclared,
// so a repo can wire it into a check command and have drift fail there
// rather than in someone's fresh workspace.
func newDepsCmd() *cobra.Command {
	var repoDir string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "deps",
		Short: "Report cross-project build edges the manifests do not declare",
		Long: `Reads the project manifests and the Bazel build files under
--repo and reports every reference that crosses a project boundary,
flagging the ones no dependencies/test_dependencies/consumes edge
declares. Undeclared edges are invisible to affected computation and to
the workspace cone, so a workspace scoped to the referencing project
cannot build. Exits 1 when any edge is undeclared.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := Deps(repoDir)
			if err != nil {
				return err
			}
			if err := printDeps(res, asJSON); err != nil {
				return err
			}
			if len(res.Undeclared) > 0 {
				return depsError(res)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoDir, "repo", ".", "path to the local repo")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the edge list as JSON")
	return cmd
}

// DepsResult is the verb's JSON contract.
type DepsResult struct {
	Projects   int                  `json:"projects"`
	BuildFiles int                  `json:"build_files"`
	FromIndex  int                  `json:"from_index"`
	Edges      []contract.LabelEdge `json:"edges"`
	Undeclared []contract.Violation `json:"undeclared"`
}

// Deps returns every cross-project build edge with its declared status.
//
// The working tree is the primary source - a local run must see the
// manifest edit you just made beside the build file that needs it - but a
// tree is not always fully materialized, and an audit that silently skips
// what it cannot see is worse than no audit. So any manifest or build file
// the INDEX knows and the disk lacks is read from HEAD instead
// (FromIndex counts them). That is not a corner case here: a sparse
// workspace is the normal way to work in this monorepo, and running the
// guard in one used to report a confident "0 undeclared" after examining
// two thirds of the projects.
func Deps(repoDir string) (DepsResult, error) {
	projects, buildFiles, err := scanTree(repoDir)
	if err != nil {
		return DepsResult{}, err
	}
	projects, buildFiles, fromIndex := addUnmaterialized(repoDir, projects, buildFiles)
	res := DepsResult{
		Projects:   len(projects),
		BuildFiles: len(buildFiles),
		FromIndex:  fromIndex,
		Edges:      contract.BuildLabelEdges(projects, buildFiles),
		Undeclared: contract.CheckBuildLabels(projects, buildFiles),
	}
	return res, nil
}

// addUnmaterialized fills in the files git tracks but the working tree does
// not hold (a sparse cone, or a plain `rm`), reading each from HEAD. A
// non-git directory, or any git failure, degrades to the on-disk answer:
// this widens coverage, so it must never be the reason the verb fails.
func addUnmaterialized(repoDir string, projects []contract.Project, buildFiles []contract.File) ([]contract.Project, []contract.File, int) {
	tracked, err := runGit(repoDir, "ls-files", "--", "*PROJECT.yaml", "*BUILD.bazel", "*BUILD")
	if err != nil || tracked == "" {
		return projects, buildFiles, 0
	}
	onDisk := make(map[string]bool, len(projects)+len(buildFiles))
	for _, p := range projects {
		onDisk[path.Join(p.Path, "PROJECT.yaml")] = true
	}
	for _, f := range buildFiles {
		onDisk[f.Path] = true
	}
	added := 0
	for _, rel := range strings.Split(tracked, "\n") {
		if rel == "" || onDisk[rel] {
			continue
		}
		content, err := runGit(repoDir, "show", "HEAD:"+rel)
		if err != nil {
			continue
		}
		base := path.Base(rel)
		if base == "PROJECT.yaml" {
			p, err := parseManifest(rel, []byte(content))
			if err != nil {
				continue
			}
			projects = append(projects, p)
		} else {
			buildFiles = append(buildFiles, contract.File{Path: rel, Content: []byte(content)})
		}
		added++
	}
	return projects, buildFiles, added
}

// parseManifest maps one PROJECT.yaml to the checker's project shape. The
// project's path is its manifest's directory - "" for the root project.
func parseManifest(rel string, content []byte) (contract.Project, error) {
	var m project.Manifest
	if err := yaml.Unmarshal(content, &m); err != nil {
		return contract.Project{}, fmt.Errorf("parse %s: %w", rel, err)
	}
	return contract.Project{
		Name:             m.Name,
		Path:             strings.TrimSuffix(strings.TrimSuffix(rel, "PROJECT.yaml"), "/"),
		Dependencies:     m.Dependencies,
		TestDependencies: m.TestDependencies,
		Consumes:         m.Consumes,
	}, nil
}

// scanTree collects the manifests and build files under repoDir. Bazel's
// convenience symlinks (bazel-out, bazel-<workspace>) point INTO the output
// base, where a copy of every BUILD file lives - walking them would report
// each edge twice under a path that is not in the tree at all.
func scanTree(repoDir string) ([]contract.Project, []contract.File, error) {
	var projects []contract.Project
	var buildFiles []contract.File
	err := filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(repoDir, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			base := filepath.Base(p)
			if rel != "." && (base == ".git" || base == "node_modules" || strings.HasPrefix(base, "bazel-")) {
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Base(p) {
		case "PROJECT.yaml":
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			parsed, err := parseManifest(rel, content)
			if err != nil {
				return err
			}
			projects = append(projects, parsed)
		case "BUILD.bazel", "BUILD":
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			buildFiles = append(buildFiles, contract.File{Path: rel, Content: content})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return projects, buildFiles, nil
}

// depsError renders the undeclared edges as the structured refusal, one
// suggestion the reader can type per offending manifest.
func depsError(res DepsResult) error {
	first := res.Undeclared[0]
	return &clierr.Error{
		Code:       first.Code,
		Field:      first.Path,
		Message:    fmt.Sprintf("%d cross-project build edge(s) no manifest declares; first: %s", len(res.Undeclared), first.Message),
		Suggestion: first.Suggestion,
	}
}

func printDeps(res DepsResult, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	for _, e := range res.Edges {
		mark := "ok"
		if !e.Declared {
			mark = "UNDECLARED"
		}
		fmt.Printf("%-10s %s -> %-14s %s (%s)\n", mark, e.From, e.To, e.Label, e.File)
	}
	fmt.Printf("\n%d projects, %d build files, %d cross-project edges, %d undeclared\n",
		res.Projects, res.BuildFiles, len(res.Edges), len(res.Undeclared))
	if res.FromIndex > 0 {
		fmt.Printf("(%d file(s) not materialized in this checkout, read from HEAD)\n", res.FromIndex)
	}
	return nil
}
