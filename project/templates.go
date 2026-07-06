package project

import (
	"fmt"
	"strings"
)

// Template is a versioned scaffold used by PlanCreate (docs/design.md §7.1,
// §10.4). This is a minimal built-in registry proving the intent -> files
// pipeline end to end; org-defined templates (loaded from config/DB) replace
// it in a later session - callers should depend on the TemplateSet interface
// shape, not on DefaultTemplates() being the only source.
type Template struct {
	ID                  string
	Name                string
	ProjectType         string
	DefaultCapabilities []string
	// Files returns the template's scaffold files (excluding PROJECT.yaml,
	// which PlanCreate always adds itself).
	Files func(intent Intent) []FileWrite
}

// TemplateSet is a lookup registry of templates, by id and by default-per-type.
type TemplateSet struct {
	byID   map[string]Template
	byType map[string]string
}

// Get returns the template with the given id.
func (s TemplateSet) Get(id string) (Template, bool) {
	t, ok := s.byID[id]
	return t, ok
}

// DefaultForType returns the default template registered for a project type.
func (s TemplateSet) DefaultForType(projectType string) (Template, bool) {
	id, ok := s.byType[projectType]
	if !ok {
		return Template{}, false
	}
	return s.Get(id)
}

// List returns every registered template, for get_template_catalog (§8.2).
func (s TemplateSet) List() []Template {
	out := make([]Template, 0, len(s.byID))
	for _, t := range s.byID {
		out = append(out, t)
	}
	return out
}

func goPackageName(projectName string) string {
	return strings.ReplaceAll(projectName, "-", "_")
}

func readmeFile(intent Intent) FileWrite {
	return FileWrite{
		Path:    "README.md",
		Action:  "create",
		Content: fmt.Sprintf("# %s\n\nA %s scaffolded by `runko project create`.\n", intent.Name, intent.Type),
	}
}

// DefaultTemplates returns the built-in registry: one default template per
// project type (library/service/app/job/other).
func DefaultTemplates() TemplateSet {
	entrypointFiles := func(intent Intent) []FileWrite {
		return []FileWrite{
			readmeFile(intent),
			{Path: "main.go", Action: "create", Content: "package main\n\nfunc main() {}\n"},
		}
	}

	templates := []Template{
		{
			ID: "library-default", Name: "Default library", ProjectType: "library",
			Files: func(intent Intent) []FileWrite {
				return []FileWrite{
					readmeFile(intent),
					{Path: "lib.go", Action: "create", Content: fmt.Sprintf("package %s\n", goPackageName(intent.Name))},
				}
			},
		},
		{ID: "service-default", Name: "Default service", ProjectType: "service", Files: entrypointFiles},
		{ID: "app-default", Name: "Default app", ProjectType: "app", Files: entrypointFiles},
		{ID: "job-default", Name: "Default job", ProjectType: "job", Files: entrypointFiles},
		{
			ID: "other-default", Name: "Default (other)", ProjectType: "other",
			Files: func(intent Intent) []FileWrite { return []FileWrite{readmeFile(intent)} },
		},
	}

	set := TemplateSet{byID: make(map[string]Template), byType: make(map[string]string)}
	for _, t := range templates {
		set.byID[t.ID] = t
		set.byType[t.ProjectType] = t.ID
	}
	return set
}
