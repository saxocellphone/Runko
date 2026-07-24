# The `deploy` capability (design.md §14.10)

Spec artifact for the `deploy` capability — same role `project.schema.json`
plays for the manifest core and `build-adapter/README.md` plays for `build`:
decided here, transcribed into `platform/`, `runkod/`, and `runko-ci`.

The capability turns "this project is deployed" into a **declared fact in the
tree**. It replaces three hand-synced copies of the same knowledge (the
compiled image→project map in `runkod/deployimages.go`, its inline duplicate in
`release-images.yml`, and the pin targets in the GitOps repo) with one source:
the manifest.

**Scope of THIS document.** Only the **build-derivation** half is specified
now — enough to compute *which images a landed change must rebuild*, per-org,
from the tree. The runtime half (chart-served `workloads` fields — port,
health, expose, chart values — and the GitOps rendering) and receive-time
validation are forthcoming and are called out inline where they land.

## The sub-blocks

`capability_config.deploy` carries three independent sub-blocks. A project may
declare any combination, or (with the capability but none of them) nothing —
the last is a no-op and will be a receive-time refusal once validation lands.

```yaml
capability_config:
  deploy:
    image:                       # "I PRODUCE a deployable image"
      name: runkod               # logical image id; the image:'s key everywhere
      context: .                 # build context (default: the project dir)
      dockerfile: Dockerfile
      build_args:                # optional, BUILD-TIME, PUBLIC (see below)
        VITE_RUNKO_URL: "/"
    workloads:                   # "these runtime workloads run from an image"
      - name: runko-mailer       # workload id
        image: runkod            # OWN image, or ANOTHER project's (the rider edge)
    binaries:                    # "I PRODUCE released standalone binaries"
      tag: cli-latest            # the rolling release tag they publish under
      items:
        - name: runko            # path defaults to <project-path>/<name>
        - name: runko-ci
```

- **`image` — the owner declaration.** The project that declares
  `deploy.image` OWNS that image; its `context` + `dockerfile` build it. In
  the Runko monorepo: `runkod`, `web`, `webadmin` each own one.
- **`workloads[].image` — the rider edge.** A workload whose `image` names a
  DIFFERENT project's image declares that this project's binary ships INSIDE
  that image (e.g. `mailer` and `watchdog` compile into the `runkod` image via
  its Dockerfile, so each declares a workload with `image: runkod`). This is a
  declared edge from **rider → image owner**, read alongside `dependencies:`
  and `consumes:` but used ONLY for image-rebuild computation (below) — never
  for check gating.

  The remaining `workloads` fields (port, health, expose, chart values) are the
  runtime/chart half and are NOT specified here yet.
- **`binaries` — the standalone-release declaration** (2026-07-24). Artifacts
  that are NOT images: cross-compiled binaries published as a rolling
  host-side release (e.g. GitHub's `cli-latest`). Each item names a buildable
  main package (`path` defaults to `<project-path>/<name>`); `tag` is the
  rolling release they publish under and is REQUIRED — a `binaries` block
  without a tag declares nothing (read-time normalization). The rebuild rule
  is the project's own affected membership — `rebuild(binaries of P) iff P ∈
  affected.Result.Projects` (or `RunEverything`) — deliberately simpler than
  the image rule: there is no rider edge for binaries, and the dependency
  closure already places `P` in the result when anything it builds from
  changes. `runko-ci binaries` is the executor's resolution verb: it emits
  `releases: [{tag, binaries: [{name, path}]}]`, so the binary-release
  workflow hardcodes no package paths, project names, dependency lists, or
  tags. Cross-compile targets are runner contract, not tree config. Two
  projects declaring the same tag merge into one release; the same binary
  name under one tag with DIFFERENT paths is a structured `ambiguous_binary`
  refusal (the `ambiguous_image` rule, applied to binaries).

## The image-rebuild derivation

The image a landed change must rebuild is a **post-filter over the affected
result** — NOT a new dependency closure. This distinction is load-bearing:
`affected.Result.Projects` is consumed by both the merge gate and the CI
executor (`index.ChecksFor` shares it), so surfacing riders INTO that result
would silently widen the check gate. The derivation READS the result, never
mutates it.

```
owner(I)  = the project whose deploy.image.name == I
riders(I) = projects declaring a workload with image == I
rebuild(I) is true  iff  affected.Result.Projects ∩ ({owner(I)} ∪ riders(I)) ≠ ∅
           RunEverything ⇒ rebuild every declared image (fail closed).
```

The dependency closure is NOT enumerated: a change to a project the owner
`depends on` (e.g. `platform` → `runkod`) already puts the owner in
`affected.Result.Projects` via the ordinary dependents expansion, so it
intersects `{owner(I)}` for free. The rider edge is what a static map would
otherwise need for the cross-project-image case (a `mailer`-only change must
rebuild the `runkod` image, and `mailer` is not in the owner's dependency
graph — it is a rider).

**Per-org by construction.** The derivation runs against the landing org's own
indexed tree, so an org that declares no `deploy.image` derives an empty set —
no image builds, no deploy record. (The predecessor hardcoded map matched bare
project names with no org scope, so any org landing a change touching a
project named `web` opened a phantom deploy record; this closes that.)

## `build_args` is public by definition

`build_args` is BUILD-TIME configuration inlined into the shipped artifact
(Vite, for one, bakes build args into the public JS bundle) — and PROJECT.yaml
is mirrored to a public git host verbatim. **A secret must never appear in a
build_arg.** Tokens and credentials are runtime, per-environment, and belong to
the GitOps/secret layer, never the tree.

## The registry lives at the root (`deploy_registry`)

A `deploy.image` declares an image's logical `name` (e.g. `runkod`), NOT where
it is published. The publishing target — the registry base — is one value for
the whole monorepo, so it lives ONCE on the root project's manifest as the
top-level `deploy_registry` field (like `root_invalidation`/`prose`), not
repeated in every `deploy.image`:

```yaml
# root PROJECT.yaml
deploy_registry: ghcr.io/acme/monorepo
```

`runko-ci images` reads it from the root project and emits a full **`image_ref`**
per image — `<deploy_registry>/<name>` — so a generic image-build workflow
hardcodes no registry: it builds/pushes/reports whatever `image_ref` the tree
declares. Absent, `image_ref` is the bare `name` (local/dev; a real push needs
the registry set). This keeps the registry in the tree (manifest-first) and
publishing config out of the workflow file — the difference between a workflow
a Runko user can copy unchanged and one they must fork.

## The GitOps pin target lives at the root too (`deploy_gitops`)

The write-back half of GitOps-shaped CD — CI pins each freshly-built digest
into a GitOps repository that a reconciler (Argo CD, Flux) deploys from — has
the same one-per-repo character as the registry, so its target is declared
once on the root manifest, as the top-level `deploy_gitops`:

```yaml
# root PROJECT.yaml
deploy_gitops:
  repository: acme/k8s-cluster                    # <owner>/<repo> on the org's git host
  kustomization: apps/monorepo/kustomization.yaml # path within that repo
```

`runko-ci images` emits it verbatim as a top-level `gitops` object (absent
when undeclared), so the workflow's pin job hardcodes no repository or
kustomization path and SKIPS entirely for an org that declares none — the pin
*mechanism* (semantic yq edit, reset-and-reapply retry, digest validation)
stays in the workflow, the pin *target* lives in the tree, and credentials
live in neither (the workflow's secret store owns authentication). Both
fields are required: a partial declaration is dropped at read time (the
schema polices authoring).

## Consumers

- `platform/index` — extracts `deploy.image` / `workloads[].image` and the
  root's `deploy_registry` into the project index (raw `capability_config` is
  otherwise dropped).
- `platform/deploy` — the pure `ImagesForAffected(result, index)` derivation,
  and `ImageBuildsForAffected` which resolves each image's build config +
  `image_ref` (registry-prefixed).
- `runkod` — opens the per-land deploy record from the derived set.
- `runko-ci images` — the CI-facing executor form: emits `{name, image_ref,
  context, dockerfile, build_args}` (the build workflow computes the image set
  itself; the daemon names no images to it, §14.9.1), plus the root's
  `gitops` target when declared.
- `runko-ci binaries` — the standalone-release executor form: emits
  `releases: [{tag, binaries}]` for the affected-scoped `deploy.binaries`
  declarations.

## Decisions

- **2026-07-21** — Born as the `deploy` capability's spec surface. The image
  sub-block + rider edge + the rebuild post-filter are the build-derivation
  half (Track A); the runtime/chart half and receive-time validation follow.
- **2026-07-21** — `deploy_registry` added (root-oriented) so `runko-ci images`
  emits a registry-prefixed `image_ref`, moving the last hardcoded value out of
  the image-build workflow and making it a reusable generic template. Registry
  is one-per-repo publishing config, so it lives at the root, not per-image.
- **2026-07-24** — `.github/` declared a generic template for any Runko-based
  monorepo, and the last two instance-shaped surfaces move into the tree:
  `deploy_gitops` (root-oriented, the CD write-back target `runko-ci images`
  emits as `gitops`) and the `binaries` sub-block (+ `runko-ci binaries`),
  replacing the binary-release workflow's hand-maintained dependency list —
  which had already drifted from the manifest edges (it omitted the
  `consumes: runkod` edge) — with the real affected closure. Same decision
  adds the `post_land` check class (project.schema.json `ci.checks[].run_when`)
  so the full-stack compose smoke is manifest-declared like every other check
  instead of a hand-written workflow job.
