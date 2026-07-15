package contract

import (
	"strings"
	"testing"
)

const module = "github.com/saxocellphone/runko"

// fixture mirrors the live repo's §13.3.1 shape: runkod owns an in-boundary
// contract (runkod/proto/gen), the transitional standalone proto project
// owns proto/gen (rpc path "."), mailer declares no deps, web declares
// proto, and the root "repo" project is the fallback owner.
func fixture() []Project {
	return []Project{
		{Name: "repo", Path: "."},
		{Name: "runkod", Path: "runkod", Dependencies: []string{"platform", "internal", "db", "proto"}, ContractGenDir: "runkod/proto/gen"},
		{Name: "proto", Path: "proto", ContractGenDir: "proto/gen"},
		{Name: "mailer", Path: "mailer"},
		{Name: "web", Path: "web", Dependencies: []string{"proto"}},
		{Name: "platform", Path: "platform"},
	}
}

func goFile(imports ...string) []byte {
	var b strings.Builder
	b.WriteString("package x\n\nimport (\n")
	for _, i := range imports {
		b.WriteString("\t_ \"" + i + "\"\n")
	}
	b.WriteString(")\n")
	return []byte(b.String())
}

func codes(vs []Violation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Code)
	}
	return out
}

func TestUndeclaredContractImportIsRefused(t *testing.T) {
	vs := Check(module, fixture(), []File{{
		Path:    "mailer/mailer.go",
		Content: goFile(module + "/runkod/proto/gen/mailer/v1"),
	}}, []string{"mailer/mailer.go"})
	if len(vs) != 1 || vs[0].Code != "undeclared_contract_dependency" {
		t.Fatalf("want one undeclared_contract_dependency, got %v", vs)
	}
	if !strings.Contains(vs[0].Message, "mailer") || !strings.Contains(vs[0].Message, "runkod") {
		t.Fatalf("violation must name consumer and owner: %q", vs[0].Message)
	}
	if !strings.Contains(vs[0].Suggestion, `"runkod"`) {
		t.Fatalf("suggestion must name the missing edge: %q", vs[0].Suggestion)
	}
}

func TestDeclaredEdgePermitsConsumption(t *testing.T) {
	vs := Check(module, fixture(), []File{{
		Path:    "runkod/rpc.go",
		Content: goFile(module + "/proto/gen/runko/v1"),
	}}, []string{"runkod/rpc.go"})
	if len(vs) != 0 {
		t.Fatalf("runkod declares proto; want no violations, got %v", vs)
	}
}

func TestTransitiveEdgeIsNotEnough(t *testing.T) {
	// platform -> (nothing); give web a file importing runkod's contract:
	// web declares proto, and even if proto declared runkod, the edge must
	// be DIRECT (§13.3.1 strict-deps).
	vs := Check(module, fixture(), []File{{
		Path:    "web/tools/gen.go",
		Content: goFile(module + "/runkod/proto/gen/mailer/v1"),
	}}, nil)
	if len(vs) != 1 || vs[0].Code != "undeclared_contract_dependency" {
		t.Fatalf("want direct-edge violation, got %v", vs)
	}
}

func TestOwnProjectAndNonContractImportsPass(t *testing.T) {
	vs := Check(module, fixture(), []File{
		// self-consumption: runkod importing its own gen dir
		{Path: "runkod/invite.go", Content: goFile(module + "/runkod/proto/gen/mailer/v1")},
		// cross-project NON-contract import: out of §13.3.1 v1 scope
		{Path: "mailer/mailer.go", Content: goFile(module + "/platform/core")},
		// stdlib / external imports
		{Path: "mailer/main.go", Content: goFile("net/http", "connectrpc.com/connect")},
	}, nil)
	if len(vs) != 0 {
		t.Fatalf("want no violations, got %v", vs)
	}
}

func TestUnparsableAndNonGoFilesAreSkipped(t *testing.T) {
	vs := Check(module, fixture(), []File{
		{Path: "mailer/broken.go", Content: []byte("this is not go")},
		{Path: "mailer/README.md", Content: []byte("import \"" + module + "/runkod/proto/gen/mailer/v1\"")},
	}, nil)
	if len(vs) != 0 {
		t.Fatalf("want no violations, got %v", vs)
	}
}

func TestEmptyModulePathSkipsImportCheck(t *testing.T) {
	vs := Check("", fixture(), []File{{
		Path:    "mailer/mailer.go",
		Content: goFile(module + "/runkod/proto/gen/mailer/v1"),
	}}, nil)
	if len(vs) != 0 {
		t.Fatalf("no module path -> no import check, got %v", vs)
	}
}

func TestMissingOpenAPIRefusedOnlyWhenTouched(t *testing.T) {
	projects := append(fixture(), Project{
		Name: "billing", Path: "billing",
		DeclaresHTTP: true, OpenAPIPath: "billing/openapi.yaml", OpenAPIPresent: false,
	})

	// Untouched project: pre-existing violation must not block unrelated work.
	if vs := Check(module, projects, nil, []string{"mailer/mailer.go"}); len(vs) != 0 {
		t.Fatalf("untouched http project must not fire, got %v", vs)
	}

	// Manifest touched (declaring http without the doc) -> refused.
	vs := Check(module, projects, nil, []string{"billing/PROJECT.yaml"})
	if len(vs) != 1 || vs[0].Code != "missing_openapi_document" {
		t.Fatalf("want missing_openapi_document, got %v", vs)
	}

	// Document deleted out from under the declaration -> refused.
	vs = Check(module, projects, nil, []string{"billing/openapi.yaml"})
	if got := codes(vs); len(got) != 1 || got[0] != "missing_openapi_document" {
		t.Fatalf("want missing_openapi_document on delete, got %v", vs)
	}

	// Document present -> clean.
	projects[len(projects)-1].OpenAPIPresent = true
	if vs := Check(module, projects, nil, []string{"billing/PROJECT.yaml"}); len(vs) != 0 {
		t.Fatalf("present document must pass, got %v", vs)
	}
}
