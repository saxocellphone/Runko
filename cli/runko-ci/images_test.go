package main

import (
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// End-to-end §14.9.1: deploy.image / rider declarations in the tree, resolved
// from a base..head range into the image-build matrix, sharing the same
// affected computation as `affected`/`checks`. A rider change rebuilds the
// OWNER's image, carrying the owner's build config.
func TestImagesResolvesRiderChangeToOwnerImage(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("runkod/PROJECT.yaml", "schema: project/v1\nname: runkod\ntype: service\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: runkod\n      context: .\n      dockerfile: Dockerfile\n")
	repo.WriteFile("mailer/PROJECT.yaml", "schema: project/v1\nname: mailer\ntype: service\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    workloads:\n      - name: runko-mailer\n        image: runkod\n")
	base := repo.Commit("seed")
	repo.WriteFile("mailer/main.go", "package main\n")
	head := repo.Commit("touch mailer")

	out, err := Images(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if out.RunEverything || len(out.Images) != 1 {
		t.Fatalf("mailer-only change must rebuild exactly the runkod image, got %+v", out)
	}
	img := out.Images[0]
	if img.Name != "runkod" || img.Context != "." || img.Dockerfile != "Dockerfile" {
		t.Fatalf("unexpected image build: %+v", img)
	}
}

// An owner declares its build config; a change to the owner rebuilds it with
// that config (build_args included).
func TestImagesOwnerChangeCarriesBuildArgs(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("web/PROJECT.yaml", "schema: project/v1\nname: web\ntype: app\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: web\n      context: web\n      dockerfile: web/Dockerfile\n      build_args:\n        VITE_RUNKO_URL: \"/\"\n")
	base := repo.Commit("seed")
	repo.WriteFile("web/app.ts", "export const x = 1\n")
	head := repo.Commit("touch web")

	out, err := Images(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(out.Images) != 1 || out.Images[0].Name != "web" || out.Images[0].BuildArgs["VITE_RUNKO_URL"] != "/" {
		t.Fatalf("web change must carry its build_args, got %+v", out)
	}
}

// The post-filter's defining property: a change to a DEPENDENCY of an image
// owner rebuilds the owner's image, WITHOUT that dependency being enumerated
// anywhere - it rides in via the affected dependents closure. This is the one
// behavior a naive "touched paths" filter would miss.
func TestImagesRebuildsOwnerWhenDependencyChanges(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml", "schema: project/v1\nname: lib\ntype: library\n")
	repo.WriteFile("api/PROJECT.yaml", "schema: project/v1\nname: api\ntype: service\ndependencies:\n  - lib\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: api\n      context: .\n      dockerfile: Dockerfile\n")
	base := repo.Commit("seed")
	repo.WriteFile("lib/lib.go", "package lib\n")
	head := repo.Commit("touch lib (api's dependency)")

	out, err := Images(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(out.Images) != 1 || out.Images[0].Name != "api" {
		t.Fatalf("a change to api's dependency must rebuild the api image, got %+v", out)
	}
}

// A root build-sensitive change escalates to run_everything: every declared
// image rebuilds (fail closed), independent of the touched project.
func TestImagesRunEverythingRebuildsAll(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", "schema: project/v1\nname: repo\ntype: other\nroot_invalidation:\n  - go.mod\n")
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\ncapabilities:\n  - deploy\ncapability_config:\n  deploy:\n    image:\n      name: svc\n      context: svc\n      dockerfile: svc/Dockerfile\n")
	base := repo.Commit("seed")
	repo.WriteFile("go.mod", "module x\n")
	head := repo.Commit("touch a root build-sensitive file")

	out, err := Images(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if !out.RunEverything || len(out.Images) != 1 || out.Images[0].Name != "svc" {
		t.Fatalf("run_everything must rebuild every declared image, got %+v", out)
	}
}

// A change touching no deployable project yields an empty image set (JSON
// still emits, with an empty images array).
func TestImagesEmptyForNonDeployChange(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml", "schema: project/v1\nname: lib\ntype: library\n")
	base := repo.Commit("seed")
	repo.WriteFile("lib/lib.go", "package lib\n")
	head := repo.Commit("touch lib")

	out, err := Images(repo.Dir, base, head, nil)
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if out.RunEverything || len(out.Images) != 0 {
		t.Fatalf("non-deploy change must yield no images, got %+v", out)
	}
}
