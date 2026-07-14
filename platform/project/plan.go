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

	lang := intent.Language
	if lang == "" {
		lang = DefaultLanguage
	}

	var tmpl Template
	switch {
	case intent.NoTemplate:
		// Escape hatch (§10.4): manifest + README only, no source scaffold.
		// The build capability still defaults on - the generated BUILD.bazel
		// filegroup is language-agnostic.
		tmpl = Template{
			Name:                "no template",
			DefaultCapabilities: []string{"build"},
			Files:               func(i Intent) []FileWrite { return []FileWrite{readmeFile(i)} },
		}
	case intent.TemplateID != "":
		tmpl, _ = templates.Get(intent.TemplateID) // existence checked by Validate
	default:
		var ok bool
		tmpl, ok = templates.DefaultFor(intent.Type, lang)
		if !ok {
			return Plan{}, []ValidationError{{
				Code: "no_default_template", Field: "type",
				Message: fmt.Sprintf("no default template registered for type %q, language %q", intent.Type, lang),
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

	// Build-engine resolution (§14.5.5): explicit intent wins; an explicit
	// `build` capability forces bazel (the only engine with a binding -
	// Validate already rejected vite/none alongside it); otherwise the
	// language default (ts -> vite, else bazel; no-template stays bazel).
	engine := intent.BuildEngine
	if engine == "" {
		if intent.Capabilities != nil && hasCapability(intent.Capabilities, "build") {
			engine = BuildEngineBazel
		} else {
			engine = DefaultBuildEngine(tmpl.Language, intent.NoTemplate)
		}
	}
	switch engine {
	case BuildEngineBazel:
		// An explicit bazel request implies the binding even when explicit
		// capabilities narrowed the template defaults away from it.
		if intent.BuildEngine == BuildEngineBazel && !hasCapability(capabilities, "build") {
			capabilities = append(append([]string{}, capabilities...), "build")
		}
	default:
		// vite/none territories carry no build binding: drop the DEFAULTED
		// capability (explicit `build` + vite/none was rejected by Validate,
		// so anything left here came from template defaults).
		capabilities = withoutCapability(capabilities, "build")
	}

	var capabilityConfig map[string]interface{}
	if hasCapability(capabilities, "build") {
		capabilityConfig = map[string]interface{}{"build": buildCapabilityConfig(path)}
	}

	// §13.3.1: the creation-step API answer maps onto the rpc/http
	// capabilities with their documented capability_config shapes; the
	// contract stubs are scaffolded in-boundary below.
	switch intent.API {
	case "grpc":
		if !hasCapability(capabilities, "rpc") {
			capabilities = append(append([]string{}, capabilities...), "rpc")
		}
		if capabilityConfig == nil {
			capabilityConfig = map[string]interface{}{}
		}
		capabilityConfig["rpc"] = map[string]interface{}{"path": "proto"}
	case "rest":
		if !hasCapability(capabilities, "http") {
			capabilities = append(append([]string{}, capabilities...), "http")
		}
		if capabilityConfig == nil {
			capabilityConfig = map[string]interface{}{}
		}
		capabilityConfig["http"] = map[string]interface{}{"openapi": "openapi.yaml"}
	}

	manifest := Manifest{
		Schema: "project/v1",
		Name:   intent.Name,
		Type:   intent.Type,
		// Echoed verbatim, never default-filled (§10.4): an intent that
		// omitted language leaves the key off disk, so pre-multi-language
		// manifests stay byte-identical.
		Language:         intent.Language,
		Owners:           intent.Owners,
		Capabilities:     capabilities,
		CapabilityConfig: capabilityConfig,
	}

	manifestYAML, err := yaml.Marshal(manifest)
	if err != nil {
		return Plan{}, []ValidationError{{Code: "internal", Message: err.Error()}}
	}

	files := make([]FileWrite, 0, 2+len(tmpl.Files(intent)))
	files = append(files, FileWrite{Path: "PROJECT.yaml", Action: "create", Content: string(manifestYAML)})
	files = append(files, tmpl.Files(intent)...)
	if hasCapability(capabilities, "build") {
		files = append(files, buildCapabilityFiles(path)...)
	}
	if engine == BuildEngineVite {
		files = append(files, viteScaffoldFiles(intent.Name)...)
	}
	files = append(files, apiScaffoldFiles(intent, path)...)

	return Plan{Path: path, EffectiveManifest: manifest, Files: files}, nil
}
