# docs

Repo-wide documents: the contract artifacts tests consume, the
histories, and the frozen design spec. Since 2026-07-16 there is **no
centralized living spec** — each project's `README.md` is its spec
surface (root `README.md` for what crosses project boundaries).

## The documentation model

- **Per-project truth**: `<project>/README.md` states what the project
  owns, its decided constraints, contract surfaces, and checks — and
  carries a dated **Decisions** section.
- **Decisions sections are NOT changelogs** (cadence corrected
  2026-07-16, same day the model landed: agents were appending an entry
  per change, and concurrent stacks serializing on the same README made
  changes unlandable — recreating exactly the central-file contention
  the retirement deleted). An entry is warranted **only for a major
  architectural shift**: a decided constraint changes, a contract
  surface appears or disappears, a prior decision is reversed, a
  project is born or dissolved. Routine work — features, fixes, flags,
  papercuts — is recorded by its change description and, where it
  alters a command's behavior, `cli-contract.md`; it must not touch any
  README. When an entry *is* warranted, it lands in the same change
  that implements the shift. README body text follows the same bar:
  update it when what it *states* stops being true, not to narrate
  activity.
- **[`design.md`](design.md) is FROZEN** (retired as the living spec,
  2026-07-16). It remains the historical record: `§` citations in
  package headers, commit messages, and older docs resolve against it,
  and its §25 changelog table is closed with the retirement as its
  final row. Don't add sections, don't edit decided content, don't
  cite it for *new* work.
- **Contract artifacts stay load-bearing**: this project declares
  `spec/` and `cli-contract.md` as its `schemas` surface, and
  platform's suites consume them as runfiles (`consumes: [docs]`), so
  editing them runs `platform-test` through the ordinary closure.
  Everything else here is prose — it re-attributes to the root project
  and gates on `docs-check` (the link checker) in seconds.

## What lives here

| File | What it is |
|---|---|
| `design.md` | the original design spec + decision changelog — **frozen history** |
| `spec/` | schema artifacts (PROJECT.yaml, MCP tool catalog, webhook/CheckRun, build-adapter) — generate types from these, don't hand-duplicate |
| `cli-contract.md` | the CLI output contract: exit codes, `--json` shapes, error codes per command — kept in lockstep with `platform/agentsmd` (drift-tested) |
| `change-lifecycle.md` | the Change state machine, executable |
| `mirror.md` | the outbound mirror's behavior contract (`platform/mirror`) |
| `smoke-plan.md` | the compose eval loop's definition of done |
| `implementation-log.md` | per-stage engineering history: what each stage shipped, what its tests caught |
| `migration-findings.md` | numbered self-hosting findings — **frozen history** (ledger closed at #50, 2026-07-16); new findings live in the change descriptions that fix them |
| `images/` | doc images (prose-gated) |

## Checks (owned here)

- `docs-check` — `make check-docs`, the relative-markdown-link checker
  (also the root project's check for prose anywhere in the repo)

## Decisions

- **2026-07-16** — design.md retired as the living spec; documentation
  moves to per-project READMEs with dated Decisions sections (user
  direction: "keeping a centralized spec isn't helpful anymore").
  design.md frozen in place so `§` citations stay resolvable; the
  histories (`implementation-log.md`, `migration-findings.md`) are
  unaffected.
- **2026-07-16** — Decisions cadence corrected (user direction: "we
  shouldn't update the README every time there's a change — only on
  major architectural shifts"): entries are reserved for shifts as
  defined above; per-change entries pruned from the READMEs that had
  accumulated them.
- **2026-07-16** — `migration-findings.md` retired, ledger closed at
  finding #50 (user direction, same conversation as the cadence fix):
  the migration is done and dogfooding is the ordinary state — a
  standing findings ledger is one more central file every agent
  appends to. A new finding is recorded in the change description of
  the change that fixes it; one that changes a decided constraint gets
  a Decisions entry in the owning project's README.
