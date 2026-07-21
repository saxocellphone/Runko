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

## The two sub-blocks

`capability_config.deploy` carries two independent sub-blocks. A project may
declare either, both, or (with the capability but neither) nothing — the last
is a no-op and will be a receive-time refusal once validation lands.

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

## Consumers

- `platform/index` — extracts `deploy.image` / `workloads[].image` into the
  project index (raw `capability_config` is otherwise dropped).
- `platform/deploy` — the pure `ImagesForAffected(result, index)` derivation.
- `runkod` — opens the per-land deploy record from the derived set.
- `runko-ci images` — the CI-facing executor form (the build workflow computes
  the image set itself; the daemon names no images to it, §14.9.1).

## Decisions

- **2026-07-21** — Born as the `deploy` capability's spec surface. The image
  sub-block + rider edge + the rebuild post-filter are the build-derivation
  half (Track A); the runtime/chart half and receive-time validation follow.
