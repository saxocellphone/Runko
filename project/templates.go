package project

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultLanguage is the template language an intent with no Language
// resolves to. Resolution only - the manifest echoes Intent.Language
// verbatim and never writes this default (§10.4).
const DefaultLanguage = "go"

// Template is a versioned scaffold used by PlanCreate (docs/design.md §7.1,
// §10.4). This is a minimal built-in registry proving the intent -> files
// pipeline end to end; org-defined templates (loaded from config/DB) replace
// it in a later session - callers should depend on the TemplateSet interface
// shape, not on DefaultTemplates() being the only source.
type Template struct {
	ID                  string
	Name                string
	ProjectType         string
	Language            string // "" for language-neutral org templates
	DefaultCapabilities []string
	// Files returns the template's scaffold files (excluding PROJECT.yaml,
	// which PlanCreate always adds itself).
	Files func(intent Intent) []FileWrite
}

type typeLang struct{ projectType, language string }

// TemplateSet is a lookup registry of templates: by id, by default-per-
// (type, language), plus alias ids (the pre-multi-language `<type>-default`
// names, kept out of byID so List never double-counts the Go set).
type TemplateSet struct {
	byID       map[string]Template
	byTypeLang map[typeLang]string
	aliases    map[string]string
}

// Get returns the template with the given id, resolving aliases.
func (s TemplateSet) Get(id string) (Template, bool) {
	if target, ok := s.aliases[id]; ok {
		id = target
	}
	t, ok := s.byID[id]
	return t, ok
}

// DefaultFor returns the default template registered for a (type, language)
// pair.
func (s TemplateSet) DefaultFor(projectType, language string) (Template, bool) {
	id, ok := s.byTypeLang[typeLang{projectType, language}]
	if !ok {
		return Template{}, false
	}
	return s.Get(id)
}

// HasLanguage reports whether any template is registered for the language.
func (s TemplateSet) HasLanguage(language string) bool {
	for key := range s.byTypeLang {
		if key.language == language {
			return true
		}
	}
	return false
}

// Languages returns the sorted set of languages with registered templates,
// for error messages and the template catalog.
func (s TemplateSet) Languages() []string {
	seen := map[string]bool{}
	for key := range s.byTypeLang {
		seen[key.language] = true
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// List returns every registered template (aliases excluded), for
// get_template_catalog (§8.2).
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

// javaPackageName strips hyphens: checkout-api -> checkoutapi (hyphens and
// underscores are both unidiomatic in Java package segments).
func javaPackageName(projectName string) string {
	return strings.ReplaceAll(projectName, "-", "")
}

// javaClassName CamelCases hyphen segments: checkout-api -> CheckoutApi.
func javaClassName(projectName string) string {
	var b strings.Builder
	for _, seg := range strings.Split(projectName, "-") {
		if seg == "" {
			continue
		}
		b.WriteString(strings.ToUpper(seg[:1]))
		b.WriteString(seg[1:])
	}
	return b.String()
}

// cppGuard renders the include guard: checkout-api -> CHECKOUT_API_H_.
func cppGuard(projectName string) string {
	return strings.ToUpper(strings.ReplaceAll(projectName, "-", "_")) + "_H_"
}

func readmeFile(intent Intent) FileWrite {
	return FileWrite{
		Path:    "README.md",
		Action:  "create",
		Content: fmt.Sprintf("# %s\n\nA %s scaffolded by `runko project create`.\n", intent.Name, intent.Type),
	}
}

// langDef is one built-in template language: an entrypoint scaffold for
// service/app/job and a library scaffold. Skeletons are source-only - no
// go.mod/Cargo.toml/package.json/tsconfig; toolchain config is an org-
// template concern, the same split as language BUILD rules (§10.4, §14.5.4).
type langDef struct {
	id      string
	display string
	entry   func(intent Intent) FileWrite
	lib     func(intent Intent) FileWrite
}

// builtinLangs are admitted by Bazel-rule maturity (§10.4, decided
// 2026-07-08): rules_java/rules_cc are Bazel-core, rules_python is
// first-party, rules_rust lives in the bazelbuild org, rules_ts is Aspect's.
// js deliberately misses the cut its own criterion sets - it flows through
// the no_template escape hatch until a later batch.
var builtinLangs = []langDef{
	{
		id: "go", display: "Go",
		entry: func(Intent) FileWrite {
			return FileWrite{Path: "main.go", Action: "create", Content: "package main\n\nfunc main() {}\n"}
		},
		lib: func(intent Intent) FileWrite {
			return FileWrite{Path: "lib.go", Action: "create", Content: fmt.Sprintf("package %s\n", goPackageName(intent.Name))}
		},
	},
	{
		id: "python", display: "Python",
		entry: func(Intent) FileWrite {
			return FileWrite{Path: "main.py", Action: "create", Content: "def main():\n    pass\n\n\nif __name__ == \"__main__\":\n    main()\n"}
		},
		lib: func(intent Intent) FileWrite {
			mod := goPackageName(intent.Name)
			return FileWrite{Path: mod + ".py", Action: "create", Content: fmt.Sprintf("\"\"\"Package %s.\"\"\"\n", mod)}
		},
	},
	{
		id: "ts", display: "TypeScript",
		entry: func(Intent) FileWrite {
			return FileWrite{Path: "main.ts", Action: "create", Content: "function main() {}\n\nmain();\n"}
		},
		lib: func(Intent) FileWrite {
			return FileWrite{Path: "index.ts", Action: "create", Content: "export {};\n"}
		},
	},
	{
		id: "rust", display: "Rust",
		entry: func(Intent) FileWrite {
			return FileWrite{Path: "src/main.rs", Action: "create", Content: "fn main() {}\n"}
		},
		lib: func(intent Intent) FileWrite {
			return FileWrite{Path: "src/lib.rs", Action: "create", Content: fmt.Sprintf("//! %s.\n", intent.Name)}
		},
	},
	{
		id: "java", display: "Java",
		entry: func(intent Intent) FileWrite {
			return FileWrite{Path: "Main.java", Action: "create",
				Content: fmt.Sprintf("package %s;\n\npublic class Main {\n    public static void main(String[] args) {}\n}\n", javaPackageName(intent.Name))}
		},
		lib: func(intent Intent) FileWrite {
			class := javaClassName(intent.Name)
			return FileWrite{Path: class + ".java", Action: "create",
				Content: fmt.Sprintf("package %s;\n\npublic class %s {}\n", javaPackageName(intent.Name), class)}
		},
	},
	{
		id: "cpp", display: "C++",
		entry: func(Intent) FileWrite {
			return FileWrite{Path: "main.cc", Action: "create", Content: "int main() { return 0; }\n"}
		},
		lib: func(intent Intent) FileWrite {
			guard := cppGuard(intent.Name)
			return FileWrite{Path: goPackageName(intent.Name) + ".h", Action: "create",
				Content: fmt.Sprintf("#ifndef %s\n#define %s\n\n#endif  // %s\n", guard, guard, guard)}
		},
	},
}

// DefaultTemplates returns the built-in registry: a `<type>-<lang>` matrix
// over library/service/app/job/other × builtinLangs, with the historical
// `<type>-default` ids aliased to the Go set. Every template defaults to
// the "build" capability (decided 2026-07-08: Bazel is the org default,
// §14.5.4's greenfield golden path) - so a bare `runko project create`
// emits generated BUILD.bazel wiring and capability_config.build
// ({engine: bazel, target_patterns: [//<path>/...]}) with zero
// hand-authored lines, and `runko-ci affected --engine bazel` can refine
// from day one. The BUILD stub is a language-agnostic filegroup, so it is
// identical across languages. Opt-out stays possible: an explicit (non-nil)
// capability list in the intent replaces the defaults entirely.
func DefaultTemplates() TemplateSet {
	set := TemplateSet{
		byID:       make(map[string]Template),
		byTypeLang: make(map[typeLang]string),
		aliases:    make(map[string]string),
	}

	for _, lang := range builtinLangs {
		for _, pt := range []string{"library", "service", "app", "job", "other"} {
			files := func(lang langDef, pt string) func(Intent) []FileWrite {
				return func(intent Intent) []FileWrite {
					switch pt {
					case "other":
						return []FileWrite{readmeFile(intent)}
					case "library":
						return []FileWrite{readmeFile(intent), lang.lib(intent)}
					default:
						return []FileWrite{readmeFile(intent), lang.entry(intent)}
					}
				}
			}(lang, pt)

			name := fmt.Sprintf("%s %s", lang.display, pt)
			if pt == "other" {
				name = fmt.Sprintf("%s (other)", lang.display)
			}
			t := Template{
				ID:                  pt + "-" + lang.id,
				Name:                name,
				ProjectType:         pt,
				Language:            lang.id,
				DefaultCapabilities: []string{"build"},
				Files:               files,
			}
			set.byID[t.ID] = t
			set.byTypeLang[typeLang{pt, lang.id}] = t.ID
		}
	}

	for _, pt := range []string{"library", "service", "app", "job", "other"} {
		set.aliases[pt+"-default"] = pt + "-" + DefaultLanguage
	}
	return set
}
