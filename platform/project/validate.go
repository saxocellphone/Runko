package project

import (
	"fmt"
	"regexp"
	"strings"
)

// These mirror the patterns/enums in docs/spec/project.schema.json exactly -
// keep in sync by hand until codegen exists (see project/doc.go).
var (
	namePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	ownerPattern    = regexp.MustCompile(`^(user|group):[a-z0-9][a-z0-9._-]*$`)
	languagePattern = regexp.MustCompile(`^[a-z][a-z0-9+_-]{0,31}$`)
)

var validTypes = map[string]bool{
	"library": true, "service": true, "app": true, "job": true, "other": true,
}

var validCapabilities = map[string]bool{
	"rpc": true, "http": true, "deploy": true, "data_store": true, "build": true,
	"release": true,
}

// Validate checks an Intent against project.schema.json's L0 constraints,
// failing at the decision rather than after a long edit cycle (§6.5). It is
// also the implementation of the validate_project_intent MCP tool.
func Validate(intent Intent, templates TemplateSet) []ValidationError {
	var errs []ValidationError

	switch {
	case intent.Name == "":
		errs = append(errs, ValidationError{
			Code: "required_field", Field: "name", Message: "name is required",
		})
	case !namePattern.MatchString(intent.Name):
		errs = append(errs, ValidationError{
			Code: "invalid_format", Field: "name",
			Message:    "name must match ^[a-z][a-z0-9-]{1,62}$",
			Suggestion: "use lowercase letters, digits, and hyphens only",
		})
	}

	if !validTypes[intent.Type] {
		errs = append(errs, ValidationError{
			Code: "invalid_enum", Field: "type",
			Message: fmt.Sprintf("type must be one of library, service, app, job, other; got %q", intent.Type),
		})
	}

	for _, o := range intent.Owners {
		if !ownerPattern.MatchString(o) {
			errs = append(errs, ValidationError{
				Code: "invalid_format", Field: "owners",
				Message:    fmt.Sprintf("owner %q must match user:<id> or group:<id>", o),
				Suggestion: "e.g. group:commerce-eng",
			})
		}
	}

	for _, c := range intent.Capabilities {
		if !validCapabilities[c] {
			errs = append(errs, ValidationError{
				Code: "invalid_enum", Field: "capabilities",
				Message: fmt.Sprintf("unknown capability %q", c),
			})
		}
	}

	// One error per field: the unsupported-language check only runs when the
	// format check passed, and never when NoTemplate makes any language legal.
	if intent.Language != "" {
		switch {
		case !languagePattern.MatchString(intent.Language):
			errs = append(errs, ValidationError{
				Code: "invalid_format", Field: "language",
				Message:    "language must match ^[a-z][a-z0-9+_-]{0,31}$",
				Suggestion: "use a lowercase identifier, e.g. go, python, ts, rust, java, cpp",
			})
		case !intent.NoTemplate && !templates.HasLanguage(intent.Language):
			errs = append(errs, ValidationError{
				Code: "unsupported_language", Field: "language",
				Message:    fmt.Sprintf("no built-in template for language %q; supported: %s", intent.Language, strings.Join(templates.Languages(), ", ")),
				Suggestion: "pass --no-template to scaffold only PROJECT.yaml and README.md, recording the language as-is",
			})
		}
	}

	if intent.NoTemplate && intent.TemplateID != "" {
		errs = append(errs, ValidationError{
			Code: "invalid_combination", Field: "template_id",
			Message: "template_id and no_template are mutually exclusive",
		})
	}

	switch intent.BuildEngine {
	case "", BuildEngineBazel, BuildEngineVite, BuildEngineNone:
	default:
		errs = append(errs, ValidationError{
			Code: "unsupported_build_engine", Field: "build_engine",
			Message:    fmt.Sprintf("unknown build engine %q; supported: %s", intent.BuildEngine, strings.Join(BuildEngines, ", ")),
			Suggestion: "omit build_engine for the language default (ts -> vite, else bazel; docs/design.md §14.5.5)",
		})
	}
	// The `build` capability declares a hermetic build-graph binding
	// (§14.5.4); vite/none territories deliberately have none - a silent
	// downgrade here would leave the capability lying about refinement.
	if (intent.BuildEngine == BuildEngineVite || intent.BuildEngine == BuildEngineNone) && hasCapability(intent.Capabilities, "build") {
		errs = append(errs, ValidationError{
			Code: "invalid_combination", Field: "build_engine",
			Message:    fmt.Sprintf("the build capability declares a qualifying build-graph binding (§14.5.4); %q is not a qualifying engine", intent.BuildEngine),
			Suggestion: "use build_engine bazel, or drop the explicit build capability",
		})
	}

	switch intent.API {
	case "", "grpc", "rest", "none":
	default:
		errs = append(errs, ValidationError{
			Code: "unsupported_api", Field: "api",
			Message:    fmt.Sprintf("unknown api %q; supported: grpc, rest, none (§13.3.1)", intent.API),
			Suggestion: "grpc scaffolds an in-boundary proto contract, rest a mandatory OpenAPI document, none records no surface",
		})
	}
	// A service decides its contract surface at creation (§13.3.1) - one
	// enum answer, not YAML; "none" is a valid answer, silence is not.
	if intent.Type == "service" && intent.API == "" {
		errs = append(errs, ValidationError{
			Code: "api_required", Field: "api",
			Message:    "a service decides its contract surface at creation (§13.3.1)",
			Suggestion: "pass --api grpc|rest|none",
		})
	}
	// Only serving types get an API surface (§13.3.1, refined 2026-07-15):
	// a library is consumed through build dependencies and a job has no
	// callers - a job that grows a serving surface is a type change first.
	if (intent.API == "grpc" || intent.API == "rest") &&
		intent.Type != "service" && intent.Type != "app" {
		errs = append(errs, ValidationError{
			Code: "invalid_combination", Field: "api",
			Message:    fmt.Sprintf("a %s cannot declare an API surface - grpc/rest are for services and apps (§13.3.1)", intent.Type),
			Suggestion: "drop --api (or pass none), or create the project as a service/app",
		})
	}

	if intent.TemplateID != "" {
		if t, ok := templates.Get(intent.TemplateID); !ok {
			errs = append(errs, ValidationError{
				Code: "not_found", Field: "template_id",
				Message:    fmt.Sprintf("unknown template %q", intent.TemplateID),
				Suggestion: "call get_template_catalog for valid ids",
			})
		} else if intent.Language != "" && t.Language != "" && t.Language != intent.Language {
			errs = append(errs, ValidationError{
				Code: "invalid_combination", Field: "template_id",
				Message: fmt.Sprintf("template %q is a %s template but language %q was requested", intent.TemplateID, t.Language, intent.Language),
			})
		}
	}

	return errs
}
