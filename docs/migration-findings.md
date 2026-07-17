# Migration findings → requirements for `monorepo import` (§18.3)

> **RETIRED 2026-07-16 — ledger closed at finding #50.** The migration
> this file tracked is done: the repo self-hosts, and dogfooding is the
> ordinary state, not an event worth a separate ledger. New findings
> live where the fix lives — the change description of the change that
> addresses them — and a finding that changes a decided constraint gets
> a dated Decisions entry in the owning project's `README.md` (see
> [`docs/README.md`](README.md)). This file stays frozen so `#N`
> citations in commits, code comments, and docs keep resolving.

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

37. **[observed, first concurrent-agent week] An agent's push is judged
    by the rotating magic ref's OLD VALUE, so another workspace's work
    gets charged to its affinity.** `refs/for/<trunk>` is a literal,
    repeatedly-overwritten ref (the no-Gerrit-rewriting design,
    cli/runko/change.go); the funnel's whole-push `changedPaths` diffed
    `u.OldSHA..u.NewSHA` - i.e. against whatever unrelated push rotated
    the ref last. Under two concurrent agents this refused a
    workspace-clean push with `path_outside_affinity` naming files ONLY
    the other agent ever touched (paths present in the stale old tip but
    not in the pusher's line read as "changed"). Fixed: a magic-ref
    push's delta is now always `merge-base(tip, trunk)..tip` (the
    zero-old first-push special case generalized); pinned by
    TestMagicRefPushDiffsAgainstTrunkNotRotatingRefValue. Single-agent
    dogfooding could never see this - the rotating ref always held your
    own previous tip; the moment a second workspace pushes, every
    rebase-then-push window is exposed. Secret-scan/size inputs ride the
    same diff, so those now also measure exactly the pushed line.

38. **[observed, twice while building §12.6] The snapshot cap judges a
    workspace's WHOLE delta against a per-Change-sized limit.** The
    §12.6 auto-snapshot on `change push` warned (and `workspace watch`
    would warn-loop) the moment this stack's workspace crossed the agent
    policy's 512 KB `max_diff_bytes`: a snapshot's policy input is the
    full workspace-vs-base delta, so a healthy 5-change stack whose
    CHANGES each pass their per-change caps still can't snapshot - the
    denominators disagree. §8.4's caps are "per Change"; a snapshot is
    not a Change, and the funnel already relaxes the per-change caps for
    stack pushes (whole-push affinity, per-member caps). The snapshot
    lane needs its own calibration: either its own (larger) cap knob or
    per-change-equivalent accounting. Warn-only today: the §12.6
    best-effort contract held - pushes continued, only durability
    cadence suffered. The separate 32 MiB artifact backstop
    (DefaultMaxSnapshotBytes) is fine as-is.

39. **[deploy note, §12.6 rollout] WatchWorkspace's ingress preconditions
    - verified already met, now load-bearing.** A server-streaming RPC
    through nginx-ingress needs response buffering OFF (frames arrive in
    bursts or never otherwise) and a `proxy-read-timeout` above the
    keepalive cadence. Checked live: the runkod ingress already carries
    `proxy-buffering: "off"` (git smart-HTTP wanted it first) and routes
    `/runko.v1.WorkspaceService` explicitly (the per-service path list -
    dot-prefixed paths don't route as one prefix), and the 25s keepalives
    satisfy the default 60s read timeout. What changes: these two
    annotations were conveniences before, they are CORRECTNESS for the
    live workspace view now - k8s-cluster must not lose them. Same
    standing rule server-side: never add a global `WriteTimeout` to the
    daemon's http.Server (the comment in cmd/runkod/main.go is the
    guard). The web app degrades to reconnect-and-refetch if any of this
    regresses - the page stays correct, just not live.

40. **[observed, first root-affinity agent workspace] Root-project
    affinity write-blocks agents entirely.** The root project's path is
    `""` (it owns what no deeper manifest claims - AGENTS.md, README,
    go.mod), so `workspace create --project repo` computes `[""]` as the
    write allowlist - and `withinAffinity`'s prefix check can never
    match an empty root (no path starts with `/`), so EVERY file the
    agent touches is `path_outside_affinity`. Found regenerating
    AGENTS.md from a root-affinity workspace (the §12.6 stack's
    follow-up). Fixed: `""` in an allowlist now grants the whole tree -
    which is exactly what root affinity means; pinned by
    TestEvaluatePolicyRootAffinityGrantsWholeTree. Note the enforcement
    lives in the DEPLOYED daemon: the fix gates nothing until runkod
    redeploys (same rollout class as #35's parser skew).

41. **[observed, web UI] The project page took ~3s: an N+1 RPC fan-out
    times an uncached full-tree scan per RPC.** Two multiplying causes.
    Client: `useGraphProjects` (the dep-graph hook the project page AND
    the projects list share) calls `listProjects` then one `getProject`
    per project, because `ProjectSummary` carries no dependency edges -
    12 concurrent RPCs on the dogfood repo. Server: every ProjectService
    read re-ran `index.Scan` at trunk tip - one `git ls-tree` subprocess
    per directory plus a `cat-file` per manifest/OWNERS, ~80 spawns per
    RPC, ~960 per page view; under the pod's CPU limit the burst
    serialized (a single call: ~250ms; inside the burst: up to 2.2s,
    2.8s wall measured with curl). Fixed server-side: `indexedProjectsAt`
    memoizes `index.Scan` by resolved commit SHA (affectedCache's
    sibling, one layer down - single-flight, since the cold case IS the
    burst), and every Server read path routes through it; pinned by
    TestScanCacheFollowsTrunk. Still open: the client N+1 itself
    (dependency edges on `ProjectSummary` would make the graph 1 RPC)
    and the same quadratic shape anywhere else a per-item RPC hides a
    per-request scan. The affectedCache comment already recorded this
    disease for merge-requirements reads ("landed/open tabs load
    slowly") - this is the scan-layer generalization it asked for.

42. **[observed twice, stage-19 workstream] Single-use agent workspaces
    plus eager landing kill a multi-change task's workspace mid-task.**
    `--single-use-agent-workspaces` (default ON) closes an agent
    workspace the moment its last open change concludes - correct for
    one-change tasks, but a stack built and pushed INCREMENTALLY has a
    kill-window between "change N landed" and "change N+1 pushed". Hit
    both ways in one session: (a) arming automerge on change 1 of 5
    before pushing change 2 - the land closed the workspace and the
    watch loop's next snapshot was refused; (b) with no automerge armed
    at all, an operator hand-landing the only open change from the web
    UI did the same. The workflow consequence (now in the workspace
    skill's spirit, worth teaching in AGENTS.md too): push the WHOLE
    stack before arming or inviting any land - series receive exists
    precisely so one push holds the full task open. Possible platform
    fix if this keeps biting: a grace window, or close-on-idle instead
    of close-on-conclude (the workspace knows its watch loop is still
    snapshotting - "concluded changes + fresh snapshots" is a signal the
    task is NOT done, and stage 19's activity feed now makes the same
    liveness signal explicit).

43. **[observed, web UI] History displayed author time, which runs
    BACKWARDS along a rebase-landed trunk.** Ica9f0e4b (the finding #41
    change) was authored 21:40, synced onto I9ae945aa's 21:48 landing,
    and landed 22:01 - graph order and landing order agree (trunk only
    fast-forwards), but the Browse history byline rendered `%at`, so the
    tip-side commit displayed a time EIGHT MINUTES OLDER than the commit
    beneath it ("landed after, appears before"). Author dates survive
    amends and rebases by git design; under rebase-land they measure
    when work STARTED, never when it reached trunk. Second-order trap:
    committer dates don't fix it either - a fast-forward land ships the
    client-created commit verbatim, committer timestamp stamped by the
    CLIENT's clock (observed: c31fd65 cd 21:58 vs landed_at 22:01).
    The only clock that provably matches landing order is the control
    plane's own changes.landed_at. Fixed: CommitInfo carries
    committed_at + landed_at (the history join already resolved every
    row's Change - it now keeps the landing time instead of discarding
    it), and the history byline shows "landed <t>" falling back to
    committer time for pre-Runko rows; author time moved to the tooltip.
    Pinned by TestListCommitsLandedTime.

44. **[found by the sign-in smoke matrix; CLOSED same session] An
    interrupted create-mode signup stranded its account.** POST
    /api/signup validates the org half before creating the account
    precisely so a REJECTED org never half-registers anyone - but an
    org half that fails AFTER validation (store/infra error, lost
    create race) left a real account that was a member of nothing:
    every retry 409ed `name_taken`, every org surface answered 403,
    and the selector was empty. Only an admin adding a membership by
    hand could recover it. Fixed with idempotent recovery: re-presenting
    the SAME name+password to /api/signup treats the account half as a
    no-op (front gates - signup enabled, invite code - unchanged; a
    wrong password keeps `name_taken`), re-joins never demote an
    existing role, and the create-failure message flips to "already
    exists" so the user learns the credential is real. The same matrix
    run also caught handleAddOrgMember 500ing on the default org in mem
    mode (no directory row until first join) - EnsureOrg-first, like
    the signup join path. Pinned by TestSignupInterruptedOrgCreate,
    TestSignupRecoveryAfterInterruptedCreate, TestSignupRejoinPreservesRole.

45. **[observed, web UI] The landed tab sorted by change number, so a
    later-created change that landed FIRST sat on top of the change
    that landed after it - with the older "landed" byline right there
    on the row.** Finding #43's sibling, one layer up: #43 fixed WHICH
    TIME history displays; this one is which ORDER the changes list
    reads in. Observed with I395611c1 (created later in a second
    workspace, fast-forward-landed 20:19) above Ib9c54264 (created
    earlier, rebase-landed on top of it 20:20) - the control-plane data
    was fully consistent, graph order and landed_at agreed; only the
    list's sort key (number DESC = creation order, fine for the open
    inbox) misread a landed history. Fixed store-side so web, CLI, and
    REST all inherit it: the landed listing orders by landed_at DESC
    (number DESC tiebreak) in both stores - ListLandedChanges[Page]
    riding a partial index (migration 0018), MemStore sorting landed by
    LandedAt - while open/abandoned/all stay on creation order. Pinned
    by TestPostgresStoreLandedListingOrder and
    TestMemStoreLandedListingOrder.

46. **[observed, live workspace pages] Agents don't stream.** Stage 18's
    `workspace watch` and stage 19's activity hooks both work - and both
    are opt-in, so nothing in the golden path (create → work → push)
    ever turns them on. Workspace pages sat empty while agents worked;
    the "live view" existed only for whoever had read the spec, and the
    first visible sign of a task was the change push at its END. Fixed
    per the 2026-07-14 "streaming becomes the golden path" row:
    create/attach/agent-create print the two commands, `agent hooks
    --install` makes the activity wiring one verb, and the funnel
    answers the first never-streamed change push with a one-time
    advisory `remote:` block naming both.
47. **[observed, dogfood review] The mailer connects to nothing in the
    dependency graph, yet something clearly calls it - a real runtime
    edge (mailer drains runkod's invite feed) that affected computation
    could not see.** `mailer/PROJECT.yaml` declared no dependencies; the
    feed contract existed only as a copy-pasted struct pinned by an
    httptest stub (the watchdog convention, which is fine for a stub's
    OWN tests and blind for the graph). A runkod change reshaping the
    feed would land without ever running mailer-test. Root cause is
    structural: `proto/` being a standalone project makes a contract a
    PLACE, not a property of the serving project, and REST surfaces
    carry no tree-resident contract artifact at all. Decided (§13.3.1):
    contracts live in the owning project's boundary (`rpc` capability:
    proto + committed gen in-tree; `http` capability: mandatory OpenAPI
    document), consumption is a server/client `consumes:` edge with a
    CONTRACT-SCOPED closure (clients join affected only when the
    provider's contract surface changes; a gen-dir import unsanctioned
    by consumes/dependencies is refused at receive as
    `undeclared_contract_dependency`, platform/contract), and the API
    surface is decided at `project create` (`--api grpc|rest|none`,
    required for services). First application: the invite feed moved to
    gRPC/Connect (`runkod/proto/mailer/v1` InviteFeedService) and
    mailer declared `consumes: [runkod]`.

    ADDENDUM (2026-07-14, whole-repo re-evaluation under the new model):
    every project audited for actual-vs-declared communication. Correct
    as declared: platform→internal, internal→db, runkod→{platform,
    internal, db, proto} (all genuine build edges), db/docs/proto/root
    leaf or glue. Mis-graded or missing, fixed in the ops manifests:
    `web` — dependencies:[proto] is really a CLIENT edge (TS generated
    from proto sources), flipped to consumes:[proto]; closure identical
    today (a pure-contract project's whole tree is its surface) but the
    flip survives the proto→runkod migration correctly. `watchdog` —
    the ORIGINAL stub-pinned REST client (mailer copied its shape):
    polls /api/changes + merge-requirements with zero declared relation
    to runkod; +consumes:[runkod]. `cli` — runko/runko-ci are REST
    clients of the daemon (docs/cli-contract.md is the pinned surface);
    +consumes:[runkod]. KNOWN LIMIT, recorded: runkod's REST surface
    carries no OpenAPI artifact, so a consumes edge on a REST-only
    client currently triggers just on runkod/proto/** and the runkod
    manifest - honest topology now, real closure value when runkod
    gains its http capability + openapi.yaml (the follow-up that
    mandate exists for).

48. **[observed, dogfood landing sessions, 2026-07-14] Five parallel
    green changes cascade: every land forced a re-sync and a full
    check matrix on every remaining change.** Five unrelated changes,
    each green at its head. Landing the first moved trunk; each
    remaining land then hit `requires_revalidation`, because
    `NeedsRevalidation` (platform/land/revalidate.go) intersects the
    full dependents CLOSURE of both sides - and on this repo nearly
    everything shares closure members (platform feeds runkod/cli; root
    glue invalidates wholesale), so "unrelated" changes intersect
    anyway (phantom closure intersection). The prescribed recovery
    (sync/rebase + re-push) mints a NEW head, and check runs and
    approvals are keyed by head_sha, so every recovery reset every
    green result and re-ran the full matrix - N green changes cost
    O(N²) full matrices to land, every re-run re-testing a diff it had
    already passed. Root cause is the PAIRING: an over-broad
    intersection default, and head-keyed results no rebase can
    survive. Fixed per the 2026-07-15 changelog row (Gerrit's model,
    §13.5 rewritten, both halves): conflict-only landing is the
    default - a clean rebase lands green work with zero re-runs,
    post-land ci.yml is the net - and a TRIVIAL_REBASE head carries
    passing checks and approvals forward instead of resetting, so even
    orgs on the stricter tiers stop paying the approval-reset half.
    Pinned by the conflict-only land tests, the carry-forward
    sync/push tests, and compose edge case E7 (kept under an
    explicitly-configured affected-intersection daemon, plus the new
    default-policy sibling E7b).

49. **[observed, dogfood, 2026-07-15] Materialization sprawl: the
    platform with the "better worktree" story had the worst worktrees
    on the machine.** Eighteen task materializations sat inside the
    developing checkout's own root (`agent-*`, `admin-*`), ~2.4 GB of
    working trees whose blobless object stores total ~33 MB, in three
    coexisting layouts: worktrees of a shared in-tree clone
    (`Runko/mono` — the CLI's `--clone-dir` default is the
    cwd-relative literal `mono`), worktrees of per-task clones
    (`runko-clones/<task>` — fresh per task NOT by accident:
    `composeRemoteURL` bakes the creating principal's token into the
    store's remote URL, so a shared store misattributes every other
    principal's push, and clone-per-task was the only correct agent
    flow), and full-cone checkouts each carrying ~296 MB of
    `web/node_modules`. 26 of 31 server-side workspaces were closed —
    auto-closed correctly by the #42 lifecycle — while every local
    materialization survived, because nothing ties local directories
    to the server lifecycle: no placement default, no local registry,
    no reclaim verb, no reuse. Collateral: `git status` noise in the
    host checkout, and gazelle destructively "fixing" BUILD files it
    reached through the nested trees. → decided as §12.7: managed
    home + credential-neutral stores + rebuildable local registry +
    `workspace gc` + worktree recycling.

50. **[observed, implementing #49's fix, 2026-07-15] runkod's e2e
    consumes the compiled cli with no declared edge - a cli change
    that breaks the daemon e2e sails through its own gates.**
    `runkod/cmd/runkod/main_test.go` builds the `runko` binary from
    source (`buildRunko`) and drives it end-to-end, but
    runkod/PROJECT.yaml declares `dependencies: [platform, internal,
    db]` - no edge to cli - so `runkod-test` is NOT in a cli-only
    change's affected closure. The §12.7 credential-neutral change
    broke `TestEndToEndDaemonWorkspaces` (blobless checkout 401ed);
    every scoped cli gate stayed green, and only a full-tree run
    (`make check` locally, or post-land ci.yml - AFTER trunk is red)
    surfaces it. Same shape as the docs/platform contracts-test
    finding (2026-07-14): a test that consumes another project's
    files needs the edge declared so the closure runs it. → fix:
    runkod declares a `consumes: [cli]` (or dependencies) edge -
    manifest change, admin lane (agents cannot touch PROJECT.yaml).
    Until then: any change to cli's push/auth/workspace surfaces must
    run `go test ./runkod/cmd/runkod/` by hand before pushing.

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
