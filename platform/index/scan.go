package index

import (
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/project"
)

// OwnerEntry is one resolved owner, tagged with where it came from - the same
// three sources as docs/spec/mcp-tools/common.schema.json#/$defs/OwnersResult.
type OwnerEntry struct {
	Ref    string
	Source string // "project_manifest" | "path_owners" | "org_default"
}

// IndexedProject is one PROJECT.yaml found by Scan, with owners resolved.
type IndexedProject struct {
	Name                 string
	Path                 string
	Type                 string
	Capabilities         []string
	DeclaredDependencies []string
	// RootInvalidation is the manifest's tree-borne §14.5.2 pattern list
	// (root-manifest-oriented; ordered, first-match-wins, "!" exceptions
	// per §14.5.8; see index.RootInvalidation). Not persisted by Sync -
	// live consumers scan the tree, which is the §10.3 truth.
	RootInvalidation []string
	// Prose is the manifest's tree-borne §14.5.7 de-escalation list
	// (ordered, first-match-wins, ! exceptions; see index.Prose). Not
	// persisted by Sync, like RootInvalidation.
	Prose []string
	// Checks are the manifest's full ci.checks definitions (name AND
	// command) - the encapsulation contract §14.9's generic CI executor
	// consumes (`runko-ci checks`). RequiredChecks above stays the
	// names-only view the merge gate uses. Not persisted by Sync, like
	// RootInvalidation.
	Checks     []CheckDef
	Visibility string
	Owners     []OwnerEntry
	// RequiredChecks are the check names PROJECT.yaml's L2/opt-in `ci.checks`
	// declares for this project (§14.9). Empty/nil when the manifest has no
	// `ci` block at all - an unset ci.checks means "no checks required",
	// not "all checks required" (anti-Boq, §6.2: L2 fields never gate a
	// default project).
	RequiredChecks []string
}

// Scan walks the tree at rev, finds every PROJECT.yaml, and resolves its
// effective owners. orgDefaultOwners is used only when neither the manifest
// nor any ancestor OWNERS file supplies owners (§7.3).
func Scan(store core.MonorepoStore, rev core.Revision, orgDefaultOwners []string) ([]IndexedProject, error) {
	s := &scanner{
		store:       store,
		rev:         rev,
		orgDefault:  orgDefaultOwners,
		hasOwnersAt: map[string]bool{},
		ownersCache: map[string][]string{},
	}
	if err := s.walk(""); err != nil {
		return nil, err
	}
	return s.projects, nil
}

type scanner struct {
	store       core.MonorepoStore
	rev         core.Revision
	orgDefault  []string
	hasOwnersAt map[string]bool
	ownersCache map[string][]string
	projects    []IndexedProject
}

// walk visits dir in pre-order: it records whether dir has its own OWNERS
// file BEFORE recursing, so every descendant's ancestor lookup already has
// this directory's answer available - no separate existence-probing pass.
func (s *scanner) walk(dir string) error {
	entries, err := s.store.GetTree(s.rev, dir)
	if err != nil {
		return err
	}

	var hasProjectYAML bool
	var subdirs []string
	for _, e := range entries {
		switch {
		case e.Type == "blob" && e.Path == "OWNERS":
			s.hasOwnersAt[dir] = true
		case e.Type == "blob" && e.Path == "PROJECT.yaml":
			hasProjectYAML = true
		case e.Type == "tree":
			subdirs = append(subdirs, path.Join(dir, e.Path))
		}
	}

	if hasProjectYAML {
		p, err := s.loadProject(dir)
		if err != nil {
			return err
		}
		s.projects = append(s.projects, p)
	}

	for _, sub := range subdirs {
		if err := s.walk(sub); err != nil {
			return err
		}
	}
	return nil
}

func (s *scanner) loadProject(dir string) (IndexedProject, error) {
	manifestPath := path.Join(dir, "PROJECT.yaml")
	blob, err := s.store.GetBlob(s.rev, manifestPath)
	if err != nil {
		return IndexedProject{}, fmt.Errorf("index: read %s: %w", manifestPath, err)
	}

	var manifest project.Manifest
	if err := yaml.Unmarshal(blob.Content, &manifest); err != nil {
		return IndexedProject{}, fmt.Errorf("index: parse %s: %w", manifestPath, err)
	}

	owners, err := s.resolveOwners(dir, manifest.Owners)
	if err != nil {
		return IndexedProject{}, err
	}

	visibility := manifest.Visibility
	if visibility == "" {
		visibility = "default"
	}

	var requiredChecks []string
	var checks []CheckDef
	if manifest.CI != nil {
		for _, c := range manifest.CI.Checks {
			requiredChecks = append(requiredChecks, c.Name)
			checks = append(checks, CheckDef{Name: c.Name, Command: c.Command})
		}
	}

	return IndexedProject{
		Name:                 manifest.Name,
		Path:                 dir,
		Type:                 manifest.Type,
		Capabilities:         manifest.Capabilities,
		DeclaredDependencies: manifest.Dependencies,
		RootInvalidation:     manifest.RootInvalidation,
		Prose:                manifest.Prose,
		Visibility:           visibility,
		Owners:               owners,
		RequiredChecks:       requiredChecks,
		Checks:               checks,
	}, nil
}

// resolveOwners implements §7.3's precedence: explicit manifest owners win
// outright; otherwise walk up looking for the nearest ancestor OWNERS file
// with at least one non-comment line; otherwise fall back to the org default.
func (s *scanner) resolveOwners(dir string, manifestOwners []string) ([]OwnerEntry, error) {
	if len(manifestOwners) > 0 {
		return ownerEntries(manifestOwners, "project_manifest"), nil
	}

	for d := dir; ; d = parentDir(d) {
		if s.hasOwnersAt[d] {
			owners, ok := s.ownersCache[d]
			if !ok {
				ownersPath := path.Join(d, "OWNERS")
				blob, err := s.store.GetBlob(s.rev, ownersPath)
				if err != nil {
					return nil, fmt.Errorf("index: read %s: %w", ownersPath, err)
				}
				owners = parseOwnersFile(blob.Content)
				s.ownersCache[d] = owners
			}
			if len(owners) > 0 {
				return ownerEntries(owners, "path_owners"), nil
			}
			// An OWNERS file with no non-comment lines falls through to the
			// next ancestor, same as if it didn't exist.
		}
		if d == "" {
			break
		}
	}

	return ownerEntries(s.orgDefault, "org_default"), nil
}

func ownerEntries(refs []string, source string) []OwnerEntry {
	if len(refs) == 0 {
		return nil
	}
	out := make([]OwnerEntry, 0, len(refs))
	for _, r := range refs {
		out = append(out, OwnerEntry{Ref: r, Source: source})
	}
	return out
}

func parentDir(dir string) string {
	if dir == "" {
		return ""
	}
	parent := path.Dir(dir)
	if parent == "." {
		return ""
	}
	return parent
}

func parseOwnersFile(content []byte) []string {
	var owners []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		owners = append(owners, line)
	}
	return owners
}

// RootInvalidation concatenates every indexed project's tree-declared
// root-invalidation patterns (§14.5.2/§14.5.8; §9.4 "the tree owns policy"
// - relocated here from daemon flags, which remain an additive override).
// Like Prose (and unlike the pre-§14.5.8 sorted union), the result is
// ORDERED: root-invalidation lists are first-match-wins with "!"
// exceptions, so each manifest's internal order is preserved and manifests
// concatenate in scan order (root first - Scan walks the tree top-down),
// meaning the root manifest's exceptions take precedence over any deeper
// manifest's patterns. In practice only the root manifest declares these;
// accepting them from any project keeps the semantics monorepo-wide
// rather than path-magic.
func RootInvalidation(projects []IndexedProject) []string {
	var out []string
	for _, p := range projects {
		out = append(out, p.RootInvalidation...)
	}
	return out
}

// Prose concatenates every indexed project's tree-declared prose patterns
// (§14.5.7). Unlike RootInvalidation this is NOT sorted or deduped: prose
// lists are ordered (first match wins, ! exceptions), so each manifest's
// internal order is preserved and manifests concatenate in scan order
// (root first - Scan walks the tree top-down). In practice only the root
// manifest declares prose.
func Prose(projects []IndexedProject) []string {
	var out []string
	for _, p := range projects {
		out = append(out, p.Prose...)
	}
	return out
}

// CheckDef is one declared check: the name the merge gate requires and the
// command a CI executor runs, both owned by the project's manifest.
type CheckDef struct {
	Name    string
	Command string
}
