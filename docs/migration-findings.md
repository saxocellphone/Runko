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
   any Bazel package instead of erroring the whole query).

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

12. **[verified-in-code] Mirror was default-org-only.** `NewOrgServer`
    wires no MirrorWorker; self-hosting in a dedicated org required the
    `--org-mirror` slice. → org settings (the tree, eventually — §9.4)
    should own mirror config, not daemon flags.

13. **[verified-in-code] Webhook envelopes never populated
    `org_id`/`monorepo_id`/`checks_expected`**, and only
    `change.updated`/`change.landed`/`change.check_rerun_requested` are
    actually emitted (opened/reopened are dead enum values). A multi-org
    daemon with one `--webhook-url` gives consumers no way to scope events.
    → stamp org identity (done in slice R1) and either populate
    `checks_expected` or drop it from the schema.

14. **[verified-in-code] No native CI plugin exists (§14.7 gap).** GitHub
    Actions cannot trigger on `refs/changes/*`; a hand-built
    webhook→repository_dispatch bridge with idempotency and mirror-lag
    retry is required. → productize as the reference GitHub plugin;
    §18.3's "CI shadow period" depends on it.

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

## Distilled §18.3 requirements (running)

- `import plan <src>` dry-run report: history size, trailer audit,
  tip-parity guarantee, junk-dir scan, generated-codegen detection,
  owner-mapping proposal with a small-team mode.
- `import execute`: history-preserving, SHA-parity-tested, with an audited
  bootstrap-land and an explicit mirror-adopt-at-SHA step.
- Reference CI plugin (GitHub Actions first): bridge + workflow template +
  `runko-ci checkout --change`.
- Org lifecycle: archive/delete; org-settings-owned mirror config.

## Open verification items

- Bazel-refinement smoke asserts engine health only until PROJECT.yaml
  manifests land (R4); revisit the assertion afterward.
- R5 scratch-org dry run to confirm findings 9-11 and 18 live.
