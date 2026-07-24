# runkod

The daemon — Runko's single write path and serving surface. Everything
that must be enforced (not merely suggested) is enforced here, server-
side. This README is the project's spec surface; rationale decided
before 2026-07-16 lives in the frozen [`docs/design.md`](../docs/design.md)
(`§` citations resolve there).

## What it serves

- **Smart-HTTP git hosting** (`smarthttp.go`) with a pre-receive
  funnel (`prereceive.go`, the hidden `runkod hook` subcommand): trunk
  is closed to direct push; `refs/for/<trunk>` and workspace snapshot
  refs are the only writes, both through `platform/receive`'s
  policy → secret scan → contract check → Change pipeline.
- **REST API** (`api.go`, `actions.go`) — changes, requirements,
  approvals, land/land-stack/automerge, workspaces, agents, releases,
  orgs, signup/invites. One structured `clierr` shape per refusal
  (§6.5); the CLI prints it verbatim.
- **Connect RPC** (`rpc.go`) for the web UI, schema in
  [`proto/`](proto/README.md) (`runko/v1`; `mailer/v1` is the
  operator invite feed) — this project's declared `rpc` contract
  surface, committed codegen under `proto/gen/` (never hand-edit,
  regenerate with buf).
- **Merge gates and landing** (`land.go`, `landstack.go`,
  `automerge.go`, `trivialrebase.go`): default-deny — every path
  resolves a policy or the land is refused; revalidation tiers per
  §13.5; uploader consent satisfies a human author's own owner
  requirement, agents never self-satisfy.
- **Webhook outbox** (`outbox.go`) — at-least-once delivery with
  retry/backoff/dead-letter on the row; the mailer reuses this model.
- **Outbound mirror worker** (`mirror.go`) — pushes landed trunk (and
  `refs/changes/*`) to any git host; transport only, freezes on
  divergence rather than force-pushing.
- **Org hub** (`orghub.go`) — multi-org: the full surface (git, REST,
  RPC) mounts under `/o/<org>/`; org creation genesis-seeds trunk
  (root manifest, OWNERS naming the creator, AGENTS.md; §6.10) so a
  fresh org can land work immediately. Public orgs serve read-only
  without a session.
- **Auth** (`auth.go`, `principal.go`, `agentprincipal.go`,
  `signup.go`, `invite.go`): named principals, per-org accounts,
  ephemeral task-agent identities (TTL-bound, can't mint or approve),
  signup with operator-fulfilled invite requests (§15.1).

`cmd/runkod` is the entrypoint (serve + hook). The webhook → GitHub
`repository_dispatch` shim for self-hosted CI (§14.4 Mode C) is now its
own top-level project, `runko-bridge/`.

## Decided constraints

- **Postgres is a rebuildable index** of trunk plus workflow state
  (changes, check runs, workspace registry, outbox) — never a second
  source of truth for anything tree-resident (§10.3). Migrations are
  embedded from `db/` and applied at boot, advisory-locked
  (`migrate.go`).
- **Server-side enforcement only**: agent policy, size caps, path
  denylists, description requirements, workspace-origin claims — all
  validated here; nothing trusts a client's say-so (§8.7, §15.3).
- **We never run CI.** GitHub Actions (or anything) executes what
  `runko-ci checks` resolves from the tree's manifests; runkod owns
  change identity, webhooks, the Checks API, and the checkout
  contract (§14). The `watchdog` project reconciles the seam.

## Checks (owned here, §14.9)

- `runkod-test` — `bazel test //runkod/...` (pg tests ride the DSN
  passthrough; httptest + compiled-binary e2e otherwise)
- `runkod-race` — `run_when: direct`; the daemon is the concurrent
  surface (land races, outbox, mirror worker), so `-race` lives here
- `bazel-check` — repo-wide gazelle drift

## Decisions

**Major architectural shifts only** — a decided constraint changes, a
contract surface appears or disappears, a prior decision is reversed.
Routine work (features, fixes, flags) is recorded by its change
description, never here (see [`docs/README.md`](../docs/README.md)).
Repo-wide shifts: the root [`README.md`](../README.md); the record
through 2026-07-16 is [`docs/design.md`](../docs/design.md)'s frozen
changelog.

- **2026-07-17** — **the default org is retired: org-less hub mode**.
  `runkod serve` without `--repo-dir` (which now requires an explicit
  `--orgs-dir`) runs a hub with NO root-mounted org: the root serves
  only the global surfaces (accounts/signup, org listing/creation,
  `/api/admin/*`, invite intake) plus the hub's own ops floor
  (`/healthz`, `/readyz`, `/metrics`), answers a structured
  `no_default_org` 404 everywhere else, and every org — the first one
  included — lives at `/o/<name>/`. `OrgHub.Default` degrades to an
  AUTH-ONLY `Server` (accounts, signup config, credential resolution;
  its `Handler` is never built), backed in Postgres by
  `NewHubPostgresStore` (shared pool, no bootstrap org row).
  `--mirror-remote` and `--zoekt-index-dir` ride the default org's repo
  and are refused in this mode (`--org-mirror`/`github connect` per
  org; per-org zoekt indexing is a recorded follow-up). The historical
  mode — a repo-dir'd default org served at the root — is unchanged
  whenever `--repo-dir` is given; nothing existing migrates
  automatically. Rationale: the default org was the multi-org
  retrofit's compatibility seam (root remotes, CI URLs), not a designed
  object — deployments born multi-org get to not have one.
- **2026-07-16** — this README becomes the project's living spec;
  `docs/design.md` is retired and frozen (see [`docs/README.md`](../docs/README.md)).
- **2026-07-16** — **GitHub App auth on the daemon and bridge**
  (the minting library is the `runkogithubapp` project, see
  [`runkogithubapp/README.md`](../runkogithubapp/README.md)): `runkod serve`
  takes `--github-app-id` + `--github-app-key-file` (+ `--github-api`
  for GHES) and any mirror — `--mirror-remote` or `--org-mirror` — on
  the App's GitHub host with no static `token=` mints installation
  tokens per push; `runko-bridge` takes the same pair as the PAT
  alternative (mutually exclusive with `--github-token`) and mints per
  dispatch, a failed mint answering 502 so the outbox re-drives.
  Onboarding an org's GitHub CI is now: install the App on the org's
  mirror repo, point `--org-mirror`/bridge at it — no PAT minting or
  rotation. This adds runkod's first `dependencies:` edge to
  `runkogithubapp` (admin-lane ops change, alongside that project's
  manifest).
- **2026-07-17** — **Native Mode C dispatch: the bridge seam retires for
  App-credentialed deployments** (`githubdispatch.go`). The outbox
  worker itself turns `change.updated` / `change.check_rerun_requested`
  envelopes into `repository_dispatch` calls minted with the
  deployment's GitHub App, resolving each org's target from the same
  `github_mirror_repo` settings wiring `runko github connect` writes -
  so one command now wires mirror AND CI dispatch, and the per-org
  `runko-bridge` deployment (plus its webhook hop and HMAC secret) is
  no longer needed. Delivery rides the outbox contract unchanged (one
  attempt per due row, backoff, dead-letter; duplicates deduped by the
  workflow's concurrency group). The bridge binary stays in-tree as the
  shim for PAT-only deployments; the `client_payload` shape is
  byte-compatible, so `runko-checks.yml` never notices the swap.
- **2026-07-24** — **the reserved `agent-policy` check** (the receive→gate
  enforcement move's server half; see `platform/README.md`): the funnel
  accepts agent pushes carrying ackable policy findings, warns in the push
  output, and mints `agent-policy` completed/failure at the member's head
  (per series member — the finding lands on exactly the change that owes
  it; a trivial-rebase carry keeps an *acknowledged* run, and an unacked
  one re-mints so a rebase can never launder findings). The run's presence
  joins the required set in `mergeRequirements`; `POST
  /api/changes/{key}/ack-policy` (approve-rights humans only, agents
  refused, `acked_by` audit in the run's reporter) completes it green and
  kicks automerge. The name is refused on the external report surface and
  unknown to rerun. Snapshot pushes warn instead of refusing for ackable
  classes — retiring the trunk-drift false positive (finding I0b79457d
  chased).
