# runkogithubapp

GitHub App installation-token minting: the deployment-wide credential
that replaces per-org PATs for everything Runko does against GitHub —
mirror pushes (`platform/mirror`) and `runko-bridge` dispatch. One App
registration (app id + RS256 private key) serves every org; an org's
GitHub setup shrinks to installing the App on its mirror repo. This
README is the project's spec surface.

## What it owns

- The App JWT (RS256 = RSASSA-PKCS1-v1_5 over SHA-256, stdlib crypto
  only — no new dependencies), issued-at backdated 60s against clock
  skew, 9-minute lifetime (GitHub caps at 10).
- Installation resolution per repo (`GET /repos/{owner}/{repo}/
  installation`) with a structured install-the-App error on 404 and
  one-shot re-resolution when a cached installation id goes stale (App
  reinstalled).
- Installation-token minting and caching: tokens are refreshed inside a
  5-minute margin of their one-hour expiry, so a handed-out token stays
  valid across a whole git push or REST call.
- `TokenSource(ownerRepo)` — the plain `func() (string, error)` shape
  provider-agnostic consumers accept (`mirror.Remote` takes the func,
  never this package), and `RepoPath(url)` — the provider-detection
  atom (github.com, or the GHES host when the API base is overridden).

## Decided constraints

- **An installation token is a drop-in PAT**: Bearer auth for REST
  (`repository_dispatch`) and the `x-access-token` basic-auth password
  for git-over-https. Consumers must not need to know which they hold.
- **Static tokens always win.** A configured `token=`/`--github-token`
  disables App minting for that remote/bridge; PATs remain fully
  supported.
- **Failed mints fail one operation, never the process** — the mirror's
  debounce + reconcile loop and the bridge's outbox-retry contract
  (502) are the retry story.
- **Stdlib only.** No JWT or GitHub SDK dependencies; external services
  stay processes or plain HTTP.

## Consumers

`runkod/cmd/runkod` (mirror TokenSource wiring, `--github-app-id` /
`--github-app-key-file` / `--github-api`) and `runkod/cmd/runko-bridge`
(per-dispatch minting). `platform` never imports this project.

## Checks (owned here)

- `runkogithubapp-test` — `bazel test //runkogithubapp/...`
- `runkogithubapp-race` — same tree under `-race` (the token cache is
  concurrent state); `run_when: direct`
- `bazel-check` — repo-wide gazelle drift, deduped by name

## Decisions

- **2026-07-16** — **Born as its own project** (companion entries:
  [`platform/README.md`](../platform/README.md),
  [`runkod/README.md`](../runkod/README.md)): GitHub App auth replaces
  per-org PATs across the GitHub integration plane, and the minting
  library is carved out of `platform` because platform never consumes
  it — its only importers are runkod's binaries. A narrow project keeps
  the affected closure of a token-minting change at `runkogithubapp-test` +
  `runkod-test` instead of platform's full dependent closure.
