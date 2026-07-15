package project

import (
	"strings"
	"testing"
)

func deleteTargets() []DeleteTarget {
	return []DeleteTarget{
		{Name: "repo", Path: "."},
		{Name: "runkod", Path: "runkod"},
		{Name: "proto", Path: "proto"},
		{Name: "proto2", Path: "proto2"},
	}
}

func TestPlanDeleteRefusals(t *testing.T) {
	if _, errs := PlanDelete("ghost", deleteTargets(), nil, nil); len(errs) != 1 || errs[0].Code != "unknown_project" {
		t.Fatalf("unknown: %v", errs)
	}
	if _, errs := PlanDelete("repo", deleteTargets(), nil, nil); len(errs) != 1 || errs[0].Code != "root_project_immutable" {
		t.Fatalf("root: %v", errs)
	}
	if _, errs := PlanDelete("proto", deleteTargets(), nil, nil); len(errs) != 1 || errs[0].Code != "unknown_project" {
		t.Fatalf("no files at rev must refuse: %v", errs)
	}
}

func TestPlanDeleteDeletesAndStripsEdges(t *testing.T) {
	runkodManifest := `# the daemon
schema: project/v1
name: runkod
type: service
dependencies:
  - platform
  - internal
  - proto
ci:
  checks:
    - name: runkod-test
      command: bazel test //runkod/...
`
	webManifest := `schema: project/v1
name: web
type: app
# clients declare the edge
consumes:
  - proto
ci:
  checks:
    - name: web-check
      command: cd web && npm run check
`
	plan, errs := PlanDelete("proto", deleteTargets(),
		[]string{"proto/PROJECT.yaml", "proto/runko/v1/common.proto"},
		[]ManifestRef{
			{Path: "runkod/PROJECT.yaml", Content: []byte(runkodManifest)},
			{Path: "web/PROJECT.yaml", Content: []byte(webManifest)},
		})
	if len(errs) != 0 {
		t.Fatalf("PlanDelete: %v", errs)
	}
	if plan.Path != "proto" || len(plan.Ops) != 4 {
		t.Fatalf("plan = %+v", plan)
	}

	byPath := map[string]DeleteOp{}
	for _, op := range plan.Ops {
		byPath[op.Path] = op
	}
	if byPath["proto/PROJECT.yaml"].Action != "delete" || byPath["proto/runko/v1/common.proto"].Action != "delete" {
		t.Fatalf("subtree not deleted: %+v", plan.Ops)
	}

	stripped := byPath["runkod/PROJECT.yaml"].Content
	if strings.Contains(stripped, "- proto") {
		t.Fatalf("proto edge survived:\n%s", stripped)
	}
	for _, keep := range []string{"- platform", "- internal", "# the daemon", "runkod-test"} {
		if !strings.Contains(stripped, keep) {
			t.Fatalf("stripping destroyed %q:\n%s", keep, stripped)
		}
	}

	// web's consumes list emptied: the key line goes with it.
	webStripped := byPath["web/PROJECT.yaml"].Content
	if strings.Contains(webStripped, "consumes:") || strings.Contains(webStripped, "- proto") {
		t.Fatalf("emptied consumes block must drop its key:\n%s", webStripped)
	}
	if !strings.Contains(webStripped, "web-check") {
		t.Fatalf("stripping destroyed the ci block:\n%s", webStripped)
	}
}

func TestStripEdgeExactNameOnly(t *testing.T) {
	m := "schema: project/v1\nname: x\ndependencies:\n  - proto2\n  - protoX\n"
	out, changed := StripEdge(m, "proto")
	if changed || !strings.Contains(out, "- proto2") || !strings.Contains(out, "- protoX") {
		t.Fatalf("prefix names must survive: changed=%v\n%s", changed, out)
	}
	if _, changed := StripEdge("no edges here\n", "proto"); changed {
		t.Fatal("no-op manifest reported change")
	}
}
