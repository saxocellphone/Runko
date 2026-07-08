# Compose smoke-test plan (§16.4, §28.3 stage 14+)

Two scripts, deliberately separate:

- **`scripts/compose-smoke.sh` — the frozen, timed claim.** The §16.4
  measured loop (`compose up → create → change → land → edit → change →
  land`) against §3.3's < 15-min budget. It stays minimal on purpose:
  every assertion added here distorts the number it exists to measure.
  Do not grow it.
- **`scripts/compose-edgecases.sh` — where invariants accumulate.** Runs
  after the smoke in the same CI job (image layers warm, so its own
  `compose up` is cheap), against a fresh stack + fresh volumes so
  neither script contaminates the other.

Selection rule: a scenario earns a compose slot only when the *full real
transport* changes what is being tested — real `git push` through
smart-HTTP + CGI + hook + daemon + Postgres + Docker networking. Anything
whose behavior is fully determined at a lower layer stays in the Go
suites (which already run with `-race` and live Postgres in CI).

## Covered in `compose-edgecases.sh`

| # | Invariant | Why the wire matters |
|---|-----------|----------------------|
| E1 | Wrong/missing token → git transport and REST both refuse; `/healthz` stays open | AuthN is enforced per-surface; only the wire proves all of them |
| E2 | Direct trunk push rejected with the §6.9 script | The platform's core write-path invariant, end to end |
| E3 | Tag pushes pass (§14.10.3 documented permissiveness); unrecognized refs (`refs/junk/*`) rejected | The skip-vs-reject boundary in the funnel |
| E4 | A pushed secret is rejected by **real gitleaks** before durability | First real gitleaks execution anywhere in this project (fake-binary-tested until now) |
| E5 | An agent principal's direct `refs/for` push is refused by the default §8.7 policy | REMOTE_USER → policy chain through every real process hop |
| E6 | Amend resets BOTH gates (§13.5 approval binding + head-keyed checks), re-gate → land | Stage 12c-①'s review-integrity story over real transport |
| E7 | **Optimistic-land revalidation** (§13.5): intersecting trunk delta → 409 `requires_revalidation` → rebase + re-push → gates reset → land | The platform's signature semantic; never previously exercised over the wire |
| E8 | Workspace snapshots: owner's snapshot accepted; another principal's push to the same ref rejected (§12.2); unregistered workspace ref rejected | Owner-only enforcement needs identity through the full chain |
| E9 | Daemon restart: landed Changes survive (Postgres + volumes), migrator is a no-op on reboot, a fresh change→land works after | §9.3 durable profile; migrator idempotence against a *populated* DB |
| E10 | `/metrics` gauges are truthful at a known end state | Cheap; catches wiring rot |
| E11 | `runko mcp serve` round-trips against the composed daemon | MCP tested against httptest until now; this is the real deployment shape |

## Deliberately NOT in compose (and where the coverage lives)

| Scenario | Why not | Covered by |
|---|---|---|
| Concurrent land race (exactly one winner) | Needs deterministic goroutine-level orchestration | `land`'s `-race` suite (6-way race, stable to GOMAXPROCS=1) |
| Textual merge-conflict land → 409 naming files | Behavior fixed at the git layer; wire adds nothing over E7 | `land` package + `runkod/land_test.go` |
| Check-staleness TTL blocker | Requires clock injection; compose has real clocks only | `runkod/lifecycle_test.go` (both clocks injected) |
| Bot lanes (path-scoped auto-land, per-principal gates) | Fully covered with a real compiled daemon already | `TestEndToEndDaemonBotLaneAutoLands`, `policy_gate_test.go` |
| Abandon/reopen lifecycle | State machine is store-level; wire adds nothing | `runkod/lifecycle_test.go` + pg round-trip |
| Webhook delivery + HMAC + backoff | Needs a receiver service; planned as a `webhook-sink` compose service when the outbox matters to evals | `checks/delivery_test.go` (real `httptest.Server`) |
| Zoekt search / indexing | Opt-in service, not in the eval profile | `search/` fake-binary + `-tags zoekt_integration` |
| Graceful-shutdown drain | Signal semantics, not transport | `TestDaemonGracefulShutdownOnSIGTERM` (real process) |
| Postgres outage → `/readyz` flips 503 | Ping path is trivial; stopping the DB container mid-suite makes every later scenario order-dependent | `Store.Ping` + readyz unit tests |

## Maintenance rules

1. New funnel rejection or gate ⇒ add an edge-case row here first, then
   the scenario (spec-before-code, in miniature).
2. Every scenario asserts on **stable wire text** (rejection strings,
   `clierr` codes) — if an assertion breaks because wording changed,
   treat it as a contract change, not a test nuisance.
3. The smoke's budget assertion (< 900s) is the only timing assertion;
   the edge suite asserts none.
