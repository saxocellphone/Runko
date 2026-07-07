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
  ChangeState,
  ChangeSummarySchema,
  MergeRequirementsSchema,
  ReasonCode,
  WorkspaceSummarySchema,
  type ChangeSummary,
  type MergeRequirements,
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
} from "../../gen/runko/v1/changes_pb";
import {
  ProjectService,
  GetProjectResponseSchema,
  ListProjectsResponseSchema,
  WhoOwnsResponseSchema,
} from "../../gen/runko/v1/projects_pb";
import {
  WorkspaceService,
  CreateWorkspaceResponseSchema,
  GetWorkspaceResponseSchema,
  ListWorkspacesResponseSchema,
  UpdateWorkspaceBaseResponseSchema,
} from "../../gen/runko/v1/workspaces_pb";
import { SearchService, SearchCodeResponseSchema } from "../../gen/runko/v1/search_pb";
import { OwnersSource, WorkspaceStatus } from "../../gen/runko/v1/common_pb";
import {
  changes as fixtureChanges,
  diffs as fixtureDiffs,
  requirements as fixtureRequirements,
  projects,
  searchCorpus,
  workspaces as fixtureWorkspaces,
  fakeSha,
  TRUNK_SHA,
} from "./fixtures";

interface FakeState {
  changes: Map<string, ChangeSummary>;
  requirements: Map<string, MergeRequirements>;
  workspaces: Map<string, WorkspaceSummary>;
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
  };
  for (const r of state.requirements.values()) recompute(r);
  return state;
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
  const chain = [root];
  const downSeen = new Set([root.id]);
  for (;;) {
    const kids = [...(childrenOf.get(chain[chain.length - 1]!.id) ?? [])].sort((a, b) =>
      Number(a.number - b.number),
    );
    const next = kids.find((k) => !downSeen.has(k.id));
    if (!next) break;
    downSeen.add(next.id);
    chain.push(next);
  }
  return chain;
}

// Longest-prefix path -> project match (§13.3), plus reverse declared-dep
// closure for the affected set.
function owningProject(path: string): string {
  let best = "";
  for (const p of projects) {
    if ((path === p.path || path.startsWith(p.path + "/")) && p.path.length > best.length) {
      best = p.path;
    }
  }
  return best;
}

function affectedForPaths(paths: string[]) {
  const direct = new Set<string>();
  for (const p of paths) {
    const owner = owningProject(p);
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
        const out = [...state.changes.values()]
          .filter((c) => c.state === want)
          .sort((a, b) => Number(b.number - a.number));
        return create(ListChangesResponseSchema, { changes: out, nextPageToken: "" });
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
        const files = fixtureDiffs.get(req.changeId) ?? [];
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
          paths = (fixtureDiffs.get(req.target.value) ?? []).map((f) => f.path);
        } else {
          throw new ConnectError("target is required", Code.InvalidArgument);
        }
        return create(GetAffectedResponseSchema, { affected: affectedForPaths(paths) });
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
        return create(ApproveChangeResponseSchema, { requirements: r });
      },

      async landChange(req) {
        await delay();
        const c = mustChange(state, req.changeId);
        if (c.state === ChangeState.LANDED) {
          // Idempotent replay, matching runkod's land endpoint.
          return create(LandChangeResponseSchema, { landed: true, landedSha: c.landedSha });
        }
        if (c.state === ChangeState.ABANDONED) {
          throw new ConnectError("change is abandoned", Code.FailedPrecondition);
        }
        const r = mustRequirements(state, req.changeId);
        if (!r.mergeable) {
          throw new ConnectError(
            `change is not mergeable: ${r.blockers.join("; ")}`,
            Code.FailedPrecondition,
          );
        }
        c.state = ChangeState.LANDED;
        c.landedSha = fakeSha(c.id + "-landed");
        return create(LandChangeResponseSchema, { landed: true, landedSha: c.landedSha });
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
    });

    service(ProjectService, {
      async listProjects(req) {
        await delay();
        const q = req.query.toLowerCase();
        const out = projects.filter(
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
        const p = projects.find((x) => x.id === req.project || x.name === req.project);
        if (!p) throw notFound("project", req.project);
        return create(GetProjectResponseSchema, { project: p });
      },

      async whoOwns(req) {
        await delay();
        if (req.target.case === "project") {
          const p = projects.find((x) => x.name === req.target.value);
          if (!p) throw notFound("project", req.target.value);
          return create(WhoOwnsResponseSchema, {
            owners: { owners: p.effectiveOwners, source: OwnersSource.PROJECT_MANIFEST },
          });
        }
        if (req.target.case === "path") {
          const owner = owningProject(req.target.value);
          const p = projects.find((x) => x.path === owner);
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
