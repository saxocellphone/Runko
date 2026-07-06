package project

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// PlanCreate is the "intent -> files" pipeline (docs/design.md §10.1):
// ValidateIntent -> ResolveTemplate -> Plan. It never requires L2/L3 fields to
// succeed for a default project (§6.2 hard rule) - Manifest.Owners is simply
// omitted from the rendered YAML when Intent.Owners is empty, so ownership
// falls back to tree-resident OWNERS inheritance (§7.3) rather than this
// package fabricating a default.
//
// On validation failure, PlanCreate returns a zero Plan and the errors -
// callers implementing preview_create_project or create_project (§8.3) must
// check len(errs) > 0 before using the Plan.
func PlanCreate(intent Intent, templates TemplateSet) (Plan, []ValidationError) {
	if errs := Validate(intent, templates); len(errs) > 0 {
		return Plan{}, errs
	}

	tmpl, ok := templates.Get(intent.TemplateID)
	if !ok {
		tmpl, ok = templates.DefaultForType(intent.Type)
		if !ok {
			return Plan{}, []ValidationError{{
				Code: "no_default_template", Field: "type",
				Message: fmt.Sprintf("no default template registered for type %q", intent.Type),
			}}
		}
	}

	path := intent.Path
	if path == "" {
		path = intent.Name
	}

	capabilities := intent.Capabilities
	if capabilities == nil {
		capabilities = tmpl.DefaultCapabilities
	}

	manifest := Manifest{
		Schema:       "project/v1",
		Name:         intent.Name,
		Type:         intent.Type,
		Owners:       intent.Owners,
		Capabilities: capabilities,
	}

	manifestYAML, err := yaml.Marshal(manifest)
	if err != nil {
		return Plan{}, []ValidationError{{Code: "internal", Message: err.Error()}}
	}

	files := make([]FileWrite, 0, 1+len(tmpl.Files(intent)))
	files = append(files, FileWrite{Path: "PROJECT.yaml", Action: "create", Content: string(manifestYAML)})
	files = append(files, tmpl.Files(intent)...)

	return Plan{Path: path, EffectiveManifest: manifest, Files: files}, nil
}
