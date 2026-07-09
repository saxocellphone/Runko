package mcp

import (
	"strings"

	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/search"
)

// This file maps runkod's REST wire shapes (Go-field-named JSON from
// index/affected/search) onto the catalog's output schemas
// (docs/spec/mcp-tools/common.schema.json $defs). The transforms are pure
// and contract-tested against those schemas - see contract_test.go.
//
// Project `id` is the project NAME in v1: the daemon serves no
// control-plane project IDs yet (§10.2's canonical IDs are a Postgres
// concern the REST layer doesn't expose), and names are unique per tree,
// so name is the stable identifier every read path here keys on. When the
// control plane starts minting IDs, this is the one place to swap.

// ProjectSummary is common.schema.json#/$defs/ProjectSummary.
type ProjectSummary struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Path          string   `json:"path"`
	OwnersSummary []string `json:"owners_summary"`
}

// ProjectDetail is common.schema.json#/$defs/ProjectDetail.
type ProjectDetail struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Type            string       `json:"type"`
	Path            string       `json:"path"`
	Visibility      string       `json:"visibility,omitempty"`
	EffectiveOwners []string     `json:"effective_owners"`
	Capabilities    []string     `json:"capabilities"`
	Dependencies    Dependencies `json:"dependencies"`
}

// Dependencies is ProjectDetail's declared/inferred split. Inferred deps
// are advisory-only and not computed anywhere yet (§13.3) - always empty
// in v1, present in the shape so callers can rely on the field existing.
type Dependencies struct {
	Declared []string `json:"declared"`
	Inferred []string `json:"inferred"`
}

// OwnersResult is common.schema.json#/$defs/OwnersResult.
type OwnersResult struct {
	Owners []string `json:"owners"`
	Source string   `json:"source"`
}

// AffectedComputation is common.schema.json#/$defs/AffectedComputation.
type AffectedComputation struct {
	ComputationID string           `json:"computation_id"`
	Projects      []ProjectSummary `json:"projects"`
	Paths         []string         `json:"paths"`
	ReasonCodes   []string         `json:"reason_codes"`
	RunEverything bool             `json:"run_everything"`
}

// ListProjectsOutput is list_projects' output_schema shape.
type ListProjectsOutput struct {
	Projects      []ProjectSummary `json:"projects"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

// SearchHit / SearchCodeOutput are search_code's output_schema shapes.
type SearchHit struct {
	Path      string `json:"path"`
	ProjectID string `json:"project_id"`
	Line      int    `json:"line"`
	Preview   string `json:"preview"`
}

type SearchCodeOutput struct {
	Hits          []SearchHit `json:"hits"`
	NextPageToken string      `json:"next_page_token,omitempty"`
}

func ownerRefs(p index.IndexedProject) []string {
	refs := make([]string, len(p.Owners))
	for i, o := range p.Owners {
		refs[i] = o.Ref
	}
	return refs
}

func projectSummary(p index.IndexedProject) ProjectSummary {
	return ProjectSummary{
		ID: p.Name, Name: p.Name, Type: p.Type, Path: p.Path,
		OwnersSummary: ownerRefs(p),
	}
}

func projectDetail(p index.IndexedProject) ProjectDetail {
	return ProjectDetail{
		ID: p.Name, Name: p.Name, Type: p.Type, Path: p.Path,
		Visibility:      p.Visibility,
		EffectiveOwners: ownerRefs(p),
		Capabilities:    nonNil(p.Capabilities),
		Dependencies: Dependencies{
			Declared: nonNil(p.DeclaredDependencies),
			Inferred: []string{},
		},
	}
}

func ownersResult(p index.IndexedProject) OwnersResult {
	// Every owner entry for one project shares one Source (§7.3's
	// precedence picks a single winning layer); a project with no owners
	// anywhere resolved through to the (empty) org default.
	source := "org_default"
	if len(p.Owners) > 0 {
		source = p.Owners[0].Source
	}
	return OwnersResult{Owners: ownerRefs(p), Source: source}
}

// affectedComputation joins an affected.Result with the project index so
// each affected project carries its full ProjectSummary (the schema embeds
// summaries, not bare name/path refs). A project in the result but absent
// from the index - possible in change mode, where the Change's own tree
// can contain a project trunk doesn't have yet - degrades to a summary
// built from the ref alone (type "other", no owners) rather than being
// dropped: the affected SET is the load-bearing part (§13.3).
func affectedComputation(result affected.Result, indexed []index.IndexedProject) AffectedComputation {
	byName := make(map[string]index.IndexedProject, len(indexed))
	for _, p := range indexed {
		byName[p.Name] = p
	}
	projects := make([]ProjectSummary, len(result.Projects))
	for i, ref := range result.Projects {
		if p, ok := byName[ref.Name]; ok {
			projects[i] = projectSummary(p)
			continue
		}
		projects[i] = ProjectSummary{
			ID: ref.Name, Name: ref.Name, Type: "other", Path: ref.Path,
			OwnersSummary: []string{},
		}
	}
	return AffectedComputation{
		ComputationID: result.ComputationID,
		Projects:      projects,
		Paths:         nonNil(result.Paths),
		ReasonCodes:   nonNil(result.ReasonCodes),
		RunEverything: result.RunEverything,
	}
}

func searchOutput(result search.Result, hits []search.Hit) SearchCodeOutput {
	out := SearchCodeOutput{Hits: make([]SearchHit, len(hits))}
	for i, h := range hits {
		out.Hits[i] = SearchHit{Path: h.Path, ProjectID: h.Project, Line: h.LineNumber, Preview: h.Line}
	}
	_ = result
	return out
}

// owningProject resolves a path to its owning project by the same
// longest-prefix rule affected.Compute and runkod's search tagging use
// (§13.3). Returns false when no project's path prefixes the given path.
func owningProject(path string, projects []index.IndexedProject) (index.IndexedProject, bool) {
	var best index.IndexedProject
	found := false
	for _, p := range projects {
		matches := p.Path == "" || path == p.Path || strings.HasPrefix(path, p.Path+"/")
		if !matches {
			continue
		}
		if !found || len(p.Path) > len(best.Path) {
			best = p
			found = true
		}
	}
	return best, found
}

func findProject(nameOrID string, projects []index.IndexedProject) (index.IndexedProject, bool) {
	for _, p := range projects {
		if p.Name == nameOrID {
			return p, true
		}
	}
	return index.IndexedProject{}, false
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
