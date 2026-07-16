# watchdog

`runko-watchdog` is the CI reconciler — §14.4.2's "a dead CI must
block loudly, not hang silently" grown an actor. It sweeps every open
Change's merge requirements and closes the two seams observed in
dogfood between Runko and its CI executor:

- **Finished but unreported**: the check's GitHub Actions run
  concluded, but the result never reached `report-check` (runner died
  mid-teardown, network blip) — the run's *real* conclusion is
  force-reported, attributed to `ci-watchdog`.
- **Never reported at all**: the dispatch was lost before CI ever saw
  it — one rescue rerun re-fires the webhook chain (one, so a
  genuinely broken pipeline still blocks loudly instead of looping).

This README is the project's spec surface; rationale decided before
2026-07-16 lives in the frozen [`docs/design.md`](../docs/design.md).

## Shape

- Ships in the runkod image; runs as its own single-replica
  Deployment. Config via flags with `RUNKO_WATCHDOG_*` env fallbacks;
  `/healthz` for probes.
- Stateless: sweep logic + two thin HTTP clients (runkod REST, GitHub
  Actions API), testable against httptest stubs (`watchdog.go` vs
  `main.go`'s flag/loop plumbing — the runko-bridge pattern).
- `consumes: [runkod]` — a stub-pinned REST client (the edge that
  finding #47 recorded); the trigger surface stays narrow until
  runkod carries an OpenAPI artifact for its REST API (recorded
  follow-up, shared with `cli`).

## Checks (owned here, §14.9)

- `watchdog-test` — `bazel test //watchdog/...` (no `-race` on
  purpose: one sequential sweep loop)
- `bazel-check` — repo-wide gazelle drift

## Decisions

New decisions land here as dated entries; the record through
2026-07-16 is [`docs/design.md`](../docs/design.md)'s frozen changelog.

- **2026-07-16** — this README becomes the project's living spec;
  `docs/design.md` is retired and frozen (see [`docs/README.md`](../docs/README.md)).
