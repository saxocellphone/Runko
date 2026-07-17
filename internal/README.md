# internal

Shared internal packages, importable by `platform/`, `runkod/`, and
`cli/` — which is why this project must stay at the module root (Go's
`internal/` visibility rule). This README is the project's spec
surface; rationale decided before 2026-07-16 lives in the frozen
[`docs/design.md`](../docs/design.md).

## Packages

| Package | Owns |
|---|---|
| `gitstore/` | the `core.MonorepoStore` implementation: shells out to system git plumbing |
| `gitfixture/` | the test harness: throwaway repos, fake clock, seeded IDs, golden diffs |
| `clierr/` | the structured error shape (§6.5): `{code, field, message, suggestion, doc_url}` — shared by CLI, REST, RPC, and UI so consumers branch on `code`, never message text |
| `gitversion/` | the git version gate (`merge-tree --merge-base` needs git ≥ 2.40) |
| `dbtest/` | live-Postgres test harness: DSN-gated skip + cross-process advisory-lock serialization (see [`db/README.md`](../db/README.md) for the rules when writing pg tests) |
| `dbgen/` | **generated** by sqlc from `db/` — never hand-edit, rerun `sqlc generate` |

## Decided constraints

- **Shell out to system `git`; never a Git-in-Go library.** Matching
  real upstream Git behavior is the point (§11); `gitfixture` exists
  so every test runs against real repositories, not mocks.
- **Test against real things**: real git, real Postgres (skipped
  without a DSN), scripted fake *binaries* for external engines,
  compiled-binary e2e for daemons/CLIs. No mock frameworks.
- **Generated code is regenerated, never edited** (`dbgen/` here;
  same rule as `runkod/proto/gen` and `web/src/gen`).

## Checks (owned here, §14.9)

- `internal-test` — `bazel test //internal/...` (consumers' checks
  arrive via the affected closure since platform/runkod/cli all
  depend on this project, but their commands are scoped to their own
  subtrees — this check is what runs internal's own tests)

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
