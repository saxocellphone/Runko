# docs

Repo-wide documents: the contract artifacts tests consume, the
histories, and the frozen design spec. Since 2026-07-16 there is **no
centralized living spec** — each project's `README.md` is its spec
surface, and new decisions are recorded there as dated entries (root
`README.md` for repo-wide decisions).

## The documentation model

- **Per-project truth**: `<project>/README.md` states what the project
  owns, its decided constraints, contract surfaces, and checks — and
  carries a dated **Decisions** section that replaces the old central
  changelog. Write the entry in the same change that implements the
  decision.
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
| `migration-findings.md` | numbered self-hosting findings — **still live**, dogfood findings keep landing here |
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
