# Compose smoke-test plan (§16.4, §28.3 stage 14+)

Three scripts, deliberately separate:

- **`scripts/compose-smoke.sh` — the frozen, timed claim.** The §16.4
  measured loop (`compose up → create → change → land → edit → change →
  land`) against §3.3's < 15-min budget. It stays minimal on purpose:
  every assertion added here distorts the number it exists to measure.
  Do not grow it.
- **`scripts/compose-edgecases.sh` — where invariants accumulate.** Runs
  after the smoke in the same CI job (image layers warm, so its own
  `compose up` is cheap), against a fresh stack + fresh volumes so
  neither script contaminates the other.
- **`scripts/compose-onboarding.sh` — the onboarding journey.** Not an
  invariant list but a replayed user session (§6.10): a new human with
  nothing but the CLI and a host URL gets from zero to landed changes,
  then brings an agent along. Own stack, fresh volumes, signup +
  org-create enabled (`RUNKO_ALLOW_SIGNUP`/`RUNKO_ALLOW_ORG_CREATE`) —
  the O-rows below. Runs last in the same CI job.

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
| E7 | **Optimistic-land revalidation** (§13.5, under an explicitly-configured `revalidation: affected-intersection` daemon — the org opt-in tier; the default `conflict-only` tier lands this scenario with zero re-runs, pinned by the runkod e2e test `TestHandleLandChangeDefaultLandsAcrossIntersectingTrunkAdvance`): intersecting trunk delta → 409 `requires_revalidation` → rebase + re-push → gates reset → land | The opt-in tier's signature semantic; never previously exercised over the wire |
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
| Workspace observability loop (§12.6): `workspace watch --once` → snapshot → event row → `WatchWorkspace` frame | Fully covered against a compiled daemon + real git already; a compose row adds only transport variety the streaming test covers over httptest | `TestRPCWorkspaceObservability` (stream e2e), `runkod/snapshot_test.go` (receive rows), `cli/runko/watch_test.go` (loop mechanics incl. jj) |

## Maintenance rules

1. New funnel rejection or gate ⇒ add an edge-case row here first, then
   the scenario (spec-before-code, in miniature).
2. Every scenario asserts on **stable wire text** (rejection strings,
   `clierr` codes) — if an assertion breaks because wording changed,
   treat it as a contract change, not a test nuisance.
3. The smoke's budget assertion (< 900s) is the only timing assertion;
   the edge suite asserts none.

## Control-plane sign-in/sign-up matrix (§15.1, §15.2, multi-org)

The scenarios below are the sign-in/sign-up contract: every user path
that begins at the web login page (`web/src/api/client.ts` `signIn`/
`signUp`) or `runko auth login`/`runko auth signup` (§6.10). Per the selection rule these live in the
Go suite, not compose — every behavior here is fully determined at the
HTTP-handler layer, and the suite drives the **full hub handler** (org
routing included), which is byte-for-byte the mux `cmd/runkod` serves.
Implementation: `runkod/signin_smoke_test.go`.

The two-sided contract:

- **Happy paths answer with zero error statuses.** Whatever credential a
  legitimate user holds, presenting it to an org they may reach
  completes the whole login sequence (config → signup? → whoami → org
  list) without a single non-2xx anywhere.
- **Every refusal is the documented structured code.** The login page
  maps statuses onto human messages (401 wrong password / 403 wrong org
  / 404 no such org), so a drifted status is a user-facing lie — and a
  bare 500 is a bug on any user-reachable path.

### Happy rows (all must be error-free end to end)

| # | Credential | Surfaces that must accept it |
|---|-----------|------------------------------|
| S1 | Operator principal (`--principal`), Basic | root, `/o/<default>/`, every other org (membership-exempt); `operator: true` in whoami |
| S2 | Deploy token — Bearer, and as Basic password under **any** username | root + every org mount; whoami `anonymous: true, operator: true` |
| S3 | Stored account, org **creator** (signup `org_mode: create`) | own org immediately after the 201; whoami `admin: true`; org list = exactly its memberships |
| S4 | Stored account, org **joiner** (signup `org_mode: join`, default org included) | root AND `/o/<default>/` (both mounts of the same org) |
| S5 | Stored account in **two orgs** | whoami on both; org list carries both roles |
| S6 | Bot lane, Basic name:token | root + org mounts (lanes are server-wide config); whoami `lane: true` |
| S7 | Agent principal (minted), Basic and Bearer | its own org; whoami `is_agent: true` |
| S8 | The full signup sequence (config → signup → whoami on the returned org → org list) - the web client's login page AND `runko auth signup` both encode over exactly this | every step 2xx, `api_base`/`git_url` usable verbatim |
| S9 | Passwords are opaque: colons allowed (Basic splits on the FIRST colon), 8-char minimum boundary accepted | signup + whoami round-trip |
| S10 | CORS preflights on `/api/signup`, `/api/auth/config`, `/api/orgs`, `/o/<org>/api/whoami` | 204 with `Allow-Origin: *`, unauthenticated |
| S11 | **Per-org identity** (migration 0017): the same username in two orgs is two independent accounts - each signs into its own org, selectors never leak the other's orgs, and an account creating a second org gets its credential cloned there | `TestSameNameDifferentOrgs` |

### Refusal rows (exact status + `clierr` code, never a bare 500)

| # | Scenario | Expected |
|---|----------|----------|
| R1 | Wrong password; right password under the wrong name; garbage base64; empty credential | 401 (plain text on `rpcMiddleware` surfaces — the login page maps by status) |
| R2 | Valid account, org it doesn't belong to (root and `/o/` forms) — including a credential that verifies only against ANOTHER org's same-named account | **403, never 401** — "wrong password" and "wrong org" must stay distinguishable |
| R3 | Unknown org in the URL | 404 `unknown_org` |
| R4 | Archived org — for members and operators alike; unarchive restores routing without restart | 410 `org_archived`, then 200 |
| R5 | Signup gates, in gate order: disabled → `signup_disabled` 403; bad invite code → `bad_signup_code` 403; bad name → `invalid_name` 400; weak password → `weak_password` 400 | as listed |
| R6 | Signup name collisions: operator principal, bot lane, existing account, racing duplicate | 409 `name_taken` |
| R7 | Signup org half: missing org 400 `missing_org`; bad mode 400 `invalid_org_mode`; create disabled 403 `org_create_disabled`; invalid/reserved name 400 `invalid_org_name`; taken 409 `org_exists`; join of unknown 404 `unknown_org` |
| R8 | Agent principal on hub org APIs (list AND create); bot lane likewise | 403 `agent_denied` / `lane_denied` |
| R9 | Agent credential presented to a foreign org | 401 (agent rows are org-scoped) |
| R10 | Member management: non-admin 403 `not_org_admin`; account not signed up IN THAT ORG 404 `unknown_principal` (per-org identity - cross-org member-add does not exist); bad role 400 `invalid_role` |
| R11 | Stored org-admin on the deployment admin surface | 403 `operator_only` |
| R12 | Account names are case-sensitive end to end (sign-in with the wrong case is a 401, not a match) | 401 |
| R13 | Interrupted create-mode signup (org assembly fails after the account row) | honest 500 naming the half-done state; retrying the SAME name+password recovers (idempotent signup, finding #44) — wrong password keeps 409 `name_taken`; re-joins never demote an existing role |

Related, covered elsewhere: anonymous public-org discovery and the
no-silent-downgrade rule (`publicread_test.go`), signup over the plain
default server (`signup_test.go`), org lifecycle + isolation
(`orghub_test.go`), agent TTL/revocation (`agentprincipal_test.go`),
credentials over the git transport (`TestSignupCredentialWorksOverGit`,
compose E1).

## Onboarding journey (§6.10): `compose-onboarding.sh`

Born from the 2026-07-16 dogfood review: every step below is something
a real first-time operator did (or could not do). The journey uses the
CLI exactly as the docs teach it — stored logins, managed workspace
homes, no raw tokens after first contact — which is precisely the
surface the other suites never touch over the wire. Auth handler
behavior stays in the Go sign-in matrix above (selection rule); this
suite asserts *journeys* and the CLI's structured-refusal text.

Persona hygiene: each actor (val the founder, worker, the agent) gets
its own `XDG_CONFIG_HOME` and `RUNKO_WORKSPACE_HOME`, because stored
credentials and materializations are part of what is being tested.

### Journey rows (every step must succeed)

| # | Step | Proves |
|---|------|--------|
| O1 | `auth signup --name val --org acme --create` → `auth status`, `org list` | First contact is one command; signup IS login, credential stored against `/o/acme`, creator is admin |
| O2 | Clone the org git URL with the signup credential; assert genesis tree: `OWNERS` names val, `AGENTS.md`, `CONTRIBUTING.md`, `.claude/skills/runko/SKILL.md`, root `PROJECT.yaml` | Org born usable (§6.10 genesis) over real smart-HTTP |
| O3 | `workspace create --name onboard --project repo` (no `--by`, no URL flags) → `doctor --json` inside it | Managed-home materialization arrives fully wired: both client hooks, `CLI` build identity, workspace binding |
| O4 | `project create --lang ts --api rest --build-engine vite` (Change-Id born in, §6.10) → `change push` → `requirements` → `change land` | The day-one solo land: genesis OWNERS + uploader consent make the creator's own change mergeable with zero other principals |
| O5 | Edit → `change create` → `push` → `land` | The steady-state loop through the org mount + stored login |
| O6 | `workspace create --new-path services/checkout` (zero `--project`) → `project create --path services/checkout` → push → land | Greenfield bootstrap: affinity for a project before it exists at trunk |
| O7 | `agent create --task greet` → agent login → agent workspace (`--project hello --new-path services/agentproj`) → edit → push → describe (after the requirements blocker) → `automerge` → val approves → poll until landed | The full agent lifecycle: RequireDescription gate, human approval as the only remaining gate, automerge firing on green |
| O8 | Agent `workspace sync` after O7's land, then a new-manifest push naming val as owner → accepted → `change abandon` | Modify-vs-create classification's allow half + sync + abandon hygiene |

### Refusal rows (structured text, exact fragments)

| # | Scenario | Expected fragment |
|---|----------|-------------------|
| R-O1 | Agent edits an existing `PROJECT.yaml` | `does not allow modifying owners` |
| R-O2 | Agent's new manifest names the agent itself in `owners:` | `grants itself ownership` |
| R-O3 | Agent runs `project delete` / `org create` | `human product action` / agent refusal |
| R-O4 | val's login pointed at an org that has a *different* `val` or none (`/o/bare`) | `not a member` — 403 shape, never a wrong-password 401 |
| R-O5 | `workspace create --new-path ../escape` / `--new-path <existing-project-path>` | `not a clean repo-relative directory path` / `is already project` |
| R-O6 | Landing in a born-but-ownerless org (deploy-token-created, trunk force-landed) | unpoliced blocker that **names `runko org bootstrap`** |
| R-O7 | `org bootstrap` as the anonymous deploy token / as a non-admin member / re-run once governed | `has no name to record` / `org admins only` / `owners already resolve` |
| R-O8 | Duplicate `org create acme` | `already exists` |
| R-O9 | `workspace delete` while a change is open, then after abandon | refusal first, success after |

### The bare-org retrofit chain (R-O6/R-O7 in sequence)

The deploy token creates org `bare` (anonymous ⇒ no genesis), a
project with no owners is pushed and **force-landed** by the operator
(the only way a born-ownerless trunk can exist post-genesis — exactly
the shape every pre-genesis org is stuck in), and then:
ordinary land refused (blocker names the verb) → bootstrap as anonymous
refused → `worker` signs up `--join`, bootstrap refused (`not_org_admin`)
→ operator promotes worker to admin → bootstrap opens the OWNERS change
→ worker lands it alone (head-tree owners + uploader consent) → an
ordinary change lands after. One chain, the whole escape hatch.

### Deliberately not here

`self-update` (needs GitHub, not the stack), the auth matrix
(Go suite above), secret scan / direct push / revalidation (E2/E4/E7),
MCP (E11). The suite asserts no timings (rule 3).
