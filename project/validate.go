package project

import (
	"fmt"
	"regexp"
)

// These mirror the patterns/enums in docs/spec/project.schema.json exactly -
// keep in sync by hand until codegen exists (see project/doc.go).
var (
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	ownerPattern = regexp.MustCompile(`^(user|group):[a-z0-9][a-z0-9._-]*$`)
)

var validTypes = map[string]bool{
	"library": true, "service": true, "app": true, "job": true, "other": true,
}

var validCapabilities = map[string]bool{
	"rpc": true, "http": true, "deploy": true, "data_store": true,
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

	if intent.TemplateID != "" {
		if _, ok := templates.Get(intent.TemplateID); !ok {
			errs = append(errs, ValidationError{
				Code: "not_found", Field: "template_id",
				Message:    fmt.Sprintf("unknown template %q", intent.TemplateID),
				Suggestion: "call get_template_catalog for valid ids",
			})
		}
	}

	return errs
}
