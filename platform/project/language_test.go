package project

import (
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
)

// findFile returns the plan file at path, failing the test when absent.
func findFile(t *testing.T, plan Plan, path string) FileWrite {
	t.Helper()
	for _, f := range plan.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("expected %s in plan files, got: %+v", path, plan.Files)
	return FileWrite{}
}

func planPaths(plan Plan) []string {
	out := make([]string, 0, len(plan.Files))
	for _, f := range plan.Files {
		out = append(out, f.Path)
	}
	return out
}

// The verbatim-echo rule (§10.4): a defaulted intent resolves to the Go
// templates but writes NO language key - pre-multi-language manifests stay
// byte-identical (the round-trip golden also pins this).
func TestPlanCreateLanguageDefaultsToGoAndOmitsLanguageFromManifest(t *testing.T) {
	plan, errs := PlanCreate(Intent{Name: "checkout-api", Type: "service"}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	findFile(t, plan, "main.go")
	manifest := findFile(t, plan, "PROJECT.yaml")
	if strings.Contains(manifest.Content, "language:") {
		t.Fatalf("defaulted intent must not write a language key, got:\n%s", manifest.Content)
	}
	if plan.EffectiveManifest.Language != "" {
		t.Fatalf("EffectiveManifest.Language must echo the (empty) intent, got %q", plan.EffectiveManifest.Language)
	}
}

func TestPlanCreatePerLanguageSkeletons(t *testing.T) {
	cases := []struct {
		lang        string
		svcFile     string
		svcContains string
		libFile     string
		libContains string
	}{
		{"go", "main.go", "func main() {}", "lib.go", "package checkout_api"},
		{"python", "main.py", "if __name__ == \"__main__\":", "checkout_api.py", `"""Package checkout_api."""`},
		{"ts", "main.ts", "function main() {}", "index.ts", "export {};"},
		{"rust", "src/main.rs", "fn main() {}", "src/lib.rs", "//! checkout-api."},
		{"java", "Main.java", "public static void main(String[] args)", "CheckoutApi.java", "public class CheckoutApi {}"},
		{"cpp", "main.cc", "int main() { return 0; }", "checkout_api.h", "#ifndef CHECKOUT_API_H_"},
	}
	templates := DefaultTemplates()
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			svc, errs := PlanCreate(Intent{Name: "checkout-api", Type: "service", Language: tc.lang}, templates)
			if len(errs) != 0 {
				t.Fatalf("service PlanCreate(%s): %v", tc.lang, errs)
			}
			if f := findFile(t, svc, tc.svcFile); !strings.Contains(f.Content, tc.svcContains) {
				t.Fatalf("%s: want %q in %s, got:\n%s", tc.lang, tc.svcContains, tc.svcFile, f.Content)
			}
			if m := findFile(t, svc, "PROJECT.yaml"); !strings.Contains(m.Content, "language: "+tc.lang) {
				t.Fatalf("%s: explicit language must be recorded, manifest:\n%s", tc.lang, m.Content)
			}

			lib, errs := PlanCreate(Intent{Name: "checkout-api", Type: "library", Language: tc.lang}, templates)
			if len(errs) != 0 {
				t.Fatalf("library PlanCreate(%s): %v", tc.lang, errs)
			}
			if f := findFile(t, lib, tc.libFile); !strings.Contains(f.Content, tc.libContains) {
				t.Fatalf("%s: want %q in %s, got:\n%s", tc.lang, tc.libContains, tc.libFile, f.Content)
			}

			// 'other' stays README-only in every language.
			other, errs := PlanCreate(Intent{Name: "checkout-api", Type: "other", Language: tc.lang}, templates)
			if len(errs) != 0 {
				t.Fatalf("other PlanCreate(%s): %v", tc.lang, errs)
			}
			for _, f := range other.Files {
				if strings.Contains(f.Path, "main") || strings.Contains(f.Path, "lib") || strings.Contains(f.Path, "index") {
					t.Fatalf("%s: type 'other' must not scaffold source, got %s", tc.lang, f.Path)
				}
			}
		})
	}
}

func TestPlanCreatePythonLibraryManifestGolden(t *testing.T) {
	plan, errs := PlanCreate(Intent{
		Name: "checkout-api", Type: "library", Language: "python",
		Owners: []string{"group:commerce-eng"},
	}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	gitfixture.Golden(t, "python_lib_manifest", findFile(t, plan, "PROJECT.yaml").Content)
}

func TestPlanCreateNoTemplateRecordsEscapeHatchLanguage(t *testing.T) {
	plan, errs := PlanCreate(Intent{
		Name: "exotic-svc", Type: "service", Language: "haskell", NoTemplate: true,
	}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}
	want := map[string]bool{"PROJECT.yaml": true, "README.md": true, "BUILD.bazel": true}
	if len(plan.Files) != len(want) {
		t.Fatalf("no-template create must emit exactly manifest+README+BUILD, got: %v", planPaths(plan))
	}
	for _, f := range plan.Files {
		if !want[f.Path] {
			t.Fatalf("unexpected scaffold file %s in no-template create: %v", f.Path, planPaths(plan))
		}
	}
	if m := findFile(t, plan, "PROJECT.yaml"); !strings.Contains(m.Content, "language: haskell") {
		t.Fatalf("escape-hatch language must be recorded verbatim, manifest:\n%s", m.Content)
	}
}

func TestValidateUnsupportedLanguage(t *testing.T) {
	errs := Validate(Intent{Name: "x-api", Type: "service", Language: "haskell"}, DefaultTemplates())
	if len(errs) != 1 || errs[0].Code != "unsupported_language" || errs[0].Field != "language" {
		t.Fatalf("want exactly one unsupported_language error, got: %+v", errs)
	}
	if !strings.Contains(errs[0].Message, "cpp, go, java, python, rust, ts") {
		t.Fatalf("error must name the supported set, got: %s", errs[0].Message)
	}
	if !strings.Contains(errs[0].Suggestion, "--no-template") {
		t.Fatalf("error must point at the escape hatch, got: %s", errs[0].Suggestion)
	}

	// NoTemplate legalizes any well-formed language - no error.
	if errs := Validate(Intent{Name: "x-api", Type: "service", Language: "haskell", NoTemplate: true}, DefaultTemplates()); len(errs) != 0 {
		t.Fatalf("no-template must accept any well-formed language, got: %+v", errs)
	}
}

func TestValidateLanguageFormat(t *testing.T) {
	errs := Validate(Intent{Name: "x-api", Type: "service", Language: "C++"}, DefaultTemplates())
	if len(errs) != 1 || errs[0].Code != "invalid_format" || errs[0].Field != "language" {
		t.Fatalf("want exactly one invalid_format error (never a second unsupported_language), got: %+v", errs)
	}
}

func TestValidateTemplateLanguageConflicts(t *testing.T) {
	templates := DefaultTemplates()

	errs := Validate(Intent{Name: "x-api", Type: "library", TemplateID: "library-default", NoTemplate: true}, templates)
	if len(errs) != 1 || errs[0].Code != "invalid_combination" {
		t.Fatalf("template_id+no_template: want invalid_combination, got: %+v", errs)
	}

	// The alias resolves to the Go template, so a python request conflicts.
	errs = Validate(Intent{Name: "x-api", Type: "library", TemplateID: "library-default", Language: "python"}, templates)
	if len(errs) != 1 || errs[0].Code != "invalid_combination" {
		t.Fatalf("template/language mismatch: want invalid_combination, got: %+v", errs)
	}

	// Matching language + template is fine.
	if errs := Validate(Intent{Name: "x-api", Type: "library", TemplateID: "library-python", Language: "python"}, templates); len(errs) != 0 {
		t.Fatalf("matching template/language must validate, got: %+v", errs)
	}
}

func TestTemplateAliasesResolveAndListIsAliasFree(t *testing.T) {
	set := DefaultTemplates()

	got, ok := set.Get("service-default")
	if !ok || got.ID != "service-go" || got.Language != "go" {
		t.Fatalf("service-default must alias service-go, got %+v ok=%v", got, ok)
	}

	list := set.List()
	if len(list) != 30 { // 6 languages x 5 types
		t.Fatalf("want 30 built-in templates, got %d", len(list))
	}
	for _, tmpl := range list {
		if strings.HasSuffix(tmpl.ID, "-default") {
			t.Fatalf("List must not contain alias ids, got %s", tmpl.ID)
		}
	}
}

// Rust is the first template with a nested scaffold path (src/main.rs) -
// pin that Apply materializes it under the project root for real.
func TestCreateRustServiceRoundTripNestedPath(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "# monorepo\n")
	base := repo.Commit("initial")

	store := gitstore.New(repo.Dir)
	plan, errs := PlanCreate(Intent{Name: "rusty-api", Type: "service", Language: "rust"}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("PlanCreate: unexpected errors: %v", errs)
	}

	newRev, err := Apply(store, core.Revision(base), plan, core.CommitMeta{
		AuthorName: "Test", AuthorEmail: "t@x.com",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	blob, err := store.GetBlob(newRev, "rusty-api/src/main.rs")
	if err != nil {
		t.Fatalf("GetBlob(rusty-api/src/main.rs): %v", err)
	}
	if string(blob.Content) != "fn main() {}\n" {
		t.Fatalf("unexpected src/main.rs content: %q", blob.Content)
	}
}
