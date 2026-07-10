# Agent instructions for this repo

Runko is a monorepo platform, implemented from `docs/design.md` (the full
design spec) and **self-hosted on itself**: `origin` is the production
Runko instance, the GitHub remote (`github`) is a read-only outbound
mirror — **never push to it**. Commit with `runko change create`, submit
with `runko change push`, land with `runko change land`; direct pushes to
`main` are rejected. This file is the short version; `CLAUDE.md` is the
fuller operating manual, and the cited design.md section always wins.

## Commands

```bash
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"   # go + sqlc are not on the default PATH in this environment
make check             # fmt + vet + test, all packages, target < 30s — the inner loop
make check-bazel-test  # the same suite under bazel (what CI's per-project *-test checks run, scoped)
make check-bazel       # build graph + gazelle drift — run `bazel run //:gazelle` after adding/moving Go files
make check-web         # web/: tsc + oxlint + vitest + build (Node >= 22, ~/.local/node/bin here)
make check-db          # live-Postgres tests — needs RUNKO_TEST_DATABASE_URL; skips are silent without it
sqlc generate          # regenerate internal/dbgen after editing db/migrations or db/queries
```

No Docker/Postgres in this sandbox: compose (`make check-compose`) and
`check-db` run for real only on CI — say so rather than claiming local
verification.

## Layout map

```
docs/design.md      # full design spec — cite §s in commits/comments, don't re-derive decisions
docs/spec/          # schema source of truth: PROJECT.yaml, MCP catalog, webhook/CheckRun, build-adapter
db/                 # migrations + sqlc queries -> internal/dbgen (generated, never hand-edit)
internal/           # gitfixture (test harness — never mock git), gitstore, clierr, dbtest, dbgen
platform/           # control-plane libraries: receive, land, affected, checks, index, project,
                    # search, mirror, buildadapter, agentsmd, mcp, core
runkod/             # write-path daemon (+ binaries under runkod/cmd/: runkod, runko-bridge)
cli/runko/          # human/agent CLI          (docs/cli-contract.md = output contracts)
cli/runko-ci/       # CI-facing CLI: affected, checks, checkout, report-check
proto/runko/v1/     # Connect schema web <-> runkod; generated Go in proto/gen, TS in web/src/gen
web/                # React + TS + Vite + Connect-ES UI
```

Every folder with a `PROJECT.yaml` is itself a Runko project; its
`ci.checks` are the checks CI runs for it (§14.9.1 — CI is a generic
executor), and `dependencies:` edges drive affected/CI scoping — keep both
true when adding cross-project imports or test suites.

## Rules

- **Read the design.md section a package cites before changing that package.** The spec is decided, not a discussion draft. If you think a decision is wrong, say so; don't silently diverge.
- **One focus per session/change.** Don't edit packages two hops away (design.md §28.2 rule 7). No mid-session dependency additions — external engines are processes (git, gitleaks, bazel, Zoekt), not go.mod imports.
- **Never hand-edit generated code** (`internal/dbgen`, `proto/gen`, `web/src/gen`). Regenerate instead.
- **Shell out to system `git`.** Never a Git-in-Go library — the spec requires matching real upstream Git behavior exactly.
- **No mocking git in tests.** Use `internal/gitfixture` (throwaway repos + golden diffs); daemon/CLI behavior is proven by compiled-binary e2e tests over real HTTP pushes.
- **Verify bazel-sensitive changes with `bazel test`**, not just `go test` — the runfiles sandbox catches what a cwd-relative test misses, and CI runs the suite under bazel.
- **Structured errors everywhere** (design.md §6.5): `{code, message, retryable, field?, suggestion?, doc_url?}`, suggestion a typeable command; exit codes 0/1/2. Never a bare string for a machine caller.
