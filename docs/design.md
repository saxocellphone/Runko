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
| **L0 Intent** | Always | name, type (`service`/`library`/…), path (optional auto), owners (or inherit) | Wizard / one API call / one CLI invoke |
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
| `rpc` | Scaffold proto/RPC stubs + register service name |
| `http` | Scaffold HTTP entrypoints |
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

Expose an official **MCP server** (and equivalent REST/gRPC) covering:

```text
# Navigation
list_projects, get_project, who_owns, get_affected(paths|change_id)

# Project lifecycle (low ceremony)
create_project(intent)      # L0 only — name, type, owners?, template?, path?
add_capability(project, cap)
adopt_path_as_project(...)

# Workspace (CitC)
create_workspace(project_ids[], purpose?)
attach_workspace / get_workspace_status
prefetch(project_id|paths)
update_workspace_base()

# Change lifecycle
create_change / update_change_description
get_change / list_change_comments
request_review / land_change (if permitted)
get_merge_requirements(change_id)  # owners + checks outstanding

# Validation
validate_project_intent(intent) → structured errors
preview_create_project(intent) → files that would be written
```

**All tools return structured errors** (`code`, `retryable`, `suggestion`). Agents should not parse human HTML.

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
| **Audit** | Coding session id links tool calls → file writes → change |

### 8.7 Policy engine (AgentPolicy)

Org-level defaults (illustrative):

```yaml
agent_policy:
  require_workspace_affinity: true
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

### 8.8 Integration patterns

| Pattern | How it works |
|---------|----------------|
| **Editor agents** (Cursor, Copilot, etc.) | MCP config points at platform; extension also shows human UI chrome |
| **CLI agents** (Claude Code, etc.) | `runko mcp serve` or remote MCP URL with user/agent token |
| **Autonomous runners** | Agent identity + CI OIDC; always headless workspace on workspace pool |
| **Internal bots** | Same API; tighter path allowlists |

We provide **reference prompts / skill files** (e.g. `AGENTS.md` snippet) generated per monorepo:

```markdown
# Monorepo agent instructions (generated)
- Use MCP tools; do not full-clone.
- Prefer create_project tool over inventing PROJECT.yaml.
- Stay within workspace affinity; call prefetch for deps.
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

---

## 10. Streamlined project configuration (detailed design)

### 10.1 Intent → files pipeline

```text
CreateProjectIntent
  name, type, template_id?, path?, owners[]?, capabilities[]?
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

**Snapshot hygiene — the bytes that actually dominate:** build artifacts and dependency trees (`node_modules/`, `target/`, `.venv/`, bazel outputs) must **never** enter snapshot commits. Exclusion = `.gitignore` (snapshots are Git commits, so this is free) + template defaults + receive-time size caps as backstop. Conflict semantics: single-writer per workspace by default; concurrent attach is explicit (`--shared`) with snapshot-on-conflict — never silent merge. Offline: plain Git — commit locally, push snapshots on reconnect; base updates refused while detached.

### 12.3 Implementation phases

#### Phase A — Git-glue workspaces (the only thing we build)

| Mechanism | Behavior |
|-----------|----------|
| Content | Partial/blobless Git + **sparse** affinity roots (+ worktree per workspace) |
| Durability | **Snapshot commits to `refs/workspaces/<id>/head`** (explicit save + auto on `create_change`/detach); continuous streaming deliberately deferred |
| Attach | `runko workspace attach` configures clone/sparse/hooks — laptop or any remote VM |
| Agents | Same path; headless VM via environment contract (below) for autonomous agents |
| Sync base | `workspace update-base` = fetch + rebase with conflict UX |

Delivery mapping (§19): attach + snapshot refs are Phase-1 *stretch* / Phase-2 core; receive-time policy completes the plane in Phase 2. **Continuous streaming sync is deliberately deferred**: snapshot semantics are easier to reason about, cheaper to run, and already satisfy the durability/audit contract. Stream only if real usage shows snapshot loss windows hurt.

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

### 13.4 Review UX

- Project-scoped default  
- Agent-assisted badge + attribution  
- Owners and checks above the fold  
- Plain-language merge blockers (`get_merge_requirements`)

### 13.5 Merge gates and landing

| Gate | v1 |
|------|-----|
| Required human owners approved | Yes — with global-approver / mechanical-change relaxations (§7.3) |
| Agent-only approval | **No** (default policy) |
| Unowned paths | Configurable block |
| Projects without a `build` binding | Org opt-in block (`require_build_binding`, hermetic discipline — §14.5.4) |
| External CI on affected set | Yes |
| Land semantics | **Optimistic land with revalidation** (below) |
| Full merge queue | v1.x — as a batching/pipelining optimization of the same rule |

**Land races are the norm, not the edge case** (even ~50 engineers on one trunk). v1 policy, specified now rather than discovered in production:

- Land = rebase Change onto trunk tip (§7.4).  
- Rebase clean **and** trunk delta since the checked `head_sha` does not intersect the Change's affected set → land without re-running checks (`revalidation: affected-intersection`, default).  
- Intersects → re-run required checks on the rebased head before the ref update.  
- Orgs can tighten to `revalidation: always`. A v1.x merge queue batches and pipelines exactly this rule — the queue is an optimization, not a new semantic.

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

### 14.10 Deploy / CD hooks (thin)

We are not Spinnaker/Argo CD. We provide:

| Hook | Use |
|------|-----|
| `change.landed` webhook | CD systems start deploy of affected services |
| Trunk push webhook | Same for trunk-based CD |
| Project metadata in payload | service name, path, optional deploy capability flags |
| Optional gitops commit (later) | Out of scope for v1 unless demand |

CD templates (Argo CD ApplicationSet examples, etc.) live under `integrations/templates/cd/` as **examples**, Tier 2+.

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
runko workspace create --project //commerce/checkout-api
runko workspace attach
runko change create -m "Reject invalid SKUs"
runko change push           # from any plain git checkout (wraps refs/for/main, §11.5)
runko change requirements   # owners + checks outstanding
runko doctor                # remotes, hooks, personal cheat-sheet (§6.9)
runko mcp serve             # local MCP for coding agents
```

### 17.2 Web UI (UX-critical surfaces)

| Surface | Priority interactions |
|---------|----------------------|
| Home | Create project CTA, recent changes, owner attention inbox |
| Create project | 3-step wizard, live validation, preview files |
| Project | Capabilities as toggles, owners, open workspace, “copy MCP snippet” |
| Change | Scoped diff, agent badge, merge requirements |
| **Connect CI** | Wizard: pick CI system → template + webhook secret + watch first green check arrive (§14.13) |
| **Import** | `import plan` report review, owners-mapping fixes, shadow-CI parity dashboard (§18.3) |
| Settings | Templates, AgentPolicy, conventions doc |

### 17.3 Editor extension

- Attach workspace; affinity indicator  
- Merge requirements / owners  
- “Create project” mini-flow  
- One-click **configure MCP for this monorepo**  

### 17.4 MCP

- Documented tool catalog with examples  
- Idempotent creates where possible  
- Pagination and compact list defaults for token efficiency  

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

- Workspace attach v0: Scalar-class (partial + sparse via platform config) + overlay snapshots; MCP workspace tools  
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
| **Jujutsu (jj)** | Git-compatible, change-centric (stable change IDs, working-copy-as-commit); Google building an internal cloud-backed server on it | Closest philosophical relative at the client layer. We adopt its change-ID discipline (§7.4); **evaluate jj as a first-class client** before building more client machinery; watch for jj-native forges as future competitors |
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
| **Land races degrade trust past ~50 eng** | Optimistic land + revalidation specified in v1 (§13.5); merge queue ships as an optimization of the same rule |
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
| MVP web stack | **Server-rendered + htmx for Phases 0–1** (wizard, change page, merge requirements); SPA investment deferred to Phase 2 review UX (§28.2) |
| Build-graph integration | **Runner-side adapter contract; Bazel first, Buck2-shaped** (§14.5.4). Platform floor stays paths + declared deps (NG4 intact); engine output refines CI scope by default, gates only by org opt-in; every engine failure ⇒ `run_everything` |
| Build-system opinionation | **Opinionated by criterion, not mandate** (§14.5.4): engine status requires declared + hermetic-at-SHA + rdeps-queryable (Bazel ✓, Buck2 ✓; task runners never); greenfield golden path `build_discipline: hermetic` with generated BUILD files; org opt-in `require_build_binding` merge gate; brownfield adoption never gated on a build migration (§18 cliff rule) |

### 22.3 Open questions

1. Exact **PROJECT.yaml** minimal schema and generated-file layout — **pre-session-1 blocker** (§28.4)  
2. Codegen marker conventions and enforcement strength  
3. Agent land exceptions: which trusted bots may auto-land, under what caps  
4. IdP group sync vs local groups  
5. Standard for agent metadata (model name, tool versions) on Changes  
6. **GHA topology default for greenfield** (mirror-first is already the default for *migrating* orgs, §18)  
7. Whether to ship an optional **webhook→provider bridge** service in-tree or docs-only  
8. Post-submit vs presubmit policy defaults for `change.landed` pipelines  
9. Global-approver granularity: org-wide role vs per-domain (e.g. `//infra` global approvers) (§7.3)  
10. jj as a supported client in v1.x: how much workspace-agent scope does it absorb? (§21.2)  

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
13. **Build-graph adapter contract spec** (engine interface, refinement post-back schema, Bazel query recipes, Buck2 mapping notes) — blocks DAG stage 9b (§14.5.4, §28.3)  

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
| MCP server (generated from tool catalog) | §8.3 | transcription | 1–2 | ~0.7M |
| Zoekt integration + AGENTS.md generator | §8.2, §8.8 | transcription | 1 | ~0.4M |
| Minimal web (SSR wizard, change page, requirements) | §17.2, §22.2 | scoped | 2–4 | ~1.7M |
| Dogfood hardening buffer | §19.2 | discovery | 3–5 | ~2.5M |

**Shape:** the two discovery components carry ~30% of the budget and ~50% of correctness risk. Budget test tokens 1:1 with product tokens there; ~0.5:1 elsewhere.

### 28.2 Standing rules (ranked by tokens saved)

1. **Spec before code** (~saves 8–15M). Write §26 artifacts #2 (PROJECT.yaml schema), #3 (MCP catalog as real JSON Schemas), #8 (webhook/CheckRun schemas — §14.4 is 80% written) before session 1. Rework from deciding-while-coding is the dominant waste.
2. **Deterministic codegen — principle 8 applied to ourselves** (~saves 5–8M). OpenAPI → `oapi-codegen` (REST boilerplate); `sqlc` (typed persistence from DDL + named queries); JSON Schema → generated types feeding platform, `runko-ci`, *and* the MCP catalog from one source. Machine-generated LoC costs zero agent tokens; agent-authored LoC drops from ~25–40k to ~15–22k.
3. **Terse test harness, built second** (~saves 3–5M). Git-fixture harness in the style of git's own `t/` suite: throwaway repos from short scripts, golden-file assertions, **one-line diffs on failure**, fake clock + seeded IDs (one flaky test is the worst token multiplier that exists), `make check` < 30s for core packages. Every funnel/land session pays rent to this harness.
4. **Shell out to `git`; never go-git** (~saves 1–2M). The spec mandates upstream-Git behavior (§12.1); debugging a library's divergence from it is token burn with no product value.
5. **SSR + htmx web for Phases 0–1** (~saves 2–4M; decided, §22.2). The wizard/change-page/requirements surfaces need no SPA; rich diff review UX is Phase 2. Use a diff library when it comes.
6. **Context locality.** One Go module; one package per design section (`receive/`, `land/`, `affected/`, `checks/`, `project/`, `mcp/`), interfaces in a tiny `core/`; each package header cites its §; **this design doc lives in the repo** (`docs/design.md`) so sessions grep it instead of being pasted it; repo AGENTS.md ≤ 150 lines (commands, layout map, "read the cited § before editing", the §6.5 error struct). This is §8.2's context-budget rule applied to building the product.
7. **One PR per session, along the DAG (28.3).** A session must not open files from a package two hops away. Rework across sessions is the hidden 2–3×.

### 28.3 Session DAG (revised 2026-07-06 — stages 0–9 complete)

> **Completed** (repo history `cb09d6d` → `590b3bd`, incl. review-driven fail-open fix `0ab8037`): spec artifacts, bootstrap + harness, persistence, project model, tree indexer + owners, affected, receive funnel (scoped), land engine, checks + merge requirements + webhook outbox, `runko` CLI + `runko-ci`. This table carries **remaining work only**. Review debt is a first-class stage (9a), not a footnote — it blocks the daemon.

| # | Stage | Depends on | Done when |
|---|-------|-----------|-----------|
| 9a | **Hardening pass — review debt** (1–2 sessions; ready now) | — | ① Live-Postgres integration tests (`make check-db`, compose/testcontainer) cover stage-2/4/6/8 SQL incl. outbox + reruns; ② stage-8 fixes: pending check-set blocker count/label corrected; missing runs appear in `required` + `pending` arrays; ③ CLI **resolve-or-explain** helper (§6.5): unborn-HEAD `project create` (empty repo, §6.7) and unknown-revision errors return structured guidance — no raw `exit status 128` passthrough; ④ git ≥ 2.40 startup check (merge-tree `--merge-base`) or env-contract bump |
| 9b | Build-graph adapter: engine contract + Bazel impl (`--engine bazel`, §14.5.4) | artifact #13 (§26) | Fake-engine fixture tests green (scripted `bazel` binary, hermetic); real-Bazel integration test behind a tag; **any engine failure ⇒ `run_everything`** table-tested |
| 9c | Opinionation mechanics (§14.5.4): `build_discipline: hermetic` golden path + `require_build_binding` gate | 9b | Greenfield template org: `project create` emits generated BUILD wiring + default `bazel test` check-sets with **zero hand-authored BUILD lines**; with the org gate on, an unbound project's Change reports the §13.5 blocker |
| 10 | **`runkod` daemon assembly** (was implicit in the old DAG; now explicit — 2–3 sessions) | 9a | Smart-HTTP hosting (bare repo + `git http-backend` + pre-receive wiring `receive.Decide()`); REST endpoints: changes / checks / affected / merge-requirements; outbox delivery worker; **gitleaks-backed `SecretScanner`** (closing the stage-6 seam); deploy-token auth. Bar, over the wire: push to `refs/for/main` creates a Change; direct trunk push gets the §6.9 script; `runko-ci report-check` round-trips against it |
| 11 | MCP server (generated from catalog) | 10 | Tools generated from `docs/spec/mcp-tools/`; structured errors per §6.5 |
| 12 | Zoekt + AGENTS.md generator | 10 | `search_code` returns project-tagged hits through the daemon |
| 13 | Minimal web (SSR + htmx) | 10 | Wizard + change page + merge requirements |
| 14 | Compose + measured 15-min loop in CI | 10–13 | §16.4 smoke: `compose up → create → change → land` timed, green per release |
| 15 | Dogfood hardening (3–5 sessions) | 14 | Platform hosts its own repo; real GHA checks gate its own Changes. **Decision point recorded:** migrate Runko's repo to Bazel — fires the §14.7 Tier-1 pull trigger and dogfoods the §14.5.4 golden path |

### 28.4 Pre-stage checklist (updated 2026-07-06)

Original pre-session-1 items: **all complete** — name (Runko), PROJECT.yaml v1 schema, MCP catalog, webhook/CheckRun schemas, module path (`github.com/saxocellphone/runko`), SSR+htmx decision. Current blockers:

1. **Build-graph adapter contract spec** (§26 #13) — blocks stages 9b and 9c: engine interface, refinement post-back schema, Bazel query recipes, Buck2 mapping notes  
2. Nothing blocks 9a or 10 — both startable today; **9a first** (it's review debt the daemon builds on)

### 28.5 Anti-goals for implementation sessions

- No refactors outside the session's package (file an issue instead)  
- No dependency additions/upgrades mid-session (bootstrap pins them)  
- Never hand-edit generated files (`sqlc`, OpenAPI, schema types) — regenerate  
- No mocking of git — the fixture harness *is* the mock  
- No UI polish before stage 13 is green — the §16.4 measured loop outranks pixels  

---

*This design prioritizes monorepo accessibility for medium organizations: Git underneath with the **tree as source of truth**; CitC-class workspaces built as a **delta over upstream Git** (Scalar substrate, our enforcement); low-ceremony progressive configuration (no Boq tax by default); humans and coding agents as co-equal, policy-aware clients with **project-granular enforcement** (the moat repo-granular platforms cannot express); **CI deeply integrated via contracts/plugins/templates (execution stays with existing CI products)**; **mirror-first adoption** so no org must bet the company to try it; open source and self-host by default.*

