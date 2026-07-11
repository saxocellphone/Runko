package project

import "testing"

func TestReleaseConfigDisabledWithoutCapability(t *testing.T) {
	m := Manifest{Capabilities: []string{"http"}}
	if _, ok := m.ReleaseConfig("commerce/checkout-api"); ok {
		t.Fatalf("release config must be disabled without the release capability")
	}
}

func TestReleaseConfigDefaults(t *testing.T) {
	m := Manifest{Capabilities: []string{"release"}}
	cfg, ok := m.ReleaseConfig("commerce/checkout-api")
	if !ok {
		t.Fatalf("expected enabled")
	}
	if cfg.TagPrefix != "commerce/checkout-api/v" || cfg.Versioning != "semver" || cfg.Changelog != "from-changes" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestReleaseConfigOverridesAndBadValuesFallBack(t *testing.T) {
	m := Manifest{
		Capabilities: []string{"release"},
		CapabilityConfig: map[string]interface{}{
			"release": map[string]interface{}{
				"tag_prefix": "checkout/v",
				"versioning": "manual",
				"changelog":  "sometimes", // not a valid value - falls back
			},
		},
	}
	cfg, _ := m.ReleaseConfig("commerce/checkout-api")
	if cfg.TagPrefix != "checkout/v" || cfg.Versioning != "manual" || cfg.Changelog != "from-changes" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
