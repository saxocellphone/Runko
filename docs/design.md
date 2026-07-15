# Technical Design: Runko — Monorepo Platform (CitC-class, Git-backed, OSS)

| Field | Value |
|-------|--------|
| **Status** | Draft |
| **Authors** | — |
| **Created** | 2026-07-06 |
| **Last updated** | 2026-07-06 |
| **Audience** | Engineering, product, design partners |

---

## 1. Summary

This document specifies a **monorepo-first developer platform**: an open-source, self-hostable system that makes monorepo the default architectural choice for medium-sized organizations.

The product is **not** a CI/CD vendor and **not** a Google-scale proprietary VCS. It is a **monorepo operating system** layered on **Git**, built on three pillars:

1. **One monorepo that feels small** — first-class Projects (ownership, boundaries, near-zero ceremony) + CitC-class Workspaces (full-repo view, materialize only your slice).  
2. **Changes that land with confidence** — change-centric review with authoritative path ownership, trustworthy affected computation, and CI integrated deeply via contracts/plugins/templates (webhooks, Checks API, checkout ergonomics — never our runners).  
3. **Humans and coding agents as co-equal clients** — every flow has a GUI/CLI *and* a stable tool/API shape (stable IDs, structured errors, MCP schemas), with **project-granular server-side enforcement** for agents.

Three constraints shape everything below: **UX is a primary product constraint** — powerful-but-hostile Boq-style configuration is the named anti-pattern (§2.3); **agentic coding is a first-class user type**, not a plugin (§8); and **CI integration is load-bearing for VCS success** even though execution stays with existing CI products (§14) — a monorepo host that cannot plug cleanly into GHA/Buildkite/GitLab loses to “worse monorepo UX on GitHub.”

**One-line thesis:** *Make monorepo accessible—opinionated architecture, delightful low-ceremony UX, CitC workspaces, agent-native APIs, and excellent CI plug-in points—on Git, open source, and self-hostable.*

> **Name (decided 2026-07-06): Runko** — Finnish for *tree trunk* and *frame/chassis*: the single trunk plus the structure everything mounts on. CLI `runko`, env prefix `RUNKO_*`, CI CLI `runko-ci`. Clearance snapshot at decision time: npm/PyPI/crates.io unclaimed; no software-class trademark found; nearest uses are a niche GPL plasma-simulation toolbox (`hel-astro-lab/runko`) and Finnish timber/engineering firms — unrelated domains. Rejected finalists (all hard collisions): `banyan` (Banyan Security ships a `banyan` CLI; BanyanDB), `cambium` (public co + squatted registries), `pando` (PANDO.AI trademark on the same tree metaphor), `stemma` (Teradata registered mark). Known wart: sounds near a vulgar Swedish slang verb (*runka*); judged survivable. Formal trademark clearance before public launch (§22.2).

---

## 2. Problem statement

### 2.1 What teams want

Monorepos deliver atomic cross-cutting changes, shared libraries without publish/version hell, consistent tooling, and a single source of truth. Large tech companies operationalize this with custom stacks (e.g. Piper + CitC + Critique + Blaze/Boq at Google; Sapling + EdenFS + Buck2 at Meta).

### 2.2 Why medium orgs fail at monorepo

| Failure mode | Cause |
|--------------|--------|
| “We tried monorepo; clones are unbearable” | Full Git worktrees; no virtual/sparse workspace product |
| “Nobody knows where to put code” | Folders without a first-class project model |
| “CODEOWNERS is theater” | No coverage, routing, or review UX built for path ownership |
| “Every PR runs the world” | No trustworthy affected graph; CI becomes the monorepo tax |
| **“Our VCS doesn’t fit our CI”** | **Forge-agnostic monorepo with weak webhooks/checks/checkout → teams abandon the platform** |
| “Gerrit scales but nobody wants to use it” | Scale-oriented forges with poor UX |
| “GitHub UX is fine but polyrepo-shaped” | Hosting optimized for many small repos |
| **“Creating a service means fighting the manifest”** | **Boq-class / platform YAML is powerful, poorly defaulted, and tribal-knowledge-heavy** |
| **“The coding agent got lost in the monorepo”** | **No structured project map, huge context, wrong owners, full-tree clones for agents** |

Existing tools attack **layers** (remote build, merge queues, forges, generic AI IDEs) but rarely the **integrated monorepo experience**: low-ceremony projects + workspace locality + ownership-native changes + **agent-safe navigation**.

#### The real competitor: the assembled GitHub stack (2026)

The honest baseline is not "GitHub alone." A medium org can assemble most individual capabilities today **without moving its system of record**:

| Our pillar | Assembled-stack alternative | Where the assembly falls short |
|---|---|---|
| Project model, templates, generators, affected | **Nx / moonrepo / Pants**: project graph, `affected`, generators, tags/ownership, native MCP | Build-tool-scoped and advisory: no merge gates, no server-side enforcement, per-ecosystem silos |
| Change-centric review, stacks, merge confidence | **Graphite** (stack-aware queue, AI review), **Aviator** (affected-target merge queues), GitHub native stacked PRs + merge queue | Bolted onto the PR/branch model; path ownership stays CODEOWNERS-theater; monorepo scoping is heuristic, not authoritative |
| Agent governance | **GitHub Agent HQ**: agent identity, mission control, audit, MCP registry, AGENTS.md | **Repo-granular.** No sub-repo write affinity, no path policy, no project map — the unit of control is the whole monorepo |
| Thin workspaces | **Scalar** (upstream Git): partial clone + sparse + fsmonitor + background maintenance | Client-side config, not a product: no cloud-primary overlay, no policy, no workspace identity, no agent limits |
| Monorepo benefits without migration | **Nx Polygraph "synthetic monorepos"**: cross-repo graph + agent memory over polyrepo | Accepts polyrepo permanently: no atomic changes, no single trunk, no unified review |

**Our thesis against the assembled stack:** each layer solves its slice *advisorily*; none is authoritative because none owns the change lifecycle. The integration seams themselves (five vendors, five config surfaces, five agent stories) are exactly the ceremony we eliminate. We must win on **enforcement + integration + sub-repo granularity** — and we must not lose on migration cost, hence mirror-first adoption (§18).

### 2.3 Lesson from Boq-style configuration (anti-pattern)

At Google, Boq and related platform manifests made the *right* things possible (service identity, owners, deploy, RPC surfaces) while often making the *first* things painful:

| Anti-pattern | What users feel | Our counter-design |
|--------------|-----------------|-------------------|
| Huge all-in-one manifest | “I must understand the platform to say hello world” | **Progressive disclosure**: 3 fields to start; advanced sections unlocked later |
| Opaque validation | Fail after long edit cycles with cryptic errors | **Live validation**, structured errors with fix suggestions (human + agent) |
| Copy-paste from a working neighbor | Drift, wrong owners, cargo-cult flags | **Golden templates** + “create from intent” wizards; deprecate copy-paste |
| Config-as-prose (comments as docs) | Docs rot; only veterans succeed | **In-product guidance**, schema docs generated from the same source as validation |
| Manual keep-in-sync across files | BUILD, manifest, owners, deploy all diverge | **Single write path** (`project create` / API) generates consistent set; infer what can be inferred |
| Experts-only knobs co-located with basics | Cognitive overload | **Layers**: Core / Runtime / Advanced (see §6, §10) |

**Design rule:** If creating a standard service takes more than a few decisions a junior engineer (or an agent) can make in one minute, the UX has failed—even if the power user surface is complete.

**Sharpened (2026-07-10, prompted by the per-check-inputs question in §14.5.8):** this doctrine constrains **defaults, not capability**. An opt-in refinement is admissible when (a) absence means exactly today's zero-config semantics — the knob can never become load-bearing for a default project; (b) it reuses an existing vocabulary (one glob dialect, one error shape) rather than minting a new one; and (c) the zero-config default is *good* — Nx's lesson is that a default coarse enough to force everyone into the opt-in is itself a Boq violation wearing an "optional" badge. Coarse-default pain is a defect to fix in the default, never an upsell for the knob.

### 2.4 Coding agents change the user model

Software is increasingly written by **agents under human supervision**:

- Agents need **structured orientation** (what projects exist, where to edit, who owns what)—not a 40GB clone and hope.  
- Agents are bad at tribal YAML and excellent at **tool calls with schemas**.  
- Agents amplify monorepo failure modes (touching the wrong package, skipping owners, mega-diffs) unless the platform **constrains and guides** them.  
- Humans still review and land; the platform must make **agent-authored Changes** first-class (attribution, policy, blast radius).

### 2.5 Non-problems (out of scope for this product’s identity)

- Winning on runner speed / RBE productization vs Namespace / BuildBuddy / GitHub-hosted runners  
- Supporting 100,000 engineers or multi-billion-LOC Google scale on day one  
- Replacing every forge feature (Issues marketplace, wikis, Actions ecosystem)  
- Building a foundation-model coding product (we **integrate** with agents; we are not “yet another chat IDE”)  
- Being the system of record for pipeline authoring UIs (we **emit** pipeline fragments and status; customers keep their CI product)

---

## 3. Goals and non-goals

### 3.1 Goals

| ID | Goal |
|----|------|
| G1 | **Monorepo as architecture**: one primary trunk monorepo per org (product default) |
| G2 | **Accessible daily loop**: time-to-first-edit and time-to-first-merge competitive with a small repo |
| G3 | **CitC-class workspaces**: full monorepo *view*, materialize only what is needed; overlay of local changes; portable workspace state |
| G4 | **First-class Projects**: create/scaffold/own units of code with explicit boundaries |
| G5 | **Ownership-native changes**: strict owners for merge; clear required reviewers from touched paths |
| G6 | **Git substrate**: Git as storage and interchange for migration and ecosystem fit |
| G7 | **OSS + self-host**: core platform fully usable offline from vendor cloud |
| G8 | **CI integration excellence**: stable events, Checks API, checkout contract, affected export, **official plugins/templates** for popular CI systems; never require our runners |
| **G9** | **Low-ceremony configuration UX**: progressive manifests; wizard/API-first creation; never require Boq-level YAML for a default service |
| **G10** | **Agent-native platform**: stable MCP/API, project map, constrained workspaces, agent identity & policy for coding agents |
| **G11** | **Dual-audience quality bar**: every core flow must be excellent for humans *and* scriptable/safe for agents |
| **G12** | **Merge confidence**: landing a Change is blocked/informed by real external CI results with monorepo-correct scope |
| **G13** | **Mirror-first adoption**: an org gets demonstrable value (projects, affected, review) **before** flipping its system of record (§18) |

### 3.2 Non-goals (v1)

| ID | Non-goal |
|----|----------|
| NG1 | Proprietary non-Git VCS as the daily driver |
| NG2 | Being the primary CI/CD **execution** platform (runners, RBE, pipeline UI as product core) — **integration is in scope and critical** |
| NG3 | Feature parity with GitHub/GitLab as a general forge |
| NG4 | Platform-wide build-system mandate. Refined (§14.5.4): we are **opinionated by criterion** — only hermetic/incremental systems (Bazel, Buck2) ever get engine status, greenfield golden path is Bazel-first, and orgs may self-mandate via `require_build_binding` — but brownfield adoption is never gated on a build migration |
| NG5 | Multi-monorepo virtual federation as the primary model |
| NG6 | Perfect multi-OS production FUSE on day one |
| NG7 | Path-isolated multi-tenant “hostile co-tenants in one repo” at bank grade (single-tenant self-host is the trust model) |
| NG8 | Training or hosting proprietary coding LLMs |
| NG9 | Forcing users to hand-author large multi-section platform manifests for default cases |

### 3.3 Success metrics

**Top 5 (dogfood dashboard — these decide go/no-go):**

1. Weekly landed Changes per active engineer (north star)  
2. Pilot-org retention at day 90  
3. New engineer → first merged Change  
4. CI: false `run_everything` rate (affected-graph quality)  
5. Create default Project: seconds + required decisions  

Full directional table:

| Metric | Target (directional) |
|--------|----------------------|
| **North star: weekly landed Changes per active engineer** | Flat-to-up vs pre-adoption baseline within 60 days |
| **Pilot retention** | > 80% of pilot orgs still landing real Changes at day 90 |
| Time to editable workspace for a Project | &lt; 60s on a warm content cache |
| Median materialised bytes / full tree | ≪ full clone (order of 1–5% for typical eng) |
| New engineer → first merged Change | &lt; 1 day with docs + template monorepo |
| **Create default Project (human)** | **&lt; 60 seconds, ≤ 3 required decisions** (name, type, owners-or-default) |
| **Create default Project (agent)** | **Single tool call** with schema validation; no multi-file YAML authoring required |
| Unregistered code | Top-level dirs without a Project trend to zero; new unowned dirs alerted weekly |
| Manifest lines authored by humans for default service | **Near zero** (generated; optional overrides only) |
| Self-host: compose eval path | Core loop (create → change → land) in &lt; 15 minutes; **mirror + Connect CI are separate wizards** (see CI row); Zoekt indexes async on first boot (§16.4) |
| Agent orientation | Agent can list projects, open affinity workspace, open Change without full-repo clone |
| Agent policy violations blocked | Wrong-path edits / unowned paths fail fast with structured errors |
| **CI: time to green integration** | **&lt; 1 day** to wire a supported CI system with affected-only jobs via template/plugin |
| **CI: required checks on Change** | Default template posts Checks; merge requirements reflect them |
| **CI: false “run everything”** | Affected set drives matrix; world rebuild only on explicit roots/tooling changes |

---

## 4. Target users and constraints

### 4.1 ICP (initial)

- **Medium organizations**: roughly **20–300 engineers** on one product/platform org  
- Greenfield monorepo **or** consolidating a manageable number of Git repos  
- Willing to adopt **trunk-based** norms and **project ownership**  
- Want self-host or need source transparency (security-sensitive eng culture)  
- Already using or adopting **AI coding agents** in the editor or CLI  

### 4.2 User types (first-class)

| User type | Needs |
|-----------|--------|
| **Human engineer** | Fast create/open/edit/review; minimal config ceremony; clear ownership |
| **Coding agent** (tool-using LLM session) | Project map, schemas, constrained workspace, structured errors, small context |
| **Human reviewer / owner** | Blast radius, agent attribution, “what did the bot touch?” |
| **Platform admin** | Templates, org defaults, agent policies, quotas |

### 4.3 Explicitly deferred ICP

- Hyperscale (10k+ eng on one trunk) requiring non-Git storage  
- Polyglot monorepos that refuse any project model at all  
- Orgs that require many isolated monorepos for legal entity separation as the *default* (may support multi-org later)

### 4.4 Scale assumptions (Git envelope)

We assume Git remains viable when the product enforces hygiene and CitC workspaces:

| Dimension | Design assumption |
|-----------|-------------------|
| Repo size | Prefer &lt; ~15–20 GB well-kept; hard policies before host ceilings (~100 GB class) |
| Concurrent eng | Tens to low hundreds on one trunk |
| Tree size | Hundreds of thousands of files OK with sparse/partial + workspace affinity |
| Concurrent agents | Same order as humans; each agent session is a Workspace with policy |

If customers outgrow this **despite** workspaces and policy, we revisit the storage backend behind a stable `MonorepoStore` interface—not as a day-one rewrite.

### 4.5 Which pains bind at which scale (honesty check)

Upstream Git (Scalar: partial clone + sparse cone + fsmonitor + background maintenance) already makes a 15–20 GB repo *mechanically* tolerable with zero new infrastructure. At ICP scale the binding pains are organizational, not storage:

| Org size | Pains that actually bind | Sequencing implication |
|----------|--------------------------|------------------------|
| ~20–50 eng | Ownership routing, review UX, affected CI cost, agent governance | Control plane + CI plane + agent policy lead; workspace plane can be thin (Scalar-class) |
| ~50–150 eng | + trunk contention (land races), stacked work-in-flight, owner coverage gaps | Stacks + optimistic land / queue move from "later" to "core loop" |
| ~150–300 eng | + clone/checkout mechanics, repo hygiene, CI fan-out cost | Workspace-plane depth (cloud overlay, prefetch) earns its complexity |

**Sequencing rule:** invest in workspace-plane depth only when telemetry from real orgs shows storage mechanics—not workflow—is the binding constraint. This drives the re-ordered phases in §19.

---

## 5. Product principles

1. **Locality of experience on a global source of truth** — monorepo is global; day-to-day work is project + change.  
2. **Opinionated defaults, escape hatches later** — one trunk, project model, owners required; power features after the core loop is excellent.  
3. **Git for interchange, platform for workflow** — raw `git clone` of the world is escape hatch, not the daily path (humans or agents).  
4. **Open monorepo OS** — self-host is first-class.  
5. **Integrate CI deeply, don’t re-own execution** — we own *events, identity of a Change, affected scope, check aggregation, checkout ergonomics*; customers own *runners and pipeline product*. Integration quality is a **launch gate**, not a post-MVP nice-to-have.  
6. **Projects are not only RPC services** — lib/service/app/job types; optional runtime surfaces.  
7. **Ceremony is a bug** — configuration surface area for the default path must stay small; progressive disclosure for power.  
8. **Generate, don’t make humans (or agents) hand-write platform YAML** — intents and wizards produce manifests.  
9. **Dual audience: human UX + agent UX** — every primary flow has a GUI/CLI *and* a stable tool/API shape with structured errors.  
10. **Constrain agents the way we constrain juniors** — affinity workspaces, owners, and path policy protect the monorepo from confident wrong edits.  
11. **Delight in the empty states** — create project, empty monorepo, first Change, first agent session are design-critical, not afterthoughts.  
12. **Build the delta, adopt the substrate** — prefer upstream Git capabilities (partial clone, sparse cone, fsmonitor, Scalar-style maintenance) and proven ecosystem pieces (Zoekt, gitleaks) over bespoke infrastructure; write code only for what nobody else ships: enforcement, integration, sub-repo granularity.

---

## 6. UX strategy (first-class)

### 6.1 UX north star

> **A new project should feel like naming a folder and picking a team—not filing a platform tax form.**  
> **A coding agent should feel like a scoped contractor with a map—not an intern lost in a skyscraper.**

### 6.2 Progressive disclosure for Project configuration

Inspired by (and rejecting the pain of) Boq-scale manifests. Project configuration has **layers**:

| Layer | When required | Examples | Authoring model |
|-------|---------------|----------|-----------------|
| **L0 Intent** | Always | name, type (`service`/`library`/…), language (optional, default `go`), path (optional auto), owners (or inherit) | Wizard / one API call / one CLI invoke |
| **L1 Core (generated)** | Always present on disk, rarely edited | `PROJECT.yaml` skeleton, default owners file, README stub, language skeleton from template | **Platform generates** |
| **L2 Runtime (opt-in)** | When deploying / exposing APIs | RPC service name, ports, deploy target class | UI toggles / “add capability” / agent tool with schema |
| **L3 Advanced** | Rare | custom CI hints, non-default visibility, experimental flags | Explicit “advanced” section; never shown in default create |

**Hard rule:** L2/L3 fields must not block L0 create. A service that is “just code + owners” is valid.

### 6.3 Create Project UX (human)

**Primary path — guided create (web or CLI interactive):**

```text
1. What are you building?  [Service] [Library] [App] [Job]
2. Name: checkout-api
3. Owners: [my team ▼]  (default: creator’s primary group)
4. Language/template: [Go service ▼]  (org-configured list)
5. [Create] → workspace opens attached to new project
```

Step 4's built-in list ships six languages — `go` (default), `python`, `ts`, `rust`, `java`, `cpp` — admitted by Bazel-rule maturity (§14.5.4); org templates extend the list (§10.4). "Other…" is the `no_template` escape hatch: PROJECT.yaml + README only, the language string recorded verbatim — any language can live in the repo, it just doesn't get a scaffold.

No free-form multi-page YAML. Optional “I need RPC / HTTP / worker” chips add **capabilities** (L2) via second step, still not raw manifest editing.

**Secondary path — power users:** edit generated `PROJECT.yaml` with schema-aware editor (autocomplete, inline validation).

**Tertiary path — import existing tree:** “Adopt folder as Project” infers name/path; asks only for owners if missing.

### 6.4 Manifest philosophy: small core, inferred rest

**On-disk core (illustrative—keep tiny):**

```yaml
# commerce/checkout/PROJECT.yaml  — humans rarely touch this for defaults
schema: project/v1
name: checkout-api
type: service
# owners: omitted → inherited from path OWNERS or create-time default
# dependencies: omitted → inferred over time from imports / optional declare
```

**Derived / control-plane state (not hand-maintained):**

- Canonical project ID, created_at, template version  
- Inferred dependency edges (async indexer)  
- Effective owners (project + path rules)  
- Capabilities enabled (rpc, http, …) as structured records that *may* reify into config files via codegen  

**When code generation runs** (templates, BUILD stubs, proto scaffolds), it is triggered by **capability enablement** or template choice—not by requiring the user to pre-author every field Boq-style.

### 6.5 Validation UX (fix Boq-style late failure)

| Principle | Implementation |
|-----------|----------------|
| Fail **at the decision**, not after a long edit | Wizard validates name/path uniqueness live |
| Structured errors | `{ code, field, message, suggestion, doc_url }` for CLI, UI, and agents |
| Safe apply | Dry-run / preview file list before write |
| No tribal flags | Unknown fields rejected with “did you mean”; deprecated fields warn with migration |

### 6.6 Review and Change UX (human)

- Default Change view is **project-scoped**; “show entire monorepo diff” is secondary.  
- **Affected / owners / checks** always above the fold.  
- Agent-authored Changes show **agent identity**, model/tooling metadata (if provided), and a one-click “human summary” field.  
- Conflict/base-update flows use plain language (“Your workspace is 12 commits behind trunk; 2 of your files conflict”).

### 6.7 Empty states and education

- Empty monorepo: single CTA **Create your first project** + sample tour.  
- Project without owners: blocking banner with one-click assign.  
- Agent session without affinity: refuse broad write access; prompt to select project(s).  

### 6.8 UX quality bar (process)

- Core flows get **interaction specs** (not only API specs) before implementation.  
- Dogfood metric: time-to-create-project measured in product analytics (self-host: optional).  
- “Manifest complexity budget”: review any proposal that adds a required field to L0/L1.

### 6.9 The closed-trunk moment (human Git UX)

Trunk is closed to direct push (§7.4). For an engineer with ten years of `git push origin main` muscle memory, this is the single most alienating moment in the product — design it, don't just enforce it:

| Moment | UX |
|--------|-----|
| `git push origin main` | Rejected **with a script, not a lecture**: pre-receive message prints the exact next command (`git push origin HEAD:refs/for/main` or `runko change push`), a one-line why, and a docs URL |
| "I just want my branch reviewed" | Any plain Git checkout works: pushing to `refs/for/<trunk>` creates/updates a Change — no workspace, CLI install, or extension required (§11.5) |
| Amend / iterate | Re-pushing the same command updates the same Change via the `Change-Id` trailer (§11.5) — no new-PR-per-push confusion |
| First-week onboarding | `runko doctor` checks remotes/hooks and prints a personal cheat-sheet; the generated repo `CONTRIBUTING.md` shows the three commands that matter |

**Bar:** a competent engineer who has never seen the product lands their first Change from a raw `git clone` guided only by pre-receive messages. If they need a wiki page, this section has failed.

---

## 7. Core concepts

### 7.1 Object model

```text
Organization
  └── Monorepo (exactly one primary per org in v1)
        ├── Project[]              # owned units of code
        ├── OwnerSet / rules       # path → owners
        ├── Template[]             # org golden paths for create
        ├── Workspace[]            # CitC sessions (human or agent)
        ├── Change[]               # proposed work toward trunk
        ├── Actor[]                # users, groups, agent identities
        └── AgentPolicy            # what coding agents may do
```

| Object | Definition |
|--------|------------|
| **Organization** | Tenant boundary for auth, policy, templates, agent policy |
| **Monorepo** | Single Git repository; trunk ref (e.g. `main`) is source of truth |
| **Project** | First-class unit: path root, minimal manifest, effective owners, type, optional capabilities |
| **Template** | Versioned scaffold (files + defaults) used by create; org-customizable |
| **Workspace** | CitC session: base revision + overlay + project affinity + principal (user or agent) |
| **Change** | Reviewable unit; attribution includes human and/or agent actors |
| **Owner** | User or group required to approve paths/projects |
| **Agent identity** | Non-human principal (bot/install/token) with policy binding |
| **Coding session** | Optional link: agent run ↔ workspace ↔ change (for audit) |

### 7.2 Project (capability-oriented, not Boq-monolith)

Projects have a **type** and optional **capabilities** rather than one giant manifest:

| Type | Intent |
|------|--------|
| `library` | Shared code, no deploy surface by default |
| `service` | Long-running backend (deploy capability optional) |
| `app` | User-facing application |
| `job` | Batch/cron worker |
| `other` | Escape hatch |

| Capability (opt-in L2) | Effect |
|------------------------|--------|
| `rpc` | Contract-bearing RPC surface (§13.3.1): proto sources + committed codegen live in-boundary at `capability_config.rpc.path` (default `proto/`); clients declare a `consumes:` edge |
| `http` | Contract-bearing REST surface (§13.3.1): an in-boundary OpenAPI document (`capability_config.http.openapi`, default `openapi.yaml`) is mandatory |
| `deploy` | Attach deploy config template (k8s/chart stub—org defined) |
| `data_store` | Document/store binding placeholders (no forced vendor) |
| `build` | Declare build-graph binding (engine: `bazel` now, `buck2` planned; target patterns default to `//<project-path>/...`) enabling adapter-refined affected (§14.5.4) |

Enabling a capability is a **product action** (`project add-capability rpc`), not “edit 80 lines of YAML.”

### 7.3 Ownership

- Path → owner mapping is authoritative for merge; ownership rules live **in the tree** (OWNERS + PROJECT.yaml) and the control plane indexes them (§10.3).  
- Touched paths in a Change compute **required owners**.  
- Gaps visible; optionally blocking.  
- Break-glass is explicit and audited.  
- **Agents cannot be sole owners** of production paths (policy default); humans remain accountable.

**Cross-cutting changes must stay cheap.** Atomic monorepo-wide change is the headline benefit (§2.1); naive strict ownership taxes it (a rename touching 30 projects → 30 approval sets → the monorepo's flagship feature becomes its most painful flow). Design:

| Mechanism | Behavior |
|-----------|----------|
| **Global approvers** | Org-designated role whose approval satisfies owner requirements repo-wide (Google-style global OWNERS); membership small and audited |
| **Mechanical-change policy** | Change flagged `mechanical` (codemod / rename / format) with tool attestation → per-directory owner requirement relaxes to global approver + spot-check sample |
| **Owner aggregation UX** | One approval satisfies every path that owner covers; reviewers see "your approval covers 14/30 projects"; remaining owners requested in one action |
| **Per-directory triviality rules** | Owners pre-declare patterns (generated files, dep bumps) land-able with global-approver review only |

Agent-driven codemods use the same path — `mechanical` + attestation + human global approver — which is how large agent refactors stay reviewable instead of banned.

### 7.4 Change model

- Trunk-based, short-lived Changes.  
- Fields: base revision, overlay, description, required owners, affected projects, checks, **actors** (human author, agent co-author).  
- **Stable change identity**: a Change has an ID that survives rebases and amends (jj-style change-ID discipline); commits are *versions of* a Change, not the Change itself.  
- **Stacks are first-class in the data model from v1** (`depends_on: change_id`); stack *UX* (restack, cascade land) phases in at v1.x. Retrofitting stacks onto a change model is why PR-based tools struggle — we will not repeat that.  
- **Review conversation is change-scoped state** (§13.4.1): comments/threads and review requests hang off the Change and bind to `head_sha` versions, like approvals — control-plane rows, never tree content.  
- **Landing is rebase-based** (decided, not an RFC): land = rebase onto trunk tip + fast-forward; linear trunk history. Checks bind to `head_sha` and go stale on rebase per §14.4.2, with scoped revalidation per §13.5.  
- **Trunk is closed to direct push** (decided): change refs are the only write path; admin break-glass push is audited and off by default. Without this, every agent policy in §8 is bypassable via raw Git.

### 7.5 Principal model (human + agent)

```text
Principal
  ├── User (human, OIDC)
  ├── Group
  └── AgentIdentity
        ├── auth: token / OAuth app / CI OIDC
        ├── display_name
        ├── policy_id
        └── metadata (tooling vendor, optional)
```

Workspaces and Changes always record **who** (and optionally **which agent session**) performed writes.

---

## 8. Agentic coding support (first-class subsystem)

### 8.1 Design intent

Coding agents are **normal API clients** with stricter defaults:

| Human default | Agent default |
|---------------|---------------|
| May browse broadly in UI | **Read** via project map + search APIs; full-tree wander discouraged |
| Workspace affinity recommended | Workspace affinity **required** for writes |
| Can open advanced manifest editor | Should use **create/update tools**, not freeform multi-file platform config |
| Learns tribal knowledge | Gets **machine-readable map + schemas** |

### 8.2 Orientation API (“monorepo map”)

Before any agent writes code, it should call orientation tools (also useful for humans/CLI):

| Tool / RPC | Returns |
|------------|---------|
| `list_projects` | id, name, type, path, owners summary |
| `get_project` | manifest effective view, capabilities, deps (declared + inferred) |
| `search_code` / `search_symbols` | path hits with project id (not raw multi-GB grep dump); **Zoekt is the default engine, shipped in compose eval** — agent orientation is only as good as indexed search, so this is core infrastructure, not "pluggable someday" |
| `who_owns` | owners for path or project |
| `explain_layout` | short org conventions (from `CONVENTIONS.md` or control-plane doc object) |
| `get_template_catalog` | allowed templates for create |

**Context budget rule:** default responses are **summaries + stable IDs**, with `detail=full` opt-in. Agents should not need the whole monorepo in context.

### 8.3 MCP and tool surface

**Decided (this revision): the CLI is the primary agent interface, not MCP.** Four reasons, in order of weight:

1. **Context economics.** MCP tool schemas load into every client session for the session's lifetime, whether or not a single tool is ever called. A CLI costs zero tokens until actually invoked. This is §8.2's own context-budget rule, applied to ourselves — it would be incoherent to preach "summaries + stable IDs, don't load the whole monorepo into context" while shipping 25 tool schemas as the default agent onboarding cost.
2. **Composability.** An agent that can shell out pipes `runko-ci affected --json | jq '.projects[].name'` in a subshell for ~0 tokens of model context. The MCP equivalent round-trips every intermediate value through the model as a tool-call/tool-result pair — strictly worse for anything beyond a single opaque lookup.
3. **Empirical.** Runko itself — the codebase this spec describes — was built across 13+ implementation sessions by a coding agent using only `make`/`git`/the CLI. Zero MCP calls. If the tool that built this platform didn't need MCP to be productive against it, that's a strong prior about what real agent workflows actually need.
4. **The moat is server-side, not protocol-side.** §8.9's differentiation claim is receive-time enforcement (policy, secret scanning, affinity, affected computation) — none of that cares whether the client spoke MCP, REST, or plain git. Betting the agent story on a specific protocol surface would be optimizing the wrong layer.

**MCP is not deleted — it is rescoped to a thin remote adapter**, for exactly the clients that can't shell out: editor-embedded agents, hosted/sandboxed agents without a git-capable shell, and MCP-registry discoverability. It exposes **seven read-only tools** (six at the stage-12 rescope; `list_change_comments` graduated at stage 16, §13.4.1), each a thin wrapper over the same REST handlers the CLI and web UI use:

```text
# v1 — MCP remote adapter (read-only, seven tools since stage 16)
list_projects, get_project, who_owns, get_affected(paths|change_id),
search_code, get_merge_requirements(change_id)
```

No write tools ship in v1. A remote agent that needs to write pushes via git smart-HTTP like any other client (§11.5's parity rule already requires this path to work from a raw clone) — MCP does not get a privileged write path plain git lacks. This is also the standing answer to §22.1's "MCP surface sprawl" risk: the six-tool adapter is deliberately small, not a growing catalog.

Everything else originally scoped for MCP is **deferred to v1.x, served by the CLI today**:

```text
# v1.x — deferred (CLI-served now; catalog.json keeps these schemas for later)

# Project lifecycle (low ceremony)
create_project(intent)      # `runko project create` today
add_capability(project, cap)
adopt_path_as_project(...)

# Workspace (CitC)
create_workspace(project_ids[], purpose?)
attach_workspace / get_workspace_status
prefetch(project_id|paths)
update_workspace_base()

# Change lifecycle
create_change / update_change_description   # `runko change push` today
get_change / list_change_comments
request_review / land_change (if permitted)

# Validation
validate_project_intent(intent) → structured errors
preview_create_project(intent) → files that would be written
```

**Single-contract rule:** the CLI's `--json` output and the MCP adapter's tool responses conform to the *same* schemas (`docs/spec/mcp-tools/`, `docs/spec/webhooks/`) — one wire contract, two transports. A tool moving from "deferred" to "v1" later is a transport change, not a schema redesign.

**All tools/commands return structured errors** (`code`, `retryable`, `suggestion` — §6.5's shape). Agents should not parse human HTML, and (per the above) should generally prefer the CLI's structured `--json` errors over a round-trip through MCP at all.

### 8.4 Agent-safe workspaces

| Control | Behavior |
|---------|----------|
| **Mandatory affinity** | Agent write paths limited to affinity + explicit allowlist — enforced **at receive** on snapshot/change refs (§12.4) |
| **Path allow/deny policy** | Org can deny e.g. `infra/prod-secrets/**`, `**/PROJECT.yaml` manual edits if desired |
| **Rate / size limits** | Max files touched, max diff bytes per Change for agent principals |
| **Secret pushback** | gitleaks at receive on snapshot/change refs — blocked *before* durability (§11.4, §12.2) |
| **No silent full clone** | Agent install docs: always use platform workspace, never `git clone` monorepo |

Prefetch for agents: template files, project source, direct deps—same as humans, tuned for tool-using loops (fast status, cheap re-read).

### 8.5 Agent project creation (avoid manifest hell)

Agents **must not** be told “write a Boq-style manifest.” Instead:

```text
create_project({
  "name": "checkout-api",
  "type": "service",
  "template": "go-service-v3",
  "owners": ["group:commerce-eng"],
  "capabilities": ["http"]    // optional
})
→ { project_id, path, files_written[], workspace_id? }
```

Platform generates on-disk files. Agent then edits **application code** in the workspace.

If an agent tries to hand-author invalid platform config, validation returns actionable errors; optional mode **rejects direct edits** to generated header sections (see codegen markers).

### 8.6 Attribution, review, and trust

| Concern | Design |
|---------|--------|
| **Co-author** | Change records `authored_by` user and `assisted_by` agent identity |
| **Labels** | Auto-label `agent-assisted` for review filters |
| **Summaries** | Agents encouraged (tool) to set `change.description` and `test_plan`; UI prompts if empty |
| **Higher scrutiny (optional policy)** | Require human owner approval even if agent is in owners group; ban agent self-approval |
| **Agent as reviewer** | Review output = anchored comments with the agent badge (§13.4.1); agents may be requested reviewers; approval stays structurally human-only (§8.7) |
| **Audit** | Coding session id links tool calls → file writes → change |

### 8.7 Policy engine (AgentPolicy)

Org-level defaults (illustrative):

```yaml
agent_policy:
  require_workspace_affinity: true
  require_description: true         # agent changes must carry a §8.6 description to land (below)
  max_changed_files: 40
  max_diff_bytes: 512000
  can_create_projects: true
  can_land_changes: false          # human land by default
  can_modify_owners: false
  can_enable_capabilities: [http, rpc]  # not deploy prod by default
  denylist_paths:
    - "security/**"
    - "**/.github/workflows/**"    # optional
```

Per-agent overrides for trusted internal bots (e.g. renamer bot) vs general coding assistants.

**`require_description` (default on) — a merge gate on §8.6 state.** A coding
agent must say WHAT changed and WHY, so an agent-authored change with an empty
§8.6 description (the blurb set by `runko change describe`, *never* derived
from the commit message — §8.6) is **not mergeable**: `mergeRequirements`
(§13.5) adds a blocker naming the fix, and land/automerge refuse on the same
gate. This is deliberately a **merge-time** gate, not a receive-time one — the
§8.6 blurb is control-plane state set *after* the push, and unlike the size
caps (§13.3) it does not move with the head — so it is evaluated against
`change.Description` when the change is considered for landing, keyed on the
authoring principal. Resolution is **liveness-independent**: an ephemeral
agent's row persists for attribution after its credential's TTL lapses (§7.5),
so its undescribed change stays gated. Humans and the anonymous deploy token
are exempt (an AgentPolicy gate); a bot lane lands under its own policy. This
is the *hard* companion to §8.6's soft "no description yet" prompt: §8.6 offers
the field and nudges; §8.7 makes it mandatory for agents.

### 8.8 Integration patterns

| Pattern | How it works |
|---------|----------------|
| **CLI agents** (Claude Code, etc.) | **Primary path (§8.3 decision)**: the robust `runko`/`runko-ci` CLI, `--json` output, git smart-HTTP for writes. `runko mcp serve` is optional, not required, for a CLI-capable agent |
| **Editor agents** (Cursor, Copilot, etc.) | Remote MCP adapter (§8.3's seven read-only tools) for orientation inside the editor's chrome; writes still go through git smart-HTTP, same as any other client |
| **Autonomous runners** | Agent identity + CI OIDC; always headless workspace on workspace pool |
| **Internal bots** | Same CLI/API surface; tighter path allowlists |

We provide **reference prompts / skill files** (e.g. `AGENTS.md` snippet) generated per monorepo — this doubles as stage 11's (§28.3) done-when bar, teaching the CLI as the primary agent interface:

```markdown
# Monorepo agent instructions (generated)
- Use the `runko`/`runko-ci` CLI (--json output); do not full-clone.
- Raw git is transport only: commit with `runko change create`, submit with `runko change push` — never `git commit`/`git push`; jj is for surgical history work, not the basic loop.
- Prefer `runko project create` over inventing PROJECT.yaml.
- Stay within workspace affinity; use `runko-ci checkout` for deps/prefetch.
- Open a Change before large refactors; respect who_owns.
```

### 8.9 Why this is strategic (and what the moat actually is)

Monorepos without agent support become **hostile to the default way code is written**. Agent support without monorepo structure becomes **unbounded blast radius**. The product sits at the intersection: **structure + locality + tools**.

**Be precise about the moat.** GitHub Agent HQ (2025–) already ships agent identity, mission control, audit trails, allowlists, and an MCP registry — *at repo granularity*. Attribution and audit are commodity. Our durable differentiation is **sub-repo granularity backed by server-side enforcement**:

| Capability | Agent HQ (repo-granular) | Us (project-granular) |
|------------|--------------------------|------------------------|
| Write scope | Whole repo or nothing | Server-enforced workspace affinity per project/path |
| Policy unit | Repo, org | Project, path, capability, diff-size caps |
| Orientation | Repo list, README | Structured project map, owners, deps, conventions |
| Merge gates | Branch protection | Owners × affected × checks per Change, agent-aware |

Every feature in §8 must cash out as *enforcement the assembled stack cannot express at repo granularity* — anything weaker is a feature GitHub ships next quarter.

### 8.10 Dual governance during mirror-first adoption (Agent HQ coexistence)

During adoption stages 0–1 (§18.1) an org runs **two agent-governance planes at once**: Agent HQ (or similar) governs agents acting on the GitHub SoR; we govern platform-mediated work. Don't pretend otherwise — specify the seam:

| Write path (stage 0–1) | Governed by | Our visibility |
|------------------------|-------------|----------------|
| Agent edits via GitHub (Copilot coding agent, PR bots) | Agent HQ / branch protection | Mirror ingests them as externally-authored commits; attribution preserved from commit/PR metadata; labeled `external` on the shadow Change |
| Agent reads via our MCP (orientation, affected, merge requirements) | Our AgentPolicy — **read path works fully at stage 0** | Full session audit |
| Agent writes via our workspaces/Changes | Our AgentPolicy (affinity, caps, owners) | Full |

Rules of the seam:

- **Stage-1 honesty:** we cannot constrain what GitHub-side agents write; we *can* measure it. Owners-coverage and blast-radius reports run on mirror-ingested commits too — the "what would policy have caught" report is the argument for stage 2.  
- **Recommended stage-1 posture:** point coding agents at our MCP for orientation/affected/requirements even while writes flow through GitHub — agent value without migration.  
- **At stage 2 (SoR flip):** the GitHub mirror becomes read-only (branch protection); agents keep *reading* via GitHub if they like, but writes route through our change refs — one governance plane again.

---

## 9. High-level architecture

### 9.1 Logical components

```text
┌──────────────────────────────────────────────────────────────────────────┐
│  Clients                                                                  │
│  - Web UI (human-first flows, empty states, review)                       │
│  - CLI                                                                    │
│  - Editor extension                                                       │
│  - MCP server (agent-first tool surface)                                  │
│  - REST/gRPC (same capabilities as MCP)                                   │
└────────────────────────────────┬─────────────────────────────────────────┘
                                 │
┌────────────────────────────────▼─────────────────────────────────────────┐
│  Control plane                                                            │
│  - AuthN/Z (OIDC users + agent identities)                                │
│  - Templates, project intents, progressive config                         │
│  - Owners, policy, AgentPolicy                                            │
│  - Changes, review, merge gates, attribution                              │
│  - Workspace registry                                                     │
│  - Webhooks / Checks / affected APIs (CI integration plane)               │
│  - Mirror & import service (bidirectional GitHub/GitLab sync, §18)        │
│  - Validation & preview services                                          │
│  - CI connection config (OIDC trust, required checks, webhook endpoints)  │
└───────────────┬───────────────────────────────┬──────────────────────────┘
                │                               │
┌───────────────▼──────────────┐  ┌─────────────▼──────────────────────────┐
│  Workspace services (thin)   │  │  MonorepoStore (Git impl)              │
│  - sparse patterns + prefetch│  │  - bare repo + smart-HTTP + our hooks  │
│    hints (project graph)     │  │    (size, LFS, secrets, policy)        │
│  - receive-time policy       │  │  - refs, commits, objects              │
│    (affinity, caps, secrets) │  │  - workspace snapshot refs (§12.2)     │
│  - snapshot-ref lifecycle/GC │  │  - optional Josh proxy (slices)        │
└───────────────┬──────────────┘  └────────────────────────────────────────┘
                │
┌───────────────▼──────────────┐
│  Workspace glue (client CLI) │
│  - configures upstream Git:  │
│    partial+sparse+worktree   │
│  - snapshot push (Git refs)  │
│  - advisory path checks      │
│  - remote VMs external via   │
│    env contract (Coder tmpl) │
└──────────────────────────────┘
```

### 9.2 Data stores (self-host friendly)

| Store | Role |
|-------|------|
| **PostgreSQL** | Workflow state (changes, review, workspaces, agents, policies, templates), mirror cursors/ref-maps (§18.6) + **rebuildable index** of tree-resident structure (projects, owners — §10.3) |
| **Object storage (S3 API / MinIO)** | Template artifacts, import staging, webhook DLQ payloads — **no file content**; Git is the only durable content store (§12.1) |
| **Git storage** | Canonical monorepo objects and refs — including change refs and workspace snapshot refs (`refs/workspaces/*`, §12.2) |
| **Redis (optional)** | Sessions, job queues, rate limits for agent traffic |

### 9.3 Deployment shapes

| Profile | Composition |
|---------|-------------|
| **Eval / dev** | `docker compose`: API, web, MCP, Postgres, MinIO, Git volume, agent |
| **Team self-host** | Compose or small k8s; OIDC; agent tokens |
| **Company self-host** | Helm HA; remote dev/agent VMs run on the org's own platform via environment contract (Coder/devcontainer templates, §12.3) — we don't operate VM fleets |
| **Managed cloud** | Same binaries; per-tenant isolation |

**Invariant:** SaaS and self-host run the **same core software**, including MCP.

### 9.4 Kubernetes and cloud alignment

Two distinct audiences; keep them separate:

**Running Runko on k8s** (self-host operators). The architecture maps cleanly by construction: one daemon binary + all durable state external (Postgres → CNPG-class operators, git PVC, S3) = Deployment + operator CRs + PVC; process-boundary adoptions (Zoekt, gitleaks) = sidecars/containers; tree-as-truth + rebuildable index (§10.3) = level-triggered reconciliation semantics — Runko is effectively an operator whose CRD is the git tree. Known constraint: git storage is RWO single-writer → one receive/land replica (`strategy: Recreate`); the "Company self-host HA" row eventually needs a leader-election or single-writer/read-replica story. Cheap conventions adopted for stage 14: env-var fallbacks for all serve flags (`RUNKO_*`), `/healthz` + `/readyz` + graceful shutdown, `/metrics` (Prometheus). **Guard for any future operator/CRDs: CRDs and Helm own infrastructure shape (replicas, storage, DB refs); the tree owns policy** (required checks, owners, AgentPolicy, root-invalidation). An operator that owns `required_checks` in a CR is the Gerrit-ReviewDb mistake wearing a k8s costume (§10.3, §22.2).

**Runko as the customer's GitOps source of record** (their k8s/cloud deployments reading from us): see §14.10.1–14.10.3 — vanilla-git read side, affected-scoped CD, mirror-first CD continuity, bot lanes for GitOps writers, tag governance.

---

## 10. Streamlined project configuration (detailed design)

### 10.1 Intent → files pipeline

```text
CreateProjectIntent
  name, type, language?, template_id?, path?, owners[]?, capabilities[]?, no_template?, api? (required for services, §13.3.1)
        │
        ▼
ValidateIntent (uniqueness, naming policy, quota)
        │
        ▼
ResolveTemplate (org defaults + template version)
        │
        ▼
Plan (file list + effective PROJECT.yaml + owners)
        │
        ▼
Apply (single Git commit or workspace overlay) + index control plane
        │
        ▼
Optional: create Workspace with affinity + return to caller
```

**Preview** is a first-class step in UI and `preview_create_project` tool.

### 10.2 What is generated vs inferred vs manual

| Kind | Examples | Who maintains |
|------|----------|---------------|
| **Generated once** | stub main, README, minimal PROJECT.yaml, test skeleton | Platform on create; user/agent edits app code after |
| **Generated on capability** | proto files, HTTP router stub | Platform on `add_capability` |
| **Inferred continuously** | import-based deps, languages used | Indexer; shown in UI; optional promote to declared |
| **Manual rare** | exotic overrides, advanced flags | Power editor only |

### 10.3 Source of truth and drift (decided: the tree is truth)

- Prefer **one apply API** over “edit four files consistently.”  
- Codegen regions marked with `BEGIN GENERATED` / `END GENERATED` where needed; agents instructed not to hand-edit those spans.  
- **The tree is the source of truth for durable org structure** (`PROJECT.yaml`, OWNERS). The control plane is a **rebuildable index of trunk** — never an independent store. Writes still flow through the intent API, but the API's output *is a commit*; the index updates by observing trunk.

Why inverted (vs. "DB is truth, manifest is projection"):

| DB-as-truth failure mode | Tree-as-truth property |
|--------------------------|------------------------|
| `git clone` backup loses org structure | Any clone/mirror carries projects + owners with it |
| DR requires Postgres + Git restored in lockstep | DR = restore Git, reindex |
| Mirrors/forks silently diverge from the registry | Structure travels with the repo |
| Self-host operators babysit two truth stores | Index rebuild is one maintenance command |

This is Gerrit's hard-won **NoteDb** lesson (review metadata migrated *out of SQL, into Git*) applied on day one. Ephemeral and derived state — inferred deps, workspace registry, check runs, sessions — stays in Postgres: it is cache and history, not identity.

### 10.4 Org templates (golden paths)

Admins define templates (e.g. `go-service`, `typescript-lib`) with:

- file skeletons  
- default capabilities  
- recommended owners patterns  
- CI path hints for export  

Creating a project **never** requires understanding template implementation—only picking one.

**Built-in templates (decided 2026-07-08):** the platform ships a `<type>-<lang>` matrix — five types × six languages (`go`, `python`, `ts`, `rust`, `java`, `cpp`), languages admitted strictly by Bazel-rule maturity (rules_java/rules_cc are Bazel-core, rules_python is first-party, rules_rust lives in the bazelbuild org, rules_ts is Aspect's); `js` deliberately misses the cut its own criterion sets (weakest Bazel story of the pool; most new JS is TS) and flows through `no_template` until a later batch. Built-ins are **source-skeleton-only**: no go.mod/Cargo.toml/package.json/tsconfig — toolchain config is an org-template concern, the same split as §14.5.4's language BUILD rules; every built-in defaults the `build` capability on (the generated BUILD.bazel filegroup is language-agnostic). The old `<type>-default` ids remain as aliases for the Go set. The `language` intent field is optional (absent → Go templates) and echoed into PROJECT.yaml **verbatim, never default-filled** — `language:` absent on disk means unspecified. An unsupported language without `no_template` is a structured `unsupported_language` error naming the supported set and the escape hatch; with `no_template`, create emits PROJECT.yaml + README only and records the language as-is.

### 10.5 Comparison: Boq pain vs our create path

| Boq-like experience | Our experience |
|---------------------|----------------|
| Read internal wiki, copy manifest | Pick type + name + team |
| Fill 20+ fields “just in case” | 0 advanced fields required |
| Wrong field → cryptic presubmit | Live validation + suggestions |
| Agent dumps invalid YAML | Agent calls `create_project` intent API |
| Fear of creating services | Create is cheap and reversible (archive project) |

---

## 11. Git as the underlying VCS

### 11.1 Decision

**Use Git as the monorepo substrate for v1 and the foreseeable medium-org horizon.**

Rationale: sufficient at target scale with CitC; best migration and ecosystem story; differentiation is UX/workspaces/agents above Git.

### 11.2 How Git is used

| Concern | Approach |
|---------|----------|
| Source of truth | Single Git monorepo; trunk ref; tree-resident manifests/owners (§10.3) |
| Daily driver (human/agent) | Workspace + platform APIs—not full clone |
| Write path | **Change refs only; direct trunk push disabled by default** (§7.4); break-glass = audited admin action |
| Escape hatch | Standard Git remote stays **readable** — clone/fetch always works; writes go through Changes |
| Interop | Optional mirror to GitHub/GitLab; **mirror-first adoption** supported (§18) |
| Changes | Managed refs under a namespace |

### 11.3 MonorepoStore interface

```text
MonorepoStore
  ResolveRef(name) → Revision
  GetTree(rev, path) → [TreeEntry]
  GetBlob(rev, path) → Blob
  CommitOverlay(base, overlay, meta) → Revision
  UpdateRef(name, rev, expected?) → error
  ListHistory(path, opts) → ...
```

v1: Git, full stop — workspace snapshots are refs (§12.2); there is no side content store. Future backend swap only if required.

### 11.4 Repository policy

- Max blob size; controlled LFS exceptions  
- Generated artifacts gated  
- Secret scanning on receive and on agent overlay push — **integrate gitleaks/trufflehog; do not build bespoke heuristics** (GitHub already exposes secret scanning to agents via MCP — parity is table stakes)  
- Size quotas and alerts  
- **Tag-namespace governance** (decided 2026-07-10, §14.10.3; implementation = stage 17): `refs/tags/*` writes gated on the org release role or a tag-namespace-scoped bot lane; release-flow tags are server-created through the same policy. Until stage 17 lands, the current unconditional accept remains the documented permissiveness — matters because customer CD keys deploys on tags  

### 11.5 Client write workflow: how commits become Changes

Three write paths, one server-side funnel (all end at change refs; §7.4):

| Path | Who | Mechanics |
|------|-----|-----------|
| **Plain Git (magic ref)** | Any engineer, any tooling, zero install | `git push origin HEAD:refs/for/main` → server creates a Change, or updates one matched via the `Change-Id` commit trailer (Gerrit-proven; compatible with §7.4's jj-style change IDs). A commit-msg hook — installed by `runko doctor` or printed by pre-receive — adds the trailer; pushes without one create a fresh Change |
| **CLI** | Daily driver | `runko change push` wraps the magic-ref push: ensures the trailer, prints Change URL + merge requirements |
| **Workspace overlay** | Workspace users, agents | `create_change` snapshots the overlay server-side; no local Git objects needed |

Server funnel, identical for all three: receive → policy (§8.7 for agents) → secret scan → Change create/update → affected compute → webhooks.

**Parity rule:** anything expressible via workspace tools must be expressible from a raw clone. The plain-Git path is a **contract, not a fallback** — §6.9 sets its UX bar, and Phase 1 ships it as the primary client story (§19.2).

---

## 12. Workspaces: CitC-class experience, upstream-Git implementation

### 12.1 Contract

A **Workspace** provides:

1. View of the monorepo at a **base revision**  
2. **Overlay** of edits (local working tree; durable as snapshot commits)  
3. **Lazy materialization** (affinity / access / dirty)  
4. **Cloud-primary** workspace identity and snapshots  
5. POSIX-usable view for the chosen mode  
6. **Principal-aware policy** (human vs coding agent limits)

Public framing: *“The whole monorepo is visible; almost nothing is downloaded; your change is a small overlay—whether you are a human or an agent.”*

**Implementation stance (decided): no custom storage plane.** There is no CAS, no overlay store, no bespoke sync protocol. A workspace is **upstream Git, configured** (partial clone + sparse cone + worktree + fsmonitor), plus **durable state as Git refs**: snapshots are real commits pushed to `refs/workspaces/<id>/…` through the same receive funnel as Changes (§11.5). This closes the loop on tree-as-truth (§10.3): **Git is the only durable content store**; Postgres never holds file content. EdenFS-class virtual overlays are multi-year OS work with no supported OSS to adopt (§21.2) — and at our envelope (§4.4) they buy nothing this glue doesn't.

**Delta over plain Scalar — be explicit, since upstream Git already does partial + sparse + fsmonitor:**

| Scalar gives you (client config) | We add (control plane + glue, not storage) |
|----------------------------------|----------------------------------|
| Partial clone + sparse cone + background maintenance | **Cloud-primary identity**: snapshot refs survive laptop loss; attach from anywhere |
| — | **Server-side policy at receive**: principal-aware write allowlists, agent caps, secret scan — one funnel for snapshots and Changes |
| — | **Workspace ↔ Change ↔ coding-session linkage** for review and audit |
| — | **Affinity-driven prefetch**: sparse patterns + fetch hints computed from the project graph |

If a proposed workspace feature does not land in the right-hand column, it belongs in upstream Git configuration, not in our codebase (§5, principle 12).

### 12.2 Data model

```text
Workspace {
  id, org_id, monorepo_id
  principal                 // user_id | agent_identity_id
  coding_session_id?        // optional audit link
  base_revision
  project_affinity[]
  write_allowlist[]         // computed from affinity + policy
  snapshot_ref              // refs/workspaces/<id>/head — a real commit chain
  mode                      // sparse_local | remote_vm (external) | josh_slice
  status
}
```

**Invariant:** durable cost ≈ snapshot commits (Git objects, deduped by Git itself) + local working copy; the registry row is metadata only.

**Snapshot-ref lifecycle:** snapshots are commits on `refs/workspaces/<id>/head` (amend-by-default), pushed through the §11.5 receive funnel — policy and secret scan apply *before* durability. Retention: workspace refs are short-lived by policy (default 30 days after workspace close), then GC'd. **Secret purge** is a runbook (delete ref → expire reflog → prune) — harder than deleting a blob from a private store, which is exactly why the scan runs at receive (§11.4).

**Workspace branches (decided 2026-07-08):** one workspace supports N parallel lines of work as sibling snapshot refs — `refs/workspaces/<id>/<branch>`, where `head` is the default branch every workspace starts with. Branch names use the same conservative charset as workspace ids (single path segment; enforced at receive, so `refs/workspaces/x/a/b` is rejected, never ambiguous). Branch *existence is derived from refs at read time* — the registry stays metadata-only per the invariant above, exactly as project existence derives from trunk (§10.3); there is no "create branch" control-plane verb. Parallel work is attach-per-branch: each branch materializes into its own worktree (local branch `ws/<id>/<branch>`, so two attaches of one workspace coexist), and every branch ref passes the identical funnel treatment (owner-only, caps, scan) since enforcement keys on the workspace id. Stacking and branching compose: a branch is where a stack is *built*; the stack's Changes still land one by one through §13.5.

**Branch ↔ stack provenance (decided 2026-07-08):** the expected shape is one workspace branch ↔ one stack (a branch is a linear line of commits; pushed, those commits are exactly one stack under the derived `B.base_sha == A.head_sha` relation) — and this mapping is *recorded provenance, never an identity constraint*. `runko change push` from an attached worktree stamps its own `runko.workspace`/`runko.branch` git config onto the push as push options (`workspace=<id>`, `workspace-branch=<name>`); the receive funnel validates the claim against the registry (an unregistered workspace or someone else's workspace is a loud rejection — a Change pinned to the wrong stack in every view is worse than no provenance) and records `origin_workspace`/`origin_branch` on the Change. **Revised 2026-07-09 — changes are born in workspaces:** the "recorded provenance, never an identity constraint" half of this decision is superseded. A refs/for push must now declare a (validated, owner-bound) workspace origin by default — workspaceless Changes turned out to be an anti-pattern in practice: they dodge affinity, snapshots, and the branch↔stack mapping everything downstream renders. The enforcement is structural, not principal-based; the one exemption is a brand-new monorepo's bootstrap/import push (unborn trunk — workspaces need a base revision, so requiring one first would deadlock every new org), plus the loud eval-profile opt-out `--allow-workspaceless-changes` (§16.4's loop must work from a bare clone). Plain git remains a first-class client via `git push -o workspace=<id>` (§11.5 parity is about the tool, not about skipping the model); the web create-project flow's server-authored scaffold Change is not a push and is unaffected. What stays from the original decision: stack relations remain *derived* (§7.4), and an amend carrying no options still preserves the recorded origin rather than erasing it. Surfaced everywhere the mapping should be obvious: the changes inbox names each stack's home branch, the workspace list shows each branch's in-flight stack, and the Change page carries the origin chip.

**Snapshot hygiene — the bytes that actually dominate:** build artifacts and dependency trees (`node_modules/`, `target/`, `.venv/`, bazel outputs) must **never** enter snapshot commits. Exclusion = `.gitignore` (snapshots are Git commits, so this is free) + template defaults + receive-time size caps as backstop. Conflict semantics: single-writer per workspace by default; concurrent attach is explicit (`--shared`) with snapshot-on-conflict — never silent merge. Offline: plain Git — commit locally, push snapshots on reconnect; base updates refused while detached.

### 12.3 Implementation phases

#### Phase A — Git-glue workspaces (the only thing we build)

| Mechanism | Behavior |
|-----------|----------|
| Content | Partial/blobless Git + **sparse** affinity roots (+ worktree per workspace) |
| Durability | **Snapshot commits to `refs/workspaces/<id>/head`** (explicit save + `workspace watch` cadence + auto on `change push` — §12.6); continuous streaming *sync* still deferred (§12.6 explains why the live view doesn't reopen it) |
| Attach | `runko workspace attach` configures clone/sparse/hooks — laptop or any remote VM |
| Agents | Same path; headless VM via environment contract (below) for autonomous agents |
| Sync base | `workspace update-base` = fetch + rebase with conflict UX |

Delivery mapping: **DAG stage 12b** (§28.3 — restored after review caught it silently dropped from the 2026-07-06 DAG revision; §19.2's "Phase-1 stretch" framing maps there). Receive-time policy for workspace refs completes with it. **Continuous streaming sync is deliberately deferred**: snapshot semantics are easier to reason about, cheaper to run, and already satisfy the durability/audit contract. Stream only if real usage shows snapshot loss windows hurt.

**Multiple workstreams = multiple worktrees.** The `+ worktree` above is the answer to "how do I own N workspaces": **one blobless clone (object store paid once), N git worktrees — each worktree is one workspace is one workstream**, with its own sparse cone, base revision, snapshot ref, and registry row. Switching workstreams is `cd`, not a stash-and-branch dance in a dirty tree:

```text
~/src/mono/           # shared blobless object store
~/ws/payments-fix/    # workspace 1: cone = checkout-api + money
~/ws/risk-refactor/   # workspace 2: cone = risk-engine
~/ws/big-rename/      # workspace 3: broad cone, mechanical-change flag (§7.3)
```

Interim reality (until 12b): plain clones + the magic ref already support N concurrent Changes (each commit chain carries its own `Change-Id`); what 12b adds is the CitC sugar — affinity cones, snapshot durability/attach-from-anywhere, the registry, and **receive-side enforcement of `refs/workspaces/<id>/*`** (owner-only push, caps, scan — currently these refs pass the funnel unconditionally, same v1 permissiveness class as tags, §14.10.3).

**Remote / agent VMs: external by contract.** We do not build or operate a VM/workspace-pool product — that is Coder / devcontainers / Codespaces territory, and the reasoning is identical to CI runners (§14.1). We ship an **environment contract** (image must provide: git ≥ 2.38, `runko` CLI, credential helper, MCP endpoint config) plus reference **Coder template + `devcontainer.json`** under `integrations/templates/workspaces/`.

#### Phase B — Josh slices (optional capability; adopted, not built)

**Josh-proxy** (§21.2) serves *filtered* remotes of the monorepo with push-back mapping. We integrate it as an **org-enabled optional capability — not the default path** — because Josh views carry **rewritten SHAs**, while everything in §13–§14 (Changes, Checks, `head_sha`, `runko-ci`) keys on monorepo-true SHAs. Where the SHA indirection earns its cost:

| Use case | Why Josh beats the default glue |
|----------|----------------------------------|
| **`visibility: restricted` projects (§15.2)** | A per-principal filtered remote is *real* Git-layer read enforcement — the only mechanism that survives a hostile client |
| **Slice-as-repo ergonomics** | A team wants `checkout-api` as its own small repo (IDE indexes just the slice, full slice history) while pushes map back to trunk |
| **Import/consolidation sync (§18.3)** | Bidirectional repo ↔ monorepo-path sync during migration windows (the Rust project's `josh-sync` precedent) |

#### Phase C — Graph-aware prefetch

Project deps, agent tool-driven prefetch, optional build-graph hints.

#### Virtual FS: adopt-only, likely never

Microsoft built VFS for Git and then **abandoned virtualization** for sparse + partial + fsmonitor (Scalar); Meta's EdenFS remains publicly unsupported. A FUSE/ProjFS layer is multi-year OS-adjacent work with no supported OSS to adopt — and at the ≤ ~20 GB envelope (§4.4) it buys nothing Phases A–C don't. Standing decision: **we never build a virtual FS.** If ≥ 3 real orgs hit storage-mechanics limits despite A–C, we *adopt* (jj's VFS direction, EdenFS if it ever gains public support, Josh full views) — an evaluation trigger, not a roadmap item.

### 12.4 Workspace protocol (mostly: Git)

There is no bespoke sync protocol. The wire surface is:

```text
Standard Git smart-HTTP: fetch (partial/sparse), push (snapshot + change refs)
Sidecar REST (thin):     GET  /workspaces/{id}             — registry state
                         GET  /sparse-patterns?projects=…  — cone patterns from graph (§14.4.4)
                         POST /workspaces/{id}/snapshot    — server-side commit for gitless agents (§11.5)
Client-local (advisory): path checks against write_allowlist for fast feedback
```

Enforcement is **at receive** (authoritative) plus advisory client checks (speed). The `EnforceWrite`-style RPC dies with the custom plane.

### 12.5 Hard problems

| Problem | Mitigation |
|---------|------------|
| IDE/agent stat storms | Sparse cone keeps the tree small; fsmonitor daemon; concrete hot roots |
| Watchers | Watch materialized roots; poll fallback |
| Agent runaway writes | Receive-time caps + allowlist denial (authoritative); advisory client checks (fast) |
| Base drift | First-class update-base UX + tool |
| Secret lands in a snapshot ref | Scan **at receive**, before durability (§11.4); purge runbook = ref delete + reflog expire + prune (§12.2) |
| Workspace-ref proliferation | Namespaced refs + retention policy (§12.2); reftable at scale; snapshots amend by default |
| Sparse/partial sharp edges (LFS interplay, cross-cone checkout) | The glue CLI exists to paper exactly these; published compat matrix |

### 12.6 Workspace observability (decided 2026-07-11)

Agents do the default share of writing now (§2.4), yet everything the platform knows about their work it learns **at push** — the in-workspace interval is a blind spot exactly where §12.1's "workspace ↔ Change ↔ coding-session linkage" promised visibility. This section makes that interval observable — a live per-branch view of WIP plus an activity timeline — without touching the §12.1 substrate decision. Three parts, all riding rails this spec already laid:

**Cadence — `runko workspace watch`, not streaming.** A client loop snapshots the working tree when (and only when) it is dirty: check every ~15s (hash of `git status --porcelain` output; git's own `core.fsmonitor` accelerates status when configured — §12.5's poll-fallback stance, we build no watcher daemon), push at most once per 15s and at least once per 60s while continuously dirty. Plus the auto-snapshot §12.3 promised: `change push` snapshots best-effort before pushing (`--no-snapshot` opts out), parking the ref at the submitted tip. Every push is an ordinary snapshot commit through the §11.5 funnel — policy, caps, and secret scan before durability, identical to a manual `workspace snapshot`. Streaming is also the golden path, not just a capability (decided 2026-07-14): `workspace create`/`attach` print the exact next commands (start `watch`, wire the §12.6.1 hooks) at the moment the worktree is born — §6.9's script pattern — and the funnel answers a workspace's first accepted change push that never streamed (no prior `change_pushed`, at most one `snapshot_pushed` — the push's own auto-snapshot — and zero §12.6.1 activity rows) with a one-time advisory `remote:` block naming both verbs: fail-open on store errors, never blocks, never repeats.

*Mechanics constraint (hard): the watcher must never mutate the working checkout.* The interactive snapshot verb commits on the checked-out branch (amend-by-default, §12.2); a background loop doing that would race the user's or agent's own git and jj operations — index-lock contention, `change create` finding a clean tree, commits behind jj's back in a colocated repo. The watch loop therefore builds its commit **out-of-band**: temporary index (`GIT_INDEX_FILE`) → `write-tree` → `commit-tree -p HEAD` → push the sha to `refs/workspaces/<id>/<branch>`. HEAD, the real index, and the worktree are untouched; the ref keeps its amend-at-the-tip semantics (§12.2).

**Why this does not reopen §12.3's streaming deferral.** That deferral is about a write-side *sync protocol* — continuous content streaming as the durability mechanism — and it stands. Durability here is still discrete snapshot commits through the policed funnel; `watch` only raises the cadence of a verb that already exists. The read side (below) is a derived, ephemeral notification channel over state Git and Postgres already hold; a dropped stream loses nothing that reconnect-and-refetch doesn't restore.

**Timeline — stats-only rows recorded at receive.** Snapshots amend by default, so their history is *deliberately unreachable* in Git (§12.2 retention). The activity timeline is therefore written where the platform already sees every event: the receive/land paths record `workspace_events` rows — type (`snapshot_pushed`, `change_pushed`, `change_landed`, `change_abandoned`, `workspace_closed`), workspace + branch, actor, sha, change key, `--numstat` totals (files, +/− lines), timestamp. **Never file content** (§12.1); these rows are §10.3's "cache and history, not identity". Retention: capped per workspace (default 500, pruned at insert), deleted with the workspace, kept on close — the page outlives the task's last land. No outbox webhook ships with this (a `workspace.snapshot` event type is explicitly deferred until an external consumer exists; the live feed below is in-process).

**Live feed — one in-process bus, one streaming RPC.** Each org's daemon carries an in-process event bus: the receive/land paths publish, `WorkspaceService.WatchWorkspace` — the surface's first server-streaming RPC (§17.4) — subscribes per workspace. Frames are *pokes, not data*: a client refetches diff and timeline via the unary RPCs (`GetWorkspaceDiff` = snapshot tip vs `base_revision`, `ListWorkspaceEvents`) on every frame and on every (re)connect, so delivery may be lossy-with-coalescing — a slow subscriber can never block a push, and the guarantee is "at least one poke after the last event", not a log. Keepalive frames (~25s) hold middleboxes open. Workspace RPCs stay authenticated (never on the §15.2 public-read allowlist). Stated assumption: the bus sees only its own process's receives — correct for the current deployment shape (one daemon over one bare repo); an HA future needs a fan-out, or leans on reconnect-refetch, which degrades to exactly polling.

#### 12.6.1 Agent session activity (decided 2026-07-11)

§12.6 made the in-workspace interval observable *at snapshot granularity* — but a snapshot still only shows what changed. What the agent is **doing** — which files it reads, which commands it runs, which file it is editing right now, before any push — exists only inside the agent's harness, on the agent's machine. The platform cannot derive it; the harness must **report** it. This subsection adds that channel, and it materializes §7.2's promised "Coding session" link (agent run ↔ workspace, for audit).

**Trust class — client-claimed, observability-only.** Every other fact in this spec is server-derived (receive-time numstat, land results, tree scans). Activity events are the platform's first *client-claimed* rows: an agent can misreport or go silent, and the server cannot tell. The consequence is structural, not advisory: activity **never feeds policy, gates, or affected computation** — it is rendered for humans and nothing else consumes it. This is §8.7's "never trust client-claimed affinity" applied in reverse: where the claim IS the data, the data may only power a dashboard. (An agent that lies produces a wrong dashboard, attributed to its own principal — §8.6's attribution still holds because the ingest path authenticates like any write.)

**Ingest — one REST endpoint, batched, owner-only.** `POST /api/workspaces/{id}/activity` accepts `{"events": [{kind, detail, session_id?}]}`, ≤100 events per request. Authentication and ownership mirror snapshot pushes (§12.2): only the workspace owner (or an operator) may report into a workspace, a closed workspace refuses, and the actor is taken from the authenticated principal — never from the body. `kind` is a deliberately soft vocabulary — `read | edit | command | search | note` — and an unknown kind coerces to `note` rather than rejecting: telemetry must never fail the work it describes, and harness tool inventories change faster than this spec. `detail` is metadata (a path, a command line), server-truncated to 240 runes. **Content policy:** command lines carry secrets in the wild, so every batch passes the same wired secret scanner as the §11.5 funnel — a finding redacts that event's detail to `[redacted: <rule-id>]`; a scanner *error* redacts the whole batch (fail-closed, the funnel's posture: a broken scanner becomes visibly redacted telemetry, never a stored secret). Nothing here stores file content (§12.1 stands).

**Storage — `workspace_activity`, deliberately not `workspace_events`.** Two reasons the timeline table is wrong for this: trust class (receive-time facts must not be diluted with client claims) and volume (an agent emits tool calls at hertz; the timeline's 500-row cap would evict a task's whole land history in an hour of chatter). Activity rows get their own table — id, workspace, actor, kind, detail, `session_id` (the coding-session link; harnesses pass their own session identifier), timestamp — capped per workspace (default 1000, pruned at insert), deleted with the workspace, kept on close. Same §10.3 class as the timeline: cache and history, never identity.

**Live delivery — the §12.6 rails, unchanged.** An accepted batch publishes one `agent_activity` poke to the org bus (a bus-only event type: never a stored `workspace_events` row). `WatchWorkspace` frames stay pokes; clients refetch through the new unary `ListWorkspaceActivity` (authenticated, like the whole workspace surface). The lossy-with-coalescing guarantee is exactly right for a ticker: bursts coalesce, reconnect refetches, nothing blocks the ingest path. **At a glance:** `WorkspaceSummary` carries `latest_activity` — the newest row, served straight from storage (no separate presence store) — so the workspaces list can render "now: ✎ `runkod/api.go` · 12s" per workspace while fresh, and the workspace page renders the same as a header presence line.

**Client — hooks report, the CLI is the reporter.** `runko agent event` posts one event per invocation; `--from-hook` reads a harness's post-tool-use JSON from stdin and maps tool→kind itself (Read→`read`, Edit/Write→`edit`, Bash→`command`, Grep/Glob→`search`, else `note`), extracting the file path or command line as detail — no external parser dependency. `runko agent hooks` prints a ready-to-paste harness hooks snippet. Credentials: verb-local `RUNKO_RUNKOD_URL`/`RUNKO_TOKEN` env fallback (flags > env > stored login) because hooks inherit an environment, not flags. One POST per tool call is the v1 cadence — the endpoint's batch body means a future client-side spooler is an API no-op, not a new contract. Reporting is per-harness by nature: observing reads/commands generically would take strace/eBPF/FUSE-class machinery, which §12.3 already rejects; self-reporting through hooks is the whole v1 story. `runko agent hooks --install` (decided 2026-07-14) merges that snippet into the worktree's Claude Code `.claude/settings.local.json` — one explicit command per worktree, preserving the 2026-07-11 posture that client-specific config is never written automatically: the merge keeps foreign keys, no-ops when the hook is already wired, and refuses an unparseable file rather than rewriting it (the verb-nudge non-clobber posture); the file is excluded from snapshots via the shared clone's `info/exclude` (worktrees share it by git's layout, and snapshot staging honors excludes), and the installer prints the exact `RUNKO_RUNKOD_URL`/`RUNKO_TOKEN` exports when no credential resolves — the env prerequisite itself is unchanged. On the read side, the activity card filters client-side by kind — chips with per-kind counts, all-on by default, selection persisted per browser — with a kind→glyph map (✎ edit et al.) shared with the presence line; unknown kinds still render as `note`.

---

## 13. Control plane: changes, review, affected

### 13.1 Project lifecycle (UX-first)

```text
intent (UI / CLI / MCP)
  → preview
  → apply template + minimal manifest
  → register project + owners
  → optional workspace
  → optional add_capability loops
```

### 13.2 Change lifecycle

```text
workspace edits (human and/or agent)
  → change create
  → affected projects + required owners
  → review (human owners; agent cannot self-approve by default)
  → external CI checks on affected set
  → land (default: human-permitted)
```

### 13.3 Affected computation (v1)

1. Paths → Projects (longest prefix)  
2. **Declared** dependents (transitive) + root-invalidation rules (tooling/root paths ⇒ `run_everything`)  
3. Export to CI  

**Decided (was an open question):** inferred dependencies are **advisory-only in v1** — surfaced in the UI as "suggested dependency: promote to declared?", never feeding merge gates. Import-based inference is a per-language, multi-year surface (it is Pants' core competency), and a stale async indexer feeding merge gates is a correctness hazard. Gate on facts (paths, declared edges); suggest from inference; fail closed to `run_everything`.

**Build graphs are a third trust class** (promoted from Tier-3; see §14.5.4): BUILD/BUCK files are *declared* facts, evaluated hermetically at the exact `head_sha` — categorically unlike async language-import inference. A **synchronous** build-graph query may therefore *refine* affected — always for CI scoping, and (org opt-in) for gate-grade check-set scoping — failing closed to `run_everything` on any query error, timeout, or version skew. The platform's own computation remains paths + declared project deps: correct with **no build system installed** (NG4 — a build graph sharpens affected; it is never required for it).

#### 13.3.1 API contracts live in the owning project (decided 2026-07-14)

**The problem, found live** (migration finding #47): `mailer/` drains runkod's invite-request feed over REST, but its manifest declares no dependencies — the coupling exists only as a copy-pasted struct pinned by an httptest stub. The dependency graph is blind to a real runtime edge: a runkod change that reshapes the feed never puts `mailer-test` in its affected closure. The cause is structural, twice over: `proto/` is a standalone project, so a contract is a *place* rather than a property of the project that serves it — and a REST surface has no tree-resident contract artifact at all, so there is nothing for the graph to see.

Decided, three legs:

1. **In-boundary contracts.** A project that serves an API owns its contract inside its own boundary. The §7.2 `rpc` capability gains its real shape: `capability_config.rpc: {path: <dir>}` (default `proto`) — proto sources at `<project>/<dir>/`, generated Go committed at `<project>/<dir>/gen/` (the `internal/dbgen` convention), each contract dir carrying its own `buf.gen.yaml`. The `http` capability gains the REST dual: `capability_config.http: {openapi: <file>}` (default `openapi.yaml`) — a project declaring a REST surface MUST carry an OpenAPI document in-boundary, and receive refuses a push that declares `http` without the document (or deletes it out from under the declaration). The standalone `proto/` project becomes **transitional**: it declares `rpc: {path: .}` so the enforcement below covers it uniformly, its `runko/v1` surface migrates into `runkod/`'s boundary as a follow-up mechanical change (web's declared edge flips `proto` → `runkod`), and no new contract lands there.

2. **Consumption is a server/client edge (`consumes:`), enforced at receive.** If project A is a client of project B's contract, A declares B in `consumes:` — a first-class edge distinct from `dependencies:` (build-grade: "I build against B's code"), and the two closures differ deliberately. A `dependencies:` dependent rides B's *every* change; a `consumes:` client joins the affected closure **only when B's contract surface changes** (the rpc `path` dir, the OpenAPI document, or B's manifest itself — fail closed, the surface may have moved). That is the honest shape at project granularity: a stub-pinned client cannot observe B's internals, so re-testing it on every internal change is pure noise — while an integration harness that runs B's real binary declares the stronger `dependencies:` edge and keeps the conservative closure. Consumes edges do not chain (being affected does not change *your* contract), and consumers pulled in participate in the ordinary dependent closure from there. Enforcement: the receive funnel scans the push's changed `.go` files synchronously at the pushed head (`platform/contract`, a fourth funnel step beside the secret scan) — a Go import under B's committed contract gen dir must be sanctioned by `consumes: [B]` or `dependencies: [B]` on the importing project; the violation is a structured rejection (`undeclared_contract_dependency`) naming the missing edge. The import scan is a *detector*, not the model: REST/TS/Python clients with no scannable import declare the same `consumes:` edge and get the same closure. This does **not** reopen §13.3's inferred-deps decision — gating still consumes only declared edges; the check refuses trees whose declarations are provably incomplete (imports are declared facts at the exact `head_sha`, the §14.5.4 trust class) — and the consumes closure is exactly what a §14.5.4 build-graph refinement computes natively (client targets depend only on gen targets), so the declared floor and the adapter agree. Because manifests are the agents' admin lane (§8.7 denylist), an agent consuming a new contract needs the edge landed first — crossing a project boundary is sanctioned, never incidental. Known v1 gap, recorded: the scan sees only the push's *changed* files, so a push that removes an edge while stale imports sit in unchanged files escapes it; the full-tree audit is test-level today and the recorded backstop.

3. **The API surface is decided at creation.** `runko project create` for a `service` requires `--api grpc|rest|none` (one enum flag — a product action, not hand-written YAML; the §2.3 layering holds): `grpc` scaffolds the `rpc` capability plus a v1 proto stub in-boundary; `rest` scaffolds the `http` capability plus a minimal OpenAPI 3.1 document; `none` is the explicit answer recorded by absence. Non-service types default to `none`; `--api` on them is opt-in, same scaffold.

**First application:** the mailer feed moves to gRPC/Connect — `runkod/proto/mailer/v1` (`InviteFeedService`: `ListDue`/`MarkSent`/`MarkFailed`, operator-gated exactly like the REST drain it replaces) — and `mailer/` declares `consumes: [runkod]`. The graph now sees the edge with the right grain: a change under `runkod/proto/**` puts mailer in the affected closure; a daemon-internals change does not (mailer's tests drive a stub of the contract and cannot observe internals anyway).

Deferred, recorded: REST consumption without a generated client is invisible to the import scan (the mailer's old shape) — for REST the mandate is the artifact plus the honest `consumes:` edge by convention; TS/web consumption scanning rides the `proto/` migration, at which point web's edge flips to `consumes: [runkod]` and web-check runs exactly when `runkod/proto/**` changes (today's web→proto behavior, preserved — a whole-project `dependencies:` edge there would run web-check on every daemon change, which is why the client edge exists); a Bazel-style `visibility:`/export allowlist on contracts is L3, on demand.

### 13.4 Review UX

- Project-scoped default  
- Agent-assisted badge + attribution  
- Owners and checks above the fold  
- Plain-language merge blockers (`get_merge_requirements`)

#### 13.4.1 Review conversation — comments and threads (decided 2026-07-10)

Runko has had approve/land since stage 11c and no way to say *why* a Change
isn't approved — review conversation is pillar 2's missing core (GitHub,
Gerrit, and Graphite all treat it as the product, and any
Copilot-review-class agent flow needs it as its output channel). The model,
decided against those references:

**Comment object** `{id, change_key, author (principal), created_at, body
(markdown), anchor, parent_id, resolved}`:

- **Anchor** is one of: change-level · file-level `{path}` · line-level
  `{path, side: base|head, line}` — and always binds to the `head_sha` it
  was written against. Amend semantics follow approvals (§13.5): after a
  re-push, prior comments render as "on v1 (outdated)" at their original
  anchor; repositioning/floating heuristics (GitHub's approach) are
  explicitly v1.x polish, not v1 — a comment silently shown on the wrong
  line is worse than one marked outdated.
- **Threads are one level deep** via `parent_id` (the GitHub model, not
  nested trees).
- **`resolved`** lives on the root comment, settable by the thread author,
  the Change author, or an owner of the anchored path. Org knob
  `require_resolved_threads` (default **off** — ceremony budget §2.3;
  GitHub defaults off for the same reason) adds an `unresolved_threads`
  blocker row to §13.5's merge requirements when on.

**Storage:** Postgres, as change-lifecycle state exactly like approvals —
durable, never tree-truth (§10.3's carve-out).

**Agents comment, never approve.** Agent review output is ordinary anchored
comments carrying the agent badge (§8.6); agents can be requested as
reviewers; §8.7's approval ban is unchanged. This is deliberately the
Copilot-code-review shape with server-side enforcement: a review agent is
just another API client whose comments are attributed and whose approval is
structurally impossible.

**Surfaces** (implementation: DAG stages 16/16b, §28.3): REST
(`GET/POST /api/changes/{key}/comments`, `POST .../comments/{id}/resolve`,
`POST .../request-review`), Connect `ChangeService` RPCs (proto extended
first, the stage-13 precedent), CLI `runko change comment` / `comments` /
`request-review` — `docs/cli-contract.md` rows land *with* the commands
(the agentsmd drift test forbids documenting commands that don't exist).
Web: inline threads on the stacked diff. MCP: `list_change_comments`
graduates from the deferred catalog at stage 16; writes stay CLI-first
(§8.3). Outbound webhooks gain `change.commented` and
`change.review_requested` (envelope enum extension — additive; payloads
carry ids and anchors, never bodies — consumers fetch bodies via the API so
CI logs don't accumulate review text).

#### 13.4.2 Review requests and the attention set (decided 2026-07-10)

`request_review(change, principal|group)` records who is asked. The
**attention set** — whose turn is it — is **derived, never manually
managed** (Gerrit's manually editable attention set is powerful and
universally confusing; we skip it):

> requested reviewers (and required owners) who have neither approved nor
> commented since the current `head_sha`, plus the author whenever any
> reviewer has responded to the current version.

The derivation is a pure function of facts the control plane already holds
(requests, approvals §13.5, comments §13.4.1, `head_sha`) — nothing new to
store beyond the request itself, nothing to drift out of sync. It feeds the
owner attention inbox on web Home (§17.2) and `runko change requirements`.

### 13.5 Merge gates and landing

| Gate | v1 |
|------|-----|
| Required human owners approved | Yes — with global-approver / mechanical-change relaxations (§7.3) |
| Agent-only approval | **No** (default policy) |
| Unresolved review threads | Org opt-in block (`require_resolved_threads`, default off — §13.4.1) |
| Unowned paths | Configurable block |
| Projects without a `build` binding | Org opt-in block (`require_build_binding`, hermetic discipline — §14.5.4) |
| External CI on affected set | Yes |
| Land semantics | **Conflict-only landing with org-tiered revalidation + trivial-rebase carry-forward** (below) |
| Full merge queue | v1.x — as a batching/pipelining optimization of the configured tier |

**Land races are the norm, not the edge case** (even ~50 engineers on one trunk). v1 policy (rewritten 2026-07-15 — the Gerrit model, superseding intersection-as-default):

- Land = rebase Change onto trunk tip (§7.4). A conflicting rebase always blocks — conflicts are never subject to revalidation policy, under any tier.
- **`revalidation: conflict-only` (default; Gerrit's "Rebase If Necessary")**: a Change with green checks at its current `head_sha` whose server-side rebase applies cleanly lands with **zero re-runs**, regardless of how far trunk moved. The pre-land gate proves the diff against the tree its checks actually saw; **post-land CI on the landed tree** (the only CI that ever builds the actually-landed, post-rebase tree) is the accepted semantic-conflict net — the operating model every Gerrit shop runs.
- **`revalidation: affected-intersection` (org opt-in; the pre-2026-07-15 default, kept verbatim)**: land without re-running checks only when the trunk delta since the Change's base doesn't intersect its affected closure; an intersecting delta demands a re-run on the rebased head. `RunEverything` on either side fails closed to re-run (§13.3).
- **`revalidation: always` (org opt-in)**: every land re-runs required checks.
- `never` is not a policy tier: it is exclusively the admin force-land override (2026-07-08), never an org default.
- Policy resolution: org setting `revalidation_policy` > daemon `--revalidation`/`RUNKO_REVALIDATION` > `conflict-only`.
- A v1.x merge queue batches and pipelines **whichever tier the org configured** — the queue remains an optimization, not a new semantic.

**Trivial-rebase carry-forward** (decided 2026-07-15; Gerrit's `copyCondition: changekind:TRIVIAL_REBASE`). When a Change's head moves to a commit that is a **trivial rebase** of the old head — commit message unchanged, and replaying the old `base..head` delta onto the new base with the land engine's own merge (`git merge-tree`, no conflict resolution) yields exactly the new head's tree — results copy instead of resetting. **Owner approvals copy under every tier** (rationale in the approvals paragraph below); **passing check-run results copy only under `conflict-only`** — under `affected-intersection`/`always`, carried checks would launder an intersecting trunk delta past exactly the re-run the org opted into (sync → copy → land with base == tip would never revalidate), so the stricter tiers get fresh verification. Gerrit precedent: Code-Review copies on TRIVIAL_REBASE by default; Verified is commonly configured likewise. Server-side stack sync (the Sync button, 2026-07-11) produces trivial rebases **by construction**; a client-side rebase-and-re-push (`change push`'s auto-sync) is detected at receive, per series member. Copied check rows are fresh attempt-1 rows stamped `copied_from_head_sha` (provenance, migration 0020). A **fully-covered** carried head — every required check green at the new head after the copy — emits **no `change.updated`**: CI does not re-run (the sync path kicks the automerge worker directly so an armed change still lands promptly); anything uncovered re-runs as today (fail closed). A conflict-resolved rebase, or any content or message amend, copies **nothing**. A same-head re-push stays the documented CI re-trigger and never enters the carry path.

**Approvals bind to the approved `head_sha`** (decided 2026-07-07, stage 12c). An owner approval satisfies the gate only while the Change's head is the commit the approval was granted for: an amend (any client-side re-push of the magic ref) returns the owner requirement to outstanding, exactly as it invalidates check runs — otherwise "get approval on v1, amend to v2, land once checks re-green" bypasses human review entirely (Gerrit resets votes on new patchsets for the same reason). Two deliberate boundaries: (a) the **server-side land rebase needs no carve-out** — the §13.5 gate is evaluated against the pushed `head_sha` *before* the land engine rebases, so the rebased commit never needs its own approval; (b) a client-side re-push resets approvals **unless the new head is a trivial rebase of the approved one** (carry-forward above, 2026-07-15, superseding the earlier "conservative default" stance): the amend-invalidation rule exists to stop "approve v1, amend to v2, land" review bypass, and a trivial rebase **provably carries the identical base..head diff the owner approved** — the carve-out preserves the rule's guarantee rather than relaxing it. Any conflict resolution, content change, or message change still resets. Stale approvals are retained for audit, not deleted; they simply stop counting.

---

## 14. CI/CD integration (not our product identity; critical to VCS success)

### 14.1 Why this section is load-bearing

A version-control and change-review system **fails in practice** if engineers cannot answer:

> “If I land this Change, did the right tests run, and can I see that on the Change?”

Monorepo platforms are judged harshly here: full-repo CI is too slow/expensive; naive path filters are wrong; and forge-centric CI (GitHub Actions on `pull_request`) assumes **GitHub is the system of record**. If we are the system of record for Changes, we must make popular CI systems **first-class citizens** via:

1. **Stable integration contracts** (events, checks, git fetch, auth)  
2. **Affected computation as a service** CI can trust  
3. **Official plugins / actions / orbs / shared libraries** (not only wiki snippets)  
4. **Drop-in pipeline templates** per CI product and language monorepo shape  
5. **Checkout ergonomics** so runners do not full-clone a growing monorepo  

**We do not build a runner fleet product.** We do build an **integration plane** good enough that platform teams choose us *with* their existing Buildkite/GHA/GitLab/Jenkins—not *instead of* a working pipeline.

**Principle:** *Own the change identity and the truth of “what must be green.” Outsource “which VM ran the test.”*

### 14.2 Division of responsibility

| Concern | Platform (us) | Customer CI product |
|---------|---------------|---------------------|
| Change id, base/head SHAs, patch refs | ✅ | Consumes |
| Affected projects / paths / optional targets | ✅ | Consumes to fan out jobs |
| Required check names / policy | ✅ (org config) | Posts results |
| Job scheduling, caches, secrets, runners | ❌ | ✅ |
| Deploy orchestration | Optional hooks / webhooks only | ✅ (Argo, Spinnaker, GHA, …) |
| Flaky retry UX at runner layer | ❌ | ✅ |
| Aggregated red/green on Change page | ✅ | — |
| Monorepo-sparse checkout recipe | ✅ document + action/plugin | Executes |

### 14.3 Integration architecture

```text
                    ┌─────────────────────────────┐
                    │  Change (system of record)  │
                    │  base, head, affected,      │
                    │  required_checks, status    │
                    └─────────────┬───────────────┘
                                  │
           ┌──────────────────────┼──────────────────────┐
           │                      │                      │
           ▼                      ▼                      ▼
   Outbound webhooks      Checks API (inbound)    Git fetch endpoints
   change.opened          POST /checks            change ref / sha
   change.updated         POST /checks/:id        sparse tips / bundle?
   change.reopened        GET  merge requirements
   change.landed
           │                      ▲
           ▼                      │
   ┌───────────────────────────────────────────────┐
   │  Customer CI (GHA, Buildkite, GitLab, …)       │
   │  plugin/template:                              │
   │   1. resolve Change from event or API          │
   │   2. fetch affected JSON                       │
   │   3. monorepo checkout (partial/sparse)        │
   │   4. run matrix per project/target             │
   │   5. report Check(s) + optional annotations    │
   └───────────────────────────────────────────────┘
```

**Two connection modes** (both supported):

| Mode | When | Flow |
|------|------|------|
| **A. Event-driven (preferred)** | CI can receive webhooks | Platform emits `change.*` → CI pipeline starts → posts Checks |
| **B. Poll / API-driven** | Locked-down networks, legacy CI | CI cron or “build with parameters” polls open Changes or is triggered by bridge job |
| **C. Git-mirror hybrid** | **Primary onboarding topology for orgs coming from GitHub** (mirror-first adoption, §18) — and for teams whose pipelines must stay on GitHub | Push of change refs / trunk to GitHub/GitLab; **Checks still post back to our Change** (mirror is transport, not SoR) |

Mode C is not a grudging migration hack — it is the **front door** for existing orgs (§18.1). The invariant stands regardless: merge gates read **our** Checks API; the mirror never becomes a second source of truth.

### 14.4 Core contracts (versioned, stable)

#### 14.4.1 Outbound webhook envelope

```json
{
  "spec_version": "1",
  "delivery_id": "uuid",
  "type": "change.updated",
  "occurred_at": "RFC3339",
  "org_id": "...",
  "monorepo_id": "...",
  "change": {
    "id": "chg_...",
    "number": 1042,
    "url": "https://…/changes/1042",
    "state": "open",
    "base_sha": "abc…",
    "head_sha": "def…",
    "git_ref": "refs/changes/1042/head",
    "title": "…",
    "actor": { "type": "user|agent", "id": "…" }
  },
  "affected": {
    "computation_id": "aff_…",
    "projects": [
      { "id": "prj_…", "name": "checkout-api", "path": "commerce/checkout" }
    ],
    "paths": ["commerce/checkout/…"],
    "reason_codes": ["direct_path", "depends_on"],
    "run_everything": false
  },
  "checks_expected": ["unit", "lint"],
  "api": {
    "change_url": "https://api/…/changes/chg_…",
    "affected_url": "https://api/…/changes/chg_…/affected",
    "checks_url": "https://api/…/changes/chg_…/checks"
  }
}
```

Requirements:

- **Signed webhooks** (HMAC) + delivery retries with exponential backoff + dead-letter visibility in admin UI  
- **Idempotent consumers**: `delivery_id` + `head_sha`  
- **Replay API** for CI admins  
- Schema versioning (`spec_version`); additive fields only within major  

#### 14.4.2 Checks API (inbound status)

GitHub-like model adapted to Changes (not PRs):

```text
CheckRun {
  name              // e.g. "unit", "lint", "project:checkout-api"
  external_id       // CI system’s job id
  status            // queued | in_progress | completed
  conclusion        // success | failure | cancelled | skipped | timed_out | action_required | neutral
  started_at, completed_at
  details_url       // deep link to GHA/Buildkite job
  output: { title, summary, text, annotations[] }
  app_id / reporter // which integration posted
}
```

**Merge requirements** aggregate:

- Required check **names** (org or project policy)  
- **Check-set policies** for per-project fan-out: `unit:* over affected` means "every affected project has a passing `unit:<project>` run" — evaluated as a set, so 40 affected projects do not require 40 hand-listed required checks, and the UI renders one collapsible row (“unit — 38/40 passed”), not 40 rows  
- Stale checks: auto-invalidate when `head_sha` changes (revalidation scope per §13.5)  
- **Expiry:** required runs carry a TTL (default 24h in `queued`/`in_progress`); expired runs surface as `stale reporter` with the integration's last-seen time — a dead CI must block loudly, not hang silently  

**Re-runs are first-class** (design away “push an empty commit”):

```text
POST /changes/{id}/checks/{name}/rerun-request
  → emits change.check_rerun_requested webhook (plugin maps it to a provider re-run)
  → new CheckRun attempt linked to the same (change, head_sha, name)
```

Permitted: change author, owners, CI admins; agents only if policy allows. Attempts are recorded — per-check flakiness telemetry feeds the §14.12 dashboard.

UI and `get_merge_requirements` show the same structure humans and agents see.

#### 14.4.3 Affected API (pull model)

```text
GET /changes/{id}/affected
GET /compute/affected?base=&head=          // for local/CI without change id
```

Response includes `run_everything` when computation cannot safely subset (root tooling, policy, missing graph)—CI templates **must** honor this flag.

#### 14.4.4 Git access contract for runners

CI must fetch monorepo content efficiently:

| Mechanism | Purpose |
|-----------|---------|
| **Change ref** `refs/changes/<id>/head` | Build exact Change head |
| **Base + head SHAs** | Explicit bisectable pins |
| **Partial clone** support on Git HTTP(S) | `filter=blob:none` (and tree filters where applicable) |
| **Sparse patterns API** | `GET /changes/{id}/sparse-checkout` → cone patterns for affected projects + deps |
| **Optional bundle endpoint (v1.x)** | Precomputed thin pack for a Change (perf) |
| **Machine auth** | CI OIDC or deploy tokens with `contents:read` + `checks:write` scopes |

**First-class “checkout action” behavior** (implemented per CI as plugin/action):

1. Authenticate  
2. Resolve change id (from webhook payload or input)  
3. Fetch affected + sparse list  
4. `git fetch` partial + sparse checkout  
5. Export env: `RUNKO_CHANGE_ID`, `RUNKO_HEAD_SHA`, `RUNKO_AFFECTED_PROJECTS` (JSON path), etc.

This is as important as the Checks API—**slow full clones will kill monorepo CI adoption**.

### 14.5 Affected computation and CI semantics

#### 14.5.1 What CI needs from “affected”

| Output | Use in CI |
|--------|-----------|
| Project list | Matrix axes (`project: [a,b,c]`) |
| Paths | Path-based tools, docker build contexts |
| `run_everything` | Global jobs (release tooling, root lint) |
| Target labels | `//foo:bar` sets from the §14.5.4 build-graph adapter when enabled |
| Computation id | Cache keys / reproducibility debug |

#### 14.5.2 When to run what (template policy defaults)

| Event | Default template behavior |
|-------|---------------------------|
| `change.opened` / `change.updated` (new head) | Run required checks on **affected** only |
| `run_everything=true` | Full suite or “heavy” workflow |
| `change.landed` (post-submit) | Optional wider suite / deploy pipelines (customer choice) |
| Scheduled trunk | Nightly full or canary (customer choice; we only supply trunk SHA webhook) |

#### 14.5.3 Correctness vs cost

Document clearly:

- v1 affected = path→project + **declared** deps + root-invalidation rules; inference is advisory-only and never gates (§13.3)  
- Build-graph adapters (§14.5.4) refine this floor to target level — runner-side, fail-closed, optional  
- Templates should treat unknown/edge as **fail closed to broader run** when `run_everything` or on computation error—not fail open to “run nothing”  
- Org setting: `affected.strictness` = `conservative` (default) | `aggressive`  

#### 14.5.4 Build-graph adapters (Bazel first; engine-agnostic by design)

Project-level affected is the **floor** — correct with zero build tooling. For monorepos with a real build graph, target-level precision is the difference between "test 4 projects" and "test 37 targets," and it is much of the monorepo's economic argument at scale. We integrate that precision **without becoming a build system** (§2.5, §14.16):

| Contract element | Definition |
|---|---|
| Inputs | Checkout at `head_sha`, changed paths, universe pattern (e.g. `//...`), engine binary from the **runner's** toolchain |
| Output | Target set (e.g. `tests(rdeps(//..., set(<changed files>)))`), optional target→project mapping |
| Runs | **Runner-side only**: `runko-ci affected --engine bazel` — the platform daemon never executes customer build tooling |
| Failure mode | Any query error, timeout, or engine/version skew ⇒ `run_everything=true` (fail closed, §14.5.3) |
| Trust class | **Declared, not inferred** (§13.3): hermetic evaluation at the exact `head_sha` makes engine output gate-eligible — unlike async import inference |
| Refinement post-back | Adapter may POST the refined target set to the Change as an *affected refinement*, shown alongside the platform's project-level computation; org policy chooses whether check-set policies key on projects (default) or refined targets (opt-in) |

**Engine matrix** — the contract is the product; engines are implementations:

| Engine | Status | Notes |
|--------|--------|-------|
| **Bazel** | v1 implementation | `bazel query`/`cquery` rdeps recipes shipped with the adapter |
| **Buck2** | planned; contract-shaped from day one | `buck2 uquery` exposes the identical rdeps shape — second implementation proves the interface |
| Pants / others | contract is public | Community implementations welcome |

Division of responsibility stays intact (§14.2): we own the affected floor (paths + declared project deps); the adapter, running on customer runners with the customer's toolchain, supplies the ceiling. RBE and remote caching stay with Namespace/BuildBuddy/EngFlow (§21.3) — they *consume* the adapter's target sets.

**Engine admission criteria (this is where we are opinionated).** A build system qualifies as an engine only if it provides:

1. **Declared** targets (explicit BUILD/BUCK-class files — not config conventions)  
2. **Hermetic evaluation at a SHA** (same checkout ⇒ same graph, no ambient state)  
3. **A reverse-dependency query** (`rdeps`-equivalent) over that graph  

Bazel and Buck2 qualify; Pants largely qualifies. **Task runners (Make, Turborepo/Nx task graphs, npm scripts) structurally never qualify** — their graphs are package-coarse and non-hermetic, so they can never earn gate-grade trust, and we will not build engine adapters for them. This is opinionation **by criterion, not by list**: the door is open to any future hermetic system and permanently closed to everything else. Non-qualifying stacks use the platform floor — which is also the escape hatch that keeps NG4 honest.

**Golden-path opinion (greenfield).** Orgs created from a template monorepo may set `build_discipline: hermetic` (recommended default for new orgs): templates emit BUILD files, `project create` wires targets automatically (principle 8 — generated, never hand-authored), and default check-sets run `bazel test` over refined targets. The full opinion, with none of the ceremony that made Bazel adoption infamous. Existing orgs importing brownfield repos (§18) are **never** gated on a build-system migration — that would re-add the adoption cliff §18 exists to remove.

**Org-level mandate (opt-in, not platform law).** `require_build_binding: true` blocks merges for projects lacking a `build` capability — for orgs that want hermetic discipline enforced. The platform recommends the opinion; the org enacts it.

#### 14.5.5 Multi-engine monorepos (decided 2026-07-09)

One build graph per repo is a **non-goal**. Real monorepos mix territories —
Go under Bazel, a web app under Vite, generated protobuf between them — and
this repo itself runs that mix (Bazel for Go, Vite/npm for `web/`, buf at
the seam) since the self-host re-carve. The design:

1. **The declared layer is the universal floor and the only default
   gate-grade layer.** PROJECT.yaml paths + `dependencies:` edges are
   engine-independent and gate merges regardless of what builds each
   territory (§13.3). Engines refine; they never replace.
2. **Engines are per-territory, and sovereign there.** A project subtree
   declares its engine via `capability_config.build` (§14.5.4). Engines
   never invoke each other — no `genrule` wrapping `vite build`, no npm
   script shelling into `bazel`. The platform routes checks to
   territories; it never orchestrates builds across them.
3. **The boundary-artifact rule.** A cross-engine dependency is expressed
   as a declared `dependencies:` edge **plus committed generated artifacts
   at the seam**, kept honest by a regenerate-and-diff CI check — never a
   build-time invocation of one system by the other. Canonical example:
   `proto/` generates committed Go (`proto/gen`, consumed by Bazel's
   territory) and committed TS (`web/src/gen`, consumed by Vite's); the
   `web → proto` edge re-runs web-check on proto changes. Neither build
   system knows the other exists.
4. **Non-qualifying build systems are territory scaffolds, not engines.**
   §14.5.4's admission criteria stand: Vite/npm/Nx/Turborepo-class tooling
   never gets an adapter or a `capability_config.build` binding. Their
   territories ride the declared floor — which is precisely adequate,
   because package-coarse territories are exactly where project-level
   affected is already target-level. `project create` still scaffolds them
   first-class (below); the distinction is refinement trust, not product
   support.
5. **Escalation scope (v1.x refinement).** An engine failure currently
   escalates the whole `AffectedOutput` to run-everything; with multiple
   territories, escalation should be scoped to the failing engine's
   territory. Gating correctness is unaffected either way (the floor
   gates); this is a CI-cost optimization.

**Create-time build-system selection (amends §10.4's source-skeleton-only
rule, 2026-07-09).** `project create` takes `build_engine`
(`bazel | vite | none`), defaulting **by language**: `ts` → `vite`,
everything else → `bazel` (`no_template` keeps its bazel default — the
filegroup is language-agnostic). `bazel` emits the §14.5.4 golden path
(BUILD.bazel + `capability_config.build` binding). `vite` emits the js
territory's graph-node marker — a minimal generated `package.json` +
`vite.config.ts` (the one sanctioned exception to "no package.json in
built-ins": for a Vite territory that file IS the build declaration, the
BUILD.bazel-equivalent) — and deliberately **no** `build` capability, per
rule 4; combining `--build-engine vite` with an explicit `build`
capability is a structured `invalid_combination` error, not a silent
downgrade. `none` scaffolds nothing (hand-managed territories). Unknown
values are a structured `unsupported_build_engine` naming the choices.

#### 14.5.6 Affected-scoped CI: the platform dogfoods its own affected set (decided 2026-07-09)

The point of carving a repo into projects with dependency edges is that a
change to one project should run *only* the tests it can affect and
rebuild *only* the artifacts it feeds — not the whole suite, not every
image, every time. Runko's own CI is the reference implementation:

- **Scoped checks.** The pre-land workflow's `setup` job runs
  `runko-ci checks` over the change's `base..head` — the affected
  computation is the *same* one the server's merge gate uses — and
  executes exactly the matrix it returns (§14.9.1): each affected
  project's own manifest-declared commands, themselves scoped to their
  project's subtree. A `cli`-only change runs `cli-test`
  (`bazel test //cli/...`); a prose change — markdown anywhere, per
  §14.5.7 — runs only `docs-check` (seconds); a `web`-only change runs
  only `web-check`. A
  `run_everything` result (unowned root path, or an engine escalation)
  fails **open** to every project's checks, matching the gate's
  fail-closed bias: the workflow must never skip a check the gate will
  require.
- **Scoped releases.** The post-land image build computes affected over
  the landed range and rebuilds each image only when its own input set (a
  project plus its transitive dependencies) intersects — a docs-only
  landing rebuilds nothing, a web-only landing rebuilds only the web
  image. Build-graph health (`bazel-check`, gazelle drift) stays
  repo-wide by nature and is not scoped.

This is the §14.5.1 "affected → CI scoping" contract turned on the
platform itself; the mechanism is entirely `runko-ci affected` +
`PROJECT.yaml` dependency edges, no new machinery.

#### 14.5.7 Prose paths — the de-escalation dual of root invalidation (decided 2026-07-10)

`root_invalidation` (§14.5.2) escalates: touching a build-sensitive path
runs everything. `prose` is its dual, closing the opposite gap: a
documentation edit *inside* a project's folder (`platform/README.md`,
`web/README.md`) is owned by that project by longest prefix, so it runs
the project's full check set — and drags the reverse-dependency closure
with it. Tests for a README.

`PROJECT.yaml` gains `prose:` — an **ordered, first-match-wins** pattern
list (same glob dialect as `root_invalidation`, plus a leading `**/` form
for any-depth matches and a gitignore-style `!` prefix for exceptions).
A changed path matching a prose pattern is re-attributed, **for check
derivation only**, to the repo-root project instead of its longest-prefix
owner: it requires the root project's (cheap, content-tier) checks, and
the dependency closure applies to that attribution as usual — the root
project simply has no dependents, so nothing rides along. Precedence and
fail-closed properties:

- **Root invalidation always wins** — checked before prose, so a pattern
  collision escalates rather than de-escalates.
- **Owners are untouched.** The §7.3 owner gate derives from raw touched
  paths by longest prefix, deliberately not from the check attribution —
  the owning team still reviews its own README; only the machines stand
  down.
- **No root project ⇒ no de-escalation.** A prose match in a repo without
  a root manifest falls through to ordinary ownership (and unowned paths
  keep failing closed to `run_everything`).
- **Load-bearing "docs" must be excepted, and covered.** Anything a test
  consumes as data is not prose — this repo's own list opens with
  `!docs/spec/**` and `!docs/cli-contract.md` (runfiles of the contract
  tests), and the `docs` project declares a `contracts-test` check that
  runs exactly those consuming suites. The `!` exceptions and the data
  checks are two halves of one obligation: if you exempt a path class,
  you must name what still gates its exceptions.

The reference carve in this repo: root `prose:` is `!docs/spec/**`,
`!docs/cli-contract.md`, `**/*.md`, `LICENSE`, `docs/images/**`; the root
and `docs` projects declare `docs-check` (`make check-docs`, a fast
markdown link checker — a *real* check, satisfying default-deny §13.5
without policy theater), and `docs` adds `contracts-test`. Net effect: a
design-doc or README edit anywhere runs one seconds-long job; a contract
schema edit runs the suites that actually read it; `go.mod` still runs
the world.

#### 14.5.8 Root invalidation, refined: `!` exceptions now; a graph-refinable class next (decided 2026-07-10)

Survey result (Nx, Turborepo, Pants, bazel-diff, target-determinator,
Meta's BTD): every affected system converges on the same three layers this
platform already has — a dependency-graph closure, a blunt
"global-invalidation" path list for files the graph cannot see, and an
optional precision layer. Two consequences, one shipped and one planned:

**1. `!` exceptions in `root_invalidation` (shipped with this decision).**
The list is now **ordered, first-match-wins**, with the same gitignore-style
`!` prefix as `prose:` (§14.5.7) — one dialect, one evaluator
(`affected.MatchOrdered`). An excepted path does not escalate; it falls
through to prose/ownership attribution, so it keeps failing closed when
unowned. Cross-manifest composition follows prose: manifests concatenate in
scan order (root first), so the root manifest's exceptions outrank deeper
manifests' patterns. Exceptions carry §14.5.7's obligation — name what
still gates the excepted path. First instance:
`!.github/workflows/ci.yml` (before `.github/**`): the post-land safety
net executes only after landing, so no edit to it can change a pre-land
check's validity — escalation bought a full matrix that exercises workflow
files not at all. What still gates it: owner review + `docs-check`
pre-land, and the workflow's own first post-land execution (a broken
`ci.yml` fails loudly against trunk, exactly the fix-forward class §14.4
assigns it). `runko-checks.yml` (the PRE-land executor) is deliberately
NOT excepted: editing the machine that runs checks invalidates every
check by definition.

**2. The blunt/graph-refinable split (decided; lands with its consumer).**
`root_invalidation` entries divide into two classes. **Out-of-graph** paths
(CI workflows, `scripts/**`, `Makefile`, `Dockerfile`, compose, sqlc) are
invisible to any build graph — every surveyed system keeps these blunt, and
so do we, permanently. **Graph-visible** paths (`go.mod`, `go.sum`,
`MODULE.bazel*`, `BUILD.bazel`, `.bazelrc*`) are where the
bazel-diff/target-determinator technique applies: hash the configured
target graph at base and head, diff, and run exactly what moved — a
`MODULE.bazel` edit stops meaning "the world" and starts meaning "the
targets whose hashes changed". Plan, in order: (i) the build-adapter
contract (§14.5.4) grows a `SnapshotDiff(base, head) → impacted targets`
strategy, v1 wrapping a target-determinator-class external process (a Go
binary — engines stay processes, not imports), fail-closed to
`run_everything` on any error exactly like rdeps refinement; the
machine-readable `refinable` marker on `root_invalidation` entries enters
`project.schema.json` in that same change, keeping schema and parser in
lockstep (spec-before-code, not schema-before-parser). (ii) It dogfoods
first in **post-land `ci.yml`** — gate-free by construction, so narrowing
is a pure CI-cost experiment measured in `migration-findings.md`. (iii)
Gate-grade narrowing is the org opt-in §14.5.4 already reserves, and
requires the gate to accept CI-reported refinement — its own recorded
decision when (ii)'s data justifies it.

**Also decided here — per-check `inputs:` conditionally admitted.** The Nx
`namedInputs` power is real, and §2.3 (as sharpened this date) does not
forbid it: `prose:`, its `!` exceptions, and `root_invalidation` are
already three special-cased input filesets, proven in-tree. A general
per-check `inputs:` fileset on `ci.checks` entries is admitted **under
conditions**: it waits for the snapshot-diff data (most of its value for
graph-covered checks may already be captured there, leaving only the
non-graph checks — `web-check`, `bazel-check` — as candidates); defaults
stay exactly today's semantics (absent = the project subtree + dependency
closure); it reuses the one glob dialect; and it rolls out
**advisory-first** — logging "this check would have been skipped" while
still running everything, so an under-inclusive fileset is caught by
comparison, not by a missed regression on trunk. Soundness note: a wrong
fileset under-gates, but so does a missing `dependencies:` edge — this is
the same declared trust class (§13.3), reviewed in-tree, not a new risk
species.

#### 14.5.9 Check classes: `run_when` (decided 2026-07-11)

Stage-15 dogfood observation (user): a change in project X pulls its
dependents into the affected closure and CI runs their ENTIRE check
suites — a one-line `platform` change ran 11 checks, dependents' race
lanes included. The affected closure is right about *which projects* are
implicated; it says nothing about *which of their checks* guard the
cross-project contract.

**Decision:** `ci.checks[].run_when: direct | affected` (default
`affected` — exactly today's semantics, so absent means nothing changed):

- `affected`: runs whenever the project is in the closure — the
  integration class.
- `direct`: runs only when the project's OWN paths were touched, never
  when it rides in via `depends_on` edges — the unit-lane class.
- `run_everything` (root invalidation, unowned paths, engine failure)
  treats every project as direct: both classes run. Fail closed.
- "Test classes" are just two named checks with different `run_when`; HOW
  a project scopes each command (bazel patterns, tag filters, separate
  packages) stays its own business (§14.9.1).

**The lockstep rule, again:** the merge gate and the executor now share a
second resolution axis, so the filter lives in exactly one function —
`index.ChecksFor(project, direct)` — that `runkod`'s
`requiredCheckNames` and `runko-ci checks` both call. A gate/executor
disagreement here is a deadlock (required-but-never-run), the same
failure §14.9.1's head-tree rule exists to prevent. `affected.Compute`
surfaces the direct-vs-closure distinction it always computed
(`ProjectRef.Direct`); §14.5.8's snapshot-diff union marks graph-named
impacted projects direct and their dependents closure-shaped.

**Recorded honestly:** (a) with warm bazel caches a dependent's unaffected
targets are already cache hits — `run_when` buys the matrix-job overhead,
cold-cache runs, and non-bazel checks; (b) declaring a check `direct` is
the project's assertion that the lane doesn't guard cross-project
behavior — dependency breakage is then caught only by its
affected-class lanes and `bazel-check`'s full build. First carve in this
repo: `platform-race` and `runkod-race` are `run_when: direct` (race
verification is an own-code class; the `-test` lanes stay affected — they
are the integration surface). Unknown `run_when` values normalize to
`affected` at scan time: the schema polices authoring, the scanner feeds
the gate and must not silently drop a check.

### 14.6 Plugins vs templates (delivery model)

We ship **both**—they solve different problems:

| Artifact | What it is | Who uses it | Maintained as |
|----------|------------|-------------|----------------|
| **Core integration library** | Language-agnostic CLI `runko-ci` / container: resolve change, fetch affected, post check | Any CI that can run a container | OSS in monorepo `integrations/ci-core` |
| **Native plugin / Action / Orb** | Thin wrapper around core for UX (marketplace listing) | GHA, Buildkite, etc. | Per-CI package; versioned |
| **Pipeline templates** | Copy-or-generate workflow YAML for monorepo shapes | Platform teams bootstrapping | `integrations/templates/<ci>/<shape>` |
| **Terraform / Helm examples** | Wire webhook secrets, OIDC trust | Self-host admins | `deploy/examples/ci-bridge` |
| **Reference bridge service (optional)** | Small service translating our webhooks → provider-specific triggers when plugins insufficient | Airgapped / awkward CI | Optional component, not required |

**Rule:** Prefer **one portable `runko-ci` core** so we do not N-expand logic. Native plugins are UX sugar + marketplace trust.

```text
┌─────────────────────────────────────────┐
│  runko-ci (Go/Rust static binary + image) │
│  checkout | affected | check report | …  │
└──────────────────▲──────────────────────┘
                   │ wraps
     ┌─────────────┼─────────────┬──────────────┐
     │             │             │              │
 actions/*    buildkite-  gitlab-ci    jenkins-  generic
              plugin      include      shared lib  curl scripts
```

### 14.7 Supported CI matrix (phased commitments)

#### Tier 1 — launch / dogfood (must work excellently)

| System | Why Tier 1 | Deliverables |
|--------|------------|--------------|
| **GitHub Actions** | Default for many medium orgs; hybrid mirror mode | Official Action(s): `checkout-change`, `affected-matrix`, `report-check`; reusable workflows; docs for webhook→`workflow_dispatch` or `repository_dispatch` **and** pure mirror mode |
| **Buildkite** | Strong monorepo/enterprise CI culture; pipelines-as-code | Plugin + pipeline template; webhook trigger recipe; annotation helpers |
| **Generic + `runko-ci` CLI** | Escape hatch for everything else | Full contract via CLI; shell examples |

#### Tier 2 — soon after (high demand)

| System | Deliverables |
|--------|--------------|
| **GitLab CI** | `include:` templates; CI OIDC; bridge for non-GitLab-hosted monorepo |
| **CircleCI** | Orb + config examples |
| **Jenkins** | Shared library + freestyle/pipeline examples; polling fallback documented |
| **Bazel adapter** | `runko-ci affected --engine bazel` per the §14.5.4 contract + "affected → bazel test" template; **pulled into Tier 1 if the dogfood monorepo is Bazel-built** |

#### Tier 3 — demand-driven

| System | Notes |
|--------|-------|
| **Tekton / Argo Workflows** | Kubernetes-native examples; Task CRDs calling `runko-ci` |
| **BuildBuddy / Bazel remote execution** | Not our runners; RBE/caching stay customer-side — they consume the §14.5.4 adapter's target sets |
| **Buck2 engine** | Second §14.5.4 implementation when demand arrives; the contract is Buck2-shaped from day one |
| **Earthly, Dagger, etc.** | Examples only |
| **Azure DevOps / Bitbucket Pipelines** | If customer demand |

**Explicit non-goal:** maintaining deep plugins for every CI forever. Tier 1+2 + excellent `runko-ci` core is the promise; Tier 3 is best-effort examples.

### 14.8 Per-system integration patterns

#### 14.8.1 GitHub Actions

Two supported topologies:

**Topology GHA-1 — Platform-native (recommended when we host Git):**

- Platform webhook → small “dispatcher” workflow via `repository_dispatch` / `workflow_dispatch` with change id payload **or** self-hosted runner agent polling  
- Jobs use `runko/checkout-change` Action (partial+sparse)  
- `runko/report-check` posts to **our** Checks API (not only `github.checks`)  
- Optional: also emit GitHub check run if repo is mirrored (dual status)

**Topology GHA-2 — Mirror-as-trigger:**

- Change head mirrored to GitHub branch `change/<id>` or `refs/runko/changes/<id>`  
- `pull_request` / `push` triggers standard GHA  
- First step: `runko-ci affected --change <id>` (id in branch name or commit trailer)  
- Report back to platform Checks (required for merge gate on platform)

Document tradeoffs: GHA-2 reuses ecosystem muscle memory; GHA-1 avoids dual-SoR confusion. **GHA-2 land semantics** (mirror-branch cleanup, PR auto-close, sync-back ordering) follow the mirror invariants in §18.6; the full protocol lives in the Migration & mirror RFC (§26).

#### 14.8.2 Buildkite

- Webhook from platform creates build with env `RUNKO_CHANGE_ID`, SHAs  
- Plugin performs checkout + affected matrix dynamically (`buildkite-agent pipeline upload`)  
- Annotations link to Change URL  
- Check report on completion (success/fail)

Buildkite’s dynamic pipelines are an excellent fit for **affected project matrices**.

#### 14.8.3 GitLab CI

- If monorepo not on GitLab: use webhook → GitLab trigger token / pipeline API with variables  
- Template uses `runko-ci` image in `default.image`  
- `parallel:matrix` from affected JSON (generate child pipeline)

#### 14.8.4 Jenkins

- Multibranch or parameterized pipeline; **poll open changes** API for airgapped  
- Shared library steps: `runkoCheckout`, `runkoAffected`, `runkoReportCheck`  
- Accept that Jenkins UX is weaker; reliability of contract matters more than beauty

### 14.9 Project-level CI hints (low ceremony)

Avoid Boq-sized CI config. Optional **hints** on Project (L2, generated/editable simply):

```yaml
# fragment of PROJECT.yaml or control-plane only
ci:
  checks:
    - name: unit
      command: "make test"          # or template-defined
  # path already known from project root
```

Templates map `checks[].name` → required Checks. **Default template** from project type (`go-service` → `unit` + `lint`) applied at create—users override rarely.

Org can define **global required checks** (e.g. `secrets-scan` always).

#### 14.9.1 Encapsulation: the CI system is a generic executor (decided 2026-07-10)

A project's tests belong to the project — `ci.checks[].command` is the
check's *definition*, not a hint. The CI side consumes it through one
subcommand:

```
runko-ci checks --base <sha> --head <sha>
  -> {run_everything, checks: [{project, name, command}]}
```

the affected closure's manifest-declared checks, **deduped by name** (a
shared check like a repo-wide build-graph gate may be declared by several
projects and runs once); the same name declared with *different* commands
is a structured `ambiguous_check` error, never silent first-wins.
`run_everything` resolves every project's checks — fail closed.

The CI workflow that consumes this knows **no project names, no commands,
no per-check environments**: resolve the matrix, run each `command`,
report each result under its `name`. Two properties make this safe:

- **Gate/executor agreement by construction.** The merge gate resolves
  required check names from the change's own head-tree manifests, and the
  executor resolves names+commands from a checkout of the same head — so
  adding or renaming a check is one manifest change; the two sides cannot
  disagree, even mid-rename.
- **Runner contract instead of per-check config.** The executor provides
  one documented environment to every check: the repo at the change head,
  the org's toolchains installed, and shared services up with their DSNs
  exported (e.g. `RUNKO_TEST_DATABASE_URL` over an always-on Postgres).
  Checks opt in by reading what they need; DB-gated tests skip where the
  DSN is absent. Shared mutable services put the serialization burden on
  the *test harness* (Runko's own `internal/dbtest` holds a session-level
  Postgres advisory lock per test), not on runner flags — that is what
  lets one project's check carry its own integration tests without a
  dedicated "db lane" in the workflow.

A matrix-resolution failure means no checks report and the gate stays
pending — visible, rerunnable, fail-closed. Future (noted, not built):
a `tools:`/`services:` field on checks to narrow the runner contract
declaratively, and `runko-ci checks --run` as a local executor.

### 14.10 Deploy / CD hooks — Runko as the GitOps source of record

We are not Spinnaker/Argo CD. We provide:

| Hook | Use |
|------|-----|
| `change.landed` webhook | CD systems start deploy of affected services |
| Trunk push webhook | Same for trunk-based CD |
| Project metadata in payload | service name, path, optional deploy capability flags |
| Optional gitops commit (later) | Out of scope for v1 unless demand |

CD templates (Argo CD ApplicationSet examples, etc.) live under `integrations/templates/cd/` as **examples**, Tier 2+.

#### 14.10.1 GitOps consumers (ArgoCD / Flux) — the read side works on day one

Because the read path is vanilla git (§11.2: clone/fetch always works, standard smart-HTTP), a GitOps controller points at Runko exactly as it would at GitHub: repo URL + deploy token as the repo credential. **No plugin is required for the read side, ever.** Two recipes ship as Tier-2 templates:

- **Repo credential + refresh webhook**: trunk-advance webhook → ArgoCD hard-refresh API (skips the poll interval), the standard GitOps-forge wiring.
- **Affected-scoped CD** — the monorepo→many-apps answer nobody else has: one monorepo feeding N Applications normally means every commit refreshes all N. Runko's `change.landed` payload carries **affected projects with their `deploy` capability flags**, so CD targets exactly the affected apps. The §13.3 floor doing CD work natively.

**Mirror-first extends to CD untouched** (§18.1): during adoption stages 0–1, the customer's GitOps controller keeps pointing at the GitHub mirror — zero CD migration to trial Runko. The deploy pipeline, usually the scariest thing to re-point, never moves until the SoR flip, and the mirror remains a valid read target even after.

#### 14.10.2 GitOps writers — the bot lane (decided; resolves former open question "agent land exceptions")

GitOps is not read-only: image-tag bumpers (ArgoCD Image Updater, Flux image automation, Renovate) **push commits**. With trunk closed (§7.4), every bot write becomes a Change — correct, but it must not require a human click per image bump. The decision:

> **Path-scoped bot lanes.** A trusted bot is an AgentIdentity (§7.5) whose policy grants `can_land_changes: true` **constrained to a path allowlist and a required-check set** — e.g. "the image-bump bot may auto-land, but only Changes touching `deploy/**` values files, and only with `manifest-lint` green." Auto-landed bot Changes are ordinary Changes: attributed, audited, revertible, visible in owners-coverage reports.

This is strictly stronger than GitHub's all-or-nothing branch-protection bypass lists — path-scoped enforcement is the §8.9 moat applied to CD. Enforcement lands with the merge-policy wiring (DAG stage 11c, §28.3).

#### 14.10.3 Tags and releases (decided 2026-07-10; resolves the §22.3 tag-governance question)

CD flows key on tags (`v1.2.3` → image tag → deploy). Two decisions close §11.4's documented permissiveness and give the platform a release story (researched against `nx release` and GitHub's immutable releases):

**Tag-namespace governance.** `refs/tags/*` writes move from unconditional-accept to policy at the receive funnel: (a) principals holding the org **release role** may push tags; (b) a **tag-namespace-scoped bot lane** — §14.10.2's path-scoped pattern applied to ref namespaces (`can_write_tags: ["commerce/checkout-api/v*"]` on the AgentIdentity policy) — covers release automation; (c) tags minted by the release flow below are server-created through the same policy code, so there is nothing to bypass. Unauthorized tag pushes get the §6.9-style structured rejection. Rollout: an org knob starts permissive (today's documented behavior) and flips under the default-deny posture — the loud-opt-out precedent.

**Releases as first-class objects.** A Release is `{project, version, tag_ref, head_change_key, changelog, created_by, immutable}` — cut by `runko release create --project <p>` (or the web), which:

1. computes the version — semver bump or `--version` explicit, per the project's `release` capability config;
2. derives the **changelog from landed Changes touching the project since the last release tag** — we own Change descriptions, test plans, and owners, which beats commit-message scraping and replaces nx-release-style version-plan files (the Change description *is* the version plan); `head_change_key` records the newest landed Change the release includes;
3. writes an annotated tag `<tag_prefix><version>` **server-side** (no push; default prefix `<project-path>/v` — per-project tag namespaces in one repo);
4. records the release row **immutably** — GitHub immutable-releases parity: a release, once cut, cannot be edited or re-pointed; the tag → commit → Change chain is the attestation anchor, while artifact attestations remain customer CI's job (§14.2);
5. emits a `release.created` webhook (`docs/spec/webhooks/release-created.schema.json`) — the real CD trigger §14.10.1's read-side recipes were missing, instead of tag-polling.

**Publishing stays out of scope**: registry pushes are customer CI, triggered by `release.created` — the §14.1 division of responsibility is unchanged. The mirror carries tags outbound as transport (§18.6), unchanged.

Per-project opt-in: the `release` capability with `capability_config.release: {tag_prefix, versioning: semver|manual, changelog: from-changes|none}` — absent means no release surface and zero config (anti-Boq, §2.3). Implementation: DAG stages 17/17b (§28.3).

### 14.11 AuthN for CI systems

| Method | Use case |
|--------|----------|
| **CI OIDC** (preferred) | GHA, GitLab, Buildkite OIDC → short-lived tokens; trust config in org settings |
| **Deploy tokens** | Scoped: `changes:read`, `contents:read`, `checks:write` |
| **Webhook secrets** | Inbound verification to CI; outbound HMAC from us |

Never recommend long-lived PATs with full admin in docs.

### 14.12 Observability of integration health

Admin “CI integrations” dashboard:

- Webhook delivery success rate / latency  
- Check reporters last seen  
- Changes blocked on missing checks (stale integrations)  
- Average checkout time reported by `runko-ci` (optional telemetry, off by default self-host)  
- Affected `run_everything` rate (signal for graph quality)

### 14.13 UX on the Change page (CI-visible)

- Check list with deep links, durations, first-class **Re-run** (rerun-request API, §14.4.2 — never “push an empty commit”)  
- Affected projects chips (why did this job run?)  
- “CI setup” empty state: if no checks ever reported, CTA → **Connect CI** wizard (pick GHA/Buildkite/…, shows template + webhook secret)  
- Agent-facing: `get_merge_requirements` includes failing check names and `details_url`

**Connect CI wizard** is productized UX—not a docs-only afterthought (same anti-ceremony stance as project create).

### 14.14 Local and agent loops vs CI

| Loop | Role |
|------|------|
| **Pre-CI** (workspace) | Fast feedback; optional `runko-ci affected` locally; not a gate |
| **Presubmit CI** | Source of merge truth for required checks |
| **Post-submit** | Wider tests / deploy; does not block land unless policy says so |

Coding agents should call `get_merge_requirements` after CI runs rather than inventing “tests passed.”

### 14.15 Testing our integrations (dogfood + CI matrix in OSS)

- Contract tests for webhook schema + Checks API (pact-style)  
- Smoke pipelines in repo: GHA + Buildkite (if secrets) on the platform’s own monorepo dogfood  
- Published `runko-ci` compatibility version matrix  

### 14.16 What we will not build (reminder)

- Multi-tenant RBE as core product  
- Replacing Buildkite/GHA pipeline UIs  
- Universal “CI for all languages” opinionated monolith beyond templates  
- Guaranteeing customer runner performance  

### 14.17 Success criteria for “CI integration done enough”

| Criterion | Bar |
|-----------|-----|
| Tier 1 systems | Official plugin/action + template + Connect wizard |
| Checkout | Partial+sparse path documented and implemented in `runko-ci`; no full clone required for affected-only |
| Merge gate | Change cannot land when required checks missing/failed (policy on) |
| New project | Inherits default checks from template without CI engineer involvement |
| Self-host | OIDC or token recipes for Tier 1; webhook egress documented |
| Escape hatch | Raw API + `runko-ci` sufficient for unsupported CI in &lt; half day for a competent platform eng |

---



## 15. Auth, security, multi-tenancy

### 15.1 AuthN

- Humans: **OIDC**  
- Agents: **service tokens** or OIDC client credentials / CI federation  
- Eval: local users  

**Interim: named-token principal registry** (decided 2026-07-07, stage 12c). Until OIDC lands, `runkod` accepts a registry of named principals — `{name, token, is_agent, agent policy}` — generalizing what §14.10.2 bot lanes already do with per-lane tokens. The single deploy token remains the anonymous eval fallback. This is deliberately NOT an auth system (no issuance, rotation, or identity federation — that stays OIDC's job); it exists because four already-built enforcement points are inert without *any* principal identity: self-approval denial (§8.7's hard rule), `authored_by`/`landed_by` attribution (§7.5), owner-only workspace-snapshot push (§12.2), and receive-time `AgentPolicy` evaluation (§8.7 — the §8.9 moat, fully implemented in the funnel since stage 6 but unreachable while every caller is the same anonymous token). Smart-HTTP identity reaches the pre-receive funnel via `REMOTE_USER` (git's own convention for authenticated receive), forwarded hook→daemon alongside the quarantine vars. When OIDC lands, principals become rows it populates; the enforcement points don't change.

**Interim, human sign-in: HTTP Basic over the principal registry** (added 2026-07-07, post-stage-13, user-directed — "full auth, Basic for now"). Every API surface (REST, Connect RPC, git smart-HTTP) accepts `Authorization: Basic name:password` where the password is the principal's registry token and BOTH must match (`runkod/auth.go`, one resolver behind all three gates so authentication and identity can never disagree); the deploy token as password with any username remains the documented anonymous form. `GET /api/whoami` validates a credential and names the caller. The web UI signs in with name+password (stored per-browser, never build-time), derives the approver/lander identity server-side from the credential, and shows/clears the session in the sidebar. Still not an auth system — same shared-secret tokens, constant-time-compared, no hashing/rotation/issuance; the upgrade path to OIDC is unchanged (the login form swaps for a redirect; the resolver swaps token-compare for session validation; every enforcement point stays put).

**Invite requests (decided 2026-07-13).** The shared `--signup-code` is a code-gated deployment's only sign-up gate, but nothing answered "how does a stranger GET the code?" — the login gate dead-ended. Deployments running `--allow-invite-requests` accept a public `POST /api/invite-requests` (`{name, email, message}`) that stores a deployment-wide request row (no org — requests precede accounts), and `GET /api/auth/config` advertises `invite_requests_enabled` so the login gate offers "request access" exactly when the deployment can act on it. A `mailer/` service (`runko-mailer`, the watchdog deployment shape) polls the operator-only due feed and emails the operator over stdlib `net/smtp`, with `Reply-To:` set to the requester — replying with the code IS the fulfillment; retry state lives server-side on the row (the §14.4.1 webhook-outbox lifecycle). Abuse posture: body/field caps, a honeypot field (silent success, nothing stored), a per-IP window, a live-backlog cap, and one live row per email. Deliberately NOT an invitation system — no codes minted, no auto-reply, no reset; per-org email invitations remain the recorded follow-up, at which point this form becomes their front door.

### 15.2 AuthZ

| Level | Model |
|-------|--------|
| Org | Roles, AgentPolicy |
| Project | Owners, contributors; optional `visibility: restricted` (below) |
| Workspace | Principal + allowlist paths |
| Path | Owner rules; enforced at receive on snapshot/change refs (§12.4) |

**Read ACLs (previously underspecified — the model, honestly):**

- Default: **org-wide read**; enforcement effort goes to writes. This matches monorepo culture and keeps v1 simple.  
- Opt-in `visibility: restricted` projects are enforced at **every read surface or not at all**: Git fetch (via **per-principal Josh-filtered remotes**, §12.3 Phase B — the only Git-layer mechanism that survives a hostile client), search (Zoekt index filtering), orientation APIs (`list_projects` redaction), *and* diff/review UI. A restricted project that leaks through search is worse than no feature.  
- Agents inherit their principal's read scope; orientation responses are filtered server-side — an agent must not be able to enumerate restricted project names via `who_owns` probing.  
- Stage-0 multi-repo overlay (§18.1): per-source-repo read tokens, minimum scopes, **no write credentials at all**.  
- Hard limit, stated in docs: Git object sharing means restricted-read is **access control, not isolation** — orgs needing hard confidentiality boundaries keep a separate repo (NG7).

**Public read-only orgs (decided 2026-07-09).** An org may opt in to
anonymous read access via the org setting `public_read: true` — the
"open-source project hosted on Runko" posture, complementing the outbound
mirror (§18.6, which remains the zero-config public view). Semantics,
fail-closed by construction:

- **Anonymous callers are read-only, allowlist-scoped, on every surface.**
  Git: `upload-pack` only (clone/fetch; `receive-pack` still authenticates).
  REST: an explicit GET allowlist (changes, merge requirements, affected,
  projects, search) — never workspaces, settings, members, or mirror ops.
  Connect: the read-RPC allowlist (change/project/repo/search reads) —
  never workspace or write RPCs. Anything not allowlisted behaves exactly
  as before: 401.
- **Workspace snapshot refs are hidden from anonymous fetch**
  (`uploadpack.hideRefs refs/workspaces`, injected per-request for
  anonymous callers only) — snapshots are people's uncommitted WIP; the
  same principle that keeps them off the outbound mirror. `refs/for/*`
  (the rotating last-push ref) is hidden alongside; `refs/changes/*` stays
  public — a change under review is public by design in a public org.
- **`public_read` and `visibility: restricted` are mutually exclusive
  until §12.3 Phase B exists**: enabling the setting is refused
  (structured error) while any trunk manifest declares a restricted
  project, because restricted-read must hold at every read surface or not
  at all (above) and anonymous Git fetch has no per-principal filtering
  yet. Fail closed, loudly.
- Anonymous identity is nobody: no principal, no lane, no write
  attribution paths reachable. The setting is org-scoped, stored in org
  settings (tree-ownership caveat §9.4 applies as with global checks).
- **Known sharp edge (documented, not fixable server-side):** on a public
  org, a git client with URL-embedded credentials never receives the 401
  challenge that makes it send them — reads silently get the anonymous
  (WIP-hidden) view. Clients that need the authenticated advertisement
  force it: `http.proactiveAuth = basic` (git ≥ 2.46; `runko workspace
  create/attach` stamps it into every clone it makes) or
  `http.extraHeader`. Writes are unaffected — receive-pack still
  challenges.

Self-host: single-tenant. Cloud: per-tenant Git + object isolation.

### 15.3 Threat notes (agent-amplified)

- Stolen agent tokens → short TTL, narrow scopes, anomaly caps on write volume  
- Prompt injection → still enforce **server-side** path policy (never trust agent-claimed affinity alone)  
- Secret exfiltration via agent → overlay scanning, denylist paths  
- Manifest / owners tampering → `can_modify_owners: false` for agents by default  

---

## 16. Open source and self-hosting

### 16.1 License

**Apache-2.0** for monorepo OS including MCP server, agents protocol, web UI, deploy manifests.

### 16.2 Open

- Create project (intent pipeline) → workspace → change → land  
- Workspace glue CLI (upstream-Git configuration + snapshot refs); optional Josh slice integration  
- MCP + REST/gRPC  
- Helm + compose  
- Reference `AGENTS.md` generator and example agent policies  

### 16.3 Commercial

Managed cloud, support/SLA, compliance/SCIM/advanced audit—not a crippled CitC or agent API.

### 16.4 Self-host definition of done

No phone-home; compose eval; backup docs; schema upgrades; OIDC; MCP reachable inside the network.

**Compose eval profile — what the "&lt; 15 minutes" claim covers (§3.3):** API + web + MCP + Postgres + MinIO + Git volume + Zoekt (indexing async; search may lag minutes on first boot). **Excluded from the claim:** mirror service (opt-in wizard), Connect CI (its own wizard; &lt; 1 day bar), editor extension. The measured loop is `compose up → create project → edit → open Change → land` — run in CI on every release so the claim cannot rot.

---

## 17. Client experience

### 17.1 CLI

```bash
runko auth login
runko project create checkout-api --type service --template go-service --owners group:commerce
runko project add-capability checkout-api http
runko workspace create --project //commerce/checkout-api --name payments-fix   # worktree + sparse cone + registry row (§12.3, stage 12b)
runko workspace list        # my workstreams, their cones and base revisions
runko workspace snapshot    # WIP -> commit -> refs/workspaces/<id>/head (durable)
runko workspace attach <id> # restore a workspace on any machine from its snapshot ref
runko workspace watch       # auto-snapshot loop while dirty (§12.6) - feeds the live workspace view
runko change create -m "Reject invalid SKUs"
runko change push           # from any plain git checkout (wraps refs/for/main, §11.5)
runko change requirements   # owners + checks outstanding
runko change comment -m "…" [--file <path> --line <n>]   # anchored to current head (§13.4.1, stage 16)
runko change comments       # threads + resolved state (stage 16)
runko change request-review <principal>                  # feeds the attention set (§13.4.2, stage 16)
runko release create --project <p> [--version x.y.z]     # tag + changelog from landed Changes (§14.10.3, stage 17b)
runko release list --project <p>                         # immutable release records (stage 17b)
runko doctor                # remotes, hooks, personal cheat-sheet (§6.9)
runko mcp serve             # local MCP for coding agents
```

### 17.2 Web UI (UX-critical surfaces)

| Surface | Priority interactions |
|---------|----------------------|
| Home | Create project CTA, recent changes, owner attention inbox (derived attention set, §13.4.2) |
| Create project | 3-step wizard, live validation, preview files |
| Project | Capabilities as toggles, owners, open workspace, “copy MCP snippet” |
| Change | Scoped diff, agent badge, merge requirements, inline review threads (§13.4.1, stage 16b) |
| **Workspace** | Live per-branch WIP diff vs base + activity timeline + live status dot via `WatchWorkspace` (§12.6); branch ↔ stack chips (§12.2) |
| **Connect CI** | Wizard: pick CI system → template + webhook secret + watch first green check arrive (§14.13) |
| **Import** | `import plan` report review, owners-mapping fixes, shadow-CI parity dashboard (§18.3) |
| Settings | Templates, AgentPolicy, conventions doc |

### 17.3 Editor extension

- Attach workspace; affinity indicator  
- Merge requirements / owners  
- “Create project” mini-flow  
- One-click **configure MCP for this monorepo**  

### 17.4 MCP

**Rescoped (§8.3): a thin remote adapter, not the primary agent surface.** v1 ships exactly six read-only tools (`list_projects`, `get_project`, `search_code`, `who_owns`, `get_affected`, `get_merge_requirements`) over the same REST handlers the CLI uses (stage 16 graduates `list_change_comments` as the seventh, still read-only — §13.4.1). The full write-capable catalog (`create_project`, `create_change`, workspace tools, etc.) stays documented in `docs/spec/mcp-tools/catalog.json` as the **deferred v1.x contract** - schemas kept, not implemented, until there's a client that actually needs MCP for writes rather than a shell.

- Documented tool catalog with examples - six v1 tools; the rest annotated `deferred-v1.x`, not removed
- Idempotent creates where possible (moot for v1's read-only set; applies once write tools graduate from deferred)
- Pagination and compact list defaults for token efficiency

**Web frontend transport (DECIDED 2026-07-07, both halves): Connect over `proto/runko/v1/`.** The `.proto` schema for the web frontend ↔ `runkod` surface lives at `proto/runko/v1/`, covering the same concepts as this section's REST/MCP surface (projects, changes, merge requirements, workspaces, search, repo browsing) - see `proto/README.md` for the original rationale. Stage 13's frontend half (`web/`) consumes it via **Connect-ES** (generated protobuf-es types committed at `web/src/gen`); the server half is **connect-go handlers mounted on runkod's existing `net/http` mux** (`runkod/rpc.go`, generated stubs committed at `gen/runko/v1` - the `internal/dbgen` convention), behind the same bearer-token gate as `/api/*`, with permissive-origin CORS (auth rides the Authorization header, never cookies). Every RPC is a thin encoder over the same decision cores the REST handlers use (`runkod/actions.go`) - two transports, one set of gate semantics; errors carry `runko.v1.ErrorDetail` (the §6.5 shape) as a Connect error detail. Server-side additions the proto anticipated: `GetChangeDiff` (real `git diff -M` parsed into FileDiff/hunks/lines), `GetChangeStack` (derived B.base_sha == A.head_sha relation over the Store), `RepoService` tree/blob reads. Stage-18 additions (§12.6): `GetWorkspaceDiff`, `ListWorkspaceEvents`, and `WatchWorkspace` — the surface's first **server-streaming** RPC; frames carry event metadata only (pokes, never diff payloads) and clients refetch via the unary RPCs. This surface is NOT a replacement for the CLI/MCP's existing REST contract; `runko`/`runko-ci`/MCP stay on REST.

---

## 18. Migration and adoption (first-class; this is the adoption cliff)

> Greenfield-first would make our TAM ≈ "startups founded after our launch." For a 20–300-eng org, repo consolidation is the hardest part of going monorepo — history stitching, in-flight PRs, per-repo permissions → path owners, CI cutover, release tags. If §18 fails, nothing else in this document matters. Nx Polygraph's pitch ("monorepo benefits without migration pain") wins by default against any design that treats migration as a footnote.

### 18.1 Adoption strategy: mirror-first (Mode C is the front door)

Never ask an org to flip its system of record on day one. The adoption ladder:

| Stage | SoR | What the org gets | Risk taken |
|-------|-----|-------------------|-----------|
| **0. Read-only overlay** | GitHub | Project map, owners-coverage report, affected computation over existing repo(s); MCP orientation tools for agents | None — read-only install |
| **1. Mirror-first** | GitHub | Changes + review + merge requirements run on our platform; trunk mirrored bidirectionally; CI keeps running on GitHub via Mode C (§14.3) | Low — GitHub remains the escape hatch |
| **2. SoR flip** | Us | Trunk lives here; GitHub becomes the mirror (interop, ecosystem tools) | Real — taken only after stages 0–1 proved value |
| **3. Consolidation** | Us | Remaining repos imported with history as Projects | Incremental, per repo |

Value must be demonstrable at stages 0–1 **without migration**. This converts a bet-the-company decision into an incremental one — and is our direct answer to "synthetic monorepo" alternatives.

### 18.2 Greenfield

Template monorepo + first-project wizard + generated agent instructions. (Still the easiest path — just not the strategy.)

### 18.3 Consolidating many Git repos (productized, not documented)

`monorepo import` is a **product surface** with a dry-run report — not a wiki page of `git filter-repo` incantations:

1. **Plan**: `import plan <repo-url> --dest commerce/checkout` → report: history size, LFS objects, secret-scan hits, tag/release mapping, proposed Project + owners (derived from CODEOWNERS/teams), CI workflows detected  
2. **Import with history**: subtree merge with rewritten paths; original SHAs preserved in commit trailers for traceability; tags namespaced (`checkout-api/v1.2.3`)  
3. **Permissions mapping**: repo collaborators/teams → path owners on the imported root; gaps are blocking report items  
4. **In-flight work**: open PRs enumerated; tooling replays a PR branch as a Change on the imported path (best-effort; stragglers finish on the old repo during the shadow window)  
5. **CI shadow period**: old-repo CI keeps running while affected-driven checks come up on the monorepo; a parity dashboard (same commits, both pipelines) gates flipping required checks  
6. **Freeze + redirect**: old repo archived with a pointer; pushes rejected with the new path printed

### 18.4 Interop invariants during (and after) transition

- Mirror is transport, never a second SoR (§14.3)  
- `git clone`/fetch of the monorepo always works (§11.2) — and tree-as-truth (§10.3) means a mirror carries projects and owners with it  
- Per-repo → path-scoped access: orgs that used repo boundaries as ACLs get **read-visibility rules** at project granularity, with limits documented honestly — Git object sharing makes confidentiality boundaries weaker than separate repos (NG7 still holds)

### 18.5 Sequencing

Stage 0–1 tooling (read-only overlay, bidirectional mirror, `import plan` report) ships **with** review/CI in Phase 1 (§19.2) — not after it. `import` execution hardening lands in Phase 2.

### 18.6 Mirror protocol invariants (v0 — full protocol in the Migration RFC, §26)

The bidirectional mirror is the highest-risk component in the adoption ladder. Whatever the final protocol says, these invariants hold:

1. **Single writer per ref, enforced by lease.** At any moment exactly one side may write a given ref namespace. Stage 1: GitHub owns `main`; we own change-ref shadows and mirror-managed branches. Stage 2: inverted.  
2. **Landing onto a GitHub-SoR trunk is a push, with races expected.** Stage-1 land = platform pushes the rebase-land commit to GitHub `main` (GitHub App auth, force-with-lease semantics, bounded retry). A lost race re-runs §13.5 revalidation on the new tip — never force-push over someone else's merge.  
3. **Externally-landed work becomes a closed Change, not a conflict.** PRs merged natively on GitHub during stage 1 ingest as `external` Changes with attribution from PR metadata, so owners-coverage and audit stay complete (§8.10).  
4. **Divergence freezes landing, loudly.** If mirror cursors disagree with observed refs (non-fast-forward surprise, deleted branch), the platform freezes its own landings on the affected refs and alerts — **no automatic reconciliation, ever**. Unfreezing is an explicit admin action with a diff report.  
5. **Mirror state is rebuildable.** Cursors/ref-maps live in Postgres (§9.2) but re-derive from the two Git histories; restoring a mirror never requires guessing.

---

## 19. Phased delivery

> **Scope discipline:** an earlier draft's Phase 1 contained three products (workspace plane, review system, CI plane). Each phase below has **one headline loop** it must prove. Migration/mirror tooling is *in* the early phases (§18.5) — it is not polish. Workspace-plane depth is deliberately late, per §4.5.

### 19.1 Phase 0 — Project model + create UX (on a plain Git repo)

**Loop proven:** create/adopt projects, owners coverage, affected computation — usable against an existing repo, read-only-safe (adoption stage 0).

- Control plane as **index of the tree** (§10.3): org, monorepo, project registry, owners  
- **Intent-based `create_project` + templates + preview**  
- Minimal on-disk manifest (L0/L1 only)  
- Git MonorepoStore + compose; **Zoekt index**  
- CLI + basic web wizard  
- Read-only orientation MCP (`list_projects`, `get_project`, `who_owns`, `search_code`)  
- Affected API (paths → projects → declared dependents)  

### 19.2 Phase 1 — Changes + CI contract (the merge-confidence release)

**Loop proven:** open a Change **from a raw `git clone`** (§11.5), the right checks run on the affected set, owners gate, land safely. The magic-ref write path is the Phase-1 client story — **no workspace plane is required for this loop**; workspaces are additive.

**Core (launch gate):**

- Change review/land + human owner gates; **stable change IDs + `depends_on` in the data model** (§7.4); **trunk closed to direct push** with the §6.9 rejection UX  
- Plain-Git write path: `refs/for/<trunk>` + `Change-Id` trailer + `runko change push` (§11.5)  
- Optimistic land with revalidation (§13.5)  
- **CI integration plane v1:** signed webhooks (`change.*`), Checks API (incl. check-set policies + rerun-requests, §14.4.2), affected API, change git refs, partial clone  
- **`runko-ci` CLI/image:** checkout-change, affected, report-check  
- **Tier 1 template:** GitHub Actions — **Mode C mirror topology first** (it doubles as the adoption path, §18.1) + generic shell  
- **Mirror-first adoption stages 0–1** (§18.1): read-only overlay + bidirectional mirror (invariants §18.6) + `import plan` report  
- **MCP:** + create project, change; `get_merge_requirements` / `get_affected`  
- Generated `AGENTS.md`  

**Stretch (first fast-follow — slips before anything above does):**

- Workspace attach v0: Scalar-class (partial + sparse via platform config) + overlay snapshots — **carried as DAG stage 12b** (§28.3); MCP workspace tools stay deferred with the v1.x catalog (§8.3)  
- **Connect CI** minimal wizard (core ships the docs-generated bootstrap only)  

**Dogfood** platform on itself **with real required checks** from GHA—not mock-only gates. Use a coding agent via MCP in dogfood.

### 19.3 Phase 2 — Agent policy + workspace plane proper + **CI Tier 1 complete**

**Loop proven:** an autonomous agent works inside a policy-enforced workspace; its Change is attributable, capped, and human-gated.

- AgentIdentity + AgentPolicy enforcement on writes (server-side affinity, caps)  
- Attribution and agent-assisted labels; audit/session linkage  
- **Workspace glue GA:** snapshot refs + receive-time policy enforcement; **Coder/devcontainer environment templates** for headless agent VMs (we don't operate VM fleets — §12.3); optional **Josh slices** for restricted-visibility orgs  
- Capability add flows (L2) without raw YAML  
- Owner coverage and merge-requirements UX; **global approvers + mechanical-change policy** (§7.3)  
- Editor extension + “configure MCP”  
- **Native GHA Action(s) + Buildkite plugin** wrapping `runko-ci`  
- **Connect CI wizard** polished; webhook delivery dashboard  
- Project default checks from templates  
- Sparse-checkout API consumed by `runko-ci`  
- `import` execution hardening (history, tags, permissions mapping, CI shadow — §18.3)  
- **Build-graph adapter** (§14.5.4): engine contract + Bazel implementation in `runko-ci` (DAG stage 9b)  

### 19.4 Phase 3 — Stack UX + scale polish + **CI Tier 2**

- **Stacked-change UX** (restack, cascade land) on the Phase-1 data model  
- **Merge queue** as batching/pipelining of the §13.5 revalidation rule  
- Inferred-dependency indexer UI (advisory → promote-to-declared flow, §13.3)  
- Graph-aware prefetch  
- GitLab CI + Jenkins shared library templates  
- Optional change bundle endpoint for faster CI fetch  
- CD example hooks (`change.landed` → sample Argo/GHA deploy)  

### 19.5 Phase 4 — Ecosystem + demand-driven depth

- Virtual FS: **adopt-only, likely never** (§12.3) — revisit only on multi-org storage-mechanics telemetry  
- Stronger forge mirrors (hybrid GHA-2 topology polish); SoR-flip + consolidation tooling at scale (§18 stages 2–3)  
- **Buck2 engine** for the §14.5.4 adapter contract (second implementation proves the interface)  
- Pluggable code search backends beyond the Zoekt default  
- Demand-driven Tier 3 CI examples  

---

## 20. System quality attributes

| Attribute | Approach |
|-----------|----------|
| **Usability** | Interaction specs; ceremony budget; progressive disclosure |
| **Agent ergonomics** | Compact tools; structured errors; mandatory affinity |
| **Performance** | Cache; prefetch; no full worktree default |
| **Reliability** | Stateless API; backup Git/Postgres/objects |
| **Safety** | Server-side path policy; agent caps; audit |
| **Operability** | Metrics: create-project time, workspace attach, agent deny rates, change land rate, repo size |

---

## 21. Competitive and prior-art landscape (2026)

### 21.1 The primary competitor: the assembled stack

Not any single vendor — the combination a platform team can assemble on GitHub today (§2.2). Per-pillar view, with why we still win (or must):

| Pillar | Best-of-breed on GitHub | Their gap (our wedge) |
|--------|-------------------------|------------------------|
| Project model / affected / generators | **Nx, moonrepo, Pants** — project graph, `affected`, generators, tags, native MCP | Advisory and build-tool-scoped: not wired to merge gates or server enforcement; per-ecosystem silos. We make the same concepts **authoritative** across the change lifecycle |
| Change-centric review + stacks + queue | **Graphite** (stack-aware queue, AI review), **Aviator** (affected-target queues), GitHub native stacked PRs / merge queue | Bolted onto the PR/branch model; ownership stays CODEOWNERS-theater; monorepo scoping is heuristic. We own change identity, so scope/owners/checks are **facts, not inference** |
| Agent governance | **GitHub Agent HQ** — identity, mission control, audit, MCP registry, AGENTS.md | **Repo-granular** (§8.9). No sub-repo write affinity, path policy, or project map. Our unit of control is the project |
| Thin checkout | **Scalar / upstream Git** — partial + sparse + fsmonitor + maintenance | Client config, not a product: no cloud overlay, policy, workspace identity, or agent limits. We build only that delta (§12.1) |
| Monorepo-without-migration | **Nx Polygraph** synthetic monorepos — cross-repo graph, agent memory | Keeps polyrepo forever: no atomic changes, single trunk, or unified review. Our counter is mirror-first adoption (§18): monorepo benefits with staged, reversible risk |

**Structural weakness we exploit:** five vendors, five config surfaces, five agent stories — all advisory. **Structural risk we accept:** each layer is individually "good enough," and GitHub can vertically integrate any of them. The bet only pays if enforcement + integration + sub-repo granularity show up as one coherent product early (§19).

### 21.2 VCS-layer prior art (adopt or learn from; mostly not compete)

| System | Status (2026) | Our relation |
|--------|---------------|--------------|
| **Jujutsu (jj)** | Git-compatible, change-centric (stable change IDs, working-copy-as-commit); Google building an internal cloud-backed server on it | Closest philosophical relative at the client layer. We adopt its change-ID discipline (§7.4); **DECIDED 2026-07-08: jj (colocated) is the primary client. REPOSITIONED 2026-07-11: the `runko` CLI is the primary interface for basic operations everywhere — create/push/land/snapshot/sync — and jj (colocated) is the SURGICAL client** (mid-stack rework via `jj edit`/`jj squash`, `jj split`, history diagnosis): one product should not teach two everyday command languages, and sequential `runko change create` stacks naturally. The jj machinery is unchanged and stays first-class where it is unmatched — descendant auto-rebase is still the evolve story, Change-Id trailers derive from jj change ids via `templates.commit_trailers`, and the receive funnel's series processing turns one tip push into a whole-stack update. Plain git remains the fully-supported parity path (§6.9); watch for jj-native forges as future competitors |
| **Josh** | OSS filtered-view Git proxy (`workspace.josh`); adopted by the Rust project | Prior art for "visible monorepo, materialize a slice, push maps back" with no FS driver. Adopt-vs-build evaluation for the workspace read path (§12.3) |
| **Scalar / VFS for Git** | VFS abandoned; Scalar upstreamed into Git | The decisive lesson for §12: sparse + partial + fsmonitor beat virtualization. FUSE is demand-gated only |
| **Sapling / Mononoke / EdenFS** | Client OSS and supported; server + VFS code public but **explicitly unsupported** for external production | Validates the gap we fill — no self-hostable CitC-class *product* exists. EdenFS ideas inform Phase B, if ever |
| **Gerrit** | Change-centric review at monorepo scale (Android/Chromium); migrated review metadata from SQL into Git (**NoteDb**) | Two lessons adopted: change-centricity works at scale; **the tree/repo must be the source of truth** (§10.3). One lesson rejected: hostile UX as the price of rigor |
| **CitC / Piper** | Google-internal | Workspace contract inspiration, re-scoped to medium orgs on Git |
| **Boq-class platforms** | Google-internal | Service identity inspiration; **anti-pattern** for configuration UX (§2.3) |

### 21.3 Adjacent (integrate, don't fight)

| System | Relation |
|--------|----------|
| **Namespace / BuildBuddy / Aspect / EngFlow** | Remote build/compute; we emit affected sets they consume |
| **Cursor / Copilot / Claude Code / Devin-class** | Clients of our MCP/workspace — never competitors to replace |
| **Gitea / Forgejo** | General self-hosted forges; polyrepo-shaped; possible mirror targets |
| **GitLab** | Forge + CI; Tier-2 CI integration target; the self-host incumbent in our ICP |

---

## 22. Risks and open questions

### 22.1 Risks

| Risk | Mitigation |
|------|------------|
| Re-creating Boq config hell | Ceremony budget; intent API; no required L2 fields |
| Workspace scope creep back toward custom storage | §12.1 stance: no CAS/overlay plane, ever; snapshot refs only; virtual FS adopt-only |
| Agents ignore tools and edit raw Git | **Trunk closed to direct push** (§7.4); change refs are the only write path; break-glass audited |
| Prompt injection bypasses “instructions” | Server-side allowlists always |
| MCP surface sprawl | Small stable core tools; versioned schemas |
| UX under-invested vs backend | Phase 0 includes wizard; dual-audience review in design process |
| Scope creep to full AI IDE | Integrate agents; do not build the model product |
| **CI integration too thin → no adoption** | Phase 1 contract + `runko-ci` + dogfood with real checks; Tier 1 plugins as launch gate |
| **N CI plugins unmaintainable** | Portable `runko-ci` core; thin native wrappers only for Tier 1–2 |
| **Hybrid GitHub mirror dual-SoR confusion** | Document topologies; merge gates read **platform** Checks |
| **Affected wrong → silent bad land or CI rage** | Conservative `run_everything`; declared-only gating (§13.3); visible computation reasons on Change |
| **The assembled stack is “good enough”** (GitHub + Nx + Graphite/Aviator + Agent HQ) | Win on enforcement + integration + sub-repo granularity (§21.1); mirror-first adoption (§18) removes migration as the counter-argument |
| **GitHub commoditizes agent governance** (Agent HQ) | Differentiate on project-granular server-side enforcement (§8.9); treat anything expressible at repo granularity as already commodity |
| **Migration cliff caps TAM** | §18 mirror-first ladder: stage 0–1 value while GitHub stays SoR; `import` as a product surface with dry-run reports |
| **Land races degrade trust past ~50 eng** | Conflict-only landing by default + trivial-rebase carry-forward (§13.5, the Gerrit model): green work lands with zero re-runs across trunk movement and results/approvals survive trivial rebases; orgs opt in to affected-intersection/always tiers; post-land CI on the landed tree is the semantic-conflict net; the v1.x merge queue batches whichever tier is configured |
| **Cross-cutting changes taxed by strict owners** | Global approvers + mechanical-change policy (§7.3) |

### 22.2 Decisions taken in this revision (were open questions)

| Was open | Decision |
|----------|----------|
| Land policy: rebase vs merge | **Rebase-based landing; linear trunk** (§7.4) |
| Direct `git push` to trunk | **Closed by default; change refs only; audited break-glass** (§7.4, §11.2) |
| Inferred-deps trust for affected CI | **Advisory-only in v1; gates use declared edges + path facts** (§13.3) |
| FUSE: build vs adopt | **Demand-gated; evaluate Josh/jj adoption first** (§12.3) |
| Required-checks matrix conventions | **Check-set policies (`unit:* over affected`)** (§14.4.2) |
| Source of truth for projects/owners | **The tree; control plane is a rebuildable index** (§10.3) |
| Workspace substrate | **Upstream Git glue + snapshot refs; no custom CAS/overlay plane** (§12.1) |
| Monorepo slices / restricted reads | **Adopt Josh-proxy as an optional capability** — not the default path (SHA identity, §12.3 Phase B) |
| Remote/agent VMs | **External by contract** (Coder/devcontainer templates); we never operate VM fleets (§12.3) |
| Virtual FS | **Adopt-only, likely never** (§12.3) — hardened from "demand-gated build" |
| Git hosting substrate | **Bare repo + smart-HTTP + our receive hooks** — not a forge (Gitea/Forgejo stay mirror targets, §21.3): the write path *is* the product |
| Product **name** | **Runko** (§1) — CLI `runko`, env `RUNKO_*`, CI CLI `runko-ci`; registries clear at decision time; formal TM clearance before public launch |
| MVP web stack | **React + TS + Vite + Connect-ES over `proto/runko/v1`** (§17.4; superseded the original SSR+htmx call 2026-07-07 when the frontend/backend transport was directed to gRPC/Connect — see changelog) |
| Build-graph integration | **Runner-side adapter contract; Bazel first, Buck2-shaped** (§14.5.4). Platform floor stays paths + declared deps (NG4 intact); engine output refines CI scope by default, gates only by org opt-in; every engine failure ⇒ `run_everything` |
| Build-system opinionation | **Opinionated by criterion, not mandate** (§14.5.4): engine status requires declared + hermetic-at-SHA + rdeps-queryable (Bazel ✓, Buck2 ✓; task runners never); greenfield golden path `build_discipline: hermetic` with generated BUILD files; org opt-in `require_build_binding` merge gate; brownfield adoption never gated on a build migration (§18 cliff rule) |
| Agent interface | **CLI-first (primary); MCP = thin remote adapter, 6 read-only tools over REST** — write tools deferred to v1.x; single schema contract for CLI JSON and MCP (§8.3) |
| Bot auto-land (was open question: agent land exceptions) | **Path-scoped bot lanes** (§14.10.2): AgentIdentity + `can_land_changes` constrained to a path allowlist + required-check set — built for GitOps writers (image bumpers, Renovate); enforced in stage 11c |
| Tag-namespace governance (was open question #10) | **Org release role + tag-namespace-scoped bot lanes + server-created release tags** (§14.10.3, 2026-07-10): `refs/tags/*` gated at receive; releases are immutable first-class objects whose changelogs derive from landed Changes; publishing stays customer CI. Implementation = stages 17/17b |
| K8s operator boundary (future) | **CRDs/Helm own infrastructure shape; the tree owns policy** (§9.4) — org policy never moves into CRDs (tree-as-truth, §10.3) |

### 22.3 Open questions

1. Exact **PROJECT.yaml** minimal schema and generated-file layout — **pre-session-1 blocker** (§28.4)  
2. Codegen marker conventions and enforcement strength  
3. IdP group sync vs local groups  
4. Standard for agent metadata (model name, tool versions) on Changes  
5. **GHA topology default for greenfield** (mirror-first is already the default for *migrating* orgs, §18)  
6. Whether to ship an optional **webhook→provider bridge** service in-tree or docs-only  
7. Post-submit vs presubmit policy defaults for `change.landed` pipelines  
8. Global-approver granularity: org-wide role vs per-domain (e.g. `//infra` global approvers) (§7.3)  
9. jj as a supported client in v1.x: how much workspace-agent scope does it absorb? (§21.2)  
10. ~~Tag-namespace governance mechanics~~ — **decided 2026-07-10** (§14.10.3, §22.2): org release role + tag-namespace-scoped bot lanes + server-created release tags; number kept so later citations stay stable  
11. **Check intelligence** (recorded 2026-07-10, Nx-parity research): platform-side flaky detection/stats over the check-run history we already own, bounded auto-rerun via the §14.4.2 rerun verb, and a self-healing-CI webhook contract (check-failed → fix-it bot lane, §14.10.2 pattern). Detection/analytics is a compatible extension of §14.2's "flaky retry UX at runner layer" row, not a reversal — execution stays with the runner  
12. **Boundary conformance** (recorded 2026-07-10): opt-in generated check asserting observed imports ⊆ declared `dependencies:`, using the §13.3 inference engine as the checker — a red check on the Change, never an affected-graph input, so §13.3's advisory-only decision is intact  

---

## 23. Appendix A — Example flows

### A.1 Human: first service without manifest pain

```text
1. Web: Create project → Service → name "checkout-api" → owners group:commerce → template Go
2. Preview shows 8 files to write; confirm
3. Workspace opens on //commerce/checkout-api
4. Human (or agent in editor) implements handler code
5. Create Change → owners notified → CI on affected → land
```

No hand-written platform YAML.

### A.2 Coding agent: feature in existing project

```text
1. Agent: list_projects / get_project(checkout-api)
2. Agent: create_workspace(project_ids=[checkout-api])
3. Agent: implement feature within allowlist
4. Agent: create_change(description, test_plan)
5. Agent: get_merge_requirements → tells human what approvals remain
6. Human owner reviews (agent-assisted badge) → land
```

### A.2b Coding agent: reviewer, not approver (§13.4.1)

```text
1. change.opened webhook → review agent fetches the scoped diff (GetChangeDiff)
2. Agent: runko change comment --file commerce/checkout/sku.go --line 42 -m "…"
   (anchored to head_sha, agent badge — approval is structurally impossible)
3. Author amends → comments mark outdated, attention returns to reviewers (§13.4.2)
4. Human owner approves; land
```

### A.3 Coding agent: new library the right way

```text
1. Agent: create_project({name, type: library, template, owners})
2. Platform generates PROJECT.yaml + stubs
3. Agent edits library code only
4. create_change → review → land
```

Agent never authors a multi-section platform manifest from memory.

---

## 24. Appendix B — Glossary

| Term | Meaning |
|------|---------|
| **CitC** | Client-in-the-Cloud workspace model |
| **Progressive disclosure** | Show only config needed for the current layer (L0–L3) |
| **Intent** | High-level create/update request; platform generates files |
| **Capability** | Opt-in project feature (rpc, http, deploy, …) |
| **MCP** | Model Context Protocol; agent tool surface |
| **Agent identity** | Non-human principal subject to AgentPolicy |
| **Affinity** | Projects/paths a workspace may materialise and write |
| **Ceremony budget** | Product constraint on required fields/steps for default paths |
| **MonorepoStore** | Storage abstraction (Git in v1) |
| **CI integration plane** | Webhooks, Checks API, affected API, git fetch contract—not runners |
| **runko-ci** | Portable CLI/image for checkout, affected, check reporting |
| **Check / CheckRun** | External CI result attached to a Change; drives merge requirements |
| **run_everything** | Affected flag forcing full/heavy CI when subsetting is unsafe |
| **Connect CI** | In-product wizard to wire a CI system to the monorepo |
| **Global approver** | Org role whose approval satisfies owner requirements repo-wide for cross-cutting changes (§7.3) |
| **Mechanical change** | Codemod/rename/format Change with tool attestation; relaxed per-directory owner requirements (§7.3) |
| **Mirror-first adoption** | Onboarding ladder where GitHub stays SoR while Changes/review/affected run on the platform (§18) |
| **Check-set policy** | Merge requirement over a family of checks (e.g. `unit:*` across all affected projects) (§14.4.2) |
| **Optimistic land** | Rebase-land without re-running checks when the trunk delta doesn't intersect the affected set (§13.5) |
| **Tree-as-truth** | Durable org structure (manifests, owners) lives in Git; control plane is a rebuildable index (§10.3) |
| **Magic ref** | `refs/for/<trunk>` push target that creates/updates a Change from any plain Git client (§11.5) |
| **External Change** | Mirror-ingested work landed natively on the GitHub SoR during stage 1; closed with preserved attribution (§18.6) |
| **Snapshot ref** | `refs/workspaces/<id>/head` — workspace durability as real commits through the receive funnel (§12.2) |
| **Environment contract** | Requirements a remote dev/agent VM image must satisfy; fulfilled by Coder/devcontainer templates (§12.3) |
| **Josh slice** | Optional per-principal filtered remote (rewritten SHAs) for restricted reads, slice-as-repo, and import sync (§12.3 Phase B) |
| **Build-graph adapter** | Runner-side engine plugin (Bazel now, Buck2 planned) refining affected to target level under the §14.5.4 contract; fail-closed to `run_everything` |
| **Affected refinement** | Adapter-posted target-level narrowing of a Change's affected set; CI-scoping by default, gate-grade by org opt-in (§14.5.4) |

---

## 25. Document history

| Date | Change |
|------|--------|
| 2026-07-06 | Initial draft (monorepo-first, CitC, Git, OSS/self-host, CI integration) |
| 2026-07-06 | UX as primary constraint; anti-Boq progressive manifests; first-class agentic coding (MCP, policy, attribution) |
| 2026-07-06 | CI/CD integration plane expanded: contracts, runko-ci, plugins/templates, Tier matrix, checkout, Connect CI wizard; Phase 1 launch gate |
| 2026-07-06 | Competitive-review revision: assembled-stack framing (§2.2, §21); tree-as-truth inversion (§10.3); mirror-first migration ladder (§18, G13); cross-cutting ownership (§7.3); change IDs + stacks-in-data-model + rebase-land + trunk-closed (§7.4); declared-only affected gating (§13.3); optimistic land (§13.5); check-sets, re-runs, TTL (§14.4.2); FUSE demand-gated with Josh/jj evaluation (§12.3); scale-honesty check (§4.5); phases resequenced (§19); naming flagged (§1) |
| 2026-07-06 | Review-response revision: §1 compressed to 3 pillars; top-5 dogfood metrics (§3.3); closed-trunk human UX (§6.9); plain-Git write path via `refs/for/*` (§11.5); dual governance during mirror stage (§8.10); mirror service in architecture (§9.1) + protocol invariants (§18.6); snapshot-first overlay sync (§12, §19.3); §14.5.3 aligned to declared-only gating; read-ACL model (§15.2); compose-eval scope for the 15-min claim (§16.4); Connect CI / Import client surfaces (§17.2); Phase 1 split into core/stretch (§19.2) |
| 2026-07-06 | Substrate-radicalization revision: custom CAS/overlay plane **deleted** — workspaces are upstream-Git glue with durability as snapshot refs through the §11.5 receive funnel (§9, §12); remote/agent VMs external via environment contract (Coder/devcontainer templates); Josh-proxy adopted as *optional* capability (restricted reads §15.2, slice-as-repo, import sync) — not the default path (SHA identity); virtual FS hardened to adopt-only-likely-never; Gitea/Forgejo-as-host **rejected** (write path is the product; they remain mirror targets); decisions table extended (§22.2) |
| 2026-07-06 | Named **Runko** (rejected: banyan, cambium, pando, stemma — all hard collisions); full `maas`→`runko` rename incl. `RUNKO_*` env contract and `runko-ci`; **Appendix D added**: token-efficient implementation strategy (per-component budget, 7 standing rules, 15-stage session DAG, pre-session-1 checklist, session anti-goals); §22.2 + §22.3 + §26 updated (naming resolved; spec artifacts #2/#3/#8 marked pre-session-1 blockers; MVP web stack decided SSR+htmx) |
| 2026-07-06 | **Build-graph adapters promoted** from Tier-3 template to first-class contract (§14.5.4): runner-side only (daemon never runs customer tooling), Bazel first / Buck2-shaped, declared-not-inferred trust class (gate-eligible by org opt-in, refining §13.3's floor without reopening it), fail-closed to `run_everything`; new `build` capability (§7.2); Bazel → Tier 2 with Tier-1 pull trigger (§14.7); DAG stage 9b + budget row (§28); adapter contract spec added to §26 |
| 2026-07-06 | **Build-system opinionation codified** (§14.5.4, NG4 refined, §13.5, §22.2): engine admission by criterion (declared + hermetic-at-SHA + rdeps) — Bazel/Buck2 in, task runners permanently out; greenfield golden path `build_discipline: hermetic` (templates generate all BUILD files); org opt-in `require_build_binding` merge gate; hard platform-wide mandate **rejected** — brownfield adoption is never gated on a build-system migration |
| 2026-07-06 | **DAG revised after stages 0–9 shipped** (§28.3, §28.4): completed stages collapsed to a history note; new stage 9a (hardening: live-Postgres tests, stage-8 check-set fixes, CLI resolve-or-explain error UX, git ≥ 2.40 gate), 9c (opinionation mechanics), and explicit stage 10 `runkod` daemon assembly (smart-HTTP + pre-receive wiring + gitleaks scanner — previously implicit); MCP/Zoekt/web/compose renumbered 11–14; dogfood is stage 15 with a recorded Bazel-migration decision point; pre-stage checklist reduced to one blocker (adapter contract spec, §26 #13) |
| 2026-07-07 | **Agent interface decided: CLI-first, MCP rescoped to a thin remote adapter** (§8.3, §8.8, §17.4, §22.2): four reasons recorded (context economics, composability, empirical - Runko itself was built via CLI with zero MCP calls, and the moat is server-side receive-time enforcement, not protocol-side); MCP v1 shrinks to six read-only tools (`list_projects`, `get_project`, `search_code`, `who_owns`, `get_affected`, `get_merge_requirements`) over the same REST handlers, no write tools until a real remote-write client need materializes; full catalog kept as the deferred v1.x contract, not deleted; single-contract rule ties CLI `--json` output to the same `docs/spec/mcp-tools/`/`docs/spec/webhooks/` schemas; §28.3 DAG stages 11/12 swapped and rescoped (Zoekt + AGENTS.md generator first, now teaching the CLI as the agent interface; MCP thin adapter second) |
| 2026-07-07 | **DAG amendment: stage 11b, land wiring through the daemon** (§28.3, §28.4 budget table) - caught in review after stage 11 shipped: `land.Land`/`NeedsRevalidation` (stage 7) were fully built and race-tested, and the merge-requirements gate (stage 8) was fully built, but nothing in `runkod` ever called either - the daemon had a REST API for changes/checks/affected/merge-requirements/search but no `/land` endpoint at all, meaning stage 14's `create → change → land` loop had no wire-level "land" verb to invoke. Same class of gap as the (already-fixed) implicit-daemon-assembly gap stage 10 closed: a load-bearing integration living in no stage's done-when bar. Inserted as its own stage between 11 and 12 (deps 7, 10; blocks 13's land button and 14's loop) rather than silently folding into a later stage, so the gap can't recur unnoticed |
| 2026-07-07 | **Web-frontend transport directed to gRPC; draft protos landed** (user direction; §17.4 note, `proto/runko/v1/`): `ProjectService`/`ChangeService`/`WorkspaceService`/`SearchService` mirroring `docs/spec/mcp-tools/common.schema.json` $defs field-for-field (single-contract rule extended to a third encoding), `buf lint`+`build` clean, Connect recommended over grpc-web+Envoy (browsers can't speak raw gRPC; Connect serves gRPC/gRPC-Web/JSON from one `net/http` server - no new infra process, matching the Zoekt/gitleaks posture). Draft status: schema only, no server/codegen; `ListChanges`/`AbandonChange`/`RerunCheck` assume stage 12c-③'s REST endpoints; supersedes §28.2's SSR+htmx call for stage 13 pending final confirmation at implementation (see `proto/README.md` open questions). Two agents drafted concurrently and collided mid-write; reconciled into one canonical set (this row is the record of that, not a decision change) |
| 2026-07-07 | **DAG amendment: stage 12c, control-plane hardening** (§13.5, §15.1, §28.3) — a deliberate pre-UI audit of the daemon (user-directed) found the §13.5 human gate survivable by amend (approvals keyed to the Change, check runs to `(change, head_sha)` — approve v1, amend v2, land once checks re-green), and three stage-6/8 enforcement mechanisms fully built but unreachable at the wire: receive-time `AgentPolicy` (no principal ever populated), `checks.RerunCheck` + the rerun webhook (no endpoint), check-staleness TTL (never consulted). Decisions recorded: approvals bind to the approved `head_sha` (§13.5 — server-side land rebase exempt by construction since the gate precedes it; trivial-rebase relaxation is v1.x org opt-in); interim named-token principal registry (§15.1 — generalizing bot-lane tokens, NOT an auth system, OIDC unchanged as the real answer). Also: `GET /api/changes` + abandon verb (UI day-one needs), `/healthz` + graceful shutdown. Same insertion pattern as 9a/11c: harden while the surface is small, before the UI multiplies callers |
| 2026-07-07 | **GitOps-consumer story + workspace restoration revision**: §14.10 expanded (14.10.1 ArgoCD/Flux read-side recipes + affected-scoped CD + mirror-first CD continuity; 14.10.2 **bot lanes decided** — path-scoped auto-land for GitOps writers, resolving the former "agent land exceptions" open question; 14.10.3 tag-ref governance flagged as documented v1 permissiveness, §11.4 + new open question); **stage 11c added** (merge policy wiring — 11b review found required checks derived from posted runs and owners `nil`, so unchecked/unapproved Changes land; default-deny outside eval mode decided); **stage 12b restored** (workspace glue v0 — silently dropped in the 2026-07-06 DAG revision; multi-workstream-as-worktrees documented in §12.3, CLI surface in §17.1, `refs/workspaces/*` receive enforcement scoped); **§9.4 added** (k8s alignment both directions; CRD-vs-tree guard decided: infrastructure shape in CRDs/Helm, policy in the tree); 13/14 deps updated to include 11c |
| 2026-07-07 | **Stage 13 frontend half shipped; Connect confirmed client-side** (§17.2, §17.4, §28.3): `web/` (React + TS + Vite) consumes `proto/runko/v1/` via Connect-ES — generated protobuf-es types committed (`web/src/gen`, the `internal/dbgen` convention), `buf lint` clean, field numbers now wire-frozen by a real consumer. §17.2's Change surface implemented as **stacked diff views in Graphite's (graphite.dev) design language** per user direction: changes inbox grouped into stacks (rail + per-change status dots, trunk at bottom), per-change scoped diff, §13.5 owner/check gates with approve/rerun, land gated on the server-reported `mergeable`. Proto extended first (spec-before-code): `GetChangeStack` (derived relation B.base_sha == A.head_sha) + `GetChangeDiff` (base..head = exactly the stacked Change's own delta) + diff shapes. No Go Connect server yet — frontend runs on an in-memory fake transport (same generated types, mutation semantics mirroring runkod's, vitest-pinned) until runkod mounts connect-go handlers (stage 13's remaining half); this supersedes §28.2's SSR+htmx call for stage 13, as the 2026-07-07 gRPC row anticipated. CI gained a `web` job (`npm run check`: tsc + oxlint + vitest + build); `make check-web` added |
| 2026-07-07 | **Stage 13 server half shipped: connect-go handlers in runkod; Connect DECIDED both halves** (§9.1, §17.4, §28.3; closes proto/README.md items 1-4): `runkod/rpc.go` mounts all six `runko.v1` services on the daemon's existing mux (generated stubs committed at `gen/runko/v1`, local protoc-gen-go/protoc-gen-connect-go via `proto/buf.gen.yaml` — the remote-plugin draft needed buf.build network access and put output one directory off its own `go_package`), behind the same bearer token as `/api/*` plus permissive-origin CORS (auth is header-borne, never cookies). **Anti-drift refactor**: approve/land/rerun/abandon/workspace REST handlers' decision logic extracted into shared cores (`runkod/actions.go`, `apiError` = status + §6.5 clierr shape) so REST 409s and Connect FailedPreconditions are one computation; errors carry `runko.v1.ErrorDetail` as a Connect detail. New server capability the proto anticipated: `GetChangeDiff` (`git diff -M` parsed to FileDiff/DiffHunk/DiffLine incl. renames/binary/hunk line math, table-pinned), `GetChangeStack` (derived stack over the Store), `RepoService` (`ls-tree -l` + blob reads, NUL-byte binary heuristic, 1 MiB truncation). Web: `/demo/*` now mounts the fake-transport demo scene under its own basename (badge cross-links), root app talks to a real runkod via `VITE_RUNKO_URL` + `VITE_RUNKO_TOKEN`. **Full stack verified end-to-end**: real daemon + real `git push` seed + Vite + headless Chromium clicking approve→land (`web/scripts/fullstack.mjs`) — trunk ref confirmed advanced via `ls-remote`, demo confirmed bleed-free; 11 Connect integration tests + 2 diff-parser tests in `runkod/` |
| 2026-07-07 | **HTTP Basic sign-in over the principal registry** (§15.1, user direction "full auth, Basic for now"; post-stage-13): one credential resolver (`runkod/auth.go`) behind REST/RPC/git accepts `Basic name:password` (both must match a principal - a name cannot claim another's credential, a credential cannot claim another's name) alongside the existing bearer forms; `GET /api/whoami` names the caller; the web UI gains a real sign-in gate (per-browser credential, server-derived approve/land attribution, sign-out) replacing the paste-a-token affordance. Verified by a full browser drive: gate → bad password rejected → sign in → approve with NO client-asserted approver → land attributed to the principal → sign out; §13.5's agent-approval denial holds through Basic identically to bearer |
| 2026-07-08 | **Bazel by default** (user direction; §14.5.4, §28.3 stage 9c's opinionation lever pulled): every built-in template now carries the `build` capability as its default, so a bare `runko project create` (CLI, UI, RPC) emits generated `BUILD.bazel` wiring + `capability_config.build` ({engine: bazel, target_patterns: [//<path>/...]}) with zero hand-authored lines - `runko-ci affected --engine bazel` can refine from day one. Opt-out preserved: an explicit (non-nil) capability list in the intent replaces the defaults entirely (table-pinned both directions). Enforcement (`require_build_binding` merge blockers for unbound pre-existing projects) stays a separate org opt-in, unchanged |
| 2026-07-08 | **Self-service sign-up over store-backed principals** (§15.1, user direction): `POST /api/signup` mints a HUMAN principal in Postgres (migration 0004; PBKDF2-HMAC-SHA256 at OWASP iterations via Go 1.24+'s stdlib `crypto/pbkdf2` - zero new deps; per-process HMAC-keyed verify cache since Basic rides every request) behind hard gates: disabled by default (`--allow-signup`), optional shared invite code (`--signup-code`), conservative name charset, operator-configured names reserved (config always wins lookups - a signup can never shadow `--principal`). Store-backed principals resolve everywhere the registry does: Basic auth on REST/RPC/git smart-HTTP, funnel attribution (`authored_by`), workspace owner-only pushes; they are always human (agent principals keep needing operator config, they carry policy). Web login gate gains a Create-account mode, offered only when `GET /api/auth/config` (unauthenticated, public CORS - the first cut had none and the offer silently never appeared cross-origin, caught by driving the real UI) says signup is on. Still deliberately not an auth system: no sessions/rotation/reset; OIDC unchanged as §15.1's real answer |
| 2026-07-08 | **Workspace branches decided + shipped** (§12.2, user direction "one workspace should allow parallel work, branching not just stacking"): N parallel lines per workspace as sibling refs `refs/workspaces/<id>/<branch>` (`head` = default; single conservative segment, enforced at receive - nested refs rejected with a naming message, never half-supported). Branch existence derives from refs at read time (registry stays metadata-only; no create-branch control-plane verb), served as `Branches` on the workspace REST/RPC/UI surfaces. CLI: `runko workspace branch <name>` forks the current worktree's line (WIP rides along, fork point snapshotted immediately), `snapshot` targets the worktree's own branch (`runko.branch` worktree config; pre-branch worktrees default to head), `attach --branch` materializes any branch into its own worktree (local branches now `ws/<id>/<branch>`, so two attaches of one workspace coexist; attaching a branch already materialized locally is a structured `branch_in_use` refusal - §12.2's single-writer rule, `--shared` remains future). Funnel treatment (owner-only, caps, scan) applies to every branch identically since enforcement keys on the workspace id; e2e-tested: fork → diverge → parallel worktrees → per-branch snapshot refs |
| 2026-07-08 | **Branch ↔ stack provenance decided + shipped** (§12.2, user direction "a workspace should map to a branch [and each branch to a stack] - the UI doesn't make this obvious"): one workspace branch ↔ one stack as *recorded provenance, not an identity constraint* (plain-git pushers/web project creation/bot lanes stay workspace-less; stack relations stay derived per §7.4, so nothing stored can drift). Mechanics: `runko change push` stamps the worktree's `runko.workspace`/`runko.branch` config as git push options; receive-pack (now advertising push options) exposes them to the real hook; the hook forwards them beside the quarantine vars; the funnel validates against the registry (unknown workspace or another owner's workspace = loud rejection) and records `origin_workspace`/`origin_branch` on the Change (migration 0005; a plain amend with no options PRESERVES origin). Served on REST + `ChangeSummary` proto fields 12/13. UI: stack cards name their home branch (`ws › branch` chip; per-row branch chip on forks), the workspaces page's Branches column becomes "Branches → stacks" (each branch with its in-flight stack), the Change page carries the origin chip. E2E-proven across every process boundary in `TestEndToEndDaemonWorkspaces` (real CLI push from an attached worktree → origin on the REST Change) |
| 2026-07-08 | **§19.2 CLI stubs closed — full CLI experience** (§17.1, user direction "I want the full cli experience now"): `runko auth login/status/logout` stores ONE validated credential (`~/.config/runko/credentials.json`, 0600, the gh convention) — named principal → HTTP Basic (works for signed-up hashed-password principals, which can never be bearer tokens) or bare token → Bearer; validation is a real `GET /api/whoami` round-trip, a rejected credential stores nothing. Every networked command (11 of them + `mcp serve`) resolves flags > stored login, so after one login the `--runkod-url/--token` flags disappear from daily use; explicit flags still win for scripts/CI. `runko change create -m` commits WIP as one Change with the Change-Id trailer baked in at creation (identity stable from the first commit, not amended in at push); `runko change requirements` renders the §13.5 gates for HEAD's Change-Id by default (`--change` to override). Verified live against the production deployment: login as a signed-up principal → create → push → requirements (owner gate correctly outstanding) → abandon → logout, zero flags after login. The §19.2 stub list is now empty |
| 2026-07-08 | **Stacked bases recorded at receive + land ordering** (§7.4, §13.5; dogfood review P0 — stacking was broken in the real receive path): `computeBaseSHA` now walks the pushed commit's first-parent ancestry (nearest first, stopping at trunk) and records the nearest ancestor belonging to another known Change as `base_sha` — the receive path finally produces the `B.base_sha == A.head_sha` relation GetChangeStack derives and GetChangeDiff's base..head scoping assumes; previously every magic-ref push recorded merge-base(head, trunk), so stacked Changes read as siblings with whole-stack diffs, and the only stack test hand-rewrote the Store. Rules: an ancestor carrying the Change's OWN Change-Id is skipped (a grown Change stays one Change, base below all of it); unknown trailer-less ancestors keep the base below them (they land as part of this delta, never silently dropped); parent state doesn't matter for the base (even a landed/abandoned parent's commit is where this delta starts). New consequence gated: landing a child whose base is not on trunk is refused 409 `parent_change_not_landed` (Gerrit's ancestors-land-first rule) — attemptLand rebases only base..head, so landing the child first would put its delta on trunk WITHOUT the parent content it was built on. Also from the same review: Change titles now move with the head on amend (both stores); client Change-Id generation (commit-msg hook + `change create`) seeds with tree/parents/idents/content + random bytes instead of identity-/paths-only seeds that collided across commits and across clones |
| 2026-07-08 | **Outbound mirror shipped (M1) — §18.6's outbound half, provider-agnostic by construction** (user direction: "people will want to host it somewhere trustworthy" + "build it so the GitHub backend can be swapped for other git providers"): `mirror/` speaks ONLY the git wire protocol (ls-remote + push --force-with-lease) - no provider SDK, zero new deps; any smart-HTTPS host / ssh / path remote works, and the sole provider-specific atom (basic-auth username for token auth: GitHub `x-access-token` default, GitLab `oauth2`, Gitea anything) is one config field. Token rides env-borne GIT_CONFIG_* http.extraHeader, never argv. `runkod.MirrorWorker`: debounced trigger on every accepted push + every land, one-minute reconcile loop; trunk pushes are LEASED against the `mirror_cursors` row (the stage-2 table's first caller - writer/frozen modeled §18.6 from day one); divergence freezes that ref's MIRRORING loudly (never landing - Runko is SoR; §18.6.4's land-freeze is the inbound stage-1 rule) with no auto-reconcile ever; `POST /api/mirror/unfreeze` (force-land's admin gate) re-points the cursor at the observed remote tip so the next leased push overwrites the divergence exactly once, reporting both tips. Syncs trunk + tags + refs/changes/* (decided: open work is part of the backup story); workspace snapshots never (§12.2 personal WIP). `GET /api/mirror/status`, `runkod_mirror_frozen` gauge; a broken mirror never blocks anything. Worker serializes its own syncs - the debounce/reconcile pair racing each other's lease read as a phantom foreign write (caught by the test suite's land-then-sync sequence). M2 (inbound stage-1: provider webhooks, PR-merge ingestion as external Changes, inverted trunk lease) is recorded as the real Provider interface seam in docs/mirror.md - deliberately no interface in M1, one git-protocol implementation needs none |
| 2026-07-08 | **Admin force-land override** (§13.5; user direction "add a force approve/merge option so owner can merge changes" - also the documented way out of the solo-install deadlock the dogfood review flagged): `POST .../land {"force": true}` / `LandChangeRequest.force` / `runko change land --force` / a confirm-gated "Force land" button on the web Change page. WHO: the anonymous deploy token (the v1 operator credential) and principals flagged `admin` (`--principal 'name=…;token=…;admin'`); agents are refused even with the flag (§8.7/§13.5 hard rule) and bot lanes are refused always (§14.10.2 - a lane that skips its own checks is the ungated auto-land the design refuses to model); everyone else gets 403 `force_denied`. WHAT it bypasses: the owner/check merge gates and the trunk-delta revalidation rule (new `land.RevalidationNever` scope - force means "land NOW", not "enter the rebase loop"). What it NEVER bypasses: terminal states, stacked-parent ordering (`parent_change_not_landed` - integrity, not policy), and real merge conflicts. AUDIT: loud by design - every bypassed blocker is logged with the caller's identity, the response carries `Forced`, and the Change durably records `landed_forced` (migration 0006; a force that bypassed nothing records an ordinary land). `TestForceLandOverride` pins the full who/what matrix |
| 2026-07-08 | **jj-first client + Gerrit-style series receive — the evolve workflow** (§7.4, §17.1, §21; user direction "changing something at the root is a critical workflow… use jujutsu's model, let jj be the primary cli"): the §21 "evaluate jj as a first-class client" question is answered YES. Two halves. **Server (transport-independent, benefits git too)**: the receive funnel now processes a magic-ref push as a SERIES — every first-parent commit between trunk and the tip carrying a Change-Id trailer gets its Change created/updated in ONE push, bottom-up so each member's base resolves to its freshly-updated parent (`runkod/prereceive.go seriesMembers`); trailer-less intermediates still fold into the nearest descendant Change, one id spanning several commits stays one Change headed at its topmost commit, landed members are skipped as history context. Previously only the tip updated, so restacking after a root amend required N per-commit pushes — the single worst stacked-workflow gap. **Client**: jj colocated workspaces are the intended daily driver. Identity WITHOUT hooks (jj runs none): `templates.commit_trailers = format_gerrit_change_id_trailer(self)` derives the Change-Id trailer deterministically from jj's own change id — stable across every rewrite, verified empirically (jj v0.43) before any code. `runko doctor --install-hook` wires it (refusing to clobber a foreign trailers template); `runko change push` detects `.jj`, resolves the tip from jj's working copy (skipping an empty undescribed @), refuses to amend behind jj's back (`jj_change_ids_not_configured` instead), and pushes the tip SHA (git HEAD is detached by design in colocated repos). The critical workflow end-to-end: `jj edit <root>` → fix → jj auto-rebases descendants (jj's evolve, client-side, conflict-aware) → `runko change push` once → every Change in the stack moves with identity intact (`TestJJEvolveWorkflowEndToEnd`; CI installs jj to run these for real). Deliberately NOT done: server-side auto-evolve (daemon rewriting descendants on amend) — rewrites commits the author never saw and cascades gate resets; jj does it better client-side. Workspace machinery (sparse cones, snapshots) stays git-driven for now — jj-native workspaces (`jj sparse`) are a follow-up evaluation, not part of this decision |
| 2026-07-08 | **Change lifecycle formalized as a state machine** (§7.4, §13.5; user direction "the logic is getting sufficiently complex"): `docs/change-lifecycle.md` is the authoritative diagram + transition matrix, and `runkod/statemachine_test.go` is its executable form — every (state × event) cell driven through the real cores against a real bare repo, one fresh fixture per cell, so doc and code cannot silently drift. Writing the matrix surfaced three unguarded cells, all closed: ① a push carrying an already-LANDED Change-Id silently moved the landed row's head and overwrote its stable ref (a zombie: landed state, post-land content) — now rejected at receive with "landed is terminal, start new work as a fresh change" (Gerrit's change-is-closed); ② approve on an abandoned Change was recorded and, because approvals bind to head_sha, would have counted after a same-commit reopen — approve and rerun-check now require the OPEN state (409 invalid_state; abandon/land already had their guards); ③ rerun-check on landed/abandoned enqueued real rerun webhooks for terminal work — same guard |
| 2026-07-08 | **Second dogfood pass — client-side footguns closed** (§6.5, §17.1; the first pass's server fixes were confirmed by the state-machine work, this pass is the CLI's half): ① `runko change push` refuses when HEAD is already reachable from the remote trunk (`already_on_trunk`) — trunk commits keep their landed Change-Id trailer, so a no-new-commit push used to re-submit the landed commit (the receive funnel also rejects landed Change-Ids since the state-machine session; the CLI now never sends them); an unreachable remote skips the guard and lets the push surface the real transport error. ② `change push` warns when `runko.workspace` differs between local and worktree git config — plain `git config runko.workspace x` writes a value that LOOKS set but is outranked by the worktree config `workspace attach` writes, a silent-wrong-origin trap. ③ `runko project create` refuses duplicate project names/paths against the local HEAD's index (`already_exists`), mirroring the guard the daemon's create-project flow already had. ④ `runko change create` fails loudly (`outside_sparse_cone`, naming the files) when paths fall outside a workspace's sparse cone instead of committing a partial change — newer gits fail `add -A` with raw advice text, older ones silently skip; both funnel into the structured error. Documented-not-fixed: `refs/for/<trunk>` lock contention under rapid concurrent pushes is inherent to the rotating-magic-ref design (the client sees git's own "cannot lock ref" - retrying is correct; a per-Change target ref is the v1.x fix if it grates); gitleaks deliberately allowlists well-known example secrets (AKIAIOSFODNN7EXAMPLE) - tests/docs must use realistic patterns; non-funnel refs (refs/junk/*) stay §14.10.3-permissive by decision |
| 2026-07-08 | **Recorded gaps from dogfood review** (not fixed, deliberately): ① *Group-owner membership* (§7.3/§15.1): any non-author principal can satisfy `group:<x>` approvals — there is no group registry; acceptable at the current interim-auth trust level (same boundary as report-check's reporter), becomes REQUIRED with real AuthN: group membership resolution (or explicit group→principal mapping in daemon config) must land with §15.1's OIDC work. ② *Solo-install bootstrap* (§8.7's self-approval denial + one human = nothing can land): the documented way out is a second principal (`--principal` operator config or invite-code signup) or a bot lane; a template defaulting owners to `user:<creator>` for solo orgs is a candidate L0 refinement. ③ *Ops-endpoint routing*: any fronting proxy must route `/healthz`, `/readyz`, `/metrics`, `/api/*`, `/runko.v1.*`, `/internal/*`, and the git mount to runkod, NOT the SPA — a catch-all SPA route answering 200 HTML on `/readyz` makes readiness lie (observed live) |
| 2026-07-10 | **CI outage postmortem: dangling change refs + the silent failure path** (§14.4 Mode C, §18.6; user report "CI failed on GitHub but Runko didn't pick it up"): two compounding defects took pre-land CI down for every change at once. ① A stable `refs/changes/<id>/head` can point at an object the repo never kept - the pre-receive hook writes refs while objects sit in git's push QUARANTINE, and an aborted push discards the quarantine but not the ref; one such corpse made the whole `refs/changes/*` wildcard mirror push die (`fatal: bad object`), so no change ref reached the mirror and every CI fetch starved. The mirror now SELF-HEALS: before the namespace push it enumerates refs, deletes any whose object is missing (loudly - a re-push recreates them), then pushes the healthy rest; pinned by `TestMirrorSelfHealsDanglingChangeRefs` (loose ref file written directly, exactly the on-disk state an aborted quarantine leaves). ② The checks workflow's `locate runko-ci` + `setup-go` steps were SKIPPED when the change-ref fetch failed, so the always-report step ran `go run ""` and died - failures never reached runkod and checks sat pending forever, indistinguishable from a slow run. Both steps now run on success-or-failure, so the failure path reports like the success path. Remaining recorded gap: writing stable refs from pre-receive against quarantined objects is the root defect - the durable fix (verify-after-accept, or post-receive ref writes) is a §11.5 follow-up |
| 2026-07-09 | **Agent workspace discipline as a first-class artifact** (§8.8, §12.2; user observation "most agents are not properly using workspaces/branches" — this session's own history was the evidence: workspace-per-change sprawl, registry claims stamped onto non-workspace checkouts, one agent pushing through another's workspace, raw git where verbs exist): two layers. ① The GENERATED `AGENTS.md` (§8.8, every Runko-managed monorepo) gains a "Workspaces: the writing discipline" section — one workspace per WORKSTREAM (never per change), one branch = one stack = one reviewable line, work inside the worktree (cone + auto-stamped origin), snapshot cadence, bottom-up landing, update-base on revalidation, abandon hygiene, never claim a workspace you didn't work in — still under §28.2's 150-line budget (89 lines). ② This repo commits a Claude Code skill (`.claude/skills/runko-workspaces/SKILL.md`, carved out of the `.claude/` gitignore) with the deployment-specific recipe: jj-colocated setup + binding, the submit/land loop with the tight revalidation retry, everything the server refuses (so agents stop discovering enforcement by rejection), and the jj identity gotchas (terminal change ids, `jj duplicate` for contaminated working copies). Also from the same session, recorded here for the missing row: the inbox regrouped to ONE CARD PER WORKSPACE (branches as a tree off a shared main anchor via a virtual trunk root; abandoned ancestors retained struck-through while depended upon; stranded roots split under an amber anchor) — landed as its own change with vitest + the four-phase stack-smoke |
| 2026-07-09 | **Honest stacks: base_on_trunk + the stranded-base blocker** (§7.4, §13.5; user report "when I abandoned a change below a pending one, the leftover change was shown directly above main"): the inbox derives stacks from OPEN changes only, so an orphaned child (parent abandoned, or landed REBASED) became a forest root and the rail drew it sitting on main - an assertion the client had no data to make; merge-requirements told the same lie as a green `mergeable` chip on a change whose land would 409. Fix at the source: ① `ChangeSummary.base_on_trunk` (proto field 15), computed server-side via `merge-base --is-ancestor` ("" = bootstrap counts as on-trunk; errors count as NOT - never claim ancestry we can't prove); the rail anchors render "main" only when true, else an amber "⚠ not on main" with the recovery in the tooltip; fake-transport fixtures derive the same bit so the playground matches. ② merge-requirements gains the stranded-base blocker: base not on trunk ⇒ `mergeable: false` with the parent NAMED and the exit tailored to its state (open → "land it first"; abandoned → "reopen (re-push its stack) or rebase and re-push"; landed → "landed as a different commit - rebase and re-push"; unknown → generic rebase). Pinned by `TestMergeRequirementsStackedBaseBlockers` (the full parent-state matrix over a real repo) and `web/scripts/stack-smoke.mjs`, a committed DOM-vs-API cross-check: three phases (two healthy stacks → abandon the bottom → land the single) each assert chips == requirements, anchors == base_on_trunk, card count == ancestry forests |
| 2026-07-09 | **Deployment admin panel + org archive** (§7.1, §15.1; user direction "an admin panel for the entire runko deployment, where the cluster admin can manage orgs" — and the archive verb closes migration finding #19's "org lifecycle needs at least archive"): operator-only surface (`GET /api/admin/orgs` — the whole estate, archived included, with description/members; `POST /api/orgs/{org}/archive|unarchive`), gated by `operator_only` (flag principals + deploy token; org admins administer their org via its settings page, never this). ARCHIVE semantics: migration 0009 (`orgs.archived_at`), the org's entire surface (web, REST, RPC, git) answers a structured 410 `org_archived` uniformly — operators included, unarchive first; it drops out of the selector listing and member org lists (PG + mem both filter), its name stays taken, its row and repo stay on disk, and unarchive restores routing IN-PLACE (archived orgs are still mounted at boot precisely so recovery needs no restart). The default org is immovable (`default_org_immutable` — it is the root mount). `GET /api/whoami` grows `operator`/`admin` flags so the web session knows who gets the panel (display only — every admin endpoint re-checks); web: operator-only Admin nav + estate page (live/archived chips, archive/unarchive with confirm, create-org). Pinned by `TestAdminPanelAndOrgArchive` (operator gate, 410 uniformity, listing hygiene, name-stays-taken, in-place unarchive, immovable default, whoami flags) + PG round-trip + a Chromium drive (operator drives archive→410→unarchive→create; a store account sees no nav and hits the gate) |
| 2026-07-09 | **One workspace branch, one stack — enforced** (§12.2, §7.4; user direction "one workspace should only be able to create one stack; the Branches→stacks and Changes visualizations don't match"): observed live with two agents sharing one owner account — both pushed unrelated work claiming the same workspace branch, so the inbox (ancestry-derived) showed two stacks while the workspaces page (origin-grouped) showed one. Fix on both sides. **Receive**: a magic-ref push claiming (workspace, branch) must carry EVERY open Change of that origin in its pushed series (`enforceOneStackPerBranch`) — amends, restacks, and grows satisfy this naturally since the series walk spans trunk..tip; a fresh trunk-based line is refused naming the open change and the three ways out (restack + push the branch tip; abandon; `runko workspace branch` for a parallel line). The subtle foot-gun of pushing a non-tip member alone (dropping a child from the series) is likewise refused. **Web**: `changesByOrigin` now derives per-branch CHAINS with the exact `buildStackForest` relation the inbox uses — the two views can never disagree again; pre-invariant split data renders honestly as "N split stacks" with an amber flag instead of silently merging. Pinned by `TestOneStackPerWorkspaceBranch` (open/refuse/parallel-branch/grow/amend/non-tip/fresh-after-close matrix) + vitest chain-split parity |
| 2026-07-09 | **Org-scoped sessions: logging in means logging into an org** (§15.1, §7.1; user direction "my runko org user can see other orgs - this is bad"): the multi-org visibility model tightens. ① `GET /api/orgs` returns exactly the caller's MEMBERSHIPS - the unconditional shared-default-org row is gone, and nothing an account doesn't belong to can be enumerated (operators still see everything: they run the server). ② The DEFAULT org loses its historical everyone-with-a-credential behavior: it is membership-gated like any other org (root mount included) - store accounts need a row, seeded at signup (org_mode=join) or by an admin; operator principals and the deploy token stay server-wide, so CI, hooks, bridge post-backs, and the eval loop are untouched. ③ The web sign-in form gains an Organization field (prefilled from the last session): `signIn` validates against the ORG's own surface (`GET /o/<org>/api/whoami` - membership is part of authentication there) and binds the browser session to that org; wrong-org logins say "not a member of org X", never "wrong password". The org switcher lists only your orgs. Identity stays server-global (one account, several orgs is still legal); REACH is per-org. Hub-level global-account routes (org list/create, per-org admin surfaces that gate themselves) ride a new membership-ungated resolver so a member of ONLY org X can still list their own orgs through the root mount. Pinned by `TestOrgScopedSessionsIsolation` (an account in only its own org cannot see, list, or reach the default org - and vice versa) + a Chromium drive of the login matrix (right org in, wrong org refused with the org named, default org signed into BY NAME like any other) |
| 2026-07-09 | **Changes are born in workspaces** (§12.2, §7.4; user direction "some changes don't have workspaces associated — this is an anti-pattern, you can only create changes via workspaces"): the 2026-07-08 "recorded provenance, never an identity constraint" stance is superseded — a refs/for push now REQUIRES a validated, owner-bound workspace origin by default (`Processor.RequireChangeWorkspace`, on unless `--allow-workspaceless-changes`). Enforcement is structural, not principal-based: humans and agents alike; the one exemption is the unborn-trunk bootstrap/import push (workspaces need a base revision — a hard requirement would deadlock every new org), and the eval profile (compose) carries the loud opt-out since §16.4's loop must work from a bare clone. Plain git stays first-class via `git push -o workspace=<id>`; `runko change push` from an attached worktree needs nothing new; the rejection message teaches the workspace flow. The web create-project scaffold Change is server-authored, not a push, and unaffected. Pinned at both levels: Processor unit tests (refuse/bootstrap-exempt/claim-accepted/flag-off) + a real-binary e2e (bootstrap exempt → land → workspaceless refused → REST workspace create → same commit accepted via `-o workspace=`) |
| 2026-07-09 | **First real agent-token workspace run — two funnel base bugs** (§8.7, §12.2; user direction "try using Runko's workspace features with agent tokens" — exercised against a live daemon with an `;agent` principal, and both finds are the stage-11b BaseSHA bug's siblings): ① a FIRST push to a fresh ref (snapshot ref or magic ref) arrives with old == zero and the funnel diffed it against the EMPTY TREE, so agent policy judged the pusher as having authored the entire repository — an agent's first snapshot violated affinity on any file outside its cone (and tripped "modifies owners" via trunk's own manifests), making agent workspaces unusable on any real repo; fixed by basing zero-old diffs on `merge-base(new, trunk)` (unborn trunk keeps the empty-tree base — there is genuinely nothing else the content is a delta over). ② Agents could snapshot but never SUBMIT: `RequireWorkspaceAffinity` refused their refs/for pushes even from their own workspace, because the stage-12c enforcement branch predates workspace push-option provenance — a validated, owner-bound origin claim now carries the workspace's write allowlist as affinity (`PushRequest.WorkspaceAffinity`, built at stage 6, fed on this path for the first time); a bare agent push stays refused, claiming someone else's workspace stays rejected outright, and the claimed workspace's allowlist still constrains what the push may touch. Both pinned by regression tests confirmed failing pre-fix; the full loop (workspace create → first snapshot → change push from workspace → land, attributed `authored_by`/`landed_by`/`origin_workspace` = the agent) verified live. Also observed: the sparse cone stops out-of-lane edits CLIENT-side before the funnel ever sees them (git refuses to stage outside the cone) — the two fences layer exactly as §8.7 intends |
| 2026-07-08 | **History + blame in the code browser** (§17.2; user direction "take Gerrit as inspiration, but make the UX nice"): RepoService grows `ListCommits` (path-scoped log — "" = whole repo; single files follow renames via `--follow`, decided per path by a cat-file type probe since `--follow` is only valid for one file; real `--skip`/`--max-count` pagination, not adapter windowing) and `BlameFile` (contiguous same-commit regions from `git blame --porcelain`, returned WITH the blamed lines so content and attribution can never come from different revisions; binary answers `binary=true` in-shape rather than erroring; >5000-line files blame-truncate). The Runko twist over Gerrit: every commit's Change-Id trailer resolves against the Store, so history rows and blame regions link to the CHANGE that landed them (state badge included) — pre-Runko commits degrade to plain rows, and a Store hiccup degrades enrichment, never the listing. Change-Id extraction rides `%(trailers:key=Change-Id,valueonly)` in one log format string; blame's trailer lookup is one batch `--no-walk` call over the file's distinct commits. Web: clicking a DIRECTORY now selects it (history panel for the subtree, `?view=dir`), files get Code/Blame/History tabs (deep-linkable via `?view=`), the empty state became repository-wide history, and blame renders an age-tinted per-region gutter (newest glows accent) with subject/author/age/sha. Fake-transport parity keeps the playground working. Verified: 5 Go tests over a real repo (rename following, region merging, enrichment, pagination) + headless-Chromium drive of all three views incl. history-row → change-page navigation |
| 2026-07-08 | **Sign-up requires an org: create or join** (§7.1, §15.1; user direction "you have to specify an org, either create one or join one; eventually existing orgs will be email invite only, but for now anyone can join any existing org"): the hub's `POST /api/signup` now demands `{org, org_mode: create\|join}` — create makes you the org's admin (gated by `--allow-org-create`), join makes you a member of ANY existing org including the shared default (OPEN for now; the deployment invite code is the only gate — **recorded interim decision**: per-org email invitations are the planned tightening, at which point open join dies). Validation still precedes account creation (unknown join target / taken create name / bad mode → structured error, no half-registered account); the web form gains a create/join toggle (join-only when creation is disabled) and the browser lands inside the chosen org. The Server-level signup handler keeps the org-less legacy contract for direct embedders; the hub shadows it at root. Browser-verified end to end: founder creates acme at signup, teammate joins it and sees the settings page read-only with both members listed, ghost-org join surfaces the structured 404 |
| 2026-07-08 | **Org settings page + org-scoped sign-up** (§7.1, §15.1; user direction "org settings page with basic configs" + "sign up should include org creation, not just user creation — follow other startups with org-like structures"): ① per-org settings stored as JSONB on the org row (migration 0008; deliberately thin — {description, global_required_checks}; root-invalidation stays daemon config since §9.4 already marks it for relocation into the TREE, not a database): org-required checks are ENFORCED at the §13.5 gate (`Server.effectiveGlobalChecks` = flag config ∪ stored settings, consulted at request time so a settings save takes effect immediately; a directory read failure logs and falls back to flag checks, never silently drops them — pinned by `TestOrgSettingsChecksGateMerge`); ② member management completes: `GET /api/orgs/{org}/settings|members`, `PUT .../settings`, `DELETE .../members/{name}` + role change via the existing upsert — reads for members, writes for org admins/operators; the DEFAULT org participates (readable by every credential, writable by its admins — membership rows on the shared org exist exactly to grant settings admin, its serving surface stays ungated); ③ **sign-up creates the org** (the standard SaaS account+workspace shape): `POST /api/signup` takes an optional `org` — validated BEFORE the account is created (a rejected org name never strands a half-registered account, pinned by `TestSignupWithOrg`), creator becomes admin, the browser lands directly inside the new org; org-less signup remains the join-an-existing-org path (admin adds you — deliberately no invitation system yet); `GET /api/auth/config` advertises `org_create_enabled` so the form only offers what the deployment allows. Web: /settings page (about + merge policy + members) and the Organization field on the sign-up form. **CORS lesson recorded**: Go 1.22 method-qualified mux patterns ("GET /api/…") swallow the browser's OPTIONS preflight into the fallback 404 — org routes are registered method-less with an internal dispatcher so preflights reach the CORS middleware; caught by driving the real cross-origin dev loop, invisible same-origin |
| 2026-07-08 | **Multi-org: each org owns a repo** (§7.1 finally reaching the daemon; user direction "I want each org to own a repo" — until now every account shared the single served repo): the schema was multi-tenant since stage 2 (everything keys on org_id/monorepo_id); the daemon-side gap closes as `runkod.OrgHub` — each org owns its own bare repo (`<orgs-dir>/<org>/repo.git`) and gets its OWN Server instance, the entire existing surface (smart-HTTP git, REST, Connect RPC, the pre-receive callback) mounted unchanged under `/o/<org>/`. A base URL is the only thing any client needs, so `--runkod-url <host>/o/acme` (or the web transport base) makes every existing command/page work per-org with zero client changes — the same property that made the CLI-first §8.3 decision cheap. **Accounts become server-global** (migration 0007: `principals` drops org_id, one account many orgs) with explicit `org_members` rows (admin|member); on org-scoped servers membership is part of AUTHENTICATION for store-backed accounts — valid credential outside your org is a structured 403 `not_org_member` (never 401) on REST/RPC/git alike. Operator principals, bot lanes, and the deploy token stay server-wide (they are daemon config); agents may never manage orgs (§8.7). The root-mounted repo becomes the "default org" (also at `/o/<name>/` for uniformity), deliberately keeping its historical everyone-with-a-credential behavior — existing deployments, remotes, and CI break zero. Self-service creation is default-deny (`--allow-org-create`); creator becomes org admin; admins add members (`POST /api/orgs/{org}/members`, `runko org add-member`). Web: sidebar org switcher (per-browser selection re-basing the Connect transport) + inline create. Per-org zoekt/mirror/webhook-target config deferred (daemon singletons apply to the default org; per-org outbox workers do run). E2E bar (`TestEndToEndDaemonOrgs`, real binary + real git): signup → create org over REST → clone `/o/acme/repo.git` → push through the org's own funnel → land → trunk advanced in the org repo only, change invisible from the default org, non-member 403 at both API and git transport |
| 2026-07-08 | **Multi-language templates + no-template escape hatch** (§6.2, §6.3, §10.1, §10.4; user direction "add js/ts/rust/java/cpp-class languages, priority by Bazel support, choose 4-5, plus an escape hatch for everything else"): built-in templates become a `<type>-<lang>` matrix — go plus five new languages (python, ts, rust, java, cpp) admitted strictly by Bazel-rule maturity (rules_java/rules_cc core, rules_python first-party, rules_rust bazelbuild-org, rules_ts Aspect); js deliberately misses the cut its own criterion sets (weakest Bazel story; most new JS is TS) and is the first candidate for the next batch. `CreateProjectIntent` gains optional `language` (+ PROJECT.yaml `language`, pattern-not-enum on disk so ANY language is recordable) and `no_template` — the escape hatch: unsupported language without it is a structured `unsupported_language` error naming the supported set; with it, create emits PROJECT.yaml + README only, language recorded verbatim. Skeletons stay source-only (no Cargo.toml/package.json/tsconfig — org-template concern, the same split as §14.5.4's BUILD rules); the `build` capability stays default for every language including no-template (the filegroup BUILD.bazel is language-agnostic). Omitted language keeps resolving to Go and stays OMITTED from the manifest (never default-filled) — existing projects, goldens, and the compose loop are byte-identical. `<type>-default` template ids live on as aliases for the Go set |
| 2026-07-08 | **This repo adopts Bazel + will self-host on its own product** (§14.5.4 dogfood, §18 executed by hand; user direction "bazel migration, then Runko migration, record findings for a future import feature"): Bazel 8.7 (bzlmod) + rules_go/gazelle as the BUILD GRAPH ONLY — `bazel build //...` green in CI with a gazelle drift check, `runko-ci affected --engine bazel` refines over the real graph (the stage-9b real-bazel integration test executed for the first time anywhere), while `make check` stays the test truth (refinement is escalate-only CI-scoping by design, so bazel-as-test-runner buys no gating). Self-host plan (staged, `docs/migration-findings.md` is the living record feeding §18.3's import feature): dedicated org on the prod instance, full-history push to refs/for/main on an unborn trunk (tip-SHA parity verified in code: prereceive never rewrites, unborn land is a zero-OID CAS), outbound mirror to the existing GitHub repo (first-sync silent adoption on tip parity), a webhook→repository_dispatch bridge so GitHub Actions runs pre-land checks (refs/changes/* never trigger Actions natively), coarse 4-project carve with owners omitted (self-approval hard-deny + solo dev), admin force-land as the sanctioned unpoliceable-import bootstrap |
| 2026-07-08 | **Per-org mirror + org-stamped envelopes + reference GitHub CI plugin** (§18.6, §14.4 Mode C — self-host slices R1-R3, docs/migration-findings.md #12-14): repeatable `--org-mirror 'org=…;remote=…[;username=…][;token=…]'`/`RUNKO_ORG_MIRRORS` gives hub orgs their own outbound MirrorWorker (org repo, org-scoped cursors; `/o/<org>/api/mirror/status\|unfreeze` light up unchanged; naming the default org is refused — that's `--mirror-remote`); webhook envelopes now carry `org_id` (the org NAME) on change.updated/landed/check_rerun_requested, since one daemon-wide `--webhook-url` fans every org's events into the same consumer; `cmd/runko-bridge` is the §14.4 Mode C reference plugin — HMAC-verified envelope → GitHub repository_dispatch, 2xx to the outbox only after GitHub's 204 (backoff re-drives failures; failed dispatches deliberately never enter the dedup set), org-filtered, shipped in the multi-binary image — paired with `.github/workflows/runko-checks.yml` (fetch-retry against mirror lag, `runko-ci report-check` post-backs under PROJECT.yaml's declared check names). E2E: the orgs test now runs the real daemon with `--org-mirror` against a local bare target and asserts land convergence |
| 2026-07-09 | **Runko now hosts its own source (§18 stage-2 posture, executed)**: the repo's source of record is the production deployment's `runko` org; GitHub is the outbound mirror + CI runner. Cutover per docs/migration-findings.md: full history imported as ONE Change at byte-equal tip parity onto the unborn trunk, mirror silently adopted the same-tip GitHub repo, and the first gated change (the 4 PROJECT.yaml manifests: platform/web/docs/proto, checks-only gating — owners omitted per the solo-dev self-approval deadlock) ran the full webhook→bridge→repository_dispatch→report-check chain and landed through the §13.5 gate unforced. `--insecure-allow-unpoliced-land` removed post-cutover; default-deny is live. Dogfood begins: every future change to this repo lands through its own funnel |
| 2026-07-09 | **Re-carve: folder-per-project + first live `dependencies:` edges** (user direction: "one folder per project; project relationships; platform is too coarse"). The coarse root catch-all manifest (path `""` owning every path) is replaced by 9 real projects — `repo` (root glue only: go.mod/Makefile/.github/scripts), `platform/` (the control-plane libraries: receive/land/affected/checks/index/project/search/mirror/mcp/buildadapter/agentsmd/core, all moved under one folder), `runkod/` (daemon + its binaries at runkod/cmd/), `cli/` (runko + runko-ci), `internal`, `db`, `proto` (now owning its generated Go at proto/gen/), `web`, `docs`. §13.3's declared-dependency closure goes live for the first time: `internal`, `db`, and `proto` declare no checks and are gated purely via reverse edges (their dependents' checks); `web` depends on `proto` so proto changes re-run web-check. Landing mechanics recorded in migration-findings #26-29 (mirror PAT workflow scope, non-org-scoped outbox triple-delivery, cancelled-run false failure reports, the default-branch-workflow two-phase dance) |
| 2026-07-09 | **Multi-engine monorepos decided (§14.5.5) + create-time build-system selection** (user direction, prompted by "is Bazel right for the frontend?" — it isn't, and that's now spec): one build graph per repo is a non-goal — sovereign per-territory engines over the engine-independent declared floor, cross-engine deps expressed as declared edges + committed boundary artifacts (the proto/gen ↔ web/src/gen pattern this repo already runs), §14.5.4's admission criteria reaffirmed (Vite/Nx/Turbo never get adapters or `build` bindings — territory scaffolds, not engines). `project create` gains `build_engine` (`bazel\|vite\|none`), defaulting by language (`ts` → vite, else bazel): vite emits a generated package.json + vite.config.ts (the sanctioned exception to §10.4's no-package.json rule — for a Vite territory that IS the build declaration) with no `build` capability; explicit `build` capability + vite is a structured `invalid_combination`. Wired through Intent/CLI (`--build-engine`), the CreateProjectIntent schema + proto, and the web create form |
| 2026-07-09 | **Workspace sync + self-recovering land (§12.3's "sync to head" verb, user direction: "auto sync to trunk... without git/jj operations")**: `runko workspace sync` (update-base renamed; alias kept) rebases the workspace line onto the fetched trunk tip via a shared jj-aware core — in a colocated repo it rebases through jj so descendants follow, in plain git it keeps §6.6's abort-and-name-the-files conflict UX (`sync_conflict`); `runko change push` auto-syncs a stale base before pushing (`--no-sync` opts out) so changes stop being born pre-stale; and `runko change land` now runs the whole §13.5 recovery loop itself on `requires_revalidation` — sync, re-push, poll the gates, retry, bounded by 4 attempts + `--sync-timeout` (default 15m), stopping structurally on failed checks (`checks_failed`) or conflicts. `parent_change_not_landed` is deliberately NOT retried (rebasing this change can't land its parent). The manual tight-loop this replaces was the single biggest dogfood toil item |
| 2026-07-09 | **Public read-only orgs (§15.2, decided + built)**: org setting `public_read` opens anonymous READ access on every surface, allowlist-scoped and fail-closed — git `upload-pack` only (anonymous advertisement hides `refs/workspaces` and `refs/for` via per-request `GIT_CONFIG` injection; `refs/changes/*` stays public by design), an explicit REST GET allowlist, and the Connect read-procedure allowlist; presented-but-wrong credentials never downgrade to the anonymous view; enabling is refused while any trunk manifest declares `visibility: restricted` (no per-principal fetch filtering until §12.3 Phase B). Known sharp edge recorded: URL-embedded credentials never see a 401 on a public org, so reads get the anonymous view unless the client forces auth (`http.proactiveAuth`, stamped into workspace clones). E2E-tested over the real transport: anonymous clone flips with the setting, WIP refs hidden anonymously + visible authenticated, anonymous push refused |
| 2026-07-09 | **Affected-scoped CI (§14.5.6, built)**: the platform's own pre-land checks and post-land image builds now consume `runko-ci affected` — a `setup`/`scope` job computes the change's affected projects (the same computation the merge gate uses) and every check job is gated + scoped to it (a cli change runs `go test ./cli/...`, docs runs only bazel-check, web runs only web-check), while each image rebuilds only when its input set intersects the landed range (docs-only lands rebuild nothing, web-only lands rebuild only web). Check NAMES are unchanged so the gate is untouched; `run_everything` fails open to the whole repo so a required check is never left unreported. Scope logic validated against real historical changes; the only unscoped checks are build-graph health (bazel/gazelle, repo-wide by nature) |
| 2026-07-10 | **`bazel test` becomes the test runner (reverses the 2026-07-08 graph-only stance; user direction: "the runko repo is the canonical standard - use bazel test as much as possible even though it doesn't speed things up")**: platform-check/-race now run `bazel test` over the affected bazel scope (`make check-bazel-test`/`check-bazel-race` locally) - the §14.5.4 golden-path check command, demonstrated on the least Bazel-friendly suite imaginable (subprocess git everywhere, compiled-daemon e2e tests, jj). What it took, as reference material: contract artifacts became runfiles `data` (a hand-written `//docs:contracts` filegroup; gazelle preserves unmanaged attrs), jj tests gained a hermetic HOME (sandbox-required, better hygiene under plain go test too), and the e2e suites resolve their subject binaries from bazel runfiles (`TEST_SRCDIR` + go_binary `data` deps) with a `go build` fallback so one test source serves both runners. Kept native: `make check` (the <30s inner loop), gofmt/vet (fast pre-steps; nogo is the noted follow-up), and check-db (external stateful Postgres wants `-p 1` semantics bazel's parallel targets don't give). All 21 targets pass under both `bazel test` and `--@rules_go//go/config:race` |
| 2026-07-10 | **Bazel conversion round two: check-db + vet move in**: `internal/dbtest` now applies migrations from the SAME embedded FS the product ships (`db.Migrations`, fed to psql over stdin) instead of repo-root file discovery - the harness works identically under plain `go test` and sandboxed `bazel test`, and gained a no-database inventory test (pairing/order of embedded migrations). `make check-bazel-db` = `bazel test --test_env=RUNKO_TEST_DATABASE_URL --test_filter=Postgres --local_test_jobs=1 --nocache_test_results` (env passthrough, the -p 1 serialization the shared-schema resets need, and no result caching because a mutable external database is not a hermetic input); CI's platform-db runs it scoped. `go vet` retired from CI: rules_go nogo (`//:runko_nogo`, vet analyzer set) rides every compilation, so a vet violation fails the build itself. Deliberately still native, by doctrine: web (§14.5.5 territory sovereignty), the bazel-in-bazel integration test (recursion buys nothing), compose (docker), gofmt (one fast step), and `make check` as the <30s inner loop |
| 2026-07-10 | **Tree-borne root invalidation + two-tier root gating (§14.5.2 relocated into the tree per §9.4; prompted by a README edit running the full CI matrix)**: `PROJECT.yaml` gains `root_invalidation` - glob patterns naming BUILD-SENSITIVE paths whose change escalates affected computation to run_everything - read from the indexed manifests by the daemon (funnel, merge gate, land) and `runko-ci affected` alike, with the old `--root-invalidation` flag demoted to an additive override; policy now rides the same review gates as the code it protects. The root `repo` project drops the Go check matrix down to bazel-check (a prose edit at the root now gates like a docs edit) while its manifest enumerates go.mod/MODULE.bazel/Dockerfile/.github/** etc. as invalidation patterns - fail-closed exactly where it matters; workflows stop treating "repo affected" as "everything", and release input sets drop `repo` (RunEverything covers the dangerous files). Companion bazel-adapter fix closes finding #6: paths outside any bazel package (no BUILD in ancestry - a filesystem test) are skipped instead of erroring the whole rdeps query, so refinement survives mixed code+prose changes; an all-non-package change reports zero targets without invoking the engine |
| 2026-07-10 | **Encapsulated checks, phase 1 (§14.9.1, new; user direction: "each project's test should be part of the project - like encapsulation in OOP")**: `runko-ci checks --base --head` resolves the affected closure's manifest-declared `ci.checks` into `{project, name, command}` rows - deduped by name, same-name-different-command a structured `ambiguous_check` error - and `.github/workflows/runko-checks.yml` collapses to a generic executor: a `setup` job resolves the matrix, one matrix job per check runs `command` and reports under `name`. The workflow now contains zero project names, zero commands, zero per-check environments; the runner contract (go/bazel/node/jj/psql + an always-on postgres with `RUNKO_TEST_DATABASE_URL`) is the only thing it owns. Gate/executor agreement is by construction: both read the change's own head tree. `index.IndexedProject` gains `Checks` (name+command) beside the gate's name-only `RequiredChecks`; `web-check`'s command self-scopes (`cd web && ...`) since the executor grants no working-directory |
| 2026-07-10 | **Encapsulated checks, phase 2 (§14.9.1): manifests own everything, db lane dissolved**: `internal/dbtest.Connect` self-serializes with a session-level Postgres advisory lock held per test (key distinct from the migrator's - daemon e2e tests boot a migrating daemon while holding it), replacing the external `-p 1`/`--local_test_jobs=1` runner flags; pg tests are now safe inside ANY test invocation, so the dedicated db lane disappears into each project's own check via `--test_env` passthrough. The re-carve: `platform-test`/`platform-race`, `runkod-test`/`runkod-race`, `cli-test` (no race - sequential CLIs; race-where-it-matters), `internal-test` (new: consumers' scoped commands never covered `//internal/...` itself), each `bazel test //<dir>/...` scoped to its own subtree; `bazel-check` stays declared across the Go projects deliberately - gazelle drift is repo-wide, and dedupe-by-name makes it one job regardless. The old `platform-check`/`platform-race`/`platform-db` names retire in the same change that lands the executor reading the new ones from the head tree - the rename-safety property §14.9.1 exists to provide |
| 2026-07-10 | **Doc estate brought current (user direction: repo cleanup)**: `CLAUDE.md` slimmed from ~71KB to a current-state operating manual — the per-stage engineering record (every stage's shipped scope, caught bugs, review findings) moved verbatim to `docs/implementation-log.md`, frozen as history; `AGENTS.md` rewritten (it still claimed no compose/CI/web/Postgres existed); `proto/README.md` reframed from "draft, needs confirming" to the settled Connect contract doc; `db/README.md` migration + serialization sections updated for `runkod.ApplyMigrations` and the §14.9.1 advisory-lock harness (the `-p 1` rationale it documented no longer exists); `web/README.md` gained the browse tabs/org surfaces; §22.2's MVP-web-stack row and §28.3's stage-13 row updated in place to the React+Connect reality their own changelog rows had already superseded; stale pre-re-carve path references (`cmd/runko*` → `cli/runko*`, `buildadapter/` → `platform/buildadapter/`) fixed in code comments and the build-adapter spec |
| 2026-07-10 | **Prose paths (§14.5.7, new; user direction: "even doc changes are triggering CI checks - add exceptions")**: `PROJECT.yaml` gains `prose:` — the de-escalation dual of `root_invalidation`: an ordered, first-match-wins pattern list (glob dialect + new leading-`**/` any-depth form + gitignore-style `!` exceptions) re-attributing matching paths, for check derivation only, to the repo-root project — so a README/design-doc edit anywhere requires one seconds-long `docs-check` (`make check-docs`, a real markdown link checker satisfying §13.5 default-deny) instead of its folder-owner's test matrix and dependency closure. Fail-closed properties pinned by tests: root invalidation beats prose; the §7.3 owner gate reads raw touched paths and never de-escalates; no root project ⇒ no de-escalation (unowned paths keep escalating). The `!` exceptions carry an obligation: paths tests consume as data (`docs/spec/**`, `docs/cli-contract.md` — runfiles of the contract suites) are excepted AND the `docs` project now declares `contracts-test` running exactly the consuming suites — which also CLOSES a pre-existing hole where a contract-doc edit gated only on a build-no-test bazel-check. Plumbed like root invalidation (index scan union → daemon funnel/gate/land + `runko-ci affected`/`checks`); zero executor/workflow changes, the §14.9.1 payoff. Dogfooded on a clone: design.md+platform/README edit → `docs-check` alone; cli-contract.md edit → `contracts-test`+`docs-check` |
| 2026-07-10 | **Post-land safety net becomes the second generic executor (§14.9.1; user direction: "I want the tooling uniformed", anticipating RBE cache sharing)**: `ci.yml`'s check job - plain `go test` lanes since stage 9d, missed by this date's bazel-test adoption because it is post-land-only - is replaced by the same resolve-then-run shape as runko-checks.yml: `runko-ci checks --base <push.before> --head <push.after>` resolves the LANDED delta's affected closure into the manifests' own check commands, evaluated on the landed, post-rebase tree. The workflow again knows no project names and no commands (the first draft hardcoded `make check-bazel-*` lanes and was caught breaking exactly this rule): what gated pre-land is what re-runs post-land, the hardcoded web job dissolves into the matrix as `web-check`, and docs-check/contracts-test now run post-land too when affected. Scope narrows from run-always-everything to the landed delta's closure - the gate's own model; an unusable `before` falls back to the empty tree, which root-invalidates to run_everything (fail closed). Per-check setup-bazel disk-cache keys are shared with the pre-land executor deliberately: pre-land warms post-land and vice versa - the pre-RBE cache story, and RBE later serves both sides identically. Kept outside the matrix by doctrine: compose-smoke (Docker territory; its subject is the deployment artifact, not a project). Plain `go test` retires from CI; `make check` remains the local <30s inner loop (§28.2 rule 3), where every session runs it |
| 2026-07-10 | **Anti-Boq sharpened: defaults, not capability (§2.3; user direction: "maybe we should revisit the anti-boq doctrine... if the power is too good")**: prompted by the Nx `namedInputs` survey — the doctrine was being over-read as banning power features. Restated: opt-in refinements are admissible when absence means today's zero-config semantics, they reuse existing vocabulary, and the zero-config default is itself good; a default coarse enough to force everyone into the opt-in is a Boq violation wearing an "optional" badge. Under that test, per-check `inputs:` filesets are **conditionally admitted** (§14.5.8): sequenced after the snapshot-diff measurement, defaults unchanged, one glob dialect, advisory-first rollout — same declared trust class as `dependencies:` edges, not a new risk species |
| 2026-07-10 | **Root invalidation refined (§14.5.8, new; follow-up to "why did this change trigger all the projects' checks?")**: affected-system survey (Nx/Turborepo/Pants/bazel-diff/target-determinator/BTD — every one converges on graph closure + a blunt global list + optional precision) turned the coarseness complaint into two decisions. SHIPPED: `root_invalidation` becomes ordered, first-match-wins with `!` exceptions — `prose:`'s exact dialect, one evaluator (`affected.MatchOrdered` replaces the unordered any-match; `index.RootInvalidation` concatenates in scan order instead of sort+dedup, root manifest first). First instance: `!.github/workflows/ci.yml` before `.github/**` — the post-land safety net can't affect pre-land check validity, so escalating on it bought a full matrix that never exercises workflow files; what still gates it is owner review + docs-check + the workflow's own post-land execution. Pinned by tests: exception-after-pattern is dead (ordering), excepted-but-unowned still fails closed (exceptions remove escalation, never gating), mixed changes still escalate. DECIDED, lands with its consumer: the blunt/graph-refinable split — out-of-graph paths (workflows, scripts, Docker) stay blunt permanently; graph-visible ones (go.mod, MODULE.bazel, BUILD, bazelrc) get a `SnapshotDiff` adapter strategy (target-determinator-class external process), dogfooded gate-free in post-land ci.yml before any gate-grade opt-in |
| 2026-07-10 | **Snapshot-diff machinery (§14.5.8 phase 1; user direction: "continue until everything is implemented")**: the whole graph-refinable pipeline, inert until a caller passes `--engine`. `root_invalidation` entries accept `{pattern, refinable: true}` beside bare strings (schema `oneOf`; custom YAML round-trip keeps blunt entries compact; a refinable `!` exception is a parse error - nothing to refine). `buildadapter` grows the OPTIONAL `SnapshotDiffer` capability + `RefineSnapshot`: `Refine`'s fail-closed table plus two harder rules - an UNMAPPED target fails closed (diff output stands in for run_everything, so an unattributable target would silently drop checks; rdeps' additive output could shrug it off, this cannot), and the caller's checkout is sacrosanct (diff tools check revisions out, so the bazel engine runs `target-determinator` against a disposable `git clone --shared`, pinned by a vandalism test). `affected.Compute` tracks `EscalationRefinableOnly` (any blunt match or unowned path poisons it) and gains the `RefinableHandled` mode (post-diff-success: refinable paths re-enter prose/ownership like `!` exceptions; blunt still escalates); `Refinement` carries `strategy: rdeps\|snapshot_diff` as the audit trail. `runko-ci checks/affected --engine` orchestrate: refinable-only escalation + successful diff ⇒ de-escalated floor ∪ diff-impacted projects' DECLARED dependents closure (cross-territory edges the graph can't see, web→proto). Gate warning carried in every layer's docs: the merge gate never consumes the narrowing - `--engine` belongs only where nothing gates on the output (post-land ci.yml, phase 2) until §14.5.4's gate-grade opt-in |
| 2026-07-10 | **Snapshot-diff enabled post-land (§14.5.8 phase 2)**: the root manifest marks its eight graph-visible entries `{refinable: true}` (go.mod/go.sum/MODULE.bazel[.lock]/BUILD.bazel/.bazelrc/.bazelversion/.bazelignore; Makefile/Docker/sqlc/workflows/scripts stay blunt strings - no graph sees them), and post-land ci.yml's resolve job gains setup-bazel + a pinned target-determinator (v0.34.0) + `--engine bazel` on `runko-ci checks`. Post-land is deliberately the ONLY consumer: nothing gates on its output, so narrowing is a pure CI-cost experiment - a MODULE.bazel edit's post-land run drops from every-check to the diff-impacted projects' checks, fail-closed back to run_everything on any determinator error, with `build_refinement` in the resolve log as the audit trail. Pre-land runko-checks.yml deliberately does NOT pass --engine: the gate resolves required checks from the blunt floor, and an executor narrower than its gate would deadlock mergeability. Gate-grade adoption (§14.5.4's org opt-in) waits on measured post-land data in migration-findings.md |
| 2026-07-10 | **Review conversation decided (§13.4.1–13.4.2; user direction: research Nx + GitHub for the next spec tracks)**: comments/threads + review requests + a derived attention set — the research pass found this pillar-2 core entirely absent (approve/land only since stage 11c) while GitHub's Copilot review went agentic and needs exactly this as its output channel. Decisions: anchors bind to `head_sha` with approvals' amend semantics (outdated, never floated — floating is v1.x); one-level threads (GitHub model); `require_resolved_threads` org knob default off (ceremony budget) adding an `unresolved_threads` §13.5 blocker when on; attention set derived from existing facts, never manually managed (skipping Gerrit's manual editing); agents comment/get requested but §8.7's approval ban is untouched (§8.6 row, flow A.2b); storage = change-lifecycle Postgres rows like approvals; webhook envelope gains `change.commented`/`change.review_requested` (additive enum + id/anchor payloads, never bodies); `ChangeComment` schema extended (`parent_id`, `resolved`, `head_sha`, `side`). Implementation = DAG stages 16/16b (CLI rows join `docs/cli-contract.md` only with the commands — agentsmd drift test). Also recorded from the same research, deliberately NOT specced: check intelligence and boundary conformance as §22.3 #11/#12; merge queue already positioned (v1.x batching of §13.5); Nx's execution-layer moats (remote cache, distributed agents, atomizer) reaffirmed out of scope per §14.1 |
| 2026-07-10 | **CI watchdog service (`watchdog/`, first project carved under the more-microservices direction)**: §14.4.2's staleness rule grown an actor — a standalone reconciler sweeping every open Change's merge requirements each minute. Two bounded remedies for the twice-observed "CI failed on GitHub but Runko never heard" incident: a pending check whose `details_url` names a COMPLETED GitHub Actions run gets the run's real conclusion force-reported (reporter `ci-watchdog`, fail-closed conclusion mapping — unknown GHA states report failure, never success); a required check that never reported at all past a grace window gets exactly ONE rescue rerun per (change, head, name) re-firing the §14.4.1 webhook chain (never a second — an infrastructure eating every dispatch must page a human, not receive a dispatch storm). `details_url` is reporter-controlled input, so it is an allowlist: only URLs into the configured repo are followed. Builds on the same-day merge-requirements `details_urls` field; ships in the runkod image, own Deployment (bridge pattern), `RUNKO_WATCHDOG_*` env config, zero GitHub calls while CI is healthy |
| 2026-07-10 | **Stage 16 shipped: review conversation implemented end-to-end (§13.4.1–13.4.2)** — in-daemon, NOT a microservice (user floated a separate review service/image/pod; rejected: comments are change-lifecycle state exactly like approvals — the gate reads them, the attention set joins them with approvals/requests/head_sha in one store, and an amend must outdate them atomically with the approval reset; §9's one-daemon doctrine stands, the extraction seam is the package boundary). Everything mirrors an approvals-shaped precedent: migration 0011 extends the SPECULATIVE stage-2 `change_comments` table (head_sha nullable-as-stale per 0002, side, parent_id, resolved) + `change_review_requests` (PK-upsert idempotency); Store grows 6 methods on both MemStore and PostgresStore (the author round-trips as a typed actors row — the agent badge is `actors.type`); cores `commentChangeCore`/`resolveCommentCore`/`requestReviewCore` in actions.go serve REST (review.go, the workspace.go split) and Connect (4 new ChangeService RPCs, both gens regenerated — no drift guard exists, discipline is manual) without semantic drift; the `require_resolved_threads` org knob rides the OrgSettings JSONB (no migration, the PublicRead precedent) and appends its blocker post-aggregation like the stacked-base/default-deny overrides; `attention_set` joins MergeRequirements in all three encodings (schema+Go+proto together — the contract test forces the pairing); webhooks `change.commented` (ids/anchors, never bodies) + `change.review_requested` ride the untouched generic outbox; CLI `change comment/comments/resolve/request-review` with cli-contract.md + agentsmd.Commands in lockstep (drift-tested); MCP graduates `list_change_comments` as the SEVENTH v1 tool (catalog flip + passthrough over the new REST endpoint). Caught in the work: regenerating the committed AGENTS.md clobbered this repo's hand-written self-hosting operating manual and was reverted — the drift test pins the GENERATOR's output, not the file |
| 2026-07-10 | **Stage 16b shipped: web review threads + attention inbox (§13.4.1–13.4.2, §17.2)**: the stacked diff anchors current-head line threads inline under their rows (hover "+" on any numbered line opens a composer; head side for lines the change's version has, base side for removed lines — the CLI's --side semantics), file-level threads at the file card top, change-level ones in a Conversation card, and everything written against an older head in an "outdated" group — marked, never floated, the §13.4.1 rule made visible. Resolve/reopen on thread roots; agent comments carry the existing AuthorChip badge. MergeGates gains the Attention section (derived set + "(you)" highlight + request-review input); the Changes inbox pins a "Needs your attention" card for signed-in principals (`inAttention` matches plain names and `user:` refs, never groups) and every open row gets the AttentionChip — §17.2's owner attention inbox, driven entirely by the set the daemon derives. Pure thread grouping/partitioning in `web/src/lib/comments.ts` (vitest-pinned, incl. missing-head_sha-reads-as-outdated); the demo transport mirrors the server semantics (head_sha stamp, one-level refusal, root-only resolve, DERIVED attention recompute on comment/request/approve) with fixture threads on the demo stack, pinned by a fake-transport suite. Read-only public_read browsing sees threads but no composers |
| 2026-07-11 | **Single-use agent workspaces (§8.7's stricter-defaults posture applied to §12.2; user direction: one task = one workspace, agents never share)**: `workspaces.status` becomes load-bearing at receive - the funnel refuses snapshot AND change pushes into a `closed` workspace with a create-a-fresh-one suggestion (unconditional: it is what closed means; the refusal-at-push is the enforcement design - agents reliably obey structured rejections, unreliably obey documentation). `--single-use-agent-workspaces` (default ON, `RUNKO_SINGLE_USE_AGENT_WORKSPACES=false` to opt out) closes an AGENT-owned workspace the moment its last open change lands or is abandoned; humans keep long-lived workspaces, store-backed accounts are always human, so only flag-config agent principals qualify. Sharing across named principals was already owner-blocked at both push paths; the remaining hole - two agents under one credential - is operational (per-agent principals), not enforceable server-side, and the skill/AGENTS.md discipline line now says so outright |
| 2026-07-12 | **Automerge (§13.5's "when ready" verb)**: a Change is ARMED once (`runko change automerge` / `POST .../automerge` / the Change page's "Auto-land when ready" button) and lands itself the moment merge requirements go green - the loop every client had been hand-rolling as poll-and-land. One AutomergeWorker per server (root + each org) sweeps armed open Changes on a 30s tick and is KICKED eagerly by the three events that flip mergeability (check report, approval, a parent landing - stacked automerge drains bottom-up); the automatic land runs the exact same landChangeCore as a human land (same gate, never force) attributed to the ARMING principal (landed_by = armer). The bit survives amends by design - §13.5's amend semantics already reset approvals and checks, so nothing lands ungated. States a land cannot resolve alone (requires_revalidation, conflicts - both need a re-push) stay armed but are memoized per (change, head, trunk-tip) so the worker never spins; only open Changes arm (landed terminal, abandoned must reopen), disarm always works. changes.automerge/automerge_by (migration 0014), ChangeSummary fields 17/18 + SetAutomerge RPC |
| 2026-07-11 | **Ephemeral agent identity (§15.1's third principal kind; user direction: agents come and go, often ten at once - identity must be runko-scoped, never operator config)**: `agent_principals` (migration 0013, org-scoped) minted over the API - `runko agent create --task <slug>` returns `agent-<task>-<suffix>` + a random 256-bit token exactly once (sha256 stored, indexed lookup, no KDF), TTL default 8h capped 168h, revocable, long-expired rows swept at mint. The task-embedding name IS the attribution: authored_by, workspace owner, and the §8.7 badge answer "what was this agent doing" by construction. Resolution wired into all three surfaces (bearer, Basic name:token, and the funnel's REMOTE_USER path), so ONE mint arms every dormant agent enforcement - receive-time policy (affinity, per-change caps, DAG nudge), single-use workspace auto-close, no-sharing owner checks, self-/agent-approval denial. Hard rule with teeth: an agent credential may never mint (`agents_cannot_mint` - no self-replication, no extending its own lifetime); the harness or a human mints at task start, and a shared-credential agent's first act is self-demotion. Deliberately NOT an auth system still: no rotation/federation - OIDC stays §15.1's endgame |
| 2026-07-11 | **Per-change size caps + the split nudge (§8.7 caps made pro-stacking; user direction: agents make big changes instead of stacking)**: the agent policy's `max_changed_files`/`max_diff_bytes` caps were measured against the WHOLE push delta - and a push is a stack under series receive, so ten small stacked changes tripped the same wall one monolith did: the cap punished exactly the splitting it exists to encourage, with a refusal that suggested nothing. Now caps measure each series member's OWN delta (commit vs first parent): a monolith is refused BY NAME ("change X is too big as ONE change") with the split workflow in the rejection (`jj split` / `jj new` per step; one push still carries the whole stack), while the same content as a stack passes. Affinity/denylist/owners stay whole-push (the union of members' paths - equivalent). Below the hard cap, a change over HALF of it earns an advisory `remote:` line on the ACCEPTED push - the nudge arrives before the wall. Skill + generated AGENTS.md gain the stacking discipline, including the true incentive: smaller changes scope required checks narrower and land faster. **DAG, not line** (same session): a stacked step touching NOTHING its parent touches earns a second advisory - orthogonal changes belong on parallel workspace branches (the capability §12.2's branches already provide), where neither waits for the other; top-level-directory disjointness is the deliberately quiet proxy (shared prefix = silence, wording conditional - file overlap is not semantic dependence, and its absence is not independence) |
| 2026-07-11 | **Workspace deletion (§12.2's registry grown its last verb)**: `DELETE /api/workspaces/{id}` + Connect `DeleteWorkspace` + `runko workspace delete <id>` + a web Workspaces-page button. One decision core, three guards: owner-only for named principals (operators exempt; the anonymous deploy token passes, the same rule snapshot pushes enforce), refused with the blocking change keys NAMED while the workspace has OPEN changes (their provenance would dangle and any re-push would fail §12.2's changes-are-born-in-workspaces validation; landed/abandoned origins are history and never block), and effects in recoverable order — every `refs/workspaces/<id>/*` branch ref deleted first, registry row last, so a partial failure leaves a retryable delete, never an orphaned row pointing at nothing. Hard delete: the id is immediately reusable; blobs stay until git gc (deletion removes reachability, not history). Local checkouts are never touched — the CLI does not delete directories it didn't create this run |
| 2026-07-10 | **Stage 17 shipped: tag governance at receive (§14.10.3, §11.4)** — the funnel's refs/tags/* unconditional-skip becomes a gate behind `OrgSettings.enforce_tag_policy` (default OFF = the documented permissiveness; unreadable settings fail OPEN with a log line, because the default here IS permissive — the inverse of the merge gate's fail-closed). Under enforcement a tag create/move/delete requires the operator credential, an admin config principal, an org membership role of admin or the NEW `releaser` role (add-member validation extended), or a bot lane whose new optional `tags=` allowlist (same MatchPath glob dialect) covers the tag name. Lane identity reaches the funnel via a new REMOTE_LANE env beside (never as) REMOTE_USER — overloading REMOTE_USER would have silently subjected lanes to workspace owner checks and authored_by attribution built for principals; requireGitAuth sets it, the hook forwards it as X-Runko-Remote-Lane, Processor gains BotLanes. Rejections are the §6.9 script (why + "cut a release instead" + "ask for the releaser role"). `authorizeTagWrite` is deliberately a shared decision function: stage 17b's server-minted release tags answer to the same code, so there is nothing to bypass. The mirror keeps transporting refs/tags/* fast-forward-only, unchanged. Pinned by a Processor-level gate table and an over-the-wire e2e (real git push refused with the script; releaser-role push accepted) |
| 2026-07-11 | **Check classes decided + shipped (§14.5.9, new; user observation: "affected is running related projects' unit tests — only integration tests should run")**: `ci.checks[].run_when: direct|affected` (default affected = unchanged semantics). The affected closure was right about WHICH projects, silent about WHICH OF THEIR CHECKS guard the cross-project contract; now a dependent's unit lane (`run_when: direct`) skips closure-affected runs while its integration lane rides as before. run_everything ⇒ everything direct (fail closed); unknown values normalize to affected at scan (the scanner feeds the gate — dropping a check silently is the one unforgivable direction). The filter is ONE shared function, `index.ChecksFor(project, direct)`, called by both the gate (`requiredCheckNames`, which switched from the unfiltered RequiredChecks names to it) and the executor (`runko-ci checks`) — the §14.9.1 lockstep rule grown a second axis, pinned by a gate-side httptest proving the gate never requires what the matrix won't run. `affected.ProjectRef` surfaces the Direct flag Compute always tracked; §14.5.8's snapshot-diff union marks graph-named seeds direct, their dependents closure-shaped. Honest ledger in the section: warm bazel caches already skip unaffected targets (this buys matrix overhead, cold caches, non-bazel checks), and `direct` is a project's assertion its lane guards no cross-project behavior. First carve: platform-race + runkod-race go direct (race = own-code class; -test lanes stay the integration surface). Version-skew note: prod daemon restart required promptly — an old gate + new executor deadlocks dependent-only changes on the race lanes |
| 2026-07-11 | **Stage 17b shipped: releases end-to-end (§14.10.3)** — migration 0012's `releases` table has NO update/delete queries anywhere (immutability by construction, the GitHub parity); `runko release create --project <p>` resolves the project + its `release` capability config from the PROJECT.yaml blob at trunk tip (index.Scan drops capability_config — tree-as-truth, not the index), versions by explicit `--version` (semver-validated unless `versioning: manual`) or patch-bump (0.1.0 first), derives the changelog from commits `lastRelease.target..trunk -- <path>` resolved through Change-Id trailers to LANDED Changes (the repo browser's trailer-extracting log format reused; head_change_key = newest resolved), writes the annotated tag server-side via a new `gitstore.CreateAnnotatedTag` (tagger = the land engine's server identity; refuses to move existing tags — IsTagExists), records the row, triggers the mirror (tags already transport), and emits `release.created` (new `checks.ReleaseCreatedWebhook` pinned to its schema by a contract test). Authorization is stage 17's SAME `authorizeTagWrite` decision — under `enforce_tag_policy` a plain principal gets `release_denied` while operator/admin/releaser/lane-in-namespace cut normally. Surfaces: REST `POST|GET /api/projects/{name}/releases` (the first project-scoped route family), ProjectService `CreateRelease`/`ListReleases` (ListReleases public-readable; both gens regenerated), CLI `runko release create|list` with cli-contract.md + agentsmd.Commands in lockstep. Pinned by an httptest end-to-end (land → 0.1.0 with the Change named in the changelog + a real annotated tag in the bare repo + the outbox delivery → land again → 0.1.1 covering only the delta), structured-failure and policy-refusal tables, a live-Postgres round-trip incl. the UNIQUE version constraint, and Connect error-detail coverage |
| 2026-07-10 | **Tags and releases decided (§14.10.3 rewritten; resolves §22.3 #10; second track of the Nx/GitHub research pass)**: `refs/tags/*` moves from unconditional-accept to receive-funnel policy — org release role, tag-namespace-scoped bot lanes (`can_write_tags` globs, §14.10.2's pattern applied to ref namespaces), server-created release tags through the same policy code; org knob starts permissive, flips under default-deny. Releases become immutable first-class objects `{project, version, tag_ref, head_change_key, changelog, created_by}` cut by `runko release create`: version per the new `release` capability (`capability_config.release`, second documented exception after `build`), **changelog derived from landed Changes since the last release tag** (the Change description is the version plan — no nx-style version-plan files), annotated per-project-prefix tag written server-side, `release.created` webhook as §14.10.1's missing CD trigger (new standalone schema — the change-event envelope requires a `change`, releases aren't change events). Publishing stays customer CI (§14.1 division unchanged); GitHub immutable-releases parity with the tag→commit→Change chain as the attestation anchor, artifact attestations remaining customer-side. §11.4's documented permissiveness now cites its closure (stage 17); DAG stages 17/17b added |
| 2026-07-10 | **Landed timestamp surfaces (§17.2; user report "changes lacks date/time of landing")**: `changes.landed_at` existed since migration 0001 and was set by the land query, but never left Postgres - `runkod.Change` now carries `LandedAt` (MemStore stamps `s.now()` in MarkChangeLanded; the pg hydrator maps the column), `ChangeSummary` gains `landed_at = 16` (unix seconds, 0 until landed - the Comment.created_at convention), REST serves it for free via the struct marshal, and the web UI renders it where landed state already shows: the Change page header ("landed as <sha> · 2h ago", absolute time on hover) and the Changes history rows ("landed 2h ago"), via the existing `timeAgo` plus a new `absoluteTime` tooltip helper. Fake transport/fixtures stamp deterministic land times (fixture epoch; first-five-digits cap because a 40-hex Change-Id's digit run overflows int64 - caught by vitest) |
| 2026-07-10 | **Running checks become clickable (§14.4.2, §17.2; user report "I can't click on the running ci checks" + direction "utilize the watchdog service")**: two halves. runko-checks.yml's in_progress report now carries `--details-url` (the run URL is known at first report; the completed report later overwrites with the same link), and gains `run-name: "checks: <change_id>@<head_sha>"` - a CONTRACT with the watchdog's new run DISCOVERY. runko-watchdog grows a third remedy between force-report and rescue: a pending check with NO details_url gets its run located by the run-name stamp in the runs-list API and its same-named matrix job fetched - a running job is reported in_progress with the job's deep URL (ungated by the grace window: a link is a fact, not an intervention), a finished one force-reports its REAL per-job conclusion (strictly better than burning the rescue rerun on work CI already did; run-level conclusion would be wrong under fail-fast:false matrices). Discovery misses (pre-stamp runs, dispatch latency, foreign CI, empty repo allowlist) fall through to the existing grace-gated single rescue; discovery answers are fetched once per (change, head) per sweep. The UI needed nothing: MergeGates already links any check whose details_urls entry exists |
| 2026-07-11 | **Workspace observability decided (§12.6 new; §12.3, §17.1, §17.2, §17.4 touched; DAG stage 18; user direction: "see the agent's work as it's doing work in its workspace")**: live per-branch WIP view + activity timeline for workspaces. Cadence = `runko workspace watch` (dirty-triggered auto-snapshot through the existing §11.5 funnel; commits built OUT-OF-BAND via temp index + `commit-tree` because the interactive snapshot verb mutates HEAD/index and would race the agent's own git/jj — the load-bearing mechanics constraint) plus best-effort auto-snapshot on `change push`; NOT streaming sync — §12.3's write-side deferral stands, durability remains discrete policed snapshot commits. Read side: stats-only `workspace_events` rows recorded at receive/land (numstat totals, never file content per §12.1; capped per workspace, deleted with it) + an in-process per-org bus feeding `WatchWorkspace`, the Connect surface's first server-streaming RPC — frames are pokes, clients refetch via unary `GetWorkspaceDiff`/`ListWorkspaceEvents`; lossy-with-coalescing delivery, ~25s keepalives, workspace RPCs stay off the public-read allowlist; single-replica bus assumption stated. `workspace.snapshot` outbox webhook explicitly deferred (no external consumer yet) |
| 2026-07-11 | **run_when sweep (§14.5.9; user direction: "scan through the codebase and actually use it")**: full audit of every consumer and every declared check. Consumers were already lockstep (gate `requiredCheckNames` and executor `runko-ci checks` both resolve through `index.ChecksFor`; run_everything treats all projects direct, fail closed) with ONE gap found and closed: the snapshot-diff de-escalation seeded `CloseOverDependentNames` with the whole de-escalated floor - closure-pulled members got stamped Direct and their unit lanes wrongly rode narrowed post-land matrices; the seed is now direct floor members + diff-impacted projects only, pinned by a mixed-change test (and its first draft caught itself touching b's manifest in the head commit, making b genuinely direct - manifest edits are direct paths). Check audit verdict: the race lanes (already `run_when: direct` from 7dc47a99) remain the ONLY sound direct-only lanes. Deliberately staying affected-class: `bazel-check` (closure coverage is load-bearing - a proto-only change reaches gazelle drift ONLY via a closure-affected Go project, and internal declares no bazel-check of its own), `web-check` (the web→proto boundary-artifact rule is precisely a closure obligation), `cli-test`/`watchdog-test`/`*-test` (integration lanes: compiled-binary e2e and cross-project JSON-shape consumers). No-ops left unmarked rather than decorated: `internal-test`/`docs-check`/`contracts-test` (bottom-of-graph or root projects that are never closure-affected - a marking there would be dead policy text) |
| 2026-07-11 | **Code browser syntax highlighting (§17.2; user request)**: BrowsePage's code AND blame views highlight through lowlight (highlight.js emitting a hast tree) with fifteen REGISTERED grammars (Go/TS/JS/YAML/JSON/proto/python-for-Starlark/bash/SQL/dockerfile/makefile/CSS/XML/markdown/ini) - no auto-detection (slow, and wrong often enough to be worse than plain), unmapped extensions render plain. The tree is flattened to (class, text) tokens and split at newlines so multiline tokens (comments, strings) keep their class on every line they span - pure React text nodes, no innerHTML, and the per-line token arrays slot into the existing line-table/blame-region markup unchanged. The grammars ride a LAZY chunk (72K, dynamic import on first file view; the main bundle is untouched) with plain text as the loading/failure floor; >512KB or >10k-line files skip highlighting (display sugar must never hang the browser). Palette: five token roles, scoped under .line-content, light + dark. Vitest pins the line-count identity (tokens reassemble to the exact source), multiline-class spanning, and the null fallbacks |
| 2026-07-11 | **Agent session activity decided (§12.6.1 new; §7.2's coding-session link materialized; DAG stage 19; user direction: "live update of what the agent is doing, including what files it's reading and what command it's running" + "which file the agent is editing before it's being pushed... at a glance")**: harness-reported activity events — the platform's first *client-claimed* rows, structurally observability-only (never policy/gates/affected; §8.7 in reverse). Ingest `POST /api/workspaces/{id}/activity` (batch ≤100, owner-only like snapshot pushes, closed refuses, actor from principal never body); soft kind vocabulary `read|edit|command|search|note` with unknown→`note` (telemetry never fails the work); detail truncated to 240 runes + funnel-scanner redaction per event, whole-batch `[redacted: scan_error]` on scanner failure (fail-closed). Storage: new `workspace_activity` table, NOT `workspace_events` (trust class + hertz-volume would evict the timeline's 500-cap history), cap 1000 prune-at-insert, deleted with workspace, kept on close, `session_id` column carries the coding-session link. Delivery rides §12.6 unchanged: one bus-only `agent_activity` poke per batch (never a stored timeline row), unary `ListWorkspaceActivity`, `WorkspaceSummary.latest_activity` for the at-a-glance presence line (derived from newest row, no presence store). Client: `runko agent event` (one POST per tool call v1; `--from-hook` parses harness post-tool-use JSON itself — batch body makes a future spooler an API no-op) + `runko agent hooks` snippet printer; verb-local `RUNKO_RUNKOD_URL`/`RUNKO_TOKEN` env fallback (hooks inherit env, not flags). Per-harness self-reporting is the whole v1 story — generic observation (strace/eBPF/FUSE) stays rejected per §12.3 |
| 2026-07-11 | **Server-side stack sync — the Change page's Sync button (§13.5's rebase machinery, §12.3's sync verb without a checkout; user request "trunk moves underneath the stack... add a sync button; if there're conflict, show that there's a conflict")**: `ChangeService.SyncChange` + `POST /api/changes/{key}/sync` rebase the requested Change's WHOLE stack onto the current trunk tip inside the daemon's own repo - `land.Rebase` (merge-tree with the recorded base as explicit merge-base) per member bottom-up, `land.CommitTree` (now exported) minting each new head with original author/message/Change-Id preserved (committer = machine, the rebase-land identity rule). COMPUTE-FIRST, WRITE-LATER: merge-tree/commit-tree only create objects, so all members rebase in memory before any ref moves - one conflicted member returns `{conflict_change_id, conflicts[]}` and leaves every ref and Store row untouched (a partially synced stack is exactly the base≠parent-head state the stack view can't derive). A clean sync CAS-moves each `refs/changes/<id>/head` against the head it computed from (concurrent push ⇒ retryable 409 `sync_race`), re-records base/head via CreateOrUpdateChange (title/author pass through - sync moves commits, not ownership; empty origin preserves provenance), and re-enters the funnel tail per member: computeAffectedAndEnqueue re-triggers required checks against the new heads, approvals go stale on their own (Approval.HeadSHA), mirror trigger rides along. The child-of-a-REBASE-landed-parent case (recorded base = a commit trunk never contained) is the same 3-way merge onto the tip carrying the parent's landed content - pinned by test. UI: Sync button beside Land; synced / already-in-sync / conflict render as land-style banners, and the post-action reload swaps in the rebased heads/diff/requirements. Fake transport mirrors the re-chaining for the demo scene. Deliberately NOT client-side: `runko workspace sync` remains the conflict-RESOLVING path; the button covers the no-conflict 90% without a workspace |
| 2026-07-11 | **The product teaches its verbs at the moment an agent reaches for raw git (§6.9, §8.8, §12.3, §21; user report "on a fresh agent, the default scm command is still git, not jj or runko")**: a fresh agent's default scm verb is `git commit`/`git push` out of training-data muscle memory, and everything that said otherwise was advisory prose it may never read. Two product surfaces, no Claude-specific config (deliberately NOT a client-side hard block - the user wants the experience any real agent gets, and §6.9's parity rule makes plain git a contract): (1) `agentsmd.Generate`'s orientation gains the load-bearing bullet - raw git is transport only; commit = `runko change create`, submit = `runko change push`, jj = surgical history work - so every generated AGENTS.md teaches it up front (§8.8 snippet updated to match); (2) an ADVISORY pre-commit verb-nudge hook: prints the runko verbs (in EVERY checkout - a `.jj` colocated one gets the same verbs plus a jj-is-for-surgery note, per §21's same-day repositioning below) to stderr and always exits 0, so the nudge lands in the command output agents actually read at the exact moment of the mistake, one moment EARLIER than the §6.9 pre-receive script. Installed by `runko doctor --install-hook` (alongside the commit-msg hook; DoctorReport gains HasVerbNudgeHook) and - the golden path - by workspace materialization into the SHARED clone's hooks dir, covering every worktree, retrofitting existing clones on the next create/attach (ensureSharedClone). Two silences are load-bearing: runko's own verbs mark their git subprocesses RUNKO_INTERNAL_GIT=1 (runGitEnv) so `change create`/`workspace snapshot` never nudge about themselves, and jj runs no git hooks, so the jj-first daily driver never sees it - the hook fires precisely and only on a raw `git commit`. A foreign pre-commit hook is never clobbered (doctor says so instead; workspace materialization skips silently) - the nudge is sugar, a real hook is policy. Pinned by raw-vs-internal stderr tests, the jj-note branch, the foreign-hook refusal, and a workspace-materialization commit test |
| 2026-07-13 | **Accounts are per-org (§15.1, §7.1; user direction "user should be per org - you can have the same username for different orgs"; migration 0017, superseding the identity half of the 2026-07-09 org-scoped-sessions decision)**: an account row is (org, name, credential) - the Slack-workspace model - so two orgs can each have their own "victor" as different humans with different passwords. What changes: signup's name_taken is scoped to the target org; sign-in on an org surface resolves exactly (that org, name); a credential that verifies only against ANOTHER org's same-named account still answers 403 deniedOrg (never 401 - "wrong password" vs "wrong org" stays distinguishable, via the new Directory.ListPrincipalOrgs cross-org probe); the credential cache keys by (org, name); the hub's org selector lists memberships whose OWN org account verifies this credential (same name+password in n orgs = one human's n accounts, listed together; different passwords = strangers, never leaked into each other's selectors); the funnel resolves stored principals as (processor org, name). What dies: cross-org member-add - POST /api/orgs/{org}/members requires the account to exist IN that org (unknown_principal otherwise, suggestion says to sign up into it; per-org invites remain the recorded follow-up) - and operator paste-into-org recovery for interrupted signups (self-service re-signup, #44, is the path). New obligation: a stored account creating a second org via POST /api/orgs gets its VERIFIED credential cloned into the new org's account space (else it could never sign in there). org_members rows are unchanged (roles/reach); operator principals, bot lanes, and the deploy token stay server-wide config; agent principals were per-org already. Migration backfill splits each global account into one per-org account per membership (memberless rows are dropped - they could sign in nowhere). Pinned by TestSameNameDifferentOrgs + the reworked matrix/org suites |
| 2026-07-13 | **Signup is idempotent for a matching credential (§15.1; found by the sign-in/sign-up smoke matrix, migration finding #44)**: POST /api/signup with a name that already belongs to a stored account whose password VERIFIES treats the account half as a no-op and proceeds to the org half - the recovery for an interrupted create-mode signup, which used to strand a real account (member of nothing, name_taken on every retry, 403 everywhere, admin-only rescue). Front gates unchanged (signup disabled / bad invite code refuse recovery exactly like a fresh signup); a NON-matching password keeps the name_taken contract, so the endpoint answers nothing sign-in doesn't already; a recovered re-join never demotes (an org admin re-presenting their credential keeps admin); the create-failure message flips "was created" -> "already exists" so the user learns the credential is real. Companion fix, same matrix run: handleAddOrgMember EnsureOrgs before upserting (the default org has no directory row in mem mode until first join - adding a member to it 500ed). The matrix itself (docs/smoke-plan.md "Control-plane sign-in/sign-up matrix", runkod/signin_smoke_test.go) is the standing contract: every credential form on every org surface, happy paths zero-error, refusals structured |
| 2026-07-11 | **runko CLI is the primary interface; jj repositioned to surgical client (§21, §8.8, §17.1; user direction "let all basic operations be done by runko cli, and use jj only for surgical or diagnosis purposes")**: the 2026-07-08 "jj colocated is the primary client" decision taught two everyday command languages side by side, and every teaching surface (doctor cheat-sheet, verb nudge, generated AGENTS.md, the workspace skill) had to fork on "am I in a jj checkout?". Repositioned: basic operations - commit (`runko change create`, which stacks naturally when repeated), submit (`change push`), land, snapshot, branch, sync - are the `runko` CLI in EVERY checkout, jj colocated included (verified: `change create` behind jj's back is absorbed by jj's auto-import on its next invocation, identity intact since the baked Change-Id travels in the message). jj remains first-class for what nothing else does: mid-stack rework (`jj edit`/`jj squash` + descendant auto-rebase), `jj split`, history diagnosis - the surgical set. NO machinery changes: the trailer template, series receive, `change push`'s jj-awareness, and `workspace sync`'s jj-aware rebase all stand; this is a positioning/teaching decision, transcribed into the doctor cheat-sheet (runko loop first, jj section reframed "surgical use"), the verb nudge (runko verbs everywhere + a surgery note in colocated checkouts), agentsmd orientation/discipline bullets, and the repo workspace skill. Future option, deliberately not now: runko-native `change amend`/`change split` verbs would make jj fully optional even for surgery |
| 2026-07-13 | **Change descriptions served end-to-end (§8.6 Summaries, §7.4 fields; user direction "PR description - so agents can add a small blurb about the change")**: the pieces existed unwired since day one - `changes.description`/`test_plan` in migration 0001, ChangeSummary proto field 8 marked "not yet served", `description`/`test_plan` already optional in common.schema.json - this connects them. `POST /api/changes/{key}/describe` sets either field on an OPEN change (structured `invalid_state` otherwise - a landed change's description is part of the record §14.10.3's release changelog derives from); omitted field preserves, explicit "" clears. Semantics pinned: the blurb is change-scoped control-plane state set EXPLICITLY (the §8.3 sketch's `update_change_description`), never derived from the commit message, and unlike Title it does NOT move with the head - an amend re-gates checks but keeps the summary (automerge's arming survives amends for the same reason). Surfaces: `runko change describe [--change <Id>] --description/--test-plan` (defaults to HEAD's Change-Id; contract row + agentsmd Commands move together under the cross-check test), REST/MCP get_change and `change list` carry both fields, the Connect surface serves description (field 8's "not yet served" marker retired), and the change page - which already rendered description in anticipation - gains §8.6's missing half, the prompt-if-empty nudge naming the verb. Pinned by daemon core/HTTP tests (set, preserve-on-omit, clear, amend-survival, guards) and CLI wire-shape tests |
| 2026-07-13 | **Landed history carries one canonical identity (§7.5, §13.5; user direction "the mirror author is inconsistent - Runko, Runko Workspace, my credential - standardize it"; supersedes the author-preserved half of the rebase-LAND identity rule - the committer-is-machine half stands, and SyncChange still preserves the author on its PRE-land heads, which re-stamp when they land)**: git's author/committer are a CLIENT artifact - a commit reached trunk stamped with whatever identity (or synthetic fallback: `Runko`, `Runko Workspace`, an unconfigured VM's git default, the same human under two emails) happened to be active on the machine that wrote it, and the outbound mirror (which transports trunk byte-for-byte, §18.6) faithfully published the mess. Decided: the land engine stamps a single configured identity (`--land-identity`/`RUNKO_LAND_IDENTITY`, default `Runko <runko@localhost>`, a deployment sets its own host) as BOTH author and committer on every landed commit; per-author attribution is a Runko concept (authored_by/landed_by, surfaced in the UI/CLI), never smuggled through git identity fields. The fast-forward land path - which used to adopt the pushed commit verbatim - now RE-MINTS it too (same tree, same parent, canonical identity), so a fast-forward land also yields LandedSHA != HeadSHA, exactly like a rebase land. Stacked consequence, handled: a synced stack used to land in sequence because fast-forward preserved each member's SHA (the child's base stayed literally on trunk); re-stamping breaks that, so mergeRequirements' base-on-trunk gate and the land-time refuseUnlandedParent guard now treat an identity-only re-mint (the parent LANDED with an identical tree) as effectively-on-trunk - the child's empty base..head delta rebases cleanly onto the tip - while a genuinely rebase-landed parent (its tree absorbed the trunk delta) still needs a re-sync ("it landed as a different commit"). Client-side WIP fallbacks collapsed to the one `Runko` placeholder (`change create`, snapshot, sync, watch - no more `Runko Workspace`); they never reach the mirror now that the daemon re-stamps at land. Existing mirror history is NOT rewritten (a force-push freezes the freeze-on-divergence mirror); this standardizes going forward. Pinned by land-identity re-mint tests on both the fast-forward and rebase paths, the bootstrap re-stamp, and the stacked/synced-stack land suites |
| 2026-07-13 | **Agent changes must carry a description to land — the hard companion to §8.6's soft prompt (§8.7 AgentPolicy; user direction "all agent changes should include description")**: §8.6 shipped the change-description field + a prompt-if-empty nudge but explicitly "never a merge gate"; this makes it mandatory FOR AGENTS. New `AgentPolicy.RequireDescription` (default ON in `DefaultAgentPolicy`) turns an empty §8.6 `change.Description` into a merge blocker for agent-authored changes: `mergeRequirements` (§13.5) sets `Mergeable=false` with a blocker naming `runko change describe`, and land/automerge refuse on the same gate. Deliberately a MERGE-TIME gate, not receive-time (unlike the §13.3 size caps) — the blurb is control-plane state set AFTER the push and never derived from the commit message (§8.6), so it is evaluated against `change.Description` at land, keyed on the authoring principal via the new `Server.agentPolicyForAuthor`. Resolution is LIVENESS-INDEPENDENT: an ephemeral agent's row persists for attribution after its TTL lapses (§7.5), so its undescribed change stays gated rather than becoming mergeable when the credential expires. Humans and the anonymous deploy token are exempt (an AgentPolicy gate); a bot lane lands under its own policy. Pinned by the default-policy assertion and runkod merge-gate tests (agent no-description → not mergeable naming the fix; `describe` → mergeable; human author exempt). NOTE: the change page still renders the description as plain text — GitHub-flavored **markdown rendering** of `change.Description` is the recorded next step (web, orthogonal) |
| 2026-07-13 | **Invite-request flow (§15.1; user direction "you need an invite code to sign in, but there's no way to get the invite code")**: public `POST /api/invite-requests` (`{name, email, message}`) on deployments running `--allow-invite-requests` stores a DEPLOYMENT-WIDE request row (migration 0019 - no org column: requests precede accounts) carrying the webhook-outbox lifecycle (§14.4.1: pending/sent/failed/dead_letter, attempt + next_attempt_at computed server-side via checks.NextBackoff, dead-letter at MaxDeliveryAttempts); `GET /api/auth/config` advertises `invite_requests_enabled` so the login gate offers "No invite code? Request access" exactly when the deployment can act on it. New `mailer/` project (runko-mailer - the watchdog deployment shape: ships in the runkod image, own single-replica Deployment, RUNKO_MAILER_* env) polls the operator-only due feed (`GET /api/invite-requests/due`, deploy token) and emails the operator over stdlib net/smtp (STARTTLS when advertised; Reply-To = the requester, so replying with the invite code IS the fulfillment), acking `/sent` or `/failed` back - at-least-once, same as the outbox. Abuse posture: publicCORS 1 MB body cap + field caps (name 120, email 254 via net/mail, message 2000; CR/LF refused - SMTP header-injection guard) + honeypot field (silent 202, nothing stored) + per-IP fixed window + live-backlog cap + one live row per email (partial unique index; duplicates answer an idempotent 202). Deliberately NOT an invitation system: no codes minted, no auto-reply, no admin UI - the operator replies by hand; per-org email invitations remain the recorded follow-up |
| 2026-07-13 | **Stacked children land under a rebase-landed parent without a forced re-push (§13.5; user report "landing a stack, the first lands but the rest demand revalidation even with nothing between them")**: the same-day land-identity change (§7.5) made EVERY land re-mint the commit, so a parent that rebase-lands (absorbs any trunk delta) always changes SHA; the stacked-base gate only forgave an IDENTICAL-tree fast-forward re-stamp (`parentReStampedInPlace`), so a child whose parent absorbed even a DISJOINT change was refused with "it landed as a different commit - rebase and re-push". Broadened to `parentLandedOnTrunk`: a child whose parent has LANDED with its commit on trunk is mergeable regardless of tree - the parent's content IS on trunk, so land.Land rebases the child's base..head delta onto the tip and re-runs checks ONLY when the actual trunk delta since the child's base intersects its affected set (§13.5's optimistic rule, unchanged). Net: a synced stack lands member-by-member with no re-push; an intervening change that touches the child's projects still revalidates it (its checks never saw that change); a landed parent whose commit trunk no longer contains (history rewound) stays the one genuinely-stranded stacked-base blocker. refuseUnlandedParent already permitted this (it refuses only a still-OPEN parent). Pinned by disjoint-intervening-lands-clean + overlapping-intervening-revalidates tests and the reworked stacked-base-blocker matrix |
| 2026-07-14 | **Streaming becomes the golden path (§12.6, §12.6.1, §6.9; user report: agents weren't streaming - watch and the activity hooks existed but nothing in the golden path turned them on; migration finding #46)**: three moves, no new machinery, no schema/proto change. (1) Provision teaches: `workspace create`/`attach` and `agent create` print the exact next commands (start `runko workspace watch`, run `runko agent hooks --install`) - the §6.9 script pattern at the moment a workspace is born; human output only, `--json` stays pure. (2) NEW `agent hooks --install` merges the PostToolUse snippet into the worktree's Claude Code `.claude/settings.local.json` - explicit opt-in preserving the 2026-07-11 "no client-specific config is written automatically" posture (plain `agent hooks` stays the harness-agnostic printer): safe merge (foreign keys survive, marker idempotency, unparseable JSON refuses untouched - the verb-nudge non-clobber posture), snapshot exclusion via the shared clone's `info/exclude` (shared across worktrees by git's layout; snapshot staging honors excludes), and the exact env exports printed when no credential resolves. (3) The funnel nudges exactly once: a workspace's FIRST accepted change push that never streamed - no prior `change_pushed`, at most one `snapshot_pushed` (tolerating the push's own auto-snapshot; zero also qualifies, covering `--no-snapshot` and failed auto-snapshots), zero activity rows - earns an advisory `remote:` block naming both commands (server-side for §6.9 parity: raw-git/jj pushers get it too; fail-open, never blocks, never repeats). Plus the read side: the web Agent activity card gains client-side kind filter chips (per-kind counts, all-on default, selection persisted per browser) and a kind→glyph map realizing §12.6.1's "now: ✎ path" presence line. Deliberately NOT a DAG stage - dogfood polish on shipped stages 18/19 (the automerge/Sync-button/verb-nudge precedent) |
| 2026-07-14 | **API contracts live in the owning project; consumption is a declared edge, enforced at receive (§13.3.1 new; §7.2, §10.1 touched; user direction "rethink the way projects communicate — the mailer connects to nothing yet something calls it; proto should live within the project boundary so runko can check whether a consumer is supposed to use it; mandate OpenAPI for REST; determine it at project creation; switch mailer to gRPC"; migration finding #47)**: three legs. ① In-boundary contracts: the §7.2 `rpc` capability gets its real shape — `capability_config.rpc: {path}` (default `proto`), sources at `<project>/<path>/`, committed codegen at `<project>/<path>/gen/`, per-dir buf.gen.yaml; the `http` capability gets the REST dual — `capability_config.http: {openapi}` (default `openapi.yaml`), the document mandatory in-boundary (receive refuses `http` without it). The standalone `proto/` project is TRANSITIONAL (declares `rpc: {path: .}`; its runko/v1 surface migrates into runkod's boundary as a recorded follow-up, web's edge flipping proto→runkod). ② Consumption enforcement: a Go import under another project's contract gen dir requires a DIRECT declared dependency edge (project-grade strict-deps); `platform/contract` checks the push's changed .go files synchronously at the pushed head as a fourth funnel step, rejecting `undeclared_contract_dependency` — NOT a reopening of §13.3 (gating still consumes only declared edges; the check refuses provably incomplete declarations, imports being declared facts at head_sha, the §14.5.4 trust class); changed-files-only scope recorded as the v1 gap (edge removal with stale imports in unchanged files escapes; full-tree audit is the test-level backstop). ③ Creation-step determination: `runko project create` requires `--api grpc\|rest\|none` for services (enum flag, product action; scaffolds rpc+proto stub / http+OpenAPI 3.1 stub / nothing). First application: the mailer feed moves to gRPC/Connect (`runkod/proto/mailer/v1` `InviteFeedService` ListDue/MarkSent/MarkFailed, operator-gated like the REST drain it replaces) and mailer declares `dependencies: [runkod]` — the runtime edge the graph was blind to becomes a declared fact . SUPERSEDED SAME DAY, before the model deployed: leg ② revised to the server/client `consumes:` edge — see the next 2026-07-14 row; kept as the record of the first cut |
| 2026-07-14 | **Contract consumption is a server/client edge: `consumes:`, contract-scoped (§13.3.1 leg ② revised same day, before deployment; user direction "project deps should be more of a server/client relationship, not import relationship")**: a whole-project `dependencies:` edge is the wrong grain for an API client — mailer would re-test on every daemon-internals change its stub-pinned tests cannot observe, and the recorded proto/→runkod migration would put web-check (npm ci + build, the costliest lane) on EVERY daemon change where today's web→proto edge fires only for contract changes. Decided: ① `consumes: [provider]` is a first-class manifest field (project.schema.json), distinct from build-grade `dependencies:` ("I build against/run your code" — integration harnesses keep it, with its conservative closure); ② the consumes closure is CONTRACT-SCOPED — a client joins affected only when the provider's contract surface changed (the capability_config.rpc path dir, the OpenAPI document, or the provider's own PROJECT.yaml, fail closed since a manifest edit can move the surface), joins NON-DIRECT (§14.5.9 direct-only lanes still skip it), never chains (joining does not change the joiner's contract), and rides the ordinary dependent closure from there; new reason code `consumes_contract` (webhook + MCP enums) — exactly the grain a §14.5.4 build-graph refinement computes natively, so the declared floor and the adapter agree; ③ receive enforcement accepts either edge: a gen-dir import must be sanctioned by `consumes` (the normal client case) or `dependencies`; ④ `index.AffectedProjectInfos` becomes the ONE IndexedProject→affected.Compute mapping (gate, land revalidation, webhooks, REST/RPC, runko-ci — six hand-built literals collapsed, so no caller can silently drop the new fields); ⑤ mailer's ops-lane edge becomes `consumes: [runkod]` |
| 2026-07-15 | **Gerrit's submission model adopted, full package (§13.5 rewritten, §22.1; user direction "if the trunk moved while a change is ready to submit, we should only prevent the submission if it touches the same code" + "do research on how gerrit does it - let's try to see if we can follow their lead"; supersedes the spec's original intersection-as-default and the 2026-07-13 stacked-children row's "(§13.5's optimistic rule, unchanged)" phrasing — the rule itself changes today; migration finding #48)**: two halves. (1) **Conflict-only landing** (Gerrit "Rebase If Necessary") becomes the default revalidation policy: a Change green at its current head whose server-side rebase applies cleanly LANDS with zero re-runs regardless of trunk movement — conflicts always block; `affected-intersection` (the former default, closure-vs-closure with RunEverything failing closed — kept verbatim under that scope) and `always` remain org opt-in tiers (`revalidation_policy` org setting > `--revalidation`/RUNKO_REVALIDATION > conflict-only); `never` stays admin-force-only; post-land ci.yml — the only CI that ever builds the actually-landed tree — is the accepted semantic-conflict net (the Gerrit-shop model); the v1.x merge queue stays framed as batching whichever tier is configured. (2) **Trivial-rebase carry-forward** (Gerrit `copyCondition: changekind:TRIVIAL_REBASE`): a head move whose message is unchanged and whose old base..head delta replays cleanly onto the new base yielding exactly the new tree (the land engine's merge-tree as the oracle — Gerrit's rebase-replay definition; server-side stack sync satisfies it BY CONSTRUCTION, the receive funnel detects it on client re-pushes, per series member) copies owner approvals to the new head under EVERY tier (the amend-invalidation rule stops approve-v1-amend-v2 bypass; a trivial rebase provably carries the identical approved diff, so the carve-out preserves the guarantee) and copies passing check-run results ONLY under conflict-only (carried checks would otherwise launder an intersecting trunk delta past an org that opted into stricter revalidation). Copied check rows are fresh attempt-1 rows stamped copied_from_head_sha (migration 0020); a fully-covered carried head emits NO change.updated (CI does not re-run; sync kicks the automerge worker directly); anything uncovered re-runs as today; conflict-resolved or amended heads copy nothing; a same-head re-push stays the documented CI re-trigger. Kills finding #48's O(N²) cascade: N green unrelated changes landing serially now re-run zero matrices instead of N-1 |

---

## 26. Next artifacts

1. **UX interaction spec**: create project wizard + empty states (human)  
2. **Project intent & minimal manifest schema** RFC — **pre-session-1 blocker** (§28.4)  
3. **MCP tool catalog** (JSON schemas, examples, error codes) — **pre-session-1 blocker** (§28.4)  
4. **AgentPolicy threat model**  
5. **Workspace glue & snapshot-refs design** (receive funnel details, retention/GC, Josh slice integration, VM environment contract)  
6. **Self-host compose/Helm** operational design  
7. **MVP milestone checklist** — seeded by the Appendix D session DAG (§28.3)  
8. **CI integration RFC**: webhook/Check JSON schemas, `runko-ci` CLI UX, GHA+Buildkite templates, OIDC trust model — **pre-session-1 blocker** (§28.4)  
9. **Connect CI** interaction spec (wizard + empty states on Change)  
10. **Migration & mirror-first onboarding RFC**: `import plan` report format, bidirectional mirror semantics, CI shadow parity dashboard, SoR-flip checklist (§18)  
11. ~~**Naming decision**~~ — **done: Runko** (§1, §22.2)  
12. **jj / Josh adopt-vs-build evaluation** for the workspace read path (§12.3, §21.2)  
13. ~~**Build-graph adapter contract spec**~~ — **done**: `docs/spec/build-adapter/` (§14.5.4, §28.4)  

---

## 27. Appendix C — CI integration quick reference

| Need | Mechanism |
|------|-----------|
| Start a pipeline | Signed `change.*` webhook or poll API |
| Know what to test | `GET …/affected` or webhook `affected` block |
| Fetch code fast | Change ref + partial clone + sparse patterns via `runko-ci checkout` |
| Block/merge on results | Checks API → merge requirements |
| Bootstrap | Connect CI wizard + Tier 1 template |
| Unsupported CI | `runko-ci` CLI + generic webhook receiver |
| Deploy after land | `change.landed` webhook (CD examples, not our orchestrator) |

---

## 28. Appendix D — Implementation strategy (token-efficient build plan)

> **Premise:** implementation is by supervised coding agents; the scarce resources are **agent tokens and review attention**, not only engineer-weeks. Scope = simple MVP: Phase 0 + Phase 1 core **minus the mirror service** (mirror is a launch gate, §19.2, but not the first loop). Target: **~15–25M fresh tokens across ~35–45 sessions** (~1M output tokens; ~$0.5–1k at 2026 frontier pricing) vs. an undisciplined 40–60M. The doc's decidedness is the asset: §22.2's decisions convert most components from *discovery* (debug loops) to *transcription* (spec → code).

### 28.1 Budget by component

| Component | Design § | Character | Sessions | Fresh tokens |
|---|---|---|---|---|
| Spec artifacts (28.4) | §26 #2/#3/#8 | investment | 3–4 | ~2M |
| Repo bootstrap: test harness, AGENTS.md, CI, compose | §16.4, §28.2 | transcription | 1–2 | ~0.7M |
| Persistence: DDL + queries (sqlc generates the rest) | §9.2, §10.3 | transcription | 1–2 | ~0.7M |
| Project model: intent→files, templates, preview | §10.1–10.4 | transcription | 2–3 | ~1.5M |
| Tree indexer + owners (rebuildable index) | §10.3, §7.3 | transcription | 1–2 | ~0.7M |
| Affected (pure function + property tests) | §13.3 | transcription | 1 | ~0.4M |
| **Receive funnel** (magic ref, Change-Id, policy, gitleaks, §6.9 UX) | §11.5 | **discovery** | 3–5 | ~3M |
| **Land engine** (rebase-land, optimistic revalidation, races) | §13.5, §7.4 | **discovery** | 3–5 | ~3M |
| Checks + merge requirements (check-sets, TTL, re-runs) | §14.4.2 | transcription | 2–3 | ~1.5M |
| Webhook outbox (HMAC, retry, DLQ, replay) | §14.4.1 | transcription | 1–2 | ~0.7M |
| `runko` CLI + doctor; `runko-ci` | §17.1, §14.6 | transcription | 2–3 | ~1M |
| Build-graph adapter: contract + Bazel engine | §14.5.4 | transcription + fixture discovery | 2–3 | ~1.5M |
| Zoekt integration + AGENTS.md generator (now stage 11, ahead of MCP - §8.3 CLI-first decision) | §8.2, §8.8 | transcription | 1 | ~0.4M |
| **Land wiring through the daemon** (inserted per review, stage 11b) | §13.5, §7.4 | transcription (the discovery already happened at stage 7 - this is plumbing + the trunk-bootstrap edge case it surfaced) | 1 | ~0.4M |
| MCP thin remote adapter (rescoped: six read-only tools, not the full catalog - §8.3) | §8.3 | transcription | 1–2 | ~0.5M |
| Minimal web (SSR wizard, change page, requirements) | §17.2, §22.2 | scoped | 2–4 | ~1.7M |
| Dogfood hardening buffer | §19.2 | discovery | 3–5 | ~2.5M |

**Shape:** the two discovery components carry ~30% of the budget and ~50% of correctness risk. Budget test tokens 1:1 with product tokens there; ~0.5:1 elsewhere.

### 28.2 Standing rules (ranked by tokens saved)

1. **Spec before code** (~saves 8–15M). Write §26 artifacts #2 (PROJECT.yaml schema), #3 (MCP catalog as real JSON Schemas), #8 (webhook/CheckRun schemas — §14.4 is 80% written) before session 1. Rework from deciding-while-coding is the dominant waste.
2. **Deterministic codegen — principle 8 applied to ourselves** (~saves 5–8M). OpenAPI → `oapi-codegen` (REST boilerplate); `sqlc` (typed persistence from DDL + named queries); JSON Schema → generated types feeding platform, `runko-ci`, *and* the MCP catalog from one source. Machine-generated LoC costs zero agent tokens; agent-authored LoC drops from ~25–40k to ~15–22k.
3. **Terse test harness, built second** (~saves 3–5M). Git-fixture harness in the style of git's own `t/` suite: throwaway repos from short scripts, golden-file assertions, **one-line diffs on failure**, fake clock + seeded IDs (one flaky test is the worst token multiplier that exists), `make check` < 30s for core packages. Every funnel/land session pays rent to this harness.
4. **Shell out to `git`; never go-git** (~saves 1–2M). The spec mandates upstream-Git behavior (§12.1); debugging a library's divergence from it is token burn with no product value.
5. **SSR + htmx web for Phases 0–1** (~saves 2–4M; original call — superseded 2026-07-07 by React+Connect, §17.4/§22.2, when the transport moved to gRPC/Connect; kept here as the budget-era rationale). The wizard/change-page/requirements surfaces need no SPA; rich diff review UX is Phase 2.
6. **Context locality.** One Go module; one package per design section (`receive/`, `land/`, `affected/`, `checks/`, `project/`, `mcp/`), interfaces in a tiny `core/`; each package header cites its §; **this design doc lives in the repo** (`docs/design.md`) so sessions grep it instead of being pasted it; repo AGENTS.md ≤ 150 lines (commands, layout map, "read the cited § before editing", the §6.5 error struct). This is §8.2's context-budget rule applied to building the product.
7. **One PR per session, along the DAG (28.3).** A session must not open files from a package two hops away. Rework across sessions is the hidden 2–3×.

### 28.3 Session DAG (revised 2026-07-06 — stages 0–9 complete)

> **Completed** (repo history `cb09d6d` → `590b3bd`, incl. review-driven fail-open fix `0ab8037`): spec artifacts, bootstrap + harness, persistence, project model, tree indexer + owners, affected, receive funnel (scoped), land engine, checks + merge requirements + webhook outbox, `runko` CLI + `runko-ci`. This table carries **remaining work only**. Review debt is a first-class stage (9a), not a footnote — it blocks the daemon.

| # | Stage | Depends on | Done when |
|---|-------|-----------|-----------|
| 9a | **Hardening pass — review debt** (1–2 sessions; ready now) | — | ① Live-Postgres integration tests (`make check-db`, compose/testcontainer) cover stage-2/4/6/8 SQL incl. outbox + reruns; ② stage-8 fixes: pending check-set blocker count/label corrected; missing runs appear in `required` + `pending` arrays; ③ CLI **resolve-or-explain** helper (§6.5): unborn-HEAD `project create` (empty repo, §6.7) and unknown-revision errors return structured guidance — no raw `exit status 128` passthrough; ④ git ≥ 2.40 startup check (merge-tree `--merge-base`) or env-contract bump |
| 9b | Build-graph adapter: engine contract + Bazel impl (`--engine bazel`, §14.5.4) | artifact #13 (§26) | Fake-engine fixture tests green (scripted `bazel` binary, hermetic); real-Bazel integration test behind a tag; **any engine failure ⇒ `run_everything`** table-tested |
| 9c | Opinionation mechanics (§14.5.4): `build_discipline: hermetic` golden path + `require_build_binding` gate | 9b | Greenfield template org: `project create` emits generated BUILD wiring + default `bazel test` check-sets with **zero hand-authored BUILD lines**; with the org gate on, an unbound project's Change reports the §13.5 blocker |
| 9d | **CI wiring** (`.github/workflows/ci.yml`) - inserted per review, blocks stage 10 | 9a | `make check` on every push/PR + a real `postgres` service container running `make check-db`, so 9a's live-Postgres tests execute for real somewhere, not just as unexecuted code no sandbox in this project's history could run |
| 10 | **`runkod` daemon assembly** (was implicit in the old DAG; now explicit — 2–3 sessions) | 9a, 9d | Smart-HTTP hosting (bare repo + `git http-backend` + pre-receive wiring `receive.Decide()`); REST endpoints: changes / checks / affected / merge-requirements; outbox delivery worker; **gitleaks-backed `SecretScanner`** (closing the stage-6 seam); deploy-token auth. Bar, over the wire: push to `refs/for/main` creates a Change; direct trunk push gets the §6.9 script; `runko-ci report-check` round-trips against it |
| 11 | **Zoekt + AGENTS.md generator** (reordered ahead of MCP, §8.3 CLI-first decision) | 10 | `search_code` returns project-tagged hits through the daemon; generated `AGENTS.md` teaches the CLI as the primary agent interface - command inventory, `--json` output contracts, exit-code convention (`docs/cli-contract.md`), the §6.5 structured-error shape |
| 11b | **Land wiring through the daemon** (inserted per review - `land.Land`/`NeedsRevalidation` were fully built and race-tested at stage 7, but nothing in `runkod` ever called them, so stage 14's `create → change → land` loop had no wire-level "land" verb) | 7, 10 | `POST /api/changes/{key}/land`, gated on the exact same `Mergeable` bool `GET .../merge-requirements` reports; `runko change land`; a successful land enqueues a `change.landed` webhook and fires the `ZoektIndexWorker` trigger stage 11 placed at the trunk-ref-update branch (previously unreachable in practice - this is what makes it reachable); `RaceRetry`/`RequiresRevalidation`/conflicts surface as structured, retryable responses, never a silent 200; landing the first-ever Change onto a brand-new monorepo (trunk has no commits - the only way it can, since direct pushes are always rejected, §6.9) is a real compare-and-swap bootstrap, not an unconditional force-write |
| 11c | **Merge policy wiring** (inserted per 11b review: the gate mechanism works but its policy inputs are homeless — required checks are currently derived *from the posted runs themselves* and owners are `nil`, so a Change with zero checks and zero approvals lands; §13.5's first two gate rows are decorative at the wire level) | 8, 10, 11b; blocks 13/14 | Required checks resolved from project `ci.checks` (§14.9) + org config, **not** from posted runs; owners requirements from the stage-4 index for touched paths + minimal `POST .../approve`; **bot lane enforcement** (§14.10.2): AgentIdentity with path-scoped `can_land_changes` auto-lands only within its allowlist with its required checks green; default posture = a Change with no resolvable policy is **not** mergeable outside eval mode (loud opt-out, the `--insecure-skip-secret-scan` precedent); e2e extended: land refused pre-check and pre-approval, succeeds after both |
| 12b | **Workspace glue v0** (restored — silently dropped in the 2026-07-06 DAG revision; caught in review. §12.3 Phase A, §19.2 stretch) | 10, 11c | `runko workspace create/list/attach/snapshot/update-base` (worktree + sparse-cone mechanics; multi-workstream = N worktrees over one object store, §12.3); daemon workspace-registry endpoints; `refs/workspaces/<id>/*` recognized by the receive funnel — owner-only push, size caps, secret scan (closing the unconditional-accept gap); retention/GC note per §12.2. Bar: two concurrent workspaces, one user, different projects; delete the local directory → `attach` restores from the snapshot ref, nothing lost; §3.3's "editable workspace < 60s" measured |
| 12 | **MCP thin remote adapter** (rescoped, §8.3: six read-only tools, not the full catalog) | 11 | Exactly six tools registered (`list_projects`, `get_project`, `search_code`, `who_owns`, `get_affected`, `get_merge_requirements`), each a thin wrapper over the existing REST handlers stage 10 already built; responses are schema-conformant against `docs/spec/mcp-tools/` (contract-tested, same technique as `checks/contract_test.go`); `runko mcp serve` (stdio) and the daemon's HTTP transport both work; catalog.json's other 19 tools remain present but annotated `deferred-v1.x`, not implemented |
| 12c | **Control-plane hardening** (inserted 2026-07-07 after a post-stage-12 audit, before any UI work; the 9a/11c pattern: close review-integrity and wiring gaps while the surface is still small) | 10, 11c, 12b | ① Approvals bind to `head_sha` (§13.5 — amend resets the human gate; approve-v1-amend-v2 must not land); ② named-token principal registry (§15.1 interim) lighting up self-approval denial (§8.7), `authored_by`/`landed_by` attribution, and receive-time AgentPolicy enforcement (built at stage 6, never fed a principal); ③ change lifecycle surface a UI needs on day one: `GET /api/changes` (ListOpenChanges query existed since stage 2, unwired), abandon verb (state enum had `abandoned`, nothing set it), check rerun endpoint (`checks.RerunCheck` + rerun webhook schema existed unwired), check-staleness TTL actually consulted (§14.4.2); ④ ops floor: `/healthz`, graceful shutdown, server timeouts |
| 13 | Web UI (React + Connect-ES; superseded the planned SSR+htmx per §17.4, 2026-07-07 — shipped in two halves: frontend on a fake transport, then connect-go handlers in runkod) | 10, 11b, 11c, 12c | Changes inbox + stacked diff change page + merge requirements + approve/land wired to 11b's endpoint, gated by 11c's policy |
| 14 | Compose + measured 15-min loop in CI | 10–13, 11b, 11c, 12c | §16.4 smoke: `compose up → create → change → land` timed, green per release — landing gated by real policy, not vacuous mergeability |
| 15 | Dogfood hardening (3–5 sessions) | 14 | Platform hosts its own repo; real GHA checks gate its own Changes. **Decision point recorded:** migrate Runko's repo to Bazel — fires the §14.7 Tier-1 pull trigger and dogfoods the §14.5.4 golden path |
| 16 | **Review conversation** (§13.4.1–13.4.2, decided 2026-07-10) | 12c, 13 | Comment / thread / resolve / request-review round-trip over REST **and** Connect (proto extended first, stage-13 precedent); comments bind to `head_sha` — an amend marks them outdated and re-derives the attention set; `require_resolved_threads` off by default, its `unresolved_threads` blocker appears in merge requirements when on; CLI `runko change comment` / `comments` / `request-review` + `docs/cli-contract.md` rows + `agentsmd.Commands` entries (drift test enforces the pairing); `change.commented` / `change.review_requested` delivered through the outbox; MCP `list_change_comments` graduates from the deferred catalog (status flip + contract-test update); agent principal's comment carries the badge, agent approval still structurally refused |
| 16b | Web review threads + attention inbox | 16 | Inline threads on the stacked diff (anchored; outdated-on-amend rendering, no floating); Home's owner attention inbox driven by the derived set (§13.4.2); resolve/unresolve from the thread |
| 17 | **Tag governance at receive** (§14.10.3, §11.4, decided 2026-07-10) | 10, 11c | `refs/tags/*` writes gated: org release role or tag-namespace-scoped bot lane (`can_write_tags` glob list on AgentIdentity policy); unauthorized tag push refused over the wire with the §6.9-style structured rejection; org knob defaults permissive until flipped (loud-opt-out precedent); mirror still transports tags outbound unchanged; e2e: unauthorized push refused, release-role push accepted, bot lane confined to its declared namespace |
| 17b | **Releases** (§14.10.3) | 17 | `runko release create --project <p>`: version per `capability_config.release` (semver bump or explicit), changelog derived from landed Changes touching the project since the last release tag (`head_change_key` recorded), annotated tag written server-side through stage 17's policy, immutable release row (no edit/re-point verbs exist), `release.created` delivered through the outbox (schema: `docs/spec/webhooks/release-created.schema.json`); `runko release list`; REST + Connect surface; `docs/cli-contract.md` + `agentsmd.Commands` rows land with the commands (drift test) |
| 18 | **Workspace observability** (§12.6, decided 2026-07-11) | 12b, 13 | `runko workspace watch` snapshots out-of-band (git status/HEAD/real index byte-identical before and after — jj-colocated fixture included) with `change push` auto-snapshot (`--no-snapshot` opt-out); receive/land paths record stats-only `workspace_events` rows (numstat; capped per workspace; deleted with the workspace) and publish to the in-process bus; `GetWorkspaceDiff` / `ListWorkspaceEvents` / `WatchWorkspace` (first server-streaming RPC: pokes + ~25s keepalives, authed-only) over Connect, branch param charset-validated before ref interpolation; web `/workspaces/:id` — per-branch live diff (DiffView reuse) + timeline + live dot, stream-as-poke with debounced unary refetch, fake-transport `/demo` parity; e2e: `watch --once` against a live daemon → diff + event row → second snapshot → subscribed client receives a frame |
| 19 | **Agent session activity** (§12.6.1, decided 2026-07-11) | 18 | `POST /api/workspaces/{id}/activity` ingests harness-reported batches (≤100; owner-only, closed refuses, actor from principal; unknown kind→`note`; 240-rune truncation; funnel-scanner redaction per event, whole-batch on scan error) into the new `workspace_activity` table (cap 1000 prune-at-insert, deleted with workspace) and publishes one bus-only `agent_activity` poke per batch; `ListWorkspaceActivity` over Connect (authed-only) + `WorkspaceSummary.latest_activity` batched into `ListWorkspaces`; CLI `runko agent event` (`--from-hook` parses post-tool-use JSON from stdin, tool→kind mapping in Go; verb-local `RUNKO_RUNKOD_URL`/`RUNKO_TOKEN` env fallback) + `runko agent hooks` snippet printer + `docs/cli-contract.md`/`agentsmd.Commands` rows (drift test); web: "Agent activity" side-card on `/workspaces/:id` refetched off the existing `useWatch` poke + "now: ✎ path" presence lines (page header + workspaces-list cards, fresh-only); fake-transport `/demo` parity; e2e: `agent event` against a live daemon → activity row + `AGENT_ACTIVITY` frame → card updates without reload |

### 28.4 Pre-stage checklist (updated 2026-07-06)

Original pre-session-1 items: **all complete** — name (Runko), PROJECT.yaml v1 schema, MCP catalog, webhook/CheckRun schemas, module path (`github.com/saxocellphone/runko`), SSR+htmx decision.

1. ~~**Build-graph adapter contract spec**~~ — **done**: `docs/spec/build-adapter/README.md` (engine interface, `Refine`'s fail-closed table, Bazel `rdeps` query recipe, Buck2 `uquery` mapping notes, admission criteria recap) + `docs/spec/build-adapter/refinement.schema.json` (post-back payload); `project.schema.json`'s `capabilities` enum gained `build`. Unblocks 9b/9c.
2. Nothing blocks 9a, 9b, 9c, or 10 — all startable; 9a shipped first as review debt the daemon builds on (see stage table above).

### 28.5 Anti-goals for implementation sessions

- No refactors outside the session's package (file an issue instead)  
- No dependency additions/upgrades mid-session (bootstrap pins them)  
- Never hand-edit generated files (`sqlc`, OpenAPI, schema types) — regenerate  
- No mocking of git — the fixture harness *is* the mock  
- No UI polish before stage 13 is green — the §16.4 measured loop outranks pixels  

---

*This design prioritizes monorepo accessibility for medium organizations: Git underneath with the **tree as source of truth**; CitC-class workspaces built as a **delta over upstream Git** (Scalar substrate, our enforcement); low-ceremony progressive configuration (no Boq tax by default); humans and coding agents as co-equal, policy-aware clients with **project-granular enforcement** (the moat repo-granular platforms cannot express); **CI deeply integrated via contracts/plugins/templates (execution stays with existing CI products)**; **mirror-first adoption** so no org must bet the company to try it; open source and self-host by default.*


