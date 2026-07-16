# platform

The control-plane domain libraries: everything Runko *decides* lives
here as pure-ish Go packages, consumed by `runkod` (the daemon), `cli`,
and `watchdog`. This README is the project's spec surface; rationale
decided before 2026-07-16 lives in the frozen [`docs/design.md`](../docs/design.md)
(the `§` citations below and throughout the package headers resolve
there).

## Packages

| Package | Owns |
|---|---|
| `receive/` | the receive funnel: magic-ref (`refs/for/<trunk>`) parsing, Change-Id extraction, policy checks, secret scan (gitleaks), series receive for stacks (§7.4, §11.5) |
| `land/` | rebase-based landing, revalidation tiers, trivial-rebase detection, race handling (§13.5) |
| `affected/` | pure function: touched paths + declared edges → affected project closure, with property tests (§13.3) |
| `checks/` | Checks API domain, merge requirements, check classes (`run_when`), webhook envelopes (§14) |
| `contract/` | receive-time contract checks: imports under another project's gen dir need a declared edge; `http` needs an in-boundary OpenAPI doc (§13.3.1) |
| `index/` | tree indexer: `PROJECT.yaml`/`OWNERS` scan → the rebuildable project index (§10.3, §7.3) |
| `project/` | intent → files pipeline, templates, preview, delete plans (§10, §13.1) |
| `buildadapter/` (+ `bazel/`) | build-graph adapter contract, fail-closed; bazel is the v1 engine (§14.5) |
| `mcp/` | MCP stdio server: read-only tools over runkod's REST API (§8.3) |
| `agentsmd/` | the generated agent teaching surfaces: `AGENTS.md` + the loadable skill (§8.8) |
| `search/` | Zoekt code-search integration — a process, not a library (§9) |
| `mirror/` | outbound mirror to any git host, git wire protocol only ([`docs/mirror.md`](../docs/mirror.md), §18) |
| `githubapp/` | GitHub App installation-token minting (stdlib RS256): the deployment-wide credential that replaces per-org PATs for mirror pushes and `runko-bridge` dispatch |
| `core/` | shared interfaces (`MonorepoStore`, …) |

## Decided constraints (do not re-litigate)

- **Git is the only substrate.** No custom CAS or overlay store. Shell
  out to system `git` (via `internal/gitstore`); never a Git-in-Go
  library — matching real upstream Git behavior is mandatory (§11).
- **Tree-as-truth.** Manifests and OWNERS live in the Git tree; the
  index in Postgres is rebuildable, never an independent source of
  truth (§10.3).
- **One write path.** Trunk is closed; change refs and workspace
  snapshots both funnel through receive: policy → secret scan →
  contract check → Change create/update → affected compute → webhooks.
- **Landing is rebase-based**, with conflict-only revalidation by
  default (Gerrit's model; orgs can tighten to `affected-intersection`
  or `always`). Trivial rebases carry approvals — and, under
  conflict-only, passing checks — forward (§13.5).
- **Affected computation gates on declared edges only.** Longest-prefix
  path→project, `dependencies:`/`consumes:` edges, root-invalidation
  patterns, prose de-escalation. Inferred deps are advisory, never
  gating (§13.3); `contract.Check` refuses provably incomplete
  declarations at receive instead.
- **Checks are encapsulated in the tree** (§14.9.1): a manifest's
  `ci.checks[].command` is the definition, CI is a generic executor,
  and a check command never names another project's targets — if your
  tests consume my files, declare the edge.
- **Agents are normal clients with stricter, server-enforced defaults**
  (`AgentPolicy`, §8.7): workspace affinity, path allow/denylist,
  per-change size caps, no self-approval — never client-trusted.
- **External engines are processes, not go.mod imports** (git,
  gitleaks, bazel, Zoekt). New Go dependencies need explicit sanction.

## Contracts consumed

`consumes: [docs]` — the suites in `checks/`, `mcp/`, and `agentsmd/`
consume `docs/spec/**` and `docs/cli-contract.md` as runfiles
(`data = ["//docs:contracts"]`), so a schema change runs `platform-test`
through the ordinary closure.

## Checks (owned here, §14.9)

- `platform-test` — `make fmt` + `bazel test //platform/...` (pg tests
  ride the `RUNKO_TEST_DATABASE_URL` passthrough and skip without it)
- `platform-race` — same tree under `-race`; `run_when: direct` (only
  when platform's own code changes)
- `bazel-check` — repo-wide gazelle drift, deduped by name

Tests use real fixtures, never mocks: `internal/gitfixture` for repos,
scripted fake binaries for engines (the bazel/gitleaks/zoekt pattern),
`internal/dbtest` for live Postgres.

## Decisions

New decisions land here as dated entries (repo-wide ones go in the root
[`README.md`](../README.md)); the record through 2026-07-16 is
[`docs/design.md`](../docs/design.md)'s frozen changelog.

- **2026-07-16** — this README becomes the project's living spec;
  `docs/design.md` is retired and frozen (see [`docs/README.md`](../docs/README.md)).
- **2026-07-16** — **GitHub App auth for the GitHub integration plane**
  (`githubapp/`): one deployment-wide App credential (app id + private
  key) replaces per-org PATs; per-org GitHub setup shrinks to
  "install the App on the mirror repo". Installation tokens are minted
  on demand via a stdlib-only RS256 App JWT (no new dependencies),
  cached, and refreshed before their one-hour expiry; they serve both
  Bearer REST auth (`repository_dispatch`) and `x-access-token` git
  pushes. `mirror/` stays git-wire-only: it gained an injected
  `TokenSource func() (string, error)` and never imports the minting
  package; a failed mint fails that one git call and the worker's
  reconcile loop re-drives it. Static `token=` config always wins over
  App auth; PATs remain fully supported.
