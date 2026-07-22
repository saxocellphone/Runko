# Runko CI/CD templates

Drop-in GitHub Actions workflows that turn any Runko-hosted monorepo's CI and
CD into **generic executors of tree-declared policy** (§14.9.1, §14.10). They
hardcode no project names, check commands, image names, or registry — all of
that lives in your `PROJECT.yaml` manifests, so adding a check or a deployable
service is a single-manifest change and these files never change.

| File | Role | Trigger |
|---|---|---|
| `runko-checks.yml` | Pre-land checks: runs each affected project's `ci.checks`, reports results to the merge gate | `runko-change` dispatch (per pushed Change) |
| `runko-images.yml` | Post-land CD: builds each landed change's deployable images and reports digests to Runko's deploy record | `runko-image-build` dispatch (per land) |

Runko drives everything; GitHub only builds and reports. Runko never runs your
checks or deploys — it emits events and consumes results (§14.16).

## Adopt (both workflows)

1. **Copy** the file(s) you want into your repo's `.github/workflows/`.
2. **Secrets** (repo → Settings → Secrets): set `RUNKO_URL` to your org mount
   (e.g. `https://<host>/o/<org>`) and `RUNKO_CI_TOKEN` to your org's **deploy
   token**. GitHub provides `GITHUB_TOKEN` automatically. *(Optional repo
   Variables: `RUNKO_CI_VERSION` pins the `runko-ci` release — default
   `cli-latest` — and `RUNKO_CI_DOWNLOAD_URL` points at a fork/mirror/GHES
   release base. The workflows download the `runko-ci` binary, so no language
   toolchain is required to run the executor.)*
3. **Wire the dispatch**: install the Runko GitHub App on your repo, **then**
   `runko github connect --repo <owner>/<name>` (connect verifies the App is
   installed and the repo reachable) points your org's outbox at this GitHub
   repo, so a pushed Change fires `runko-change` and a land fires
   `runko-image-build`. The workflows fetch the change ref from the mirror (with
   a retry, since the dispatch can beat the debounced mirror push).

That is the whole wiring. The mirror + dispatch is the only integration point;
the workflows read the rest from your tree.

## `runko-checks.yml` — adjust the runner contract

The executor structure is generic, and the workflow **downloads the `runko-ci`
binary itself** — no language runtime is needed just to run it. Your check
*commands*, though, may need a runner, so two commented blocks marked
`RUNNER CONTRACT (ADJUST)` are yours to fill in:

- **Services** (e.g. a Postgres your integration tests read via an env var).
- **Toolchains** (e.g. `setup-bazel`, `setup-node`) your `ci.checks` commands
  invoke.

Uncomment/edit those to match what your checks actually use. A check whose tool
is missing fails loud and leaves the gate pending — visible, never silently
skipped.

## `runko-images.yml` — declare images + registry in the tree

CD needs two manifest facts:

- **Per service**, in that project's `PROJECT.yaml`:
  ```yaml
  capability_config:
    deploy:
      image: { name: <image>, context: <dir>, dockerfile: <path> }
  ```
  A project whose binary ships inside another's image declares a rider instead:
  `deploy: { workloads: [{ name: <workload>, image: <owner-image> }] }`.

- **Once, on your ROOT `PROJECT.yaml`**: the registry base.
  ```yaml
  deploy_registry: ghcr.io/<owner>/<repo>
  ```
  `runko-ci images` prefixes it to each image name to form the full ref the
  workflow tags, pushes, and reports — so the workflow itself names no
  registry. The template logs in to **GHCR**; for another registry, change only
  the `docker/login-action` step.

See `docs/spec/deploy/README.md` for the full `deploy` capability spec.

## What you do NOT need

- No per-image workflow edits — the matrix is computed from the tree.
- No hand-synced project→check or project→image maps — Runko derives them, and
  the merge gate + these executors read the *same* computation, so they can
  never disagree.
- No pin/rollout step here — reporting a digest is the whole CD contract;
  Runko's deploy record + your GitOps controller (Argo/Flux) do the rest.
