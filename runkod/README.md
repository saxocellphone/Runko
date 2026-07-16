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

`cmd/runkod` is the entrypoint (serve + hook); `cmd/runko-bridge` is
the webhook → GitHub `repository_dispatch` shim for self-hosted CI
(§14.4 Mode C).

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

New decisions land here as dated entries; the record through
2026-07-16 is [`docs/design.md`](../docs/design.md)'s frozen changelog.

- **2026-07-16** — this README becomes the project's living spec;
  `docs/design.md` is retired and frozen (see [`docs/README.md`](../docs/README.md)).
