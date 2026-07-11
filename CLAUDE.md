# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## THIS REPO IS SELF-HOSTED ON RUNKO (cutover 2026-07-09)

The source of record is the `runko` org on the production deployment:
`origin` = `https://…runko.victornazzaro.com/o/runko/repo.git`; the GitHub
remote is named `github` and is the OUTBOUND MIRROR — **never push to it**
(a direct push would freeze the mirror on divergence). The flow:

- Commit with `runko change create` (commits the whole working tree with a
  fresh `Change-Id`), submit with `runko change push` (or
  `git push origin HEAD:refs/for/main`); direct pushes to `main` are
  rejected (§6.9). Pushes need workspace provenance — this checkout carries
  it in git config (`runko.workspace`). For stable Change identity across
  amends, install the hook (`runko doctor --install-hook`) or work in a jj
  colocated repo.
- Required checks come from the tree (folder-per-project manifests,
  encapsulated per §14.9.1 — each project OWNS its check commands, CI is a
  generic executor): `platform/` declares `platform-test`/`platform-race`,
  `runkod/` declares `runkod-test`/`runkod-race`, `cli/` declares
  `cli-test`, `internal/` declares `internal-test` (all scoped
  `bazel test //<dir>/...`; pg tests ride the `-test` checks via
  `--test_env=RUNKO_TEST_DATABASE_URL` and `internal/dbtest`'s
  advisory-lock self-serialization — no db lane). Check classes (§14.5.9):
  the race lanes are `run_when: direct` — they run only when their own
  project's paths change, never for closure-affected dependents; the
  `-test` lanes stay affected-class (the integration surface); the filter
  is `index.ChecksFor`, shared by gate and executor; `bazel-check`
  (= `make check-bazel`, gazelle drift, repo-wide by nature) is declared by
  the Go projects and dedupes by name; `web/PROJECT.yaml`
  declares `web-check`; `db/` and `proto/` declare none and are gated via
  `dependencies:` edges. PROSE paths (§14.5.7 — markdown anywhere, LICENSE,
  doc images; the root manifest's ordered `prose:` list) de-escalate to the
  root project's `docs-check` (`make check-docs`, markdown link checker,
  seconds); the `!`-excepted load-bearing docs (`docs/spec/**`,
  `docs/cli-contract.md`) gate on `docs`'s `contracts-test` instead.
  GitHub Actions is the executor:
  webhook→`runko-bridge`→`repository_dispatch`→
  `.github/workflows/runko-checks.yml` resolves the matrix with
  `runko-ci checks --base --head` and runs each returned command; land
  with `runko change land` once green.
- Landing mirrors to GitHub `main` automatically, which still triggers
  `ci.yml` (post-land safety net — the only CI that builds the
  actually-landed, post-rebase tree) and `release-images` (affected-scoped
  image builds + the rolling `cli-latest` binary release + GitOps digest
  write-back to `k8s-cluster`, which Argo CD auto-deploys — no manual
  rollout).
- Default-deny is ON (no unpoliced lands). The `operator` principal (admin)
  exists for force-land/mirror-unfreeze; agents can never force.
- The migration record lives in `docs/migration-findings.md`.

## Repository status

The design spec (`docs/design.md`, ~2300 lines) is fully implemented
through its §28.3 session DAG: daemon (`runkod` — smart-HTTP git, receive
funnel, merge gates, land engine, REST + Connect APIs, webhook outbox,
outbound mirror, multi-org), CLIs (`runko`, `runko-ci`), workspaces,
affected computation with a Bazel adapter, checks/merge requirements,
Zoekt search, MCP server, the React web UI, and the measured
docker-compose eval loop. Current work is **dogfood hardening** (§28.3
stage 15): the platform develops on itself, and findings land as ordinary
Changes.

History lives in three places — do not re-derive it: the per-stage
engineering record (what each stage shipped, the bugs its tests caught) in
`docs/implementation-log.md`; decision-by-decision history in
`docs/design.md`'s changelog table; self-hosting findings in
`docs/migration-findings.md`.

The spec is deliberately decided (not a discussion doc) — read the cited
section before editing; implementation is transcription of a decision, not
a fresh design exercise.

Go module: `github.com/saxocellphone/runko`. In this environment the Go
toolchain, Node, jj, and bazelisk are hand-installed (`~/.local/go/bin`,
`~/.local/node/bin`, `~/.local/bin`) — export them onto PATH. Docker and
Postgres are NOT available here: compose/`check-db` code for them, but say
so explicitly rather than claiming verification. CI runs everything for
real (postgres service, bazel, jj).

## Commands

```bash
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$PATH"   # go + sqlc aren't on PATH by default here
make check       # fmt + vet + test across all packages, target < 30s (§28.2 rule 3) — the inner loop
make check-bazel-test  # the test suite under bazel (§14.5.4 golden path; the manifests' *-test checks run this scoped per project); -race / -db siblings exist
make check-db    # live-Postgres integration tests; needs RUNKO_TEST_DATABASE_URL + psql (db/README.md) — not runnable in this environment
make check-web   # web frontend: tsc + oxlint + vitest + vite build; needs Node >= 22
make check-bazel # build graph + gazelle drift + real-bazel adapter test — run `bazel run //:gazelle` after adding/moving Go files
sqlc generate    # regenerate internal/dbgen after editing db/migrations or db/queries (see db/README.md)
```

Extend `Makefile` rather than introducing a second build entrypoint.

## What Runko is

A **monorepo operating system layered on Git** — self-hostable, OSS
(Apache-2.0), for orgs of ~20–300 engineers. Three pillars (spec §1):

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
- **Affected computation is declared-only for gating in v1.** Paths → Projects (longest prefix) + declared dependency edges + root-invalidation rules (tree-borne in PROJECT.yaml). Import-based inferred deps are advisory-only and never gate merges (§13.3).
- **Checks are encapsulated in the tree** (§14.9.1): `ci.checks[].command` is the definition, the CI system is a generic executor, and the merge gate resolves required names from the change's own head tree — check renames are single-manifest changes.
- **We do not build CI execution, a VM fleet, or a virtual filesystem.** CI: we own change identity, webhooks, Checks API, affected API, checkout contract; customers own runners/pipelines (§14). Remote/agent VMs are external via an environment contract. Virtual FS is "adopt-only, likely never" — sparse+partial+fsmonitor (Scalar-class) is the whole workspace story unless real telemetry says otherwise (§12.3).
- **Josh-proxy is an optional, not default, capability** for restricted-visibility projects, slice-as-repo ergonomics, and import sync — because Josh views carry rewritten SHAs while everything else keys on true monorepo SHAs (§12.3 Phase B).
- **Agents are normal API clients with stricter defaults**: mandatory workspace affinity for writes, path allow/denylist, diff/file-count caps, no self-approval, no approving at all, server-side enforcement only (never trust client-claimed affinity) — see `AgentPolicy` (§8.7, §15.3).
- **Configuration is layered (L0–L3), anti-Boq.** Project create requires only name/type/owners (L0); everything else is generated, inferred, or opt-in via `add_capability`. Never require hand-written multi-field YAML for a default project (§2.3, §6, §10).
- **Mirror-first adoption is the front door**: the outbound mirror (M1, `docs/mirror.md`) speaks only the git wire protocol to any host; the mirror is transport, never a second source of truth (§18).
- **jj colocated is the primary client** (§21, 2026-07-08): Change-Ids derive from jj change ids via a trailer template; one push updates every Change-bearing commit in a stack (series receive); plain git remains first-class.
- **Lean dependencies.** External engines are processes, not go.mod imports (git, gitleaks, bazel, Zoekt, psql). New Go deps need explicit sanction (connect-go and protobuf were sanctioned by stage 13).

## Repo layout

One Go module; every folder with a `PROJECT.yaml` is itself a Runko
project (`repo` at the root owns only glue; `platform`, `runkod`, `cli`,
`internal`, `db`, `proto`, `web`, `docs`), and `dependencies:` edges
between them drive the affected closure — see the manifests themselves.

```
docs/design.md      # the full design spec — cite §s from here in package docs and commits
docs/spec/          # schema artifacts (PROJECT.yaml, MCP catalog, webhooks/CheckRun, build-adapter) — generate types from these, don't hand-duplicate
proto/runko/v1/     # Connect/gRPC schema for web <-> runkod (§17.4) — served by runkod/rpc.go; see proto/README.md
proto/gen/          # generated by protoc-gen-go + protoc-gen-connect-go (see proto/buf.gen.yaml) — never hand-edit
db/migrations/      # Postgres DDL (golang-migrate numbered up/down files); runkod.ApplyMigrations embeds + applies them at boot
db/queries/         # sqlc named queries, one file per domain
internal/dbgen/     # generated by sqlc (sqlc.yaml) — never hand-edit, rerun `sqlc generate`
internal/gitfixture/# terse git-fixture test harness: throwaway repos, fake clock, seeded IDs, golden diffs
internal/gitstore/  # core.MonorepoStore impl: shells out to system git plumbing
internal/clierr/    # structured CLI/agent error shape (§6.5): {code,field,message,suggestion,doc_url}
internal/gitversion/# git version gate (merge-tree --merge-base needs git >= 2.40)
internal/dbtest/    # live-Postgres test harness (DSN-gated skip; advisory-lock self-serialization)
platform/index/     # tree indexer: PROJECT.yaml/OWNERS scan -> rebuildable project index (§10.3, §7.3)
platform/receive/   # magic-ref + Change-Id + policy + secret scan (the "receive funnel")
platform/land/      # rebase-land + optimistic revalidation + race handling
platform/affected/  # pure function: paths/deps -> affected projects, + property tests
platform/buildadapter/ # build-graph adapter contract (fail-closed); bazel/ is the v1 engine
platform/checks/    # Checks API, merge requirements, check-set policies, webhook envelopes
platform/project/   # intent -> files pipeline, templates, preview
platform/mcp/       # MCP stdio server: seven read-only v1 tools over runkod's REST API (§8.3, §13.4.1)
platform/core/      # shared interfaces (MonorepoStore, etc.)
platform/search/    # Zoekt code-search integration (process, not library)
platform/mirror/    # outbound mirror to any git host (git protocol only; docs/mirror.md)
platform/agentsmd/  # AGENTS.md generator for Runko-managed monorepos (`runko agents-md`)
runkod/             # the daemon: Store, pre-receive Processor, smart-HTTP, REST + Connect APIs, outbox, org hub
runkod/cmd/runkod/  # daemon entrypoint (serve + hidden hook pre-receive subcommand)
runkod/cmd/runko-bridge/ # webhook -> GitHub repository_dispatch shim (§14.4 Mode C; self-host CI)
cli/runko/          # human/agent CLI (see docs/cli-contract.md for every command's output contract)
cli/runko-ci/       # CI-facing CLI: affected, checks, checkout, report-check
web/                # web UI: React+TS+Vite+Connect-ES over proto/runko/v1 (web/README.md); src/gen committed
```

Each package header cites the spec section(s) it implements. Shell out to
system `git`; do not use a Git-in-Go library (the spec mandates matching
real upstream Git behavior).

## Working rules (spec §28.2, still in force)

- **Spec-before-code**: new contract surfaces get their schema/doc under `docs/spec/` (or a design.md section) before implementation; record decisions in design.md's changelog as they're made.
- **Never hand-edit generated code** (`internal/dbgen`, `proto/gen`, `web/src/gen`) — regenerate.
- **Test against real git/Postgres/binaries, never mocks**: `internal/gitfixture` for repos, `internal/dbtest` for Postgres (skips without a DSN), scripted fake binaries for engines (bazel/gitleaks/zoekt pattern), compiled-binary e2e tests for the daemon and CLIs. Verify bazel-graph-sensitive changes with `bazel test`, not just `go test`.
- **One Change per session focus**; don't refactor packages two hops away. No mid-session dependency additions.
- **Structured errors everywhere** (§6.5): `{code, field, message, suggestion, doc_url}` with a suggestion the user can type; exit codes 0/1/2.
- **`make check` stays under 30s** — anything slower belongs in a scoped project check, not the inner loop.

## Where to look for a given topic

| Topic | Where |
|---|---|
| Object model (Org/Monorepo/Project/Workspace/Change/Owner/Agent) | design.md §7 |
| Agentic coding, MCP tool catalog, AgentPolicy | design.md §8 |
| Component architecture, data stores, deployment shapes | design.md §9 |
| Project creation intent→files pipeline | design.md §10 |
| Git usage, write paths, `MonorepoStore` interface | design.md §11 |
| Workspaces (CitC-class, snapshot refs, Josh) | design.md §12 |
| Change lifecycle, affected computation, merge gates/landing | design.md §13; docs/change-lifecycle.md (state machine, executable) |
| CI/CD contracts, encapsulated checks, webhook/Checks schemas | design.md §14 (executor: §14.9.1) |
| Auth, multi-tenancy, read ACLs, threat model | design.md §15 |
| OSS/self-host scope, compose eval definition of done | design.md §16; docs/smoke-plan.md |
| CLI/Web/Editor/MCP client surfaces | design.md §17; docs/cli-contract.md |
| Migration, mirror-first adoption, outbound mirror | design.md §18; docs/mirror.md |
| Competitive landscape / prior art (jj, Josh, Scalar, Gerrit, CitC) | design.md §21 |
| Decided-vs-open questions | design.md §22 |
| Implementation strategy + session DAG | design.md §28; docs/implementation-log.md (history) |
| Self-hosting findings (numbered, ongoing) | docs/migration-findings.md |
