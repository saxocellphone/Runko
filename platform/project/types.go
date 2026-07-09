package project

import "fmt"

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
}

// Manifest mirrors docs/spec/project.schema.json (PROJECT.yaml v1). Field
// order matches the schema's example ordering, since yaml.Marshal preserves
// struct field order.
type Manifest struct {
	Schema           string                 `yaml:"schema"`
	Name             string                 `yaml:"name"`
	Type             string                 `yaml:"type"`
	Language         string                 `yaml:"language,omitempty"`
	Owners           []string               `yaml:"owners,omitempty"`
	Capabilities     []string               `yaml:"capabilities,omitempty"`
	CapabilityConfig map[string]interface{} `yaml:"capability_config,omitempty"`
	Dependencies     []string               `yaml:"dependencies,omitempty"`
	Visibility       string                 `yaml:"visibility,omitempty"`
	CI               *CIConfig              `yaml:"ci,omitempty"`
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
