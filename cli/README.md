# cli

The client binaries — `runko/` (the human/agent CLI) and `runko-ci/`
(the CI-facing CLI). Pure `main` packages over the `platform`
libraries; REST clients of the daemon. This README is the project's
spec surface; rationale decided before 2026-07-16 lives in the frozen
[`docs/design.md`](../docs/design.md).

**The output contract is [`docs/cli-contract.md`](../docs/cli-contract.md)**
— exit codes, `--json` shapes, and error codes for every command. It
lives under `docs/` (not here) because it is a declared schema surface
consumed by tests as runfiles; keep it in lockstep with
`platform/agentsmd`'s command inventory (a drift test enforces this).

## Decided constraints

- **The runko CLI is the primary interface** for the basic loop —
  commit (`change create`), submit (`change push`), land, snapshot —
  in every checkout, jj-colocated included. jj is the surgical client
  for mid-stack rework (`jj edit`/`jj squash`/`jj split`) and
  diagnosis (§21, repositioned 2026-07-11).
- **In a colocated checkout, jj owns the working copy** (2026-07-24).
  The local verbs drive jj there rather than git: `change create`
  describes `@`, `change amend` squashes into it, and Change identity
  DERIVES from jj's change id. Transport, the daemon, the git wire
  protocol, CI checkout, and workspace materialization stay pure git —
  the split is working copy versus everything else, not client versus
  server.
- **Structured errors everywhere** (§6.5): `{code, field, message,
  suggestion, doc_url}` with a suggestion the user can type; exit
  codes 0 (success) / 1 (recognized failure) / 2 (usage).
- **The command shell is cobra/pflag, shaped by
  [clig.dev](https://clig.dev)** (2026-07-22): grouped root help,
  per-command `--help` with examples, did-you-mean suggestions,
  generated shell completions, POSIX short/long flags (`-m`, `-w`),
  and `--runkod-url`/`--token` as root persistent flags with a
  uniform flags > `RUNKO_RUNKOD_URL`/`RUNKO_TOKEN` env > stored-login
  credential rule. Command names, aliases, `--json` shapes, and exit
  codes are contract-frozen across the redesign; cobra is the ONLY
  sanctioned CLI-framework dependency (with its pflag/mousetrap
  closure), and command files stay wiring-only over the platform
  libraries.
- **Every data-producing command takes `--json`**; human output names
  the next command rather than describing state abstractly.
- **Raw git is transport only.** The CLI wraps the write path
  (`refs/for/`, snapshot refs, workspace provenance push options);
  the generated teaching surfaces (`runko agents-md`, the agent
  skill) say so to agents.
- **Workspaces are materialized into the managed home**
  (`$RUNKO_WORKSPACE_HOME`, default `~/runko-ws`) from
  credential-neutral shared stores; auth is injected per invocation
  via the credential helper, never baked into remote URLs (§12.7).
- **`runko self-update` converges the binary on the rolling
  `cli-latest` GitHub release** — content-hash identity,
  checksum-verified, atomic swap (2026-07-16); the release is the
  source of truth for binary distribution.
- **No `-race` lane on purpose**: these are sequential CLIs; the
  concurrent surfaces (land engine, daemon) carry their own.

## Releases

The first project with the `release` capability: `runko release create
--project cli` cuts `cli/vX.Y.Z` with a changelog derived from landed
Changes touching this folder (§14.10.3). Independently, the rolling
`cli-latest` GitHub release rebuilds whenever a landing affects the
CLI input set (`.github/workflows/release-images.yml`).

## Checks (owned here, §14.9)

- `cli-test` — `bazel test //cli/...`
- `bazel-check` — repo-wide gazelle drift

## Decisions

**Major architectural shifts only** — a decided constraint changes, a
contract surface appears or disappears, a prior decision is reversed.
Routine work (features, fixes, flags) is recorded by its change
description and `docs/cli-contract.md`, never here (see
[`docs/README.md`](../docs/README.md)). Repo-wide shifts: the root
[`README.md`](../README.md); the record through 2026-07-16 is
[`docs/design.md`](../docs/design.md)'s frozen changelog.

- **2026-07-16** — this README becomes the project's living spec;
  `docs/design.md` is retired and frozen (see [`docs/README.md`](../docs/README.md)).
- **2026-07-22** — the hand-rolled stdlib-flag dispatch is replaced by
  a cobra/pflag command tree per clig.dev (the first sanctioned CLI
  framework dependency); the output contract survives byte-for-byte,
  single-dash long flags (`-json`) do not.
- **2026-07-24** — **jj owns the working copy in a colocated
  checkout.** The local working-copy verbs stop shelling git there and
  drive jj instead: `change create` describes `@` and opens a fresh
  working copy above it, `change amend` folds `@` into the change
  below with `jj squash` (it used to refuse jj outright), and
  `status`'s dirty count reads `@`'s own diff. Change identity is
  DERIVED from jj's change id rather than minted client-side — jj
  replaces a foreign `Change-Id` trailer with its own on any rewrite,
  so a git-minted id split one piece of work across two Changes the
  first time an author ran `jj describe`. The verbs also refuse, as
  `outside_sparse_cone` / `suspect_artifact`, work jj cannot see
  (outside `jj sparse`) or declined to snapshot (over
  `snapshot.max-new-file-size`), both of which jj otherwise omits from
  the commit with only a warning on stderr and a zero exit.

  The boundary is the working copy, NOT client-versus-server:
  transport (`push`/`fetch`/`ls-remote`), the daemon's bare-repo
  plumbing, `git http-backend`, server-side rebase via `merge-tree`,
  the outbound mirror, and `runko-ci checkout` all stay pure git and
  have no jj counterpart. Workspace MATERIALIZATION also stays git:
  jj 0.43 cannot read a partial clone (it fails to fetch from the
  promisor remote rather than lazily fetching), so adopting it there
  would cost `--filter=blob:none` machine-wide — revisit if jj gains
  promisor support.
