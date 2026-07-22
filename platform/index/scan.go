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
	// RootInvalidationRefinable is the subset of RootInvalidation the
	// manifest marks {refinable: true} (§14.5.8): GRAPH-VISIBLE patterns
	// whose escalation a successful build-graph snapshot diff may replace,
	// runner-side only (runko-ci) - the daemon's merge gate never consumes
	// this (gate-grade narrowing is a separate org opt-in, §14.5.4).
	RootInvalidationRefinable []string
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
	// Consumes are the manifest's §13.3.1 server/client edges: providers
	// whose declared API contract this project is a client of. They join
	// this project into the affected closure only when the provider's
	// contract surface changes - see ContractDir below.
	Consumes []string
	// ContractDir is the repo-relative root of this project's declared
	// contract surface (§13.3.1): <path>/<capability_config.rpc.path>
	// when the manifest declares the rpc capability, "" otherwise. It is
	// what a consumes closure keys on; ContractGenDir below is its
	// committed-codegen subdir, what the receive-time import check keys on.
	ContractDir string
	// ContractGenDir is <ContractDir>/gen when the rpc capability is
	// declared, "" otherwise. A Go import under another project's
	// ContractGenDir must be sanctioned by a consumes (or dependencies)
	// edge (platform/contract).
	ContractGenDir string
	// SchemaPaths are the schemas capability's declared surface
	// (§13.3.1's third contract shape): repo-relative files/dirs from
	// capability_config.schemas.paths. They feed the consumes closure
	// exactly like ContractDir/OpenAPIPath; there is no gen dir, so no
	// import enforcement applies - closure only.
	SchemaPaths []string
	// OpenAPIPath/OpenAPIPresent carry §13.3.1's http mandate: the
	// manifest-implied OpenAPI document path (capability_config.http.
	// openapi, default openapi.yaml, repo-relative) and whether it exists
	// at rev. Zero-valued unless the http capability is declared.
	OpenAPIPath    string
	OpenAPIPresent bool
	// RequiredChecks are the check names PROJECT.yaml's L2/opt-in `ci.checks`
	// declares for this project (§14.9). Empty/nil when the manifest has no
	// `ci` block at all - an unset ci.checks means "no checks required",
	// not "all checks required" (anti-Boq, §6.2: L2 fields never gate a
	// default project).
	RequiredChecks []string
	// DeployImage is the deploy.image sub-block when this project OWNS a
	// deployable image (nil otherwise) - the §14.10 `deploy` capability's
	// build-derivation half (docs/spec/deploy/README.md). RidesImages are the
	// image NAMES this project's deploy.workloads run from (its binary ships
	// inside each) - the rider edge. Together they drive the manifest-derived
	// image-rebuild set (platform/deploy), the per-org replacement for
	// runkod's hardcoded map. Zero-valued unless the deploy capability is
	// declared; raw capability_config is otherwise dropped by Scan.
	DeployImage *ImageDecl
	RidesImages []string
}

// ImageDecl is a project's deploy.image sub-block: it declares this project
// as the OWNER of one deployable image. Context defaults to the project dir.
type ImageDecl struct {
	Name       string
	Context    string
	Dockerfile string
	BuildArgs  map[string]string
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
			// Unknown run_when values normalize to the affected default at
			// read time (§14.5.9): the schema polices authoring; the
			// scanner feeds the merge gate and must fail closed (running a
			// check too often is safe, silently dropping one is not).
			runWhen := c.RunWhen
			if runWhen != RunWhenDirect {
				runWhen = RunWhenAffected
			}
			checks = append(checks, CheckDef{Name: c.Name, Command: c.Command, RunWhen: runWhen})
		}
	}

	var rootInvalidation, refinable []string
	for _, e := range manifest.RootInvalidation {
		rootInvalidation = append(rootInvalidation, e.Pattern)
		if e.Refinable {
			refinable = append(refinable, e.Pattern)
		}
	}

	var contractDir, contractGenDir, openAPIPath string
	var schemaPaths []string
	if hasCapability(manifest.Capabilities, "schemas") {
		for _, p := range capabilityConfigStringList(manifest.CapabilityConfig, "schemas", "paths") {
			schemaPaths = append(schemaPaths, path.Join(dir, p))
		}
	}
	openAPIPresent := false
	if hasCapability(manifest.Capabilities, "rpc") {
		contractDir = path.Join(dir, capabilityConfigString(manifest.CapabilityConfig, "rpc", "path", "proto"))
		contractGenDir = path.Join(contractDir, "gen")
	}
	if hasCapability(manifest.Capabilities, "http") {
		openAPIPath = path.Join(dir, capabilityConfigString(manifest.CapabilityConfig, "http", "openapi", "openapi.yaml"))
		_, err := s.store.GetBlob(s.rev, openAPIPath)
		openAPIPresent = err == nil
	}
	var deployImage *ImageDecl
	var ridesImages []string
	if hasCapability(manifest.Capabilities, "deploy") {
		deployImage = deployImageDecl(manifest.CapabilityConfig, dir)
		ridesImages = deployRiderImages(manifest.CapabilityConfig)
	}

	return IndexedProject{
		Name:                      manifest.Name,
		Path:                      dir,
		Type:                      manifest.Type,
		Capabilities:              manifest.Capabilities,
		DeclaredDependencies:      manifest.Dependencies,
		RootInvalidation:          rootInvalidation,
		RootInvalidationRefinable: refinable,
		Prose:                     manifest.Prose,
		Visibility:                visibility,
		Owners:                    owners,
		RequiredChecks:            requiredChecks,
		Checks:                    checks,
		Consumes:                  manifest.Consumes,
		ContractDir:               contractDir,
		ContractGenDir:            contractGenDir,
		SchemaPaths:               schemaPaths,
		OpenAPIPath:               openAPIPath,
		OpenAPIPresent:            openAPIPresent,
		DeployImage:               deployImage,
		RidesImages:               ridesImages,
	}, nil
}

func hasCapability(caps []string, name string) bool {
	for _, c := range caps {
		if c == name {
			return true
		}
	}
	return false
}

// capabilityConfigString reads capability_config.<cap>.<key> as a string,
// falling back to def when the config, key, or type is absent - the
// scanner's read-time normalization posture (the schema polices authoring).
// capabilityConfigStringList reads capability_config.<cap>.<key> as a
// string list, dropping non-string entries (read-time normalization, same
// posture as capabilityConfigString below).
func capabilityConfigStringList(cfg map[string]interface{}, cap, key string) []string {
	sub, _ := cfg[cap].(map[string]interface{})
	raw, _ := sub[key].([]interface{})
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func capabilityConfigString(cfg map[string]interface{}, cap, key, def string) string {
	sub, _ := cfg[cap].(map[string]interface{})
	if v, ok := sub[key].(string); ok && v != "" {
		return v
	}
	return def
}

// deployImageDecl reads capability_config.deploy.image into an ImageDecl, or
// nil when the sub-block is absent or names no image (an image with no name
// declares nothing rebuildable). Context defaults to the project dir. Same
// read-time normalization posture as the helpers above: malformed values are
// dropped, not errors - the schema polices authoring.
func deployImageDecl(cfg map[string]interface{}, dir string) *ImageDecl {
	deploy, _ := cfg["deploy"].(map[string]interface{})
	img, _ := deploy["image"].(map[string]interface{})
	name, _ := img["name"].(string)
	if name == "" {
		return nil
	}
	ctx, _ := img["context"].(string)
	if ctx == "" {
		ctx = dir
	}
	if ctx == "" {
		ctx = "." // the root project's dir is "" - as a build context that is "."
	}
	dockerfile, _ := img["dockerfile"].(string)
	var buildArgs map[string]string
	if raw, ok := img["build_args"].(map[string]interface{}); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				if buildArgs == nil {
					buildArgs = map[string]string{}
				}
				buildArgs[k] = s
			}
		}
	}
	return &ImageDecl{Name: name, Context: ctx, Dockerfile: dockerfile, BuildArgs: buildArgs}
}

// deployRiderImages reads the distinct image names capability_config.deploy.
// workloads[].image references (the rider edge): images this project's
// binaries ship inside. Order-preserving, deduped; malformed entries dropped.
func deployRiderImages(cfg map[string]interface{}) []string {
	deploy, _ := cfg["deploy"].(map[string]interface{})
	raw, _ := deploy["workloads"].([]interface{})
	var out []string
	seen := map[string]bool{}
	for _, w := range raw {
		wm, _ := w.(map[string]interface{})
		img, _ := wm["image"].(string)
		if img != "" && !seen[img] {
			seen[img] = true
			out = append(out, img)
		}
	}
	return out
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

// RootInvalidationRefinable concatenates the patterns every indexed project
// marks {refinable: true} (§14.5.8), in the same scan order as
// RootInvalidation. Consumed only runner-side (runko-ci) to decide whether
// a run_everything escalation may be replaced by a build-graph snapshot
// diff; the daemon's merge gate deliberately never reads this.
func RootInvalidationRefinable(projects []IndexedProject) []string {
	var out []string
	for _, p := range projects {
		out = append(out, p.RootInvalidationRefinable...)
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
	// RunWhen is the §14.5.9 check class, normalized at scan time to
	// exactly RunWhenAffected or RunWhenDirect.
	RunWhen string
}

// The §14.5.9 check classes.
const (
	// RunWhenAffected (the default): the check runs whenever its project
	// is in the affected closure - today's semantics, the integration
	// class.
	RunWhenAffected = "affected"
	// RunWhenDirect: the check runs only when its project's own paths
	// were touched, never when the project rides in via depends_on edges
	// - the unit-lane class. Declaring it is the project's assertion that
	// this lane doesn't guard cross-project behavior.
	RunWhenDirect = "direct"
)

// ChecksFor is THE run_when rule (§14.5.9), shared by the merge gate
// (runkod requiredCheckNames) and the CI executor (runko-ci checks) so the
// two can never disagree about which checks a change owes - a mismatch
// deadlocks changes as required-but-never-run (§14.9.1's lockstep
// requirement, now with a second axis). direct says whether p's own paths
// were touched; callers MUST pass true for every project under
// run_everything (fail closed: both classes run).
func ChecksFor(p IndexedProject, direct bool) []CheckDef {
	if direct {
		return p.Checks
	}
	var out []CheckDef
	for _, c := range p.Checks {
		if c.RunWhen != RunWhenDirect {
			out = append(out, c)
		}
	}
	return out
}
