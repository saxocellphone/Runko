package main

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

const binariesManifest = "schema: project/v1\nname: cli\ntype: app\ndependencies:\n  - lib\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    binaries:\n      tag: cli-latest\n      items:\n        - name: runko\n        - name: runko-ci\n"

// TestBinariesScopedByAffectedClosure: the declaring project's binaries
// republish when it is affected - directly OR through the dependency
// closure (the real edge set, replacing the workflow's hand-maintained
// project list) - and not otherwise.
func TestBinariesScopedByAffectedClosure(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml", checksManifest("lib", "lib-test", "go test ./lib/..."))
	repo.WriteFile("other/PROJECT.yaml", checksManifest("other", "other-test", "go test ./other/..."))
	repo.WriteFile("cli/PROJECT.yaml", binariesManifest)
	base := repo.Commit("seed")

	// A dependency change pulls cli in via the closure.
	repo.WriteFile("lib/lib.go", "package lib\n")
	head := repo.Commit("touch lib")
	out, err := Binaries(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Binaries: %v", err)
	}
	if len(out.Releases) != 1 || out.Releases[0].Tag != "cli-latest" {
		t.Fatalf("closure change must republish, got %+v", out.Releases)
	}
	rel := out.Releases[0]
	if len(rel.Binaries) != 2 || rel.Binaries[0].Name != "runko" || rel.Binaries[0].Path != "cli/runko" || rel.Binaries[1].Path != "cli/runko-ci" {
		t.Fatalf("binaries with defaulted paths wrong: %+v", rel.Binaries)
	}

	// An unrelated project's change republishes nothing.
	repo.WriteFile("other/o.go", "package other\n")
	head2 := repo.Commit("touch other")
	out2, err := Binaries(repo.Dir, head, head2, nil)
	if err != nil {
		t.Fatalf("Binaries: %v", err)
	}
	if len(out2.Releases) != 0 {
		t.Fatalf("unrelated change must republish nothing, got %+v", out2.Releases)
	}
}
