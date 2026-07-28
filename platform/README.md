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

**Major architectural shifts only** — a decided constraint changes, a
contract surface appears or disappears, a prior decision is reversed.
Routine work (features, fixes, flags) is recorded by its change
description, never here (see [`docs/README.md`](../docs/README.md)).
Repo-wide shifts: the root [`README.md`](../README.md); the record
through 2026-07-16 is [`docs/design.md`](../docs/design.md)'s frozen
changelog.

- **2026-07-16** — this README becomes the project's living spec;
  `docs/design.md` is retired and frozen (see [`docs/README.md`](../docs/README.md)).
- **2026-07-24** — **agent path/size policy moves from receive-refusal to a
  human-acknowledged check** (a prior decision reversed: §8.7's receive-time
  enforcement of content-shaped rules). `receive.PolicyViolation` carries an
  enforcement class: *ackable* findings (denylist paths, size caps,
  owners/project/capability gates, self-grant) no longer refuse the push —
  the funnel accepts it and the findings become the reserved
  **`agent-policy`** check (`checks.AgentPolicyCheckName`) on the change,
  minted completed/failure at receive and completable only by a human with
  approve rights (never an agent, never an external reporter, never
  rerun-check). *Hard* findings still refuse: workspace affinity/provenance
  (merge-time cannot retrofit attribution) and land requests. The secret
  scan is untouched — push is publish (the mirror ships `refs/changes/*`),
  so it can never move to the gate. Rationale: a refused push is invisible
  while a pushed-but-gated change is reviewable; the executor already runs
  the change's own manifest-declared commands pre-land, so path denial at
  receive guarded the inert surface while the live one rode the ordinary
  gate. Per-head minting means every amend re-evaluates; a trivial-rebase
  carry keeps an acknowledgement exactly as it keeps approvals.
- **2026-07-28** — **approval is a tree-declared posture, not a global one**
  (a new contract surface: `PROJECT.yaml`'s `auto_approve`). A project may
  declare its subtree an **auto-approve zone**, where the merge gate reads
  owner requirements as satisfied and `agent-policy` findings as
  acknowledged — the bootstrap posture, so an agent scaffolding a new
  project lands unattended instead of blocking on a human who has nothing
  to review yet. It waives **approvals only**: required checks still gate
  (unreviewed is a governance choice, unbuilt is breakage). Resolution is
  `index.AutoApproved`: the **nearest ancestor project that declares the
  field decides**, so the root manifest is the whole-repo switch and a
  nested `auto_approve: false` carves back out — OWNERS' nearest-file rule
  applied to governance, tri-state (`*bool`) so unset stays inheritable.
  Two properties make it safe to hand agents: the daemon resolves it from
  **trunk's** tree, never the change's own head (a change enabling it
  cannot approve itself — it lands under the zone it is trying to leave),
  and enabling it edits a manifest, which is already the human lane. The
  gate reports the waiver as `MergeRequirements.AutoApproved`
  (`auto_approved` on the wire, omitted when false): a green gate must
  never make a reader infer that nobody read the change.
