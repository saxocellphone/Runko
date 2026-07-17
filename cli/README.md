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
- **Structured errors everywhere** (§6.5): `{code, field, message,
  suggestion, doc_url}` with a suggestion the user can type; exit
  codes 0 (success) / 1 (recognized failure) / 2 (usage).
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
