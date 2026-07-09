// Package search implements the search_code seam (docs/design.md §8.3,
// §28.3 stage 11): project-tagged code search served through runkod.
//
// Decision (2026-07-07, recorded here verbatim since it governs every file
// in this package): keep Zoekt, but as a PROCESS, not a library import - the
// same shell-out pattern this repo already uses for gitleaks (runkod/
// gitleaks.go), git (internal/gitstore), and Bazel (buildadapter/bazel),
// per §28.2 rule 4 ("shell out to system tools; do not vendor a Go
// reimplementation"), §11.4, and §14.5.4. A prototype that imported
// github.com/sourcegraph/zoekt as a Go library pulled in gRPC, protobuf,
// Prometheus, OpenTracing, and a WASM-based RE2 engine transitively - a poor
// fit for this project's lean-dependency, fast-build posture (`make check`
// under 30s, §28.2 rule 3) for a feature that only needs an HTTP call out
// and a CLI invocation. Zero new go.mod dependencies result from this
// package.
//
// CodeSearcher is the seam (mirroring receive.SecretScanner and
// buildadapter.Engine): ZoektSearcher is a stdlib net/http client against a
// zoekt-webserver's JSON API (/search?format=json); Indexer/ZoektIndexer
// shells out to zoekt-git-index to build/refresh the corpus a
// zoekt-webserver then serves. Neither binary is installed in this sandbox
// (see CLAUDE.md) - both are tested against scripted fake binaries
// (zoekt_test.go, indexer_test.go, the buildadapter/bazel technique) plus a
// real-binary integration test behind the zoekt_integration build tag
// (zoekt_integration_test.go, the buildadapter/bazel_integration_test.go
// technique) that is unverified in this environment.
//
// NO silent git-grep fallback (§8.2): when no zoekt-webserver is configured,
// NotConfiguredSearcher returns a structured §6.5 error rather than
// degrading to an unindexed, unranked substring scan a caller might mistake
// for the real thing.
package search
