package project

import (
	"strings"
	"testing"
)

// §14.5.5 build-engine selection. The behavior matrix: language defaults
// (ts -> vite, else bazel; no-template stays bazel), explicit overrides in
// both directions, vite's scaffold shape (package.json + vite.config.ts,
// NO build capability/binding), and the structured errors.

func filePaths(files []FileWrite) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func hasFile(files []FileWrite, path string) bool {
	for _, f := range files {
		if f.Path == path {
			return true
		}
	}
	return false
}

func TestBuildEngineDefaultsByLanguage(t *testing.T) {
	cases := []struct {
		name     string
		intent   Intent
		wantVite bool
	}{
		{"go default", Intent{Name: "svc", Type: "service"}, false},
		{"python -> bazel", Intent{Name: "svc", Type: "service", Language: "python"}, false},
		{"ts -> vite", Intent{Name: "webapp", Type: "app", Language: "ts"}, true},
		{"ts library -> vite", Intent{Name: "ui-kit", Type: "library", Language: "ts"}, true},
		{"no-template ts stays bazel", Intent{Name: "legacy", Type: "other", Language: "ts", NoTemplate: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, errs := PlanCreate(tc.intent, DefaultTemplates())
			if len(errs) != 0 {
				t.Fatalf("PlanCreate: %v", errs)
			}
			gotVite := hasFile(plan.Files, "vite.config.ts")
			gotBazel := hasFile(plan.Files, "BUILD.bazel")
			if gotVite != tc.wantVite || gotBazel != !tc.wantVite {
				t.Fatalf("want vite=%v, got files %v", tc.wantVite, filePaths(plan.Files))
			}
			if tc.wantVite {
				if hasCapability(plan.EffectiveManifest.Capabilities, "build") {
					t.Fatalf("a vite territory must not carry the build capability (§14.5.5 rule 4), got %+v", plan.EffectiveManifest.Capabilities)
				}
				if plan.EffectiveManifest.CapabilityConfig != nil {
					t.Fatalf("a vite territory must have no build binding, got %+v", plan.EffectiveManifest.CapabilityConfig)
				}
				if !hasFile(plan.Files, "package.json") {
					t.Fatalf("vite scaffold must include package.json, got %v", filePaths(plan.Files))
				}
			}
		})
	}
}

func TestBuildEngineExplicitOverrides(t *testing.T) {
	// Force bazel onto a ts project: golden path emitted, binding present.
	plan, errs := PlanCreate(Intent{Name: "webapp", Type: "app", Language: "ts", BuildEngine: BuildEngineBazel}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("bazel-on-ts: %v", errs)
	}
	if !hasFile(plan.Files, "BUILD.bazel") || hasFile(plan.Files, "vite.config.ts") {
		t.Fatalf("explicit bazel on ts must emit BUILD.bazel and no vite scaffold, got %v", filePaths(plan.Files))
	}
	if !hasCapability(plan.EffectiveManifest.Capabilities, "build") {
		t.Fatalf("explicit bazel must carry the build binding, got %+v", plan.EffectiveManifest.Capabilities)
	}

	// Force vite onto a Go project.
	plan, errs = PlanCreate(Intent{Name: "svc", Type: "service", BuildEngine: BuildEngineVite}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("vite-on-go: %v", errs)
	}
	if hasFile(plan.Files, "BUILD.bazel") || !hasFile(plan.Files, "vite.config.ts") {
		t.Fatalf("explicit vite must emit the vite scaffold only, got %v", filePaths(plan.Files))
	}

	// none: no scaffold at all, defaulted build capability dropped.
	plan, errs = PlanCreate(Intent{Name: "svc", Type: "service", BuildEngine: BuildEngineNone}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("none: %v", errs)
	}
	if hasFile(plan.Files, "BUILD.bazel") || hasFile(plan.Files, "vite.config.ts") || hasFile(plan.Files, "package.json") {
		t.Fatalf("engine none must scaffold nothing, got %v", filePaths(plan.Files))
	}
	if hasCapability(plan.EffectiveManifest.Capabilities, "build") {
		t.Fatalf("engine none must drop the defaulted build capability, got %+v", plan.EffectiveManifest.Capabilities)
	}

	// Explicit build capability with no engine forces bazel even on ts.
	plan, errs = PlanCreate(Intent{Name: "webapp", Type: "app", Language: "ts", Capabilities: []string{"build"}}, DefaultTemplates())
	if len(errs) != 0 {
		t.Fatalf("explicit-build-cap on ts: %v", errs)
	}
	if !hasFile(plan.Files, "BUILD.bazel") || hasFile(plan.Files, "vite.config.ts") {
		t.Fatalf("explicit build capability must force bazel, got %v", filePaths(plan.Files))
	}
}

func TestBuildEngineStructuredErrors(t *testing.T) {
	_, errs := PlanCreate(Intent{Name: "svc", Type: "service", BuildEngine: "gradle"}, DefaultTemplates())
	if len(errs) != 1 || errs[0].Code != "unsupported_build_engine" || !strings.Contains(errs[0].Message, "bazel") {
		t.Fatalf("expected unsupported_build_engine naming the set, got %v", errs)
	}

	_, errs = PlanCreate(Intent{Name: "svc", Type: "service", BuildEngine: BuildEngineVite, Capabilities: []string{"build"}}, DefaultTemplates())
	if len(errs) != 1 || errs[0].Code != "invalid_combination" || errs[0].Field != "build_engine" {
		t.Fatalf("expected invalid_combination for vite + explicit build capability, got %v", errs)
	}
}
