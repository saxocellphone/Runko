// Release capability config (§14.10.3, stage 17b): the typed view of
// capability_config.release - docs/spec/project.schema.json's second
// documented capability_config exception, after build.
package project

// ReleaseConfig is capability_config.release with the schema's defaults
// applied. Absent capability = no release surface at all (anti-Boq §2.3:
// zero config until the project opts in).
type ReleaseConfig struct {
	// TagPrefix prefixes every release tag, defaulting to
	// "<project-path>/v" - per-project tag namespaces in one repo
	// (checkout-api's v1.2.3 is commerce/checkout-api/v1.2.3).
	TagPrefix string
	// Versioning is "semver" (default: versions validate as x.y.z and
	// auto-bump patch) or "manual" (any non-empty version string,
	// explicit-only).
	Versioning string
	// Changelog is "from-changes" (default: derived from landed Changes
	// touching the project since the previous release tag) or "none".
	Changelog string
}

// ReleaseConfig returns the project's release configuration and whether
// the release capability is declared. projectPath seeds the default tag
// prefix. Unknown/malformed config values fall back to the defaults -
// the schema validates shape at authoring time; at read time a typo'd
// value must not brick the release surface.
func (m Manifest) ReleaseConfig(projectPath string) (ReleaseConfig, bool) {
	enabled := false
	for _, c := range m.Capabilities {
		if c == "release" {
			enabled = true
			break
		}
	}
	if !enabled {
		return ReleaseConfig{}, false
	}
	cfg := ReleaseConfig{
		TagPrefix:  projectPath + "/v",
		Versioning: "semver",
		Changelog:  "from-changes",
	}
	raw, _ := m.CapabilityConfig["release"].(map[string]interface{})
	if v, ok := raw["tag_prefix"].(string); ok && v != "" {
		cfg.TagPrefix = v
	}
	if v, ok := raw["versioning"].(string); ok && (v == "semver" || v == "manual") {
		cfg.Versioning = v
	}
	if v, ok := raw["changelog"].(string); ok && (v == "from-changes" || v == "none") {
		cfg.Changelog = v
	}
	return cfg, true
}
