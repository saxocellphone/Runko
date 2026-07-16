# Outbound mirror (M1) — design.md §18.6's outbound half

Decided 2026-07-08: Runko is the source of truth; a downstream mirror on
any git host gives "hosted somewhere trustworthy" — backup, ecosystem
visibility, browseable landed history. This is §18.1's **stage-2 posture**
built first, because it is what a self-hosted SoR needs; the stage-1
inbound direction (GitHub as SoR, PRs ingested as external Changes) is M2.

## Provider-agnostic by construction

The mirror speaks **only the git wire protocol** (`ls-remote`,
`push --force-with-lease`) — no provider REST API, no provider SDK, zero
new dependencies. Any smart-HTTPS git host works; so do ssh remotes and
local paths (the test suite's mirror target is a plain bare directory).

The single provider-specific atom in the outbound direction is the
basic-auth **username** for token auth:

| Provider | `--mirror-username` | Token type |
|---|---|---|
| GitHub | `x-access-token` *(the default — omit the flag)* | **GitHub App installation token** (preferred, minted automatically — see below) or a fine-grained PAT, `contents: write` on the mirror repo |
| GitLab | `oauth2` | project access token, `write_repository` |
| Gitea / Forgejo | any non-empty string | access token |
| Bitbucket Cloud | the account username | app password |
| anything else / ssh / path | *(no token auth injected)* | — |

The token rides git's env-borne config (`GIT_CONFIG_*` →
`http.extraHeader`), never argv — `ps` on the daemon host shows nothing.

## GitHub App auth (2026-07-16, preferred for GitHub mirrors)

Per-org PATs don't scale: every new org means minting, wiring, and
rotating another token. With one **GitHub App** registration
(permissions: `contents: write`) the whole deployment holds a single
credential — `runkod serve --github-app-id <id>
--github-app-key-file <pem>` (+ `--github-api https://<host>/api/v3`
for GHES) — and every mirror on the App's GitHub host that declares
**no** static `token=` mints short-lived installation tokens on demand
(`platform/githubapp`, stdlib-only RS256; cached, refreshed before
their one-hour expiry). An explicit `token=`/`--mirror-token` always
wins, and non-GitHub remotes are untouched — the mirror itself stays
git-wire-only, fed by an injected `TokenSource`.

Onboarding an org's GitHub mirror is then two steps: **install the App
on the mirror repo**, add the `--org-mirror org=…;remote=…` entry (no
`token=`). `runko-bridge` accepts the same pair
(`--github-app-id`/`--github-app-key-file`, mutually exclusive with
`--github-token`) so Mode C dispatch rides the same App — see
`runkod/README.md`'s 2026-07-16 decision entry.

## Semantics (§18.6 invariants, outbound reading)

- **Trunk is leased** (invariant 1): each push expects the mirror's trunk
  to still point where our cursor left it (`--force-with-lease`).
- **Divergence freezes mirroring, loudly** (invariant 4): a foreign write
  to the mirror freezes that ref's cursor — never auto-overwritten. Land
  is NOT blocked (Runko is the SoR; the inbound stage-1 rule that freezes
  *landing* applies only when the mirror owns trunk). Surfaced via
  `runkod_mirror_frozen` on `/metrics` and `GET /api/mirror/status`.
- **Unfreeze is an explicit admin action with a diff report**:
  `POST /api/mirror/unfreeze {"ref": "refs/heads/main"}` (admin principals
  and the deploy token; agents never — the force-land gate). It re-points
  the cursor at the mirror's *observed* tip so the next leased push
  overwrites the divergence exactly once, atomically, and reports both
  tips so the admin sees what they sanctioned.
- **Cursors are rebuildable** (invariant 5): `mirror_cursors` rows are
  cache + audit; deleting them re-derives from the two histories (worst
  case: a spurious freeze to review).
- **What syncs**: trunk (leased) + `refs/tags/*` (fast-forward only) +
  `refs/changes/*` (forced — that namespace is server-owned on both
  sides). **Workspace snapshots never** — personal WIP (§12.2).
- **When**: debounced trigger on every accepted push and every land, plus
  a one-minute reconcile loop so restarts self-heal. A lagging or broken
  mirror never blocks any Runko operation.

## Setup

1. Create an **empty** repo on the host; enable branch protection /
   disable direct pushes there (the mirror is read-only by contract; the
   freeze exists for when someone violates it anyway).
2. Mint the token per the table above.
3. `runkod serve --mirror-remote https://github.com/org/repo.git`
   (+ `--mirror-username` if not GitHub) with `RUNKO_MIRROR_TOKEN` in the
   environment. All three have `RUNKO_MIRROR_*` env forms.

## M2 — the inbound provider seam (recorded, not built)

Inbound is where providers genuinely diverge, and where a real `Provider`
interface earns its existence (M1 deliberately has none — an interface
with one git-protocol implementation would be speculation):

```go
// mirror.Provider — M2 shape, per §18.6 stage-1 invariants.
type Provider interface {
    // VerifyWebhook authenticates an inbound event (HMAC for GitHub/Gitea,
    // token header for GitLab) and normalizes it.
    VerifyWebhook(r *http.Request) (Event, error)
    // MergedPulls lists merges since a cursor - ingested as *external*
    // Changes with attribution (§18.6.3), so audit stays complete.
    MergedPulls(ctx, since Cursor) ([]ExternalChange, Cursor, error)
    // PostStatus mirrors merge-requirements back as a commit status /
    // check run, so the provider-side UI shows Runko's gates.
    PostStatus(ctx, sha string, s Status) error
}
```

Candidates: `github` (App auth — installation-token minting already
exists in `platform/githubapp` since 2026-07-16, M2 reuses it),
`gitlab`, `gitea`. The git-transport half (push/lease/cursors/freeze)
is M1's code, reused unchanged; stage-1 additionally inverts the trunk
lease (provider owns `main`, landing becomes a leased push to the
provider, §18.6.2).
