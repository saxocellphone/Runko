// Package citemplates embeds the canonical generic Runko CI/CD GitHub
// Actions workflow templates (templates/ci/*.yml) so `runko ci init` can
// scaffold them into any Runko-hosted repo. The embedded files ARE the
// shipped templates - one source of truth, so `ci init` output can never
// drift from what templates/ci/README.md documents.
package citemplates

import "embed"

// FS holds the generic workflow templates: runko-checks.yml (pre-land CI,
// dispatched by runko-change) and runko-images.yml (post-land CD,
// dispatched by runko-image-build), plus the adoption README. Both
// workflows download the runko-ci binary themselves, so they run in any
// repo, not just this one.
//
//go:embed runko-checks.yml runko-images.yml README.md
var FS embed.FS
