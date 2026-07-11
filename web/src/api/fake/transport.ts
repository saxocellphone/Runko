// In-memory Connect transport implementing all four services against the
// fixture scene, so the UI is fully navigable (and mutable: approve, land,
// rerun, abandon) without a running runkod. Swapped for a real Connect
// transport by setting VITE_RUNKO_URL - see ../client.ts.
//
// Behavior deliberately mirrors runkod's semantics where they exist:
// blockers phrased like checks.ComputeMergeRequirements (§6.6), land gated
// on the same mergeable bool merge-requirements reports (§13.5), stack
// derivation per GetChangeStack's documented relation (changes.proto).
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { clone, create } from "@bufbuild/protobuf";
import {
  ActorType,
  ChangeState,
  ChangeSummarySchema,
  CommentSchema,
  CommentSide,
  MergeRequirementsSchema,
  ProjectDetailSchema,
  ProjectType,
  ReasonCode,
  WorkspaceSummarySchema,
  type ChangeSummary,
  type Comment,
  type MergeRequirements,
  type ProjectDetail,
  type WorkspaceSummary,
} from "../../gen/runko/v1/common_pb";
import {
  ChangeService,
  GetAffectedResponseSchema,
  GetChangeDiffResponseSchema,
  GetChangeResponseSchema,
  GetChangeStackResponseSchema,
  GetMergeRequirementsResponseSchema,
  ApproveChangeResponseSchema,
  LandChangeResponseSchema,
  AbandonChangeResponseSchema,
  RerunCheckResponseSchema,
  ListChangesResponseSchema,
  ListCommentsResponseSchema,
  CreateCommentResponseSchema,
  ResolveCommentResponseSchema,
  RequestReviewResponseSchema,
} from "../../gen/runko/v1/changes_pb";
import {
  ProjectService,
  GetProjectResponseSchema,
  ListProjectsResponseSchema,
  WhoOwnsResponseSchema,
  PreviewCreateProjectResponseSchema,
  CreateProjectResponseSchema,
  type CreateProjectIntent,
} from "../../gen/runko/v1/projects_pb";
import {
  WorkspaceService,
  CreateWorkspaceResponseSchema,
  GetWorkspaceResponseSchema,
  ListWorkspacesResponseSchema,
  UpdateWorkspaceBaseResponseSchema,
  DeleteWorkspaceResponseSchema,
} from "../../gen/runko/v1/workspaces_pb";
import { SearchService, SearchCodeResponseSchema } from "../../gen/runko/v1/search_pb";
import {
  RepoService,
  TreeEntryType,
  GetTreeResponseSchema,
  GetBlobResponseSchema,
  ListCommitsResponseSchema,
  BlameFileResponseSchema,
} from "../../gen/runko/v1/repo_pb";
import { OwnersSource, WorkspaceStatus } from "../../gen/runko/v1/common_pb";
import type { FileDiff } from "../../gen/runko/v1/changes_pb";
import {
  addedFileDiff,
  changes as fixtureChanges,
  comments as fixtureComments,
  diffs as fixtureDiffs,
  requirements as fixtureRequirements,
  reviewRequests as fixtureReviewRequests,
  projects as fixtureProjects,
  searchCorpus,
  workspaces as fixtureWorkspaces,
  fakeSha,
  fsFiles,
  historyForPath,
  BINARY_MARKER,
  TRUNK_SHA,
} from "./fixtures";

interface FakeState {
  changes: Map<string, ChangeSummary>;
  requirements: Map<string, MergeRequirements>;
  workspaces: Map<string, WorkspaceSummary>;
  projects: ProjectDetail[];
  diffs: Map<string, FileDiff[]>;
  // Tree-as-truth in miniature: a UI-created project rides its open
  // Change and only joins `projects` when that Change LANDS.
  pendingProjects: Map<string, ProjectDetail>;
  // Review conversation (§13.4.1): flat comment lists per change, plus
  // reviewer -> requested_by (§13.4.2). Attention derives, never stored.
  comments: Map<string, Comment[]>;
  reviewRequests: Map<string, Map<string, string>>;
  nextCommentID: number;
}

// Deep-clone the fixtures: each transport owns mutable state (approve,
// land, ...) and must not bleed into other transports through the shared
// fixture singletons.
function freshState(): FakeState {
  const state: FakeState = {
    changes: new Map(
      fixtureChanges.map((c) => [c.id, clone(ChangeSummarySchema, c)]),
    ),
    requirements: new Map(
      fixtureRequirements.map((r) => [r.changeId, clone(MergeRequirementsSchema, r)]),
    ),
    workspaces: new Map(
      fixtureWorkspaces.map((w) => [w.id, clone(WorkspaceSummarySchema, w)]),
    ),
    projects: fixtureProjects.map((p) => clone(ProjectDetailSchema, p)),
    diffs: new Map(fixtureDiffs),
    pendingProjects: new Map(),
    comments: new Map(
      [...fixtureComments].map(([id, list]) => [id, list.map((c) => clone(CommentSchema, c))]),
    ),
    reviewRequests: new Map([...fixtureReviewRequests].map(([id, m]) => [id, new Map(m)])),
    nextCommentID: 1000,
  };
  for (const r of state.requirements.values()) {
    recompute(r);
    recomputeAttention(state, r.changeId);
  }
  return state;
}

// Mirrors runkod's derived attention set (§13.4.2): requested reviewers
// and required owners who have neither approved nor commented at the
// CURRENT head, plus the author once any reviewer has responded to it.
function recomputeAttention(state: FakeState, changeId: string): void {
  const r = state.requirements.get(changeId);
  const c = state.changes.get(changeId);
  if (!r || !c) return;
  const authorName = c.authoredBy?.id ?? "";
  const commented = new Set<string>();
  let reviewerResponded = false;
  for (const cm of state.comments.get(changeId) ?? []) {
    if (cm.headSha !== c.headSha) continue;
    const name = cm.author?.id ?? "";
    commented.add(name);
    if (name !== authorName) reviewerResponded = true;
  }
  const set = new Set<string>();
  for (const reviewer of state.reviewRequests.get(changeId)?.keys() ?? []) {
    if (reviewer === authorName) continue;
    if (commented.has(reviewer)) continue;
    set.add(reviewer);
  }
  const owners = r.owners;
  for (const ref of owners?.required ?? []) {
    if (owners!.satisfied.includes(ref)) {
      reviewerResponded = true;
      continue;
    }
    const userName = ref.startsWith("user:") ? ref.slice("user:".length) : "";
    if (userName && (userName === authorName || commented.has(userName))) {
      if (commented.has(userName)) reviewerResponded = true;
      continue;
    }
    set.add(ref);
  }
  if (reviewerResponded && authorName) set.add(authorName);
  r.attentionSet = [...set].sort();
}

// Mirrors checks.ComputeMergeRequirements's plain-language blockers (§6.6).
function recompute(r: MergeRequirements): void {
  const blockers: string[] = [];
  for (const o of r.owners?.outstanding ?? []) {
    blockers.push(`owner approval outstanding: ${o}`);
  }
  for (const c of r.checks?.failing ?? []) {
    blockers.push(`required check failing: ${c}`);
  }
  const pending = r.checks?.pending ?? [];
  if (pending.length > 0) {
    blockers.push(`${pending.length}/${r.checks?.required.length ?? 0} required checks still running`);
  }
  r.blockers = blockers;
  r.mergeable = blockers.length === 0;
}

function notFound(what: string, id: string): ConnectError {
  return new ConnectError(`${what} not found: ${id}`, Code.NotFound);
}

function mustChange(state: FakeState, id: string): ChangeSummary {
  const c = state.changes.get(id);
  if (!c) throw notFound("change", id);
  return c;
}

function mustRequirements(state: FakeState, id: string): MergeRequirements {
  mustChange(state, id);
  const r = state.requirements.get(id);
  if (!r) throw notFound("merge requirements for change", id);
  return r;
}

// Walk the derived stack relation (changes.proto GetChangeStack): down to
// the trunk-most ancestor, then up child-by-child. Abandoned changes never
// participate.
function stackOf(state: FakeState, id: string): ChangeSummary[] {
  const alive = [...state.changes.values()].filter((c) => c.state !== ChangeState.ABANDONED);
  const byHead = new Map(alive.map((c) => [c.headSha, c]));
  const childrenOf = new Map<string, ChangeSummary[]>();
  for (const c of alive) {
    const parent = byHead.get(c.baseSha);
    if (parent && parent.id !== c.id) {
      const kids = childrenOf.get(parent.id);
      if (kids) kids.push(c);
      else childrenOf.set(parent.id, [c]);
    }
  }
  // Two phases with SEPARATE cycle guards: reusing one visited set would
  // mark the queried change as seen during the up-walk and then truncate
  // the descend at it - any mid-stack query would return only its
  // ancestors.
  let root = mustChange(state, id);
  const upSeen = new Set([root.id]);
  for (;;) {
    const parent = byHead.get(root.baseSha);
    if (!parent || upSeen.has(parent.id)) break;
    upSeen.add(parent.id);
    root = parent;
  }
  // The FULL tree below the root, pre-order (parents before children,
  // siblings by number) - stacks can fork when a workspace's parallel
  // branches (§12.2) build on one base; clients rebuild the edges from
  // base/head, mirroring runkod's stackForChange.
  const chain = [root];
  const downSeen = new Set([root.id]);
  const walk = (parent: ChangeSummary) => {
    const kids = [...(childrenOf.get(parent.id) ?? [])].sort((a, b) =>
      Number(a.number - b.number),
    );
    for (const k of kids) {
      if (downSeen.has(k.id)) continue;
      downSeen.add(k.id);
      chain.push(k);
      walk(k);
    }
  };
  walk(root);
  return chain;
}

// Longest-prefix path -> project match (§13.3), plus reverse declared-dep
// closure for the affected set.
function owningProject(projects: ProjectDetail[], path: string): string {
  let best = "";
  for (const p of projects) {
    if ((path === p.path || path.startsWith(p.path + "/")) && p.path.length > best.length) {
      best = p.path;
    }
  }
  return best;
}

function affectedForPaths(projects: ProjectDetail[], paths: string[]) {
  const direct = new Set<string>();
  for (const p of paths) {
    const owner = owningProject(projects, p);
    if (owner) direct.add(owner);
  }
  const reasons = new Set<ReasonCode>();
  if (direct.size > 0) reasons.add(ReasonCode.DIRECT_PATH);
  const affected = new Set(direct);
  for (;;) {
    let grew = false;
    for (const p of projects) {
      if (affected.has(p.name)) continue;
      if (p.dependencies?.declared.some((d) => affected.has(d))) {
        affected.add(p.name);
        reasons.add(ReasonCode.DEPENDS_ON);
        grew = true;
      }
    }
    if (!grew) break;
  }
  return {
    computationId: fakeSha("affected-" + paths.join(",")).slice(0, 12),
    projects: projects
      .filter((p) => affected.has(p.name))
      .map((p) => ({
        id: p.id,
        name: p.name,
        type: p.type,
        path: p.path,
        ownersSummary: p.effectiveOwners,
      })),
    paths,
    reasonCodes: [...reasons],
    runEverything: false,
  };
}

// planProject is the fake's §10.1 intent -> files pipeline: a loose mirror
// of project.DefaultTemplates (PROJECT.yaml + README + a type entrypoint),
// with the same validation/collision error codes the daemon returns.
const PROJECT_TYPES = ["library", "service", "app", "job", "other"];

// The §10.4 built-in template languages (Bazel-maturity set). Sorted to
// match project.TemplateSet.Languages() so error messages line up.
const TEMPLATE_LANGUAGES = ["cpp", "go", "java", "python", "rust", "ts"];

// Loose mirrors of project/templates.go's per-language skeletons (the go
// library file deliberately stays doc.go where the server emits lib.go -
// the fake's documented approximation).
function fakeEntrypoint(lang: string, short: string) {
  switch (lang) {
    case "python":
      return {
        path: "main.py",
        action: "create",
        content: 'def main():\n    pass\n\n\nif __name__ == "__main__":\n    main()\n',
      };
    case "ts":
      return { path: "main.ts", action: "create", content: "function main() {}\n\nmain();\n" };
    case "rust":
      return { path: "src/main.rs", action: "create", content: "fn main() {}\n" };
    case "java":
      return {
        path: "Main.java",
        action: "create",
        content: `package ${short.replace(/-/g, "")};\n\npublic class Main {\n    public static void main(String[] args) {}\n}\n`,
      };
    case "cpp":
      return { path: "main.cc", action: "create", content: "int main() { return 0; }\n" };
    default:
      return {
        path: "main.go",
        action: "create",
        content: `package main\n\nfunc main() {\n\t// ${short}: generated entrypoint (§10.1)\n}\n`,
      };
  }
}

function fakeLibrary(lang: string, short: string) {
  const mod = short.replace(/-/g, "_");
  switch (lang) {
    case "python":
      return { path: `${mod}.py`, action: "create", content: `"""Package ${mod}."""\n` };
    case "ts":
      return { path: "index.ts", action: "create", content: "export {};\n" };
    case "rust":
      return { path: "src/lib.rs", action: "create", content: `//! ${short}.\n` };
    case "java": {
      const cls = short
        .split("-")
        .filter(Boolean)
        .map((s) => s[0].toUpperCase() + s.slice(1))
        .join("");
      return {
        path: `${cls}.java`,
        action: "create",
        content: `package ${short.replace(/-/g, "")};\n\npublic class ${cls} {}\n`,
      };
    }
    case "cpp": {
      const guard = `${mod.toUpperCase()}_H_`;
      return {
        path: `${mod}.h`,
        action: "create",
        content: `#ifndef ${guard}\n#define ${guard}\n\n#endif  // ${guard}\n`,
      };
    }
    default: {
      const pkg = short.replace(/[^a-z0-9]/gi, "");
      return {
        path: "doc.go",
        action: "create",
        content: `// Package ${pkg} - generated by create_project.\npackage ${pkg}\n`,
      };
    }
  }
}

function planProject(state: FakeState, intent: CreateProjectIntent | undefined) {
  const name = intent?.name.trim() ?? "";
  const type = intent?.type ?? "";
  if (!name) {
    throw new ConnectError("invalid_intent: name is required", Code.InvalidArgument);
  }
  if (!/^[a-z][a-z0-9-]{1,62}$/.test(name)) {
    // Same rule and message as project.Validate server-side.
    throw new ConnectError(
      "invalid_format: name must match ^[a-z][a-z0-9-]{1,62}$ (use lowercase letters, digits, and hyphens only)",
      Code.InvalidArgument,
    );
  }
  if (!PROJECT_TYPES.includes(type)) {
    throw new ConnectError(
      `invalid_intent: type must be one of ${PROJECT_TYPES.join(", ")}`,
      Code.InvalidArgument,
    );
  }
  // Language rules mirror project.Validate (§10.4): pattern check first,
  // then supported-set check unless no_template legalizes any language.
  const language = intent?.language ?? "";
  const noTemplate = intent?.noTemplate ?? false;
  if (language && !/^[a-z][a-z0-9+_-]{0,31}$/.test(language)) {
    throw new ConnectError(
      "invalid_format: language must match ^[a-z][a-z0-9+_-]{0,31}$",
      Code.InvalidArgument,
    );
  }
  if (language && !noTemplate && !TEMPLATE_LANGUAGES.includes(language)) {
    throw new ConnectError(
      `unsupported_language: no built-in template for language "${language}"; supported: ${TEMPLATE_LANGUAGES.join(", ")} (pass --no-template to scaffold only PROJECT.yaml and README.md)`,
      Code.InvalidArgument,
    );
  }
  const path = intent?.path || name;
  for (const p of state.projects) {
    if (p.name === name || p.path === path) {
      throw new ConnectError(
        `already_exists: project ${p.name} already exists at ${p.path}`,
        Code.AlreadyExists,
      );
    }
  }
  const owners = intent?.owners ?? [];
  // Build-engine resolution (§14.5.5), mirroring project.PlanCreate:
  // explicit wins, ts defaults to vite (a territory scaffold with NO build
  // capability/binding), everything else to the bazel golden path;
  // no_template stays bazel.
  const requestedEngine = intent?.buildEngine ?? "";
  if (requestedEngine && !["bazel", "vite", "none"].includes(requestedEngine)) {
    throw new ConnectError(
      `unsupported_build_engine: unknown build engine "${requestedEngine}"; supported: bazel, vite, none`,
      Code.InvalidArgument,
    );
  }
  const engine =
    requestedEngine || (language === "ts" && !noTemplate ? "vite" : "bazel");
  const manifest = [
    "schema: project/v1",
    `name: ${name}`,
    `type: ${type}`,
    // Echoed verbatim, never default-filled - a defaulted (Go) create
    // leaves the key absent, same as the server.
    ...(language ? [`language: ${language}`] : []),
    ...(owners.length ? ["owners:", ...owners.map((o) => `  - ${o}`)] : []),
    ...(engine === "bazel"
      ? [
          "capabilities:",
          "  - build",
          "capability_config:",
          "  build:",
          "    engine: bazel",
          "    target_patterns:",
          `      - //${intent?.path || name}/...`,
        ]
      : []),
  ].join("\n") + "\n";
  const short = name.split("/").pop()!;
  const files = [
    { path: "PROJECT.yaml", action: "create", content: manifest },
    { path: "README.md", action: "create", content: `# ${name}\n` },
  ];
  const lang = language || "go";
  if (!noTemplate && (type === "service" || type === "app" || type === "job")) {
    files.push(fakeEntrypoint(lang, short));
  } else if (!noTemplate && type === "library") {
    files.push(fakeLibrary(lang, short));
  }
  if (engine === "bazel") {
    files.push({
      path: "BUILD.bazel",
      action: "create",
      content: `# Generated by create_project (build capability, \u00a714.5.4).\n# Target pattern: //${path}/...\n\nfilegroup(\n    name = "srcs",\n    srcs = glob(["**/*"], exclude = ["BUILD.bazel"]),\n    visibility = ["//visibility:public"],\n)\n`,
    });
  } else if (engine === "vite") {
    files.push({
      path: "package.json",
      action: "create",
      content: `{\n  "name": "${name}",\n  "private": true,\n  "type": "module",\n  "scripts": {\n    "dev": "vite",\n    "build": "vite build",\n    "preview": "vite preview"\n  },\n  "devDependencies": {\n    "vite": "^8"\n  }\n}\n`,
    });
    files.push({
      path: "vite.config.ts",
      action: "create",
      content: `// Generated by create_project (build_engine: vite, \u00a714.5.5).\nexport default {};\n`,
    });
  }
  return { path, files, owners, name, type };
}

const simulatedDelayMs = 150;
const delay = () =>
  new Promise((resolve) => setTimeout(resolve, Math.random() * simulatedDelayMs));

export function createFakeTransport(): Transport {
  const state = freshState();

  return createRouterTransport(({ service }) => {
    service(ChangeService, {
      async getChange(req) {
        await delay();
        return create(GetChangeResponseSchema, { change: mustChange(state, req.changeId) });
      },

      async listChanges(req) {
        await delay();
        const want = req.state === ChangeState.UNSPECIFIED ? ChangeState.OPEN : req.state;
        let out = [...state.changes.values()]
          .filter((c) => c.state === want)
          .sort((a, b) => Number(b.number - a.number));
        // Offset-token pagination, mirroring runkod's ListChanges: a
        // positive pageSize windows the list and reports the next offset;
        // pageSize 0 keeps the fetch-everything contract.
        let nextPageToken = "";
        const size = req.pageSize ?? 0;
        if (size > 0) {
          const offset = req.pageToken ? Number.parseInt(req.pageToken, 10) : 0;
          out = out.slice(offset);
          if (out.length > size) {
            out = out.slice(0, size);
            nextPageToken = String(offset + size);
          }
        }
        return create(ListChangesResponseSchema, { changes: out, nextPageToken });
      },

      async getChangeStack(req) {
        await delay();
        const chain = stackOf(state, req.changeId);
        return create(GetChangeStackResponseSchema, {
          changes: chain,
          position: chain.findIndex((c) => c.id === req.changeId),
        });
      },

      async getChangeDiff(req) {
        await delay();
        const c = mustChange(state, req.changeId);
        const files = state.diffs.get(req.changeId) ?? [];
        return create(GetChangeDiffResponseSchema, {
          changeId: c.id,
          baseSha: c.baseSha,
          headSha: c.headSha,
          files,
        });
      },

      async getAffected(req) {
        await delay();
        let paths: string[];
        if (req.target.case === "paths") {
          paths = req.target.value.paths;
        } else if (req.target.case === "changeId") {
          mustChange(state, req.target.value);
          paths = (state.diffs.get(req.target.value) ?? []).map((f) => f.path);
        } else {
          throw new ConnectError("target is required", Code.InvalidArgument);
        }
        return create(GetAffectedResponseSchema, { affected: affectedForPaths(state.projects, paths) });
      },

      async getMergeRequirements(req) {
        await delay();
        return create(GetMergeRequirementsResponseSchema, {
          requirements: mustRequirements(state, req.changeId),
        });
      },

      async approveChange(req) {
        await delay();
        const r = mustRequirements(state, req.changeId);
        const owners = r.owners!;
        if (!owners.required.includes(req.ownerRef)) {
          throw new ConnectError(
            `not_a_required_owner: ${req.ownerRef} is not a required owner of this change (required: ${owners.required.join(", ") || "none"})`,
            Code.InvalidArgument,
          );
        }
        if (!owners.satisfied.includes(req.ownerRef)) {
          owners.satisfied.push(req.ownerRef);
          owners.outstanding = owners.outstanding.filter((o) => o !== req.ownerRef);
        }
        recompute(r);
        recomputeAttention(state, req.changeId);
        return create(ApproveChangeResponseSchema, { requirements: r });
      },

      async landChange(req) {
        await delay();
        const c = mustChange(state, req.changeId);
        if (c.state === ChangeState.LANDED) {
          // Idempotent replay, matching runkod's land endpoint.
          return create(LandChangeResponseSchema, { landed: true, landedSha: c.landedSha, forced: c.landedForced });
        }
        if (c.state === ChangeState.ABANDONED) {
          throw new ConnectError("change is abandoned", Code.FailedPrecondition);
        }
        const r = mustRequirements(state, req.changeId);
        let forced = false;
        if (!r.mergeable) {
          // Mirrors runkod's landChangeCore: force is the admin override
          // (design.md 13.5); the demo scene has no principals, so it
          // plays the anonymous-operator role and always allows it.
          if (!req.force) {
            throw new ConnectError(
              `change is not mergeable: ${r.blockers.join("; ")}`,
              Code.FailedPrecondition,
            );
          }
          forced = true;
        }
        c.state = ChangeState.LANDED;
        c.landedForced = forced;
        c.landedSha = fakeSha(c.id + "-landed");
        c.landedAt = BigInt(Math.floor(Date.now() / 1000));
        const pending = state.pendingProjects.get(c.id);
        if (pending) {
          state.projects.push(pending);
          state.pendingProjects.delete(c.id);
        }
        return create(LandChangeResponseSchema, { landed: true, landedSha: c.landedSha, forced });
      },

      async abandonChange(req) {
        await delay();
        const c = mustChange(state, req.changeId);
        if (c.state === ChangeState.LANDED) {
          throw new ConnectError("cannot abandon a landed change", Code.FailedPrecondition);
        }
        c.state = ChangeState.ABANDONED;
        return create(AbandonChangeResponseSchema, { change: c });
      },

      async rerunCheck(req) {
        await delay();
        const r = mustRequirements(state, req.changeId);
        const checks = r.checks!;
        if (!checks.required.includes(req.checkName)) {
          throw new ConnectError(
            `check_not_required: ${req.checkName} is not a required check of this change`,
            Code.InvalidArgument,
          );
        }
        checks.failing = checks.failing.filter((c) => c !== req.checkName);
        checks.passing = checks.passing.filter((c) => c !== req.checkName);
        if (!checks.pending.includes(req.checkName)) checks.pending.push(req.checkName);
        recompute(r);
        return create(RerunCheckResponseSchema, { requirements: r });
      },

      // ---- review conversation (§13.4.1-13.4.2), mirroring runkod's
      // commentChangeCore/resolveCommentCore/requestReviewCore semantics.
      async listComments(req) {
        await delay();
        mustChange(state, req.changeId);
        let out = state.comments.get(req.changeId) ?? [];
        let nextPageToken = "";
        const size = req.pageSize ?? 0;
        if (size > 0) {
          const offset = req.pageToken ? Number.parseInt(req.pageToken, 10) : 0;
          out = out.slice(offset);
          if (out.length > size) {
            out = out.slice(0, size);
            nextPageToken = String(offset + size);
          }
        }
        return create(ListCommentsResponseSchema, { comments: out, nextPageToken });
      },

      async createComment(req) {
        await delay();
        const c = mustChange(state, req.changeId);
        if (c.state !== ChangeState.OPEN) {
          throw new ConnectError("change is not open", Code.FailedPrecondition);
        }
        if (!req.body.trim()) {
          throw new ConnectError("a comment needs a body", Code.InvalidArgument);
        }
        const list = state.comments.get(req.changeId) ?? [];
        if (req.parentId) {
          const parent = list.find((x) => x.id === req.parentId);
          if (!parent) {
            throw new ConnectError(`no comment ${req.parentId} on this change`, Code.InvalidArgument);
          }
          if (parent.parentId) {
            // One-level threads (§13.4.1) - same refusal runkod issues.
            throw new ConnectError(
              "thread_depth_exceeded: threads are one level deep - reply to the thread root",
              Code.InvalidArgument,
            );
          }
        }
        state.nextCommentID++;
        const comment = create(CommentSchema, {
          id: `cmt-${state.nextCommentID}`,
          author: { type: ActorType.USER, id: req.author || "demo" },
          body: req.body,
          createdAt: BigInt(Math.floor(Date.now() / 1000)),
          path: req.parentId ? "" : req.path,
          side: req.parentId ? CommentSide.UNSPECIFIED : req.line > 0 && req.side === CommentSide.UNSPECIFIED ? CommentSide.HEAD : req.side,
          line: req.parentId ? 0 : req.line,
          headSha: c.headSha, // the §13.4.1 binding: an amend outdates this
          parentId: req.parentId,
          resolved: false,
        });
        state.comments.set(req.changeId, [...list, comment]);
        recomputeAttention(state, req.changeId);
        return create(CreateCommentResponseSchema, { comment });
      },

      async resolveComment(req) {
        await delay();
        mustChange(state, req.changeId);
        const comment = (state.comments.get(req.changeId) ?? []).find((x) => x.id === req.commentId);
        if (!comment) throw notFound("comment", req.commentId);
        if (comment.parentId) {
          throw new ConnectError(
            "not_a_thread_root: resolved lives on the thread root, not on replies",
            Code.InvalidArgument,
          );
        }
        comment.resolved = req.resolved;
        return create(ResolveCommentResponseSchema, { comment });
      },

      async requestReview(req) {
        await delay();
        const c = mustChange(state, req.changeId);
        if (c.state !== ChangeState.OPEN) {
          throw new ConnectError("change is not open", Code.FailedPrecondition);
        }
        if (!req.reviewer.trim()) {
          throw new ConnectError("reviewer is required", Code.InvalidArgument);
        }
        const requests = state.reviewRequests.get(req.changeId) ?? new Map<string, string>();
        requests.set(req.reviewer.trim(), "demo");
        state.reviewRequests.set(req.changeId, requests);
        recomputeAttention(state, req.changeId);
        return create(RequestReviewResponseSchema, { reviewer: req.reviewer.trim(), requestedBy: "demo" });
      },
    });

    service(ProjectService, {
      async listProjects(req) {
        await delay();
        const q = req.query.toLowerCase();
        const out = state.projects.filter(
          (p) => !q || p.name.toLowerCase().includes(q) || p.path.toLowerCase().includes(q),
        );
        return create(ListProjectsResponseSchema, {
          projects: out.map((p) => ({
            id: p.id,
            name: p.name,
            type: p.type,
            path: p.path,
            ownersSummary: p.effectiveOwners,
          })),
          nextPageToken: "",
        });
      },

      async getProject(req) {
        await delay();
        const p = state.projects.find((x) => x.id === req.project || x.name === req.project);
        if (!p) throw notFound("project", req.project);
        return create(GetProjectResponseSchema, { project: p });
      },


      async previewCreateProject(req) {
        await delay();
        const plan = planProject(state, req.intent);
        return create(PreviewCreateProjectResponseSchema, {
          path: plan.path,
          files: plan.files,
        });
      },

      async createProject(req) {
        await delay();
        const plan = planProject(state, req.intent);
        const number = Math.max(0, ...[...state.changes.values()].map((c) => Number(c.number))) + 1;
        const id = "I" + fakeSha(`create-${plan.name}-${number}`);
        const change = create(ChangeSummarySchema, {
          id,
          state: ChangeState.OPEN,
          baseSha: TRUNK_SHA,
          headSha: fakeSha(`head-create-${plan.name}-${number}`),
          gitRef: `refs/changes/${id}/head`,
          title: `Create project ${plan.name}`,
          description: "Generated by the create-project flow (\u00a710.1): land to make it real.",
          authoredBy: { type: ActorType.USER, id: "you" },
          number: BigInt(number),
        });
        state.changes.set(id, change);
        const reqs = create(MergeRequirementsSchema, {
          changeId: id,
          owners: { required: plan.owners, satisfied: [], outstanding: [...plan.owners] },
          checks: { required: [], passing: [], failing: [], pending: [] },
        });
        recompute(reqs);
        state.requirements.set(id, reqs);
        state.diffs.set(
          id,
          plan.files.map((f) => addedFileDiff(`${plan.path}/${f.path}`, plan.name, f.content)),
        );
        const typeEnum =
          { library: ProjectType.LIBRARY, service: ProjectType.SERVICE, app: ProjectType.APP, job: ProjectType.JOB }[
            plan.type
          ] ?? ProjectType.OTHER;
        state.pendingProjects.set(
          id,
          create(ProjectDetailSchema, {
            id: plan.name,
            name: plan.name,
            type: typeEnum,
            path: plan.path,
            effectiveOwners: plan.owners,
            capabilities: ["build"],
            dependencies: { declared: [], inferred: [] },
          }),
        );
        return create(CreateProjectResponseSchema, { change });
      },

      async whoOwns(req) {
        await delay();
        if (req.target.case === "project") {
          const p = state.projects.find((x) => x.name === req.target.value);
          if (!p) throw notFound("project", req.target.value);
          return create(WhoOwnsResponseSchema, {
            owners: { owners: p.effectiveOwners, source: OwnersSource.PROJECT_MANIFEST },
          });
        }
        if (req.target.case === "path") {
          const owner = owningProject(state.projects, req.target.value);
          const p = state.projects.find((x) => x.path === owner);
          return create(WhoOwnsResponseSchema, {
            owners: p
              ? { owners: p.effectiveOwners, source: OwnersSource.PROJECT_MANIFEST }
              : { owners: ["group:eng"], source: OwnersSource.ORG_DEFAULT },
          });
        }
        throw new ConnectError("target is required", Code.InvalidArgument);
      },
    });

    service(WorkspaceService, {
      async listWorkspaces() {
        await delay();
        return create(ListWorkspacesResponseSchema, {
          workspaces: [...state.workspaces.values()],
        });
      },

      async getWorkspace(req) {
        await delay();
        const w = state.workspaces.get(req.id);
        if (!w) throw notFound("workspace", req.id);
        return create(GetWorkspaceResponseSchema, { workspace: w });
      },

      async createWorkspace(req) {
        await delay();
        if (!req.name || req.projects.length === 0) {
          throw new ConnectError(
            "name and at least one project are required",
            Code.InvalidArgument,
          );
        }
        if (state.workspaces.has(req.name)) {
          throw new ConnectError(`workspace already exists: ${req.name}`, Code.AlreadyExists);
        }
        const w = create(WorkspaceSummarySchema, {
          id: req.name,
          owner: req.owner || "you",
          baseRevision: TRUNK_SHA,
          projectAffinity: req.projects,
          writeAllowlist: [],
          snapshotRef: `refs/workspaces/${req.name}/head`,
          status: WorkspaceStatus.ACTIVE,
          // No refs until the first snapshot - so no branches yet, exactly
          // like the real daemon's derived-from-refs answer.
          branches: [],
        });
        state.workspaces.set(w.id, w);
        return create(CreateWorkspaceResponseSchema, { workspace: w });
      },

      async updateWorkspaceBase(req) {
        await delay();
        const w = state.workspaces.get(req.id);
        if (!w) throw notFound("workspace", req.id);
        w.baseRevision = req.baseRevision || TRUNK_SHA;
        return create(UpdateWorkspaceBaseResponseSchema, { workspace: w });
      },

      // Mirrors deleteWorkspaceCore's open-changes guard so the playground
      // teaches the real refusal, not a silent success.
      async deleteWorkspace(req) {
        await delay();
        if (!state.workspaces.has(req.id)) throw notFound("workspace", req.id);
        const blocking = [...state.changes.values()]
          .filter((c) => c.state === ChangeState.OPEN && c.originWorkspace === req.id)
          .map((c) => c.id);
        if (blocking.length > 0) {
          throw new ConnectError(
            `workspace_has_open_changes: workspace ${req.id} still has open changes: ${blocking.join(", ")} (land or abandon them first)`,
            Code.FailedPrecondition,
          );
        }
        state.workspaces.delete(req.id);
        return create(DeleteWorkspaceResponseSchema, {});
      },
    });

    service(RepoService, {
      async getTree(req) {
        await delay();
        const prefix = req.path === "" ? "" : req.path.replace(/\/+$/, "") + "/";
        const dirs = new Set<string>();
        const files: { name: string; path: string; size: number }[] = [];
        let sawPrefix = prefix === "";
        for (const [p, content] of Object.entries(fsFiles)) {
          if (!p.startsWith(prefix)) continue;
          sawPrefix = true;
          const rest = p.slice(prefix.length);
          const slash = rest.indexOf("/");
          if (slash >= 0) {
            dirs.add(rest.slice(0, slash));
          } else {
            files.push({ name: rest, path: p, size: content.length });
          }
        }
        if (!sawPrefix) throw notFound("directory", req.path);
        const entries = [
          ...[...dirs].sort().map((name) => ({
            name,
            path: prefix + name,
            type: TreeEntryType.DIR,
            size: 0n,
            project: owningProject(state.projects, prefix + name),
          })),
          ...files
            .sort((a, b) => a.name.localeCompare(b.name))
            .map((f) => ({
              name: f.name,
              path: f.path,
              type: TreeEntryType.FILE,
              size: BigInt(f.size),
              project: owningProject(state.projects, f.path),
            })),
        ];
        return create(GetTreeResponseSchema, { entries, rev: req.rev || TRUNK_SHA });
      },

      async getBlob(req) {
        await delay();
        const content = fsFiles[req.path];
        if (content === undefined) throw notFound("file", req.path);
        const binary = content === BINARY_MARKER;
        return create(GetBlobResponseSchema, {
          path: req.path,
          rev: req.rev || TRUNK_SHA,
          content: binary ? "" : content,
          binary,
          truncated: false,
          size: BigInt(binary ? 3 : content.length),
          project: owningProject(state.projects, req.path),
        });
      },

      async listCommits(req) {
        await delay();
        const all = historyForPath(req.path);
        const size = req.pageSize > 0 ? req.pageSize : 30;
        const off = req.pageToken ? parseInt(req.pageToken, 10) : 0;
        const page = all.slice(off, off + size);
        return create(ListCommitsResponseSchema, {
          commits: page.map((c) => ({
            sha: c.sha,
            subject: c.subject,
            authorName: c.authorName,
            authorEmail: c.authorEmail,
            authoredAt: BigInt(c.authoredAt),
            changeId: c.changeId,
            changeState: c.changeState,
          })),
          nextPageToken: off + size < all.length ? String(off + size) : "",
          rev: req.rev || TRUNK_SHA,
        });
      },

      async blameFile(req) {
        await delay();
        const content = fsFiles[req.path];
        if (content === undefined) throw notFound("file", req.path);
        if (content === BINARY_MARKER) {
          return create(BlameFileResponseSchema, {
            path: req.path,
            rev: req.rev || TRUNK_SHA,
            binary: true,
          });
        }
        const lines = content.split("\n");
        // Deterministic demo attribution: cycle the commits that touched
        // this path across chunks of lines, newest owning the top.
        const commits = historyForPath(req.path);
        const regions = [];
        let at = 1;
        let i = 0;
        while (at <= lines.length) {
          const c = commits[i % commits.length]!;
          const count = Math.min(3 + ((i * 2) % 4), lines.length - at + 1);
          regions.push({
            startLine: at,
            lineCount: count,
            sha: c.sha,
            subject: c.subject,
            authorName: c.authorName,
            authoredAt: BigInt(c.authoredAt),
            changeId: c.changeId,
            changeState: c.changeState,
          });
          at += count;
          i++;
        }
        return create(BlameFileResponseSchema, {
          path: req.path,
          rev: req.rev || TRUNK_SHA,
          regions,
          lines,
        });
      },
    });

    service(SearchService, {
      async searchCode(req) {
        await delay();
        if (!req.query) {
          throw new ConnectError("query is required", Code.InvalidArgument);
        }
        const q = req.query.toLowerCase();
        const hits = [];
        for (const doc of searchCorpus) {
          if (req.project && doc.project !== req.project) continue;
          for (let i = 0; i < doc.lines.length; i++) {
            if (doc.lines[i]!.toLowerCase().includes(q)) {
              hits.push({
                path: doc.path,
                projectId: doc.project,
                line: i + 1,
                preview: doc.lines[i]!,
              });
            }
          }
        }
        const limit = req.pageSize > 0 ? req.pageSize : 50;
        return create(SearchCodeResponseSchema, {
          hits: hits.slice(0, limit),
          nextPageToken: "",
        });
      },
    });
  });
}
