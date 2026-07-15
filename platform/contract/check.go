// Package contract implements §13.3.1's receive-time contract checks: a
// Go import under another project's committed contract gen dir requires a
// DIRECT declared dependency edge on the owning project (project-grade
// strict-deps), and a project declaring the http capability must carry its
// OpenAPI document in-boundary. Pure functions over the push's changed
// files plus a tree-derived project snapshot - the funnel (platform/receive)
// calls Check as its fourth step, beside the secret scan. This does not
// reopen §13.3's inferred-deps decision: gating still consumes only
// declared edges; Check refuses trees whose declarations are provably
// incomplete, imports in the pushed files being declared facts at the
// exact head_sha (the §14.5.4 trust class), never async inference.
package contract

import (
	"fmt"
	"go/parser"
	"go/token"
	"path"
	"strings"
)

// Project is the slice of an indexed project (platform/index) the checks
// need. Callers map index.IndexedProject values into this shape so the
// package stays dependency-free in both directions.
type Project struct {
	Name         string
	Path         string   // repo-relative project root; "" or "." for the root project
	Dependencies []string // declared, direct (§13.3 edges)

	// ContractGenDir is the repo-relative directory of the project's
	// committed contract codegen (<path>/<rpc.path>/gen), "" when the
	// manifest declares no rpc capability.
	ContractGenDir string

	// DeclaresHTTP/OpenAPIPath/OpenAPIPresent carry §13.3.1's REST
	// mandate: whether the manifest declares the http capability, the
	// repo-relative OpenAPI document path it implies, and whether that
	// document exists at the pushed head.
	DeclaresHTTP   bool
	OpenAPIPath    string
	OpenAPIPresent bool
}

// File is one changed file with content, the funnel's own currency
// (receive.FileContent carries the same pair).
type File struct {
	Path    string
	Content []byte
}

// Violation is a structured contract rejection, mirroring the
// {code,message,suggestion} shape of §6.5 / receive.PolicyViolation.
type Violation struct {
	Code       string // "undeclared_contract_dependency" | "missing_openapi_document"
	Path       string // offending file (consumer .go file, or the manifest/document path)
	Message    string
	Suggestion string
}

// Check runs both §13.3.1 checks over one push. modulePath is the Go
// module path at the pushed head ("" skips the import check - a non-Go
// monorepo has no gen-dir imports to police). changedPaths is the push's
// full changed-path list (deletions included), files the subset that has
// content. Unparsable .go files are skipped: their imports cannot compile
// either, and the project's own checks reject them with a better message.
func Check(modulePath string, projects []Project, files []File, changedPaths []string) []Violation {
	var out []Violation
	out = append(out, checkImports(modulePath, projects, files)...)
	out = append(out, checkOpenAPI(projects, changedPaths)...)
	return out
}

func checkImports(modulePath string, projects []Project, files []File) []Violation {
	if modulePath == "" {
		return nil
	}
	var out []Violation
	fset := token.NewFileSet()
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f.Path, f.Content, parser.ImportsOnly)
		if err != nil {
			continue
		}
		consumer := ownerOf(projects, f.Path)
		for _, imp := range parsed.Imports {
			ipath := strings.Trim(imp.Path.Value, `"`)
			rel, ok := strings.CutPrefix(ipath, modulePath+"/")
			if !ok {
				continue
			}
			owner := ownerOf(projects, rel)
			if owner == nil || owner.ContractGenDir == "" || !underDir(rel, owner.ContractGenDir) {
				continue
			}
			if consumer != nil && consumer.Name == owner.Name {
				continue // a project may always consume its own contract
			}
			consumerName := "(no project)"
			if consumer != nil {
				consumerName = consumer.Name
				if declaresDirect(consumer.Dependencies, owner.Name) {
					continue
				}
			}
			out = append(out, Violation{
				Code: "undeclared_contract_dependency",
				Path: f.Path,
				Message: fmt.Sprintf("%s imports %s's contract (%s) without a declared dependency edge (§13.3.1)",
					consumerName, owner.Name, ipath),
				Suggestion: fmt.Sprintf("declare %q in %s's PROJECT.yaml dependencies (direct - consuming a contract is a sanctioned edge, §13.3.1)",
					owner.Name, consumerName),
			})
		}
	}
	return out
}

// checkOpenAPI refuses a push whose own touched surface establishes or
// preserves an http declaration without the document: it fires only when
// the push changes the project's manifest or the document path itself, so
// a pre-existing violation never blocks unrelated work in the project.
func checkOpenAPI(projects []Project, changedPaths []string) []Violation {
	changed := make(map[string]bool, len(changedPaths))
	for _, p := range changedPaths {
		changed[p] = true
	}
	var out []Violation
	for _, p := range projects {
		if !p.DeclaresHTTP || p.OpenAPIPresent || p.OpenAPIPath == "" {
			continue
		}
		manifest := path.Join(p.Path, "PROJECT.yaml")
		if !changed[manifest] && !changed[p.OpenAPIPath] {
			continue
		}
		out = append(out, Violation{
			Code: "missing_openapi_document",
			Path: p.OpenAPIPath,
			Message: fmt.Sprintf("%s declares the http capability but %s does not exist (§13.3.1: a REST surface carries its OpenAPI document in-boundary)",
				p.Name, p.OpenAPIPath),
			Suggestion: fmt.Sprintf("add %s (OpenAPI 3.1; `runko project create --api rest` scaffolds one) or drop the http capability",
				p.OpenAPIPath),
		})
	}
	return out
}

// ownerOf maps a repo-relative path to its project by longest prefix
// (§13.3 step 1). The root project (Path "" or ".") matches everything at
// prefix length zero, so it is the fallback owner.
func ownerOf(projects []Project, p string) *Project {
	var best *Project
	bestLen := -1
	for i := range projects {
		root := projects[i].Path
		if root == "." {
			root = ""
		}
		if root == "" {
			if bestLen < 0 {
				best, bestLen = &projects[i], 0
			}
			continue
		}
		if (p == root || strings.HasPrefix(p, root+"/")) && len(root) > bestLen {
			best, bestLen = &projects[i], len(root)
		}
	}
	return best
}

func underDir(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func declaresDirect(deps []string, name string) bool {
	for _, d := range deps {
		if d == name {
			return true
		}
	}
	return false
}
