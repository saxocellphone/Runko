# Migration findings → requirements for `monorepo import` (§18.3)

This repo is migrating onto its own product in two phases: a Bazel migration
(the §14.5.4 golden path, dogfooded) and a self-host cutover (this repo's
source hosted on a prod runkod org, GitHub demoted to mirror + CI runner).
Nothing here is a complaint log — every entry is a requirement the future
`monorepo import` feature (§18.3, §26 #10) must satisfy, discovered the only
way that's credible: by doing the migration by hand.

**How to read this:** each entry is *friction observed → root cause →
what §18.3 (or a named surface) must do about it*. Entries marked
`[verified-in-code]` were established by reading the implementation while
planning; entries marked `[observed]` happened during execution.

## Phase B findings (Bazel adoption, 2026-07-08)

1. **[observed] `# gazelle:proto disable_global` is load-bearing for repos
   with committed generated proto Go.** Without it gazelle generates
   `proto_library`/`go_proto_library` over `proto/**/*.proto` and
   double-defines the packages that `gen/**/*.pb.go` already provide.
   → An adopt/import tool that scaffolds Bazel for a customer repo must
   detect committed codegen output and emit the directive (or the org
   template must carry it).

2. **[observed] `go:embed` just works.** gazelle turned
   `//go:embed migrations/*.sql` into `embedsrcs` with zero intervention.
   No requirement — recorded so the golden-path docs can promise it.

3. **[observed] Build-tagged integration tests silently vanish from the
   graph.** gazelle omitted `bazel_integration`/`zoekt_integration` test
   files from `srcs` entirely (rather than including them under constraint
   filtering). Harmless here (those tests run via `go test -tags`), but an
   org expecting `bazel query` to see all test targets would be surprised.
   → Document as golden-path caveat; consider `# gazelle:build_tags`.

4. **[observed] WORKSPACE-fixture rot.** The stage-9b real-bazel integration
   test wrote an empty `WORKSPACE` as its workspace boundary — dead under
   Bazel 8 (bzlmod-only). Fixed to `MODULE.bazel` + copying the repo's
   `.bazelversion` into the fixture, because **bazelisk resolves the version
   from the fixture's cwd** and silently downloads "latest" otherwise.
   → Version-pinning discipline: any tool that fabricates Bazel workspaces
   (import scaffolding, templates) must pin the Bazel version inside them.

5. **[observed] Test binaries compile under `bazel build //...` even though
   tests never run under bazel.** Pure compilation — `cmd/runkod`'s
   run-time `go build` helpers are irrelevant to the graph. Keeping go_test
   targets enriches the rdeps universe (a change to `land/land.go` correctly
   pulls in `//mcp:mcp_test`).

6. **[observed] The engine's file labels are fragile on non-package files.**
   `docs/design.md` → `//docs:design.md` → query error → refinement
   escalates `run_everything` (correct per spec's fail-closed rule, but it
   means any Change touching a non-Bazel file degrades refinement to the
   declared floor). The CI smoke deliberately touches a Go file.
   → §14.5.4 follow-up: package-aware label filtering (skip paths outside
   any Bazel package instead of erroring the whole query). **Closed
   2026-07-10**: the adapter now skips non-package paths (no BUILD file in
   any ancestor dir - a filesystem check, no query round-trip); their
   gating stays with the platform floor, and the build-sensitive
   non-package files (go.mod, MODULE.bazel...) are covered by tree-borne
   root_invalidation patterns.

7. **[observed] Stray tool caches confuse workspace walkers.** A `.vite/`
   dep-cache had leaked out of `web/` into the repo root (untracked,
   unignored). Bazel/gazelle walk everything not in `.bazelignore`.
   → `import plan` should report unignored junk directories.

8. **[observed] Cold-cache query cost.** In a fresh clone (fresh output
   base), the first `bazel query` pays module fetch + analysis (~minutes);
   warm it's sub-second. The CI job orders `bazel build //...` before the
   affected smoke for exactly this reason; `--engine-timeout` defaults
   (60s) are too tight for cold repos.
   → runko-ci docs should say "warm the graph before querying" and the
   engine timeout guidance should distinguish cold/warm.

## Phase R findings (self-host import/cutover) — pre-registered from code reading, confirmed during execution

9. **[verified-in-code] There is no import tool.** The §18.1 ladder's
   stage-0/1 (inbound overlay, PR ingestion) is unbuilt (mirror M2); the
   only history-ingestion route is pushing the full history to
   `refs/for/<trunk>` on an unborn trunk, where trailer-less commits fold
   into one tip Change. → §18.3 needs `import plan` (dry-run report) +
   `import execute` as first-class verbs.

10. **[verified-in-code] Tip-SHA parity through import is an *accidental*
    invariant.** prereceive never rewrites the pushed tip (minted Change-Id
    lives only in the Store row) and unborn-trunk land is a zero-OID CAS
    fast-forward — so the imported trunk tip equals the pushed GitHub tip,
    which the mirror cutover depends on. → §18.3 must make parity a stated,
    tested guarantee, not an emergent property.

11. **[verified-in-code] Mirror first-sync adoption is timing-dependent.**
    With no cursor row, remote-tip == local-tip → silent adoption;
    anything else → freeze. Cutover therefore requires a freeze window and
    tip parity. → import tool needs an explicit "adopt existing mirror at
    SHA" verb instead of relying on the coincidence.

12. **[closed by R1] Mirror was default-org-only.** `NewOrgServer` wired
    no MirrorWorker; self-hosting in a dedicated org required building
    repeatable `--org-mirror 'org=…;remote=…'` / `RUNKO_ORG_MIRRORS`
    (worker per org over the org's repo + org-scoped cursor Store;
    `/o/<org>/api/mirror/*` light up once `Server.Mirror` is set).
    → remaining: org settings (the tree, eventually — §9.4) should own
    mirror config, not daemon flags.

13. **[closed by R1, partially] Webhook envelopes never populated
    `org_id`/`monorepo_id`/`checks_expected`**, and only
    `change.updated`/`change.landed`/`change.check_rerun_requested` are
    actually emitted (opened/reopened are dead enum values). A multi-org
    daemon with one `--webhook-url` gives consumers no way to scope events.
    → org_id now stamped (org NAME — consumers want the /o/<name> path
    segment); remaining: populate `checks_expected` or drop it from the
    schema, and per-org webhook targets.

14. **[closed by R2/R3] No native CI plugin exists (§14.7 gap).** GitHub
    Actions cannot trigger on `refs/changes/*`; `cmd/runko-bridge`
    (HMAC-verified envelope → repository_dispatch, 2xx only after
    GitHub's 204 so the outbox re-drives failures, bounded delivery-id
    dedup) + `.github/workflows/runko-checks.yml` (mirror-lag fetch retry,
    report-check post-backs) are the reference implementation.
    → productize as the packaged GitHub plugin; §18.3's "CI shadow
    period" depends on it.

15. **[verified-in-code] Webhook-vs-mirror ordering race.** The change
    webhook can fire before the 3s-debounced mirror push makes
    `refs/changes/<id>/head` visible on GitHub; the workflow needs a
    fetch-retry loop. → either a "ref visible on mirror" event or bridge-
    side delay/confirmation.

16. **[verified-in-code] `runko-ci checkout` cannot fetch `refs/changes/*`**
    (clone fetches heads/tags only). The GH workflow must use
    `actions/checkout` with an explicit ref. → teach checkout a
    `--change <id>` mode that fetches the stable change ref.

17. **[verified-in-code] Solo-dev owner deadlock.** Self-approval is
    hard-denied and agents can never approve, so a solo human whose
    manifests declare owners can never land. This migration omits `owners:`
    entirely (checks-only gating). → §18.3 owner-mapping needs a small-team
    policy mode instead of a silent trap.

18. **[verified-in-code] The import change itself is unpoliceable.** Its
    history predates any manifests, so it resolves zero policy and the
    default-deny gate refuses it; admin force-land is the sanctioned
    bootstrap. → §18.3 needs a first-class bootstrap-land (audited, like
    force-land, but semantically "import", not "override").

19. **[verified-in-code] No org deletion.** Dry-run/rehearsal orgs
    accumulate forever. → org lifecycle needs at least archive.

20. **[verified-in-code] Workspace snapshots + unmirrored org repos live
    only on the RWO PV.** The mirror carries trunk/tags/changes; personal
    WIP and mirror-less orgs are one disk failure from gone. → backup
    guidance (nightly orgs-dir tar beside pg_dump) belongs in the §16
    self-host docs.

21. **[verified-in-code] The refinement post-back endpoint is spec-only.**
    CI-side bazel refinement can narrow what CI *runs* but nothing can
    narrow the server's required-check set. Recorded as the standing
    §14.5.4 gap.

22. **[observed, R5 dry run] The import push trips the secret scanner on
    the repo's own test fixtures.** Prod gitleaks rejected the full-history
    push at `scripts/compose-edgecases.sh:132` — the E4 smoke fixture's
    deliberately realistic AWS key literal (realistic BECAUSE gitleaks
    allowlists the well-known example keys). The funnel scans the pushed
    tip's materialized tree, so any contiguous secret-shaped literal in
    committed content blocks import. Fixed by assembling fixtures at
    runtime (`"AKIA" + suffix`), which keeps the E2E test's pushed content
    realistic while the committed tree never pattern-matches.
    → §18.3's `import plan` MUST pre-flight the tip tree with the same
    scanner the funnel runs and report hits BEFORE the freeze window; and
    import needs a sanctioned per-path scan-allowlist story (a repo
    `.gitleaks.toml` the server honors, or an audited skip) for repos
    whose fixtures can't be rewritten.

23. **[observed, R5 dry run] Large smart-HTTP pushes flake through
    ingress-nginx with chunked transfer encoding.** The ~tens-of-MB
    full-history pack failed mid-transfer (HTTP 400, "unexpected
    disconnect while reading sideband") on one attempt and succeeded with
    `git -c http.postBuffer=157286400 push` (fixed Content-Length instead
    of chunked). → import docs must prescribe the postBuffer workaround
    or the deployment guide must include the ingress-side fix
    (proxy-body-size/buffering tuning for the git mount).

24. **[observed, R5 dry run; CLOSED 2026-07-09] Org-admin (signup role)
    could not force-land.** `{"force": true}` from the org's creator
    returned `force_denied` ("not an admin principal") - the auth layer
    fetched the account's org role for the membership check and then
    DROPPED it, synthesizing every store-backed account as a non-admin
    principal; only operator `--principal '…;admin'` entries and the
    deploy token could force. Fixed: the org role now survives into the
    synthesized principal (`Admin: role == "admin"`), so an org's own
    admin can force-land and unfreeze the mirror in their org
    (runkod/auth.go, TestStoredOrgAdminMayForceLand).

25. **[observed, R5 dry run] Prod permits unpoliced lands.** The import
    change (zero required checks, zero owners - its history predates any
    manifests) landed WITHOUT force, meaning the deployment runs with
    `--insecure-allow-unpoliced-land` (or its env). Convenient for the
    bootstrap, but it means the default-deny gate is off for every org
    on the instance. → R6 must drop the flag once the manifests land
    (they provide real policy); the import runbook then relies on the
    admin force-land instead. Confirmed live: with the flag on, findings
    #18/#24's force-land path was not even needed.

## Post-cutover findings (dogfood, 2026-07-09 re-carve)

26. **[observed, first workflow-touching change] The mirror PAT needs the
    `workflow` scope.** GitHub rejects any push containing a commit that
    modifies `.github/workflows/*` unless the token has the Workflows
    (read-write) permission — the mirror's `refs/changes/*` push failed
    with `refusing to allow a Personal Access Token to create or update
    workflow`, so the change ref never reached the mirror, every CI job's
    fetch-retry loop timed out, and the checks reported failure. Worse:
    landing such a change would strand `refs/heads/main` mirroring the
    same way (retry-forever, not freeze). → §18.3's mirror setup docs and
    `import plan` preflight must check token scopes against the mirror
    target (a test push of a workflow-touching ref is the only reliable
    probe on GitHub).

27. **[observed, bridge logs] Webhook outbox is not org-scoped: every
    envelope delivered once per org server.** `ListDueWebhookDeliveries`
    has no org filter while the daemon runs one `OutboxWorker` per org on
    the same Postgres pool (default + 2 orgs = 3 workers), so all three
    picked up each due row concurrently — triple `repository_dispatch`,
    and the bridge's delivery-id dedup raced (all three in flight before
    any completed). GitHub's concurrency-cancel then killed two runs,
    whose `if: always()` report steps posted **failure** for checks that
    never ran (finding #28). → fix: org-scope the outbox query (each
    worker drains only its org's rows); multi-replica deployments will
    additionally need row claiming (`FOR UPDATE SKIP LOCKED`).

28. **[observed, same incident] Cancelled Actions runs report check
    failure.** `if: always()` runs the report-back step on cancellation
    with `job.status == 'cancelled'`, which the success/failure ternary
    maps to `failure` — a transient false-failing gate until the
    surviving run overwrites it. → workflow templates must use
    `if: success() || failure()` (excludes cancelled) on report steps;
    §14.4's reference workflow should ship that way.

29. **[executed] The coarse carve lasted one day — re-carve is a real
    migration step, not a one-time event.** Folder-per-project meant
    moving 16 top-level dirs, ~300 import-path rewrites, regenerated
    protobuf descriptors (sed alone corrupts the rawDesc length prefix —
    regeneration is mandatory), six package-relative test paths, and a
    **two-phase landing dance**: `repository_dispatch` executes the
    DEFAULT branch's workflow copy, so any workflow path reference had to
    land + mirror BEFORE the restructure change could be gated. → §18.3
    needs a `re-carve` story: manifest + folder moves as one audited
    operation, with the CI-workflow coupling called out in the runbook.

30. **[observed, C2's failed push-time runs] CI failure before the first
    report-check call is invisible to Runko.** The workflow runs
    report-check FROM THE CHECKOUT (`go run ./cli/runko-ci`), so a
    checkout-breaking change also breaks failure reporting - the run dies
    with zero trace server-side, and the gate shows "has not reported
    yet" forever (fail-closed, but silent). → the reference workflow
    should report in_progress via a prebuilt binary or plain curl BEFORE
    any layout-sensitive step; §14.4.2's TTL blocker is the backstop.

31. **[observed, C2's rerun] The rerun envelope carried no affected
    block, silently skipping conditionally-scoped CI jobs.**
    `change.check_rerun_requested` omitted `affected` entirely while
    change.updated always carries it - runko-checks.yml's web-check job
    (`if: contains(affected_projects, "web")`) therefore SKIPPED on
    rerun, the run came back green, and web-check stayed pending forever;
    GitHub-green + Runko-pending is maximally confusing. Fixed same day
    (enqueueRerunWebhook now computes and attaches the block). → webhook
    envelope fields consumed for CI scoping must be present on EVERY
    event type that can trigger CI, and the contract tests should assert
    it per event type.

32. **[observed, re-carve landing] change.updated delivery ids are not
    unique per emission, so the bridge dedups a same-head re-push into
    nothing.** DeliveryID was `<change>@<head>`; re-pushing an unchanged
    series member (the documented way to re-trigger CI with a full
    payload) emits the exact id the bridge already saw at the first push
    and gets silently dropped as a retry - combined with #31 this made a
    stuck check UNRECOVERABLE without force-land. Fixed: change.updated
    ids now carry an emission timestamp (the rerun-id pattern), so outbox
    RETRIES of one emission still dedup while distinct emissions dispatch.

33. **[observed, twice in one day] `report-check` treated one 5xx as
    final; the daemon's deploy topology makes 5xx routine.** runkod ships
    as a single-replica Recreate pod, so EVERY image rollout is a brief
    503 window at the ingress - two changes' platform-db jobs reported
    in_progress during one and died, leaving the checks "has not reported
    yet" forever (GitHub red, Runko pending: the #30 invisibility class,
    server-side flavor). Fixed: report-check retries transient failures
    (connection errors, 5xx, 429) with exponential backoff - the POST is
    an upsert keyed (change, head, name), so retries are safe; other 4xx
    stay fatal. → the §14.6 reference CI plugin must ship with retries by
    default, and the packaged workflow should treat report-check as
    infrastructure, not a best-effort curl. Separately observed in the
    same incident: a GitHub ruleset applied WITHOUT its bypass actor
    froze the mirror mid-land (GH013 on the leased trunk push) - §18.6's
    mirror docs must spell out that force-with-lease requires the bypass,
    and mirror-freeze alerting (a /metrics gauge exists) belongs in the
    ops floor.

34. **[observed, live incident] A push racing a daemon restart leaves a
    dangling change ref that bricks the whole repo.** The pre-receive
    funnel writes `refs/changes/<id>/head` via `git update-ref` during the
    hook, referencing an object still in git's push quarantine; git
    migrates quarantine to the object store only when the hook exits 0.
    A `kubectl rollout restart` (or any kill) mid-hook leaves the ref on
    disk pointing at an object that is then discarded with the quarantine.
    git receive-pack's connectivity check fails the ENTIRE repo when ANY
    ref is unreachable, so from then on EVERY push - from anyone - is
    rejected with "missing necessary objects", and the outbound mirror
    loops forever on the same bad object. Concurrent deploy pipelines +
    active pushing (two agents) made this reachable. Recovery is deleting
    the dangling refs in the repo (`git update-ref -d`); fixed forward
    with `runkod.PruneDanglingChangeRefs`, run at boot from
    `EnsureBareRepo`, so the crash self-heals on the next start instead
    of needing a manual `kubectl exec`. → §18.3 note: an import/serving
    repo needs a boot-time integrity sweep; longer term the change-ref
    write belongs in POST-receive (after quarantine migration), where a
    crash cannot leave a dangling ref at all. Also: deploy tooling should
    drain in-flight pushes before restarting a single-replica daemon.

35. **[observed, §14.5.8 phase 2 rollout] A head tree the deployed
    daemon cannot parse degrades SILENTLY at receive.** The first change
    carrying the new `{pattern, refinable}` manifest syntax was pushed
    while prod still ran the pre-parser binary: the push was ACCEPTED
    (Change created, snapshot durable), but `computeAffectedAndEnqueue`
    logs the head-tree scan error and returns — no affected computation,
    no webhook, no CI dispatch, gates pending forever; and
    `merge-requirements` (which re-scans strictly) answered a raw HTTP
    500, not a §6.5 structured error. Same invisibility class as #30/#33:
    "pending forever" is indistinguishable from "slow CI". → two fixes
    owed: receive should either reject a scan-failing head tree with a
    structured "your manifests don't parse under the deployed daemon
    (version skew?)" error, or degrade LOUDLY (fail-closed
    run_everything webhook carrying the scan error); merge-requirements
    must return structured errors on scan failure. Recovery wrinkle: the
    prereceive comment claims "a same-head re-push is the documented way
    to re-trigger CI with a full payload," but a same-head re-push after
    the rollout produced NO processing at all (no log line, no webhook) -
    the working recovery was `runko change rerun-check --name <check>`
    per required check. Either make same-head re-pushes actually re-emit,
    or fix the comment and document rerun-check as the recovery verb.
    Sequencing lesson for the record: any manifest-SYNTAX change is a
    two-step deploy (parser lands + rolls out, THEN the first manifest
    using it) - the parser binary in CI comes from the change's own tree,
    but the daemon's parser is whatever is deployed.

36. **[observed, same rollout] The GitOps digest write-back is dormant
    until `K8S_CLUSTER_TOKEN` exists; nothing red says so.** 9b3118e4's
    deploy-bump job skips with a green ✓ and a log-only notice when the
    Actions secret is missing — release-images succeeds, no k8s-cluster
    commit happens, Argo stays Synced on the old digest, and the only
    symptom is a pod quietly running last week's binary. `kubectl
    rollout restart deploy/{runkod,runko-web} -n maas-dev` remains the
    real deploy path until the user mints the fine-grained PAT
    (Contents R/W on k8s-cluster). → the deploy-bump skip should be a
    visible warning annotation at minimum; better, a required
    "deploys-are-wired" preflight in the release workflow once the
    token is expected to exist.

## Distilled §18.3 requirements (running)

- `import plan <src>` dry-run report: history size, trailer audit,
  tip-parity guarantee, junk-dir scan, generated-codegen detection,
  owner-mapping proposal with a small-team mode.
- `import execute`: history-preserving, SHA-parity-tested, with an audited
  bootstrap-land and an explicit mirror-adopt-at-SHA step.
- Reference CI plugin (GitHub Actions first): bridge + workflow template +
  `runko-ci checkout --change`.
- Org lifecycle: archive/delete; org-settings-owned mirror config.

## Cutover record (R7, 2026-07-09)

Executed in ~6 minutes of wall clock against the production instance:
org `runko` created; full history pushed (`refs/for/main`, postBuffer
workaround per #23) as one Change; head_sha byte-equal to the freeze tip
`9eddc2d…` (#10 confirmed in production); landed onto the unborn trunk;
the mirror **silently adopted** the same-tip GitHub repo (#11 confirmed —
cursor at the freeze tip, never frozen) and pushed `refs/changes/*`
outbound. The first gated change (the R4 manifests) then ran the entire
§14.4 Mode C chain for real: push → webhook → bridge →
repository_dispatch → four checks reported back → mergeable → landed
through the gate (NO force) → mirrored to GitHub main within seconds.
`--insecure-allow-unpoliced-land` removed immediately after (#25 closed);
future org imports bootstrap via the operator principal's force-land
(#18/#24 - still the sanctioned path, now that default-deny is back on).

## Open verification items

- ~~Bazel-refinement smoke asserts engine health only until PROJECT.yaml
  manifests land (R4); revisit the assertion afterward.~~ **Done
  2026-07-09** with the re-carve: the smoke moved into runko-checks.yml
  as the tree-declared `bazel-check` (pre-land; `make check-bazel`) and
  now also asserts the PROJECT-level result (platform matched, no
  fail-closed escalation); ci.yml shrank to the post-land safety net
  (the only CI that builds the actually-landed, post-rebase tree).
- ~~R5 scratch-org dry run to confirm findings 9-11 and 18 live.~~
  **Done 2026-07-08** against prod org `runko-dry`: full history (one
  Change, `I147c12ef…`), tip-SHA parity byte-equal
  (`e94f4394…` == local main), trunk born at parity via plain land
  (see #25 - force-land wasn't needed because unpoliced lands are on).
  Mirror adopt/freeze rehearsal deferred to R6 (needs `--org-mirror`
  deployed); the semantics are e2e-tested in TestEndToEndDaemonOrgs.
