# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository status

Implementation has started, following the session DAG in `docs/design.md` §28.3. The design spec (~1900 lines) lives at `docs/design.md` (moved from the original `spec.md` per its own standing rule, §28.2 item 6) — read the relevant section(s) cited below before editing; the spec is deliberately decided (not a discussion doc) so implementation should be transcription of a decision, not a fresh design exercise.

Progress against the session DAG (§28.3):

| # | Stage | Status |
|---|-------|--------|
| 0 | Spec artifacts (`docs/spec/`) | Done — PROJECT.yaml schema, MCP tool catalog, webhook/CheckRun schemas |
| 1 | Repo bootstrap + git-fixture harness | Done — `go.mod`, package skeletons, `internal/gitfixture` (repo builder + fake clock + seeded IDs + golden-file diffs), `make check` green in ~4s |
| 2 | Persistence (DDL + sqlc) | Done — `db/migrations` (Postgres DDL), `db/queries` (sqlc), generated `internal/dbgen` (pgx/v5). No live Postgres in this environment; verified via sqlc's own schema/query analysis + up/down symmetry check, not a real DB round-trip |
| 3 | Project model + templates + preview | Done — `project/` (Validate, PlanCreate, Apply, built-in template registry) + `internal/gitstore` (git-shell-out `core.MonorepoStore` impl, needed to make this stage's round-trip real rather than mocked). `create_project` intent → files → commit verified end-to-end in `internal/gitfixture` |
| 4 | Tree indexer + owners | Done — `index/` (`Scan` walks a revision for `PROJECT.yaml`, resolves owners: manifest > nearest-ancestor `OWNERS` > org default, per §7.3; `Sync` replaces a monorepo's project rows via `internal/dbgen`). Owners precedence table-tested via `gitfixture`; `Sync` itself unverified against live Postgres (same caveat as stage 2) |
| 5 | Affected computation | Done — `affected/` (`Compute`: pure function, longest-prefix path→project match, transitive declared-dependency closure over reverse edges, root-invalidation patterns, conservative-by-default fail-closed for unowned paths per §14.5.3). Property-tested (determinism, no dup projects, run_everything⇒reason_codes, fail-closed rule) via stdlib `testing/quick` |
| 6 | Receive funnel | Done (scoped) — `receive/`: Change-Id trailer parse/generate (Gerrit-style), magic-ref parsing, §6.9 direct-trunk-push rejection UX, `AgentPolicy`/`EvaluatePolicy` (affinity, path allow/deny, diff/file caps, owners/land/capability gating), `SecretScanner` seam (deliberately no bespoke heuristic — real scanning needs gitleaks), `Decide()` orchestration. **Out of scope**: real pre-receive hook/server wiring, real gitleaks integration — see receive/doc.go. `CreateOrUpdateChange` (Postgres persistence) unverified against live DB, same caveat as stages 2/4 |
| 7 | Land engine | Done — `land/`: rebase via `git merge-tree --write-tree` (3-way merge using the Change's recorded `base_sha` as explicit merge-base, not git's history search); `NeedsRevalidation` (pure, §13.5's affected-intersection rule + `revalidation: always` override, reusing `affected.Compute` for the trunk-delta's own affected set); `Land()` orchestrates fast-forward / rebase-and-land / revalidation-required / conflict, with the ref update always a compare-and-swap so a lost race surfaces as `RaceRetry`, never a silent overwrite. Race suite includes a real concurrent test (6 goroutines racing to land, `-race` clean, stable across repeated runs) proving exactly one wins. **Post-merge fix**: `NeedsRevalidation` originally compared only project-name lists, silently dropping `RunEverything` from both sides and hardcoding empty `affected.Options` for the trunk-delta computation - a fail-open bug (caught in review, not by CI). Fixed to take full `affected.Result` on both sides and to accept caller-supplied `affected.Options`; see git history for the isolating test cases. **Post-CI fix** (stage 9d's CI, first run on a 2-core GitHub Actions runner instead of this sandbox's 12 cores): `TestLandConcurrentRaceExactlyOneWins` asserted every losing contender gets `RaceRetry` - true only when every goroutine reads the trunk tip before anyone lands; on constrained parallelism, a goroutine scheduled late enough reads the tip AFTER the winner already moved it and goes straight to the trunk-delta/`NeedsRevalidation` check (this fixture's unowned-path fail-closed default always answers true there), correctly returning `RequiresRevalidation` instead - a second legitimate non-winning outcome `Land()` itself already documented, not a bug in `Land()`. Reproduced locally at `GOMAXPROCS=2` (fails ~65% of runs pre-fix, stable across 20+ runs post-fix down to `GOMAXPROCS=1`); fixed by accepting either outcome as "lost the race" while still requiring exactly one winner |
| 8 | Checks + merge requirements + webhook outbox | Done — `checks/`: `EvaluateCheckSet` (pattern expansion like `unit:*`, table-tested), `ComputeMergeRequirements` (owners + individual checks + check-sets → mergeable bool + plain-language blockers per §6.6), `IsStale`/TTL, webhook envelope + HMAC signing, HTTP delivery with exponential backoff. Genuinely **contract-tested** against `docs/spec/` using `santhosh-tekuri/jsonschema` (not hand-rolled assertions) — including conditional `if/then` rules (completed CheckRun requires conclusion; rerun event requires a rerun block). HTTP delivery tested against a real local `httptest.Server` (success/5xx/connection-refused), so that part is actually verified, not DB-glue-caveated. `persist.go` (Postgres wiring) stays unverified against live DB like prior stages. **Post-merge fix** (caught in review): `ComputeMergeRequirements`'s "still running" blocker used `total-len(pending)` instead of `len(pending)` (label/count mismatch — "38/40 still running" when 2 were pending); and missing check-set members (no run posted at all) were mentioned only in a blocker string, never added to `RequiredChecks`/`PendingChecks`, breaking the `required == passing ∪ failing ∪ pending` invariant callers rely on. Both fixed with regression tests confirmed to fail against the pre-fix code |
| 9 | `runko` CLI + doctor; `runko-ci` | Done (scoped) — `cmd/runko`: `doctor` (remote/hook detection + installable commit-msg Change-Id hook + cheat-sheet), `project create` (wired to `project/`+`internal/gitstore`, advances the *current local branch*, never trunk directly), `change push` (ensures a Change-Id trailer, pushes to `refs/for/<trunk>`). `cmd/runko-ci`: `affected` (wired to `affected/`+`index/`, no control-plane call needed), `checkout` (real partial-clone + cone-mode sparse-checkout), `report-check` (bearer-token POST). All genuinely tested against real local git repos/remotes (a local bare repo stands in for "remote" — real push/fetch, no mocking) and `httptest` for HTTP. **Manual end-to-end smoke test caught a real bug**: stdlib `flag` stops parsing at the first positional arg, so `project create checkout-api --type service` silently dropped `--type` — fixed by making `--name` a flag instead of positional, sidestepping the ordering trap. Stubbed (need a live control plane not available here): `auth login`, `workspace create/attach`, `change create/requirements`, `mcp serve` |
| 9a | Hardening pass — review debt (§28.3 revised DAG) | Done — ① `internal/dbtest` + `*_pg_test.go` (`index/sync_pg_test.go`, `receive/persist_pg_test.go`, `checks/persist_pg_test.go`): real integration tests for `index.Sync`, `receive.CreateOrUpdateChange`, `checks.RerunCheck`/`EnqueueWebhook`/`RecordDeliveryResult`, gated on `RUNKO_TEST_DATABASE_URL` (skip, not fail, when unset) + `make check-db` — still unverified *in this environment* (no Docker/Postgres/psql here), but now real code any environment with Postgres can run, not just sqlc analysis; ② stage-8 pending-count/vanishing-check-set fixes were already shipped (see stage 8 row above); ③ `internal/clierr` (§6.5's `{code,field,message,suggestion,doc_url}`) wired into `runko project create` (unborn-HEAD on an empty repo now **succeeds** — builds the first commit, per §6.7's "create your first project" CTA — instead of raw git exit-128; not-a-repo/detached-HEAD get structured errors) and into `runko-ci affected`/`checkout` (bad `--base`/`--head`/`--rev` get structured guidance naming the actual culprit flag); ④ `internal/gitversion` (git ≥ 2.40 check for `merge-tree --merge-base`) wired into `land.Rebase` (fails loud before the cryptic git error) and `runko doctor`'s report/cheat-sheet. **Post-CI fix** (stage 9d's first real `make check-db` run against a live Postgres): `index.Sync` passed a nil `Capabilities`/`DeclaredDependencies` slice straight through to `internal/dbgen` whenever a project's PROJECT.yaml declares neither (the common case, both being L2/opt-in) — pgx encodes a nil Go `[]string` as SQL `NULL`, violating the `NOT NULL` constraint on those columns. Exactly the class of bug sqlc's schema analysis can't catch and no sandbox here could run against a real database; found on CI's first execution, fixed by normalizing nil to `[]string{}` at the persistence boundary, confirmed by a subsequent green `make check-db` run |
| 9b | Build-graph adapter (Bazel first) | Done — spec artifact first (`docs/spec/build-adapter/`: engine interface, refinement post-back schema, Bazel `rdeps` recipe, Buck2 mapping notes — unblocked stages 9b/9c). `buildadapter/` (engine-agnostic `Engine`/`Refine`: ANY query error → `RunEverything=true`, no partial-success path) + `buildadapter/bazel` (shells out to `bazel query "rdeps(<universe>, set(<file-labels>))" --output=label`). Wired into `runko-ci affected --engine bazel [--universe] [--engine-timeout]`; an engine failure escalates the *whole* `AffectedOutput.RunEverything`, not just its own sub-field. Fake-engine fixture tests (scripted `bazel` shell script standing in for the real binary) cover success/non-zero-exit/timeout/no-changed-paths; a real-Bazel integration test exists behind `-tags bazel_integration` (no Bazel install in this sandbox, so unverified here). **Caught a real `exec.CommandContext` pitfall**: without `cmd.WaitDelay` set, a killed process whose grandchild still holds stdout/stderr open makes `Wait()` block until the *grandchild* exits too — the timeout test hung for the full sleep duration until `WaitDelay` was added |
| 9c | Opinionation mechanics (`build_discipline`, `require_build_binding`) | Done — `project/build.go`: enabling the `build` capability makes `PlanCreate` emit a generated `BUILD.bazel` (minimal filegroup stub — org-specific language rules are an org-template concern) + `capability_config.build = {engine: bazel, target_patterns: [//<path>/...]}`, with **zero hand-authored BUILD lines** (verified end-to-end via a real `runko project create` in `cmd/runko`, not just the `project/` unit test). `checks/buildbinding.go`: `DefaultBuildCheckSetPolicy` (`bazel_test:*`, scoped `affected`) + `RequireBuildBindingBlockers`, wired into `ComputeMergeRequirements` as a new `unboundProjects []string` parameter — empty/nil is a no-op (org hasn't opted in), matching how `staleCheckNames` already works; non-empty reports the §13.5 blocker per project |
| 9d | GitHub Actions CI (`.github/workflows/ci.yml`) | Done — `make check` on every push/PR, plus a real `postgres:16` service container running `make check-db`. Inserted per user direction as a hard blocker on stage 10: stages 9a's live-Postgres tests had never actually executed anywhere (no Docker/Postgres in this sandbox or the original implementer's) — this is the first environment that runs them for real, closing that gap instead of leaving it open indefinitely |
| 10 | `runkod` daemon assembly (smart-HTTP + pre-receive wiring + gitleaks) | Done — `runkod/`: `Store` interface + `MemStore` (a genuine in-memory reference implementation, also the §9.3 "Eval/dev" deployment profile, not merely a test double); `Processor` wires `receive.Decide` to real git pre-receive hook stdin, all-or-nothing across a batch like real git pre-receive semantics; `net/http/cgi`-wrapped `git http-backend` for smart-HTTP, gated by the same deploy token as the REST API (HTTP Basic, `git clone http://user:<token>@host/repo.git/`); REST API (`GET /api/changes/{key}`, `.../affected`, `.../merge-requirements`, `POST .../checks` matching `runko-ci report-check`'s exact body); `OutboxWorker` polling+delivering webhooks via `checks.Deliver`; `GitleaksScanner` (real gitleaks CLI wiring, fake-binary-tested, no gitleaks install in this sandbox so unverified here) closing the receive/secretscan.go seam. `cmd/runkod`: `serve` (bootstraps the bare repo, installs the hook, starts the HTTP server) + hidden `hook pre-receive` subcommand the installed hook shells back into. **`TestEndToEndDaemon` is a real compiled binary driven by real `git push` over real HTTP** (not local-path, not in-process shortcuts) — this caught two genuine bugs no unit test could: ① `runko change push`'s non-forced push failed non-fast-forward on amend, since `runkod` keeps `refs/for/<trunk>` as a literal repeatedly-overwritten ref rather than Gerrit's server-side redirect (fixed: force-push, `+HEAD:refs/for/<trunk>`); ② git's **object quarantine** feature sets `GIT_OBJECT_DIRECTORY`/`GIT_ALTERNATE_OBJECT_DIRECTORIES` on the pre-receive hook's own process only, but the hook forwards to the daemon over HTTP as a *different* process — the daemon couldn't see a push's just-received objects at all until those two vars were explicitly forwarded as headers and merged into every git subprocess (`internal/gitstore.Store` gained an `ExtraEnv` field for this, itself unit-tested). Deferred: workspace snapshot-ref wiring (stage 12), multi-tenant org/monorepo routing, full OIDC AuthN (§15.1). **Post-merge fixes** (caught in review): ① accepted pushes only updated the rotating `refs/for/<trunk>` ref, so a later Change's push (or the same Change's own amend) made an earlier accepted Change's commit unreachable — a real GC hazard, and it left `runko-ci` with no stable ref to fetch a specific Change by id (§14.4.4); fixed by writing a stable `refs/changes/<change-id>/head` ref on every accepted commit and storing *that* as `Change.GitRef`, proven with a test that pushes two unrelated Changes and confirms the first's ref survives the second overwriting `refs/for/main`; ② `cmd/runkod serve` constructed `NewMemStore()` unconditionally — the fully CI-tested Postgres layer sat wired to nothing, so any restart lost every Change/check/webhook even in a "durable" deployment; added `runkod.PostgresStore` (thin `internal/dbgen` adapter, reusing `receive.CreateOrUpdateChange`/`checks.RecordDeliveryResult`; one new sqlc query, `UpsertCheckRunByName`, for report-check's create-or-update-in-place semantics) wired behind a new `--database-url` flag, defaulting to `MemStore` with a loud startup warning when absent. `runkod/pgstore_pg_test.go` + `cmd/runkod`'s `TestEndToEndDaemonPersistsAcrossRestartWithPostgres` (kills and restarts the real daemon binary, confirms a Change survives via the REST API) are gated on `RUNKO_TEST_DATABASE_URL` like every other live-Postgres test in this repo; ③ `computeAffectedAndEnqueue`'s error paths were bare `return`s despite a comment promising "logged, not fatal" — now actually logged via `log.Printf`; ④ the webhook affected computation hardcoded `affected.Options{}`, ignoring org root-invalidation config entirely — `Processor` gained a `RootInvalidationPatterns` field, wired to a new `--root-invalidation` flag mirroring `runko-ci affected`'s own. Known, deliberately unfixed: hook output can appear as a doubled `remote: remote: ...` prefix, since git's client-side relay already adds one and `receive`'s rejection strings (from an earlier, already-tested stage) bake in their own — cosmetic, documented in `cmd/runkod/main.go`'s `cmdHook` |
| 11–15 | MCP server, Zoekt + AGENTS.md generator, minimal web, compose + measured loop, dogfood hardening | Not started |

Go module: `github.com/saxocellphone/runko`. Go toolchain is **not preinstalled** in this environment — it was installed to `~/.local/go/bin` by hand; export `PATH="$HOME/.local/go/bin:$PATH"` in any shell that needs `go`. Docker is not available in this environment either, so the compose eval loop (§16.4) cannot be run/tested here — code for it, but say so explicitly rather than claiming it was verified.

## Commands

```bash
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"   # go + sqlc aren't on PATH by default here
make check       # fmt + vet + test across all packages, target < 30s (§28.2 rule 3)
go test ./...    # tests only
go build ./...   # compile-check only
sqlc generate    # regenerate internal/dbgen after editing db/migrations or db/queries (see db/README.md)
make check-db    # live-Postgres integration tests; needs RUNKO_TEST_DATABASE_URL + psql (db/README.md) — not runnable in this environment
```

There is no compose stack, CI wiring, or web UI yet. Extend `Makefile` rather than introducing a second build entrypoint.

## What Runko is

Runko is a planned **monorepo operating system layered on Git** — self-hostable, OSS (Apache-2.0), for orgs of ~20–300 engineers. Three pillars (spec §1):

1. **One monorepo that feels small** — first-class Projects + CitC-class Workspaces (full-repo view, materialize only your slice).
2. **Changes that land with confidence** — change-centric review, path ownership, trustworthy affected computation, deep CI integration (never our own runners).
3. **Humans and coding agents as co-equal clients** — every flow has a GUI/CLI and a stable tool/API (MCP), with project-granular server-side enforcement for agents.

CLI name: `runko`. CI-facing CLI: `runko-ci`. Env prefix: `RUNKO_*`.

## Key architectural decisions (already settled — do not re-litigate)

These are final per spec §22.2 and should be treated as constraints on any implementation:

- **Git is the only substrate.** No custom CAS/overlay store, ever. Workspaces are upstream Git (partial clone + sparse cone + fsmonitor) plus durability via **snapshot refs** (`refs/workspaces/<id>/head`) pushed through the normal receive path (§12.1–12.2).
- **Tree-as-truth.** `PROJECT.yaml` and OWNERS live in the Git tree; the control plane (Postgres) is a **rebuildable index** of trunk, never an independent source of truth (§10.3). Ephemeral/derived state (inferred deps, workspace registry, check runs, sessions) is fine in Postgres.
- **Trunk is closed to direct push.** The only write path is change refs: `refs/for/<trunk>` with a `Change-Id` trailer (Gerrit-style), or workspace-overlay snapshot commits — both funnel through one receive path: policy → secret scan (gitleaks) → Change create/update → affected compute → webhooks (§7.4, §11.5).
- **Landing is rebase-based** with **optimistic revalidation**: if the trunk delta since the Change's checked `head_sha` doesn't intersect its affected set, land without re-running checks; otherwise re-run required checks (§13.5). A merge queue is a later optimization of this same rule, not a new semantic.
- **Affected computation is declared-only for gating in v1.** Paths → Projects (longest prefix) + declared dependency edges + root-invalidation rules. Import-based inferred deps are advisory-only and never gate merges (§13.3).
- **We do not build CI execution, a VM fleet, or a virtual filesystem.** CI: we own change identity, webhooks, Checks API, affected API, checkout contract; customers own runners/pipelines (§14). Remote/agent VMs are external via an environment contract (Coder/devcontainer templates). Virtual FS is "adopt-only, likely never" — sparse+partial+fsmonitor (Scalar-class) is the whole workspace story unless real telemetry says otherwise (§12.3).
- **Josh-proxy is an optional, not default, capability** for restricted-visibility projects, slice-as-repo ergonomics, and import sync — because Josh views carry rewritten SHAs while everything else keys on true monorepo SHAs (§12.3 Phase B).
- **Agents are normal API clients with stricter defaults**: mandatory workspace affinity for writes, path allow/denylist, diff/file-count caps, no self-approval, no owning production paths alone, server-side enforcement only (never trust client-claimed affinity) — see `AgentPolicy` (§8.7, §15.3).
- **Configuration is layered (L0–L3), anti-Boq.** Project create requires only name/type/owners (L0); everything else is generated, inferred, or opt-in via `add_capability`. Never require hand-written multi-field YAML for a default project (§2.3, §6, §10).
- **Mirror-first adoption is the front door**, not a migration afterthought: stage 0 (read-only overlay on GitHub) → stage 1 (Changes/review run on Runko, GitHub stays system of record, mirror is bidirectional) → stage 2 (SoR flips to Runko) → stage 3 (consolidate remaining repos). The mirror is transport, never a second source of truth (§18).

## Repo layout

One Go module, one package per design section, thin `core/` for interfaces (per §28.2 item 6 and the session DAG in §28.3):

```
docs/design.md      # the full design spec — cite §s from here in package docs and commits
docs/spec/          # pre-session-1 schema artifacts (PROJECT.yaml, MCP catalog, webhooks/CheckRun) — generate types from these, don't hand-duplicate
db/migrations/      # Postgres DDL (golang-migrate numbered up/down files)
db/queries/         # sqlc named queries, one file per domain
internal/dbgen/     # generated by sqlc (sqlc.yaml) — never hand-edit, rerun `sqlc generate`
internal/gitfixture/# terse git-fixture test harness: throwaway repos, fake clock, seeded IDs, golden diffs
internal/gitstore/  # core.MonorepoStore impl: shells out to system git via plumbing (read-tree/hash-object/write-tree/commit-tree)
internal/clierr/    # structured CLI/agent error shape (§6.5): {code,field,message,suggestion,doc_url}
internal/gitversion/# git --version detection + minimum-version gate (merge-tree --merge-base needs git >= 2.40)
internal/dbtest/    # live-Postgres test harness (RUNKO_TEST_DATABASE_URL-gated, skips without it)
index/              # tree indexer: PROJECT.yaml/OWNERS scan -> rebuildable Postgres project index (§10.3, §7.3)
receive/            # magic-ref + Change-Id + policy + secret scan (the "receive funnel") — discovery, not transcription
land/               # rebase-land + optimistic revalidation + race handling — discovery, not transcription
affected/           # pure function: paths/deps -> affected projects, + property tests
buildadapter/       # build-graph adapter contract (engine-agnostic Engine/Refine, fail-closed); buildadapter/bazel is the v1 engine
checks/             # Checks API, merge requirements, check-set policies, rerun-requests
project/            # intent -> files pipeline, templates, preview
mcp/                # MCP server, generated from the tool catalog in docs/spec/mcp-tools/
core/               # shared interfaces (MonorepoStore, etc.)
runkod/             # write-path daemon: Store, pre-receive Processor, smart-HTTP, REST API, webhook outbox, gitleaks scanner
cmd/runko/          # human/agent CLI
cmd/runko-ci/       # CI-facing CLI/image
cmd/runkod/         # runkod daemon entrypoint (serve + hidden hook pre-receive subcommand)
```

Each package header cites the spec section(s) it implements. Shell out to system `git`; do not use a Git-in-Go library (the spec mandates matching real upstream Git behavior).

## Implementation strategy (spec Appendix D, §28) — read before starting real build work

The spec's own build plan, since it's the most concrete guidance available:

- **Spec-before-code**: three pre-session-1 blockers must exist under `docs/spec/` before any implementation session — the `PROJECT.yaml` v1 schema, the MCP tool catalog as real JSON Schemas, and the webhook/CheckRun JSON Schemas (§26 #2/#3/#8, §28.4).
- **Deterministic codegen over hand-written boilerplate**: `oapi-codegen` from OpenAPI, `sqlc` from DDL + named queries, generated types shared across platform/`runko-ci`/MCP from one schema source. Never hand-edit generated files — regenerate.
- **Terse git-fixture test harness** (git's own `t/`-suite style): throwaway repos from short scripts, golden-file diffs, fake clock + seeded IDs, `make check` < 30s. Build this before the receive/land engines — they're the highest-risk ("discovery", not "transcription") components per §28.1.
- **SSR + htmx** for the Phase 0–1 web UI (wizard, change page, merge requirements) — no SPA until Phase 2.
- **One PR per session, along the dependency DAG in §28.3**; don't touch packages two hops away from the session's focus.
- **No mid-session dependency additions, no refactors outside the session's package, no UI polish before the end-to-end compose loop (`compose up → create project → change → land`) is green.**

## Where to look in docs/design.md for a given topic

| Topic | Section |
|---|---|
| Object model (Org/Monorepo/Project/Workspace/Change/Owner/Agent) | §7 |
| Agentic coding subsystem, MCP tool catalog, AgentPolicy | §8 |
| High-level component architecture, data stores, deployment shapes | §9 |
| Project creation intent→files pipeline | §10 |
| Git usage, write paths, `MonorepoStore` interface | §11 |
| Workspaces (CitC-class, snapshot refs, Josh, phases A/B/C) | §12 |
| Change lifecycle, affected computation, merge gates/landing | §13 |
| CI/CD integration contracts, webhook/Checks schemas, CI tier matrix | §14 |
| Auth, multi-tenancy, read ACLs, threat model | §15 |
| OSS/self-host scope, license, compose eval definition of done | §16 |
| CLI/Web/Editor/MCP client surfaces | §17 |
| Migration & mirror-first adoption ladder | §18 |
| Phased delivery plan (Phase 0–4) | §19 |
| Competitive landscape and prior art (jj, Josh, Scalar, Gerrit, CitC) | §21 |
| Decided-vs-open questions | §22 |
| Implementation strategy, token budget, session DAG | §28 |
