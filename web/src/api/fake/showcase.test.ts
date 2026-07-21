import { describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createFakeTransport, demoScene } from "./transport";
import { ChangeService } from "../../gen/runko/v1/changes_pb";
import { WorkspaceService } from "../../gen/runko/v1/workspaces_pb";
import { ChangeState, WorkspaceStatus } from "../../gen/runko/v1/common_pb";
import {
  AGENT,
  CHANGE_A_ID,
  CHANGE_B_ID,
  SHOWCASE,
  WS_ID,
  startsOnEmptyWorld,
} from "../../demo/showcase";

// The "Watch me work" script (demo/showcase.ts) drives the same store
// the fake transport serves. These tests play the whole timeline the
// way Director.tsx does - scene.mutate(beat.apply) in order - and
// assert the story the visitor watches: workspace born, agent activity
// streaming, snapshots growing the WIP diff, a stack pushed, checks
// failing then recovering, a human approving, automerge landing
// bottom-up, the workspace closing behind it.
describe("watch-me-work showcase script", () => {
  it("is a well-formed timeline", () => {
    expect(SHOWCASE.length).toBeGreaterThan(10);
    let prev = -1;
    for (const beat of SHOWCASE) {
      expect(beat.at).toBeGreaterThanOrEqual(0);
      expect(beat.at).toBeLessThanOrEqual(1);
      expect(beat.at).toBeGreaterThanOrEqual(prev);
      prev = beat.at;
      expect(beat.caption.title).not.toBe("");
      expect(beat.caption.body).not.toBe("");
    }
    // First and last beats bookend the runtime, so the progress bar and
    // the story start and end together.
    expect(SHOWCASE[0]!.at).toBe(0);
    expect(SHOWCASE[SHOWCASE.length - 1]!.at).toBe(1);
  });

  it("knows which page loads auto-start the tour (the pre-paint reset trigger)", () => {
    expect(startsOnEmptyWorld("/demo/watch", "")).toBe(true);
    expect(startsOnEmptyWorld("/demo/watch/", "")).toBe(true);
    expect(startsOnEmptyWorld("/watch", "")).toBe(true);
    expect(startsOnEmptyWorld("/demo/changes", "?watch=1&t=120")).toBe(true);
    expect(startsOnEmptyWorld("/demo/changes", "")).toBe(false);
    expect(startsOnEmptyWorld("/demo/watchman", "")).toBe(false);
  });

  it("opens on an empty world: the reset beat clears everything in flight", () => {
    createFakeTransport();
    const scene = demoScene()!;
    expect(scene.state.changes.size).toBeGreaterThan(0); // the fixture scene
    scene.mutate(SHOWCASE[0]!.apply);
    expect(scene.state.changes.size).toBe(0);
    expect(scene.state.workspaces.size).toBe(0);
    expect(scene.state.workspaceEvents.size).toBe(0);
    expect(scene.state.workspaceActivity.size).toBe(0);
    // Projects and the browsable tree stay - the org the agent works IN.
    expect(scene.state.projects.length).toBeGreaterThan(0);
    // The canned watchWorkspace scene stands down for the tutorial.
    expect(scene.state.showcaseActive).toBe(true);
  });

  it("plays end to end: agent works, human approves, stack lands, workspace closes", async () => {
    const transport = createFakeTransport();
    const scene = demoScene()!;
    expect(scene).not.toBeNull();
    const changes = createClient(ChangeService, transport);
    const ws = createClient(WorkspaceService, transport);

    let mutations = 0;
    scene.bus.addEventListener("mutate", () => mutations++);

    const applyThrough = (untilAt: number) => {
      for (const beat of SHOWCASE) {
        if (beat.at > untilAt) break;
        scene.mutate(beat.apply);
      }
    };

    // Mid-show: the workspace exists and is streaming activity, with a
    // growing WIP diff served per branch.
    applyThrough(0.45);
    const born = await ws.getWorkspace({ id: WS_ID });
    expect(born.workspace?.status).toBe(WorkspaceStatus.ACTIVE);
    expect(born.workspace?.owner).toBe(AGENT);
    const feed = await ws.listWorkspaceActivity({ id: WS_ID });
    expect(feed.events.length).toBeGreaterThan(4);
    const kinds = new Set(feed.events.map((e) => e.kind));
    for (const kind of ["read", "search", "command", "note", "edit"]) {
      expect(kinds).toContain(kind);
    }
    const wip = await ws.getWorkspaceDiff({ id: WS_ID });
    expect(wip.snapshotSha).not.toBe("");
    expect(wip.files.length).toBe(3);

    // The stack arrives: two open agent-authored changes, child on parent.
    applyThrough(0.5);
    const a = await changes.getChange({ changeId: CHANGE_A_ID });
    const b = await changes.getChange({ changeId: CHANGE_B_ID });
    expect(a.change?.state).toBe(ChangeState.OPEN);
    expect(b.change?.baseSha).toBe(a.change?.headSha);
    const stack = await changes.getChangeStack({ changeId: CHANGE_B_ID });
    expect(stack.changes.map((c) => c.id)).toEqual([CHANGE_A_ID, CHANGE_B_ID]);

    // The flake beat: B's required check fails, so B is not mergeable.
    applyThrough(0.56);
    let reqB = await changes.getMergeRequirements({ changeId: CHANGE_B_ID });
    expect(reqB.requirements?.checks?.failing.length).toBe(1);
    expect(reqB.requirements?.mergeable).toBe(false);

    // Approval is the LAST outstanding gate before landing: checks are
    // green again, automerge armed, priya (a different user - never the
    // agent) satisfies group:commerce.
    applyThrough(0.89);
    reqB = await changes.getMergeRequirements({ changeId: CHANGE_B_ID });
    expect(reqB.requirements?.mergeable).toBe(true);
    expect((await changes.getChange({ changeId: CHANGE_A_ID })).change?.automerge).toBe(true);

    // Bottom-up landing: after the parent's beat, A is landed while B is
    // still open; the child follows on its own beat.
    applyThrough(0.92);
    expect((await changes.getChange({ changeId: CHANGE_A_ID })).change?.state).toBe(
      ChangeState.LANDED,
    );
    expect((await changes.getChange({ changeId: CHANGE_B_ID })).change?.state).toBe(
      ChangeState.OPEN,
    );

    applyThrough(1);
    expect((await changes.getChange({ changeId: CHANGE_B_ID })).change?.state).toBe(
      ChangeState.LANDED,
    );
    const closed = await ws.getWorkspace({ id: WS_ID });
    expect(closed.workspace?.status).toBe(WorkspaceStatus.CLOSED);
    // The end state is exactly what the story built - the world started
    // empty, so nothing else is in flight.
    expect(scene.state.changes.size).toBe(2);
    expect(scene.state.workspaces.size).toBe(1);

    // Every beat poked the bus - that's what makes the mounted pages
    // refetch while the visitor browses.
    expect(mutations).toBeGreaterThanOrEqual(SHOWCASE.length);
  });

  it("never fights the visitor: pre-empted actions leave the show consistent", async () => {
    const transport = createFakeTransport();
    const scene = demoScene()!;
    const changes = createClient(ChangeService, transport);

    // Play until the stack exists, then the visitor lands the parent
    // themselves (force: its owner gate is still outstanding - the demo
    // plays the anonymous-operator role, same as the Land button).
    for (const beat of SHOWCASE) {
      if (beat.at > 0.5) break;
      scene.mutate(beat.apply);
    }
    await changes.landChange({ changeId: CHANGE_A_ID, force: true });

    // The rest of the script - including the beat that would land A -
    // must not throw, and the world must still converge.
    for (const beat of SHOWCASE) {
      if (beat.at <= 0.5) continue;
      scene.mutate(beat.apply);
    }
    expect((await changes.getChange({ changeId: CHANGE_A_ID })).change?.state).toBe(
      ChangeState.LANDED,
    );
    expect((await changes.getChange({ changeId: CHANGE_B_ID })).change?.state).toBe(
      ChangeState.LANDED,
    );
  });

  it("beats are idempotent enough to replay without duplicating the world", () => {
    createFakeTransport();
    const scene = demoScene()!;
    for (const beat of SHOWCASE) scene.mutate(beat.apply);
    const changesAfterOnce = scene.state.changes.size;
    const wsEventsAfterOnce = scene.state.workspaceEvents.get(WS_ID)?.length ?? 0;
    // Re-applying the creation beats must not mint duplicates (activity
    // rows are append-by-design and excluded from this claim).
    for (const beat of SHOWCASE) scene.mutate(beat.apply);
    expect(scene.state.changes.size).toBe(changesAfterOnce);
    expect(scene.state.workspaces.get(WS_ID)?.status).toBe(WorkspaceStatus.CLOSED);
    expect(scene.state.workspaceEvents.get(WS_ID)?.length).toBeGreaterThanOrEqual(
      wsEventsAfterOnce,
    );
  });
});
