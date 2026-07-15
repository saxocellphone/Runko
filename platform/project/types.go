package project

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Intent is the L0-only create request (docs/design.md §10.1, §8.5), mirroring
// docs/spec/mcp-tools/common.schema.json#/$defs/CreateProjectIntent.
type Intent struct {
	Name         string
	Type         string
	Language     string   // optional; empty -> the default (Go) template set; echoed into the manifest verbatim
	TemplateID   string   // optional; empty -> the (type, language) default template
	Path         string   // optional; empty -> derived from Name
	Owners       []string // optional; empty -> inherited (§7.3)
	Capabilities []string // optional; nil -> template defaults
	NoTemplate   bool     // escape hatch (§10.4): PROJECT.yaml + README only, Language recorded as-is
	BuildEngine  string   // optional; "" -> language default (ts -> vite, else bazel; §14.5.5): bazel | vite | none
	// API is the §13.3.1 contract surface, decided at creation: grpc |
	// rest | none. REQUIRED for type service (api_required otherwise);
	// "" on other types means none. grpc scaffolds the rpc capability +
	// an in-boundary proto stub; rest scaffolds the http capability + a
	// minimal in-boundary OpenAPI document.
	API string
}

// FileWrite is one file a Plan will write, relative to the project root.
type FileWrite struct {
	Path    string
	Action  string // "create" | "modify"
	Content string
}

// CIConfig and CICheck mirror the optional `ci` block of project.schema.json.
// Unused by PlanCreate today (create_project never needs L2/L3 fields, §6.2);
// present so Manifest models the whole schema for reuse by add_capability
// later.
type CIConfig struct {
	Checks []CICheck `yaml:"checks,omitempty"`
}

type CICheck struct {
	Name    string `yaml:"name"`
	Command string `yaml:"command"`
	// RunWhen is §14.5.9's check class: "affected" (default - runs
	// whenever this project is in the affected closure) or "direct" (runs
	// only when this project's own paths were touched, not when it's
	// pulled in via depends_on edges - the unit-lane class).
	RunWhen string `yaml:"run_when,omitempty"`
}

// Manifest mirrors docs/spec/project.schema.json (PROJECT.yaml v1). Field
// order matches the schema's example ordering, since yaml.Marshal preserves
// struct field order.
type Manifest struct {
	Schema           string                  `yaml:"schema"`
	Name             string                  `yaml:"name"`
	Type             string                  `yaml:"type"`
	Language         string                  `yaml:"language,omitempty"`
	Owners           []string                `yaml:"owners,omitempty"`
	Capabilities     []string                `yaml:"capabilities,omitempty"`
	CapabilityConfig map[string]interface{}  `yaml:"capability_config,omitempty"`
	Dependencies     []string                `yaml:"dependencies,omitempty"`
	RootInvalidation []RootInvalidationEntry `yaml:"root_invalidation,omitempty"`
	Prose            []string                `yaml:"prose,omitempty"`
	Visibility       string                  `yaml:"visibility,omitempty"`
	CI               *CIConfig               `yaml:"ci,omitempty"`
}

// RootInvalidationEntry is one root_invalidation list entry (§14.5.8). YAML
// accepts two forms: a bare pattern string (blunt - the default and the
// fail-closed reading), or {pattern: ..., refinable: true} marking the
// pattern GRAPH-VISIBLE - eligible to have its run_everything escalation
// replaced by a successful build-graph snapshot diff, runner-side only. A
// "!" exception cannot be refinable (there is no escalation to refine away)
// and is rejected at parse.
type RootInvalidationEntry struct {
	Pattern   string `yaml:"pattern"`
	Refinable bool   `yaml:"refinable,omitempty"`
}

func (e *RootInvalidationEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		e.Pattern = node.Value
		e.Refinable = false
		return nil
	}
	// Plain struct alias so Decode doesn't recurse into this method.
	type entry RootInvalidationEntry
	var raw entry
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.Pattern == "" {
		return fmt.Errorf("root_invalidation: entry object needs a non-empty pattern")
	}
	if raw.Refinable && strings.HasPrefix(raw.Pattern, "!") {
		return fmt.Errorf("root_invalidation: exception %q cannot be refinable - a '!' entry removes escalation, there is nothing for a graph diff to refine", raw.Pattern)
	}
	*e = RootInvalidationEntry(raw)
	return nil
}

// MarshalYAML round-trips the compact form: blunt entries stay bare strings.
func (e RootInvalidationEntry) MarshalYAML() (interface{}, error) {
	if !e.Refinable {
		return e.Pattern, nil
	}
	type entry RootInvalidationEntry
	return entry(e), nil
}

// Plan is the output of the intent -> files pipeline (§10.1): the resolved
// project path, the effective manifest, and every file Apply will write.
// preview_create_project (§8.3) returns exactly this, unapplied.
type Plan struct {
	Path              string
	EffectiveManifest Manifest
	Files             []FileWrite
}

// ValidationError mirrors docs/spec/mcp-tools/common.schema.json#/$defs/Error,
// minus `retryable` (a transport-layer concern callers add).
type ValidationError struct {
	Code       string
	Field      string
	Message    string
	Suggestion string
}

func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}
