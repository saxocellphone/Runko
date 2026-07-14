import { describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createFakeTransport } from "./transport";
import { WorkspaceService, WorkspaceEventType } from "../../gen/runko/v1/workspaces_pb";

// The fake transport mirrors the §12.6 observability surface: per-branch
// WIP diffs (empty state = "" sha, never an error), newest-first
// timelines with offset paging, and a WatchWorkspace stream whose first
// frame is the liveness keepalive and whose scripted event genuinely
// moves the timeline - so the /demo playground exercises the exact
// stream-as-poke contract WorkspacePage runs against the real daemon.
describe("fake workspace observability", () => {
  it("serves per-branch WIP and the no-snapshot empty state", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());

    const head = await ws.getWorkspaceDiff({ id: "sku-validation" });
    expect(head.branch).toBe("head"); // "" defaults to head
    expect(head.snapshotSha).not.toBe("");
    expect(head.files.length).toBeGreaterThan(0);

    const parallel = await ws.getWorkspaceDiff({ id: "sku-validation", branch: "inline-errors" });
    expect(parallel.files.map((f) => f.path)).toEqual(["web/storefront/src/InlineError.tsx"]);

    // authz-cache never pushed a snapshot: an answer, not an error.
    const empty = await ws.getWorkspaceDiff({ id: "authz-cache" });
    expect(empty.snapshotSha).toBe("");
    expect(empty.files).toEqual([]);

    await expect(ws.getWorkspaceDiff({ id: "no-such" })).rejects.toThrow(/workspace/);
  });

  it("lists the timeline newest-first and tiles it with offset tokens", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());

    const full = await ws.listWorkspaceEvents({ id: "sku-validation" });
    expect(full.nextPageToken).toBe("");
    const ids = full.events.map((ev) => ev.id);
    expect(ids).toEqual([...ids].sort((a, b) => Number(b - a)));
    expect(full.events.length).toBeGreaterThan(2);

    const size = full.events.length - 1;
    const first = await ws.listWorkspaceEvents({ id: "sku-validation", pageSize: size });
    const second = await ws.listWorkspaceEvents({
      id: "sku-validation",
      pageSize: size,
      pageToken: first.nextPageToken,
    });
    expect(second.nextPageToken).toBe("");
    expect([...first.events, ...second.events].map((ev) => ev.id)).toEqual(ids);
  });

  it("streams the liveness frame, then a poke that moved the timeline", async () => {
    const transport = createFakeTransport();
    const ws = createClient(WorkspaceService, transport);
    const before = await ws.listWorkspaceEvents({ id: "refactor-bot-cfg" });

    const abort = new AbortController();
    const frames: { hasEvent: boolean; type?: WorkspaceEventType }[] = [];
    for await (const frame of ws.watchWorkspace(
      { id: "refactor-bot-cfg" },
      { signal: abort.signal },
    )) {
      frames.push({ hasEvent: frame.event !== undefined, type: frame.event?.type });
      if (frames.length === 2) {
        abort.abort(); // unparks the fake's keepalive sleep
        break;
      }
    }

    expect(frames[0]).toEqual({ hasEvent: false, type: undefined });
    expect(frames[1].hasEvent).toBe(true);
    expect(frames[1].type).toBe(WorkspaceEventType.SNAPSHOT_PUSHED);

    // The poke's refetch must see something NEW - the whole point of
    // stream-as-poke is that the unary reads carry the state.
    const after = await ws.listWorkspaceEvents({ id: "refactor-bot-cfg" });
    expect(after.events.length).toBe(before.events.length + 1);
    expect(after.events[0].type).toBe(WorkspaceEventType.SNAPSHOT_PUSHED);
  });

  // The §12.6.1 half: the harness-reported feed, its at-a-glance surfacing
  // on ListWorkspaces, deletion riding workspace delete, and the stream's
  // AGENT_ACTIVITY poke moving the feed.
  it("lists agent activity newest-first with offset tokens", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());

    const full = await ws.listWorkspaceActivity({ id: "refactor-bot-cfg" });
    const ids = full.events.map((ev) => ev.id);
    expect(ids).toEqual([...ids].sort((a, b) => Number(b - a)));
    expect(full.events[0].kind).toBe("edit");
    expect(full.events[0].actor?.id).toBe("refactor-bot");
    expect(full.events[0].sessionId).toBe("sess-refactor-1");

    const first = await ws.listWorkspaceActivity({ id: "refactor-bot-cfg", pageSize: 3 });
    expect(first.nextPageToken).not.toBe("");
    const second = await ws.listWorkspaceActivity({
      id: "refactor-bot-cfg",
      pageSize: 3,
      pageToken: first.nextPageToken,
    });
    expect([...first.events, ...second.events].map((ev) => ev.id)).toEqual(ids);

    await expect(ws.listWorkspaceActivity({ id: "no-such" })).rejects.toThrow(/workspace/);
  });

  it("serves the full kind vocabulary so the filter chips all populate", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());
    const full = await ws.listWorkspaceActivity({ id: "refactor-bot-cfg" });
    const kinds = new Set(full.events.map((ev) => ev.kind));
    for (const k of ["read", "edit", "command", "search", "note"]) {
      expect(kinds, `demo feed should carry a ${k} row`).toContain(k);
    }
  });

  it("surfaces the newest activity as latest_activity on the workspace list", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());
    const list = await ws.listWorkspaces({});
    const bot = list.workspaces.find((w) => w.id === "refactor-bot-cfg");
    expect(bot?.latestActivity?.kind).toBe("edit");
    expect(bot?.latestActivity?.detail).toBe("commerce/checkout-api/config.go");
    // A workspace that never reported carries no claim at all.
    const quiet = list.workspaces.find((w) => w.id === "authz-cache");
    expect(quiet?.latestActivity).toBeUndefined();
  });

  it("drops the activity feed with the workspace", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());
    // pricing-spike has no open changes, so deletion is allowed - seed a
    // row through state by watching? No: fixtures only cover two feeds,
    // so assert on sku-validation's after abandoning its blocker is
    // overkill; instead delete pricing-spike and confirm the feed read
    // 404s with the workspace, which is the coupling under test.
    await ws.deleteWorkspace({ id: "pricing-spike" });
    await expect(ws.listWorkspaceActivity({ id: "pricing-spike" })).rejects.toThrow(/workspace/);
  });

  // Timeout: the scripted scene is 2.5s (snapshot beat) + 1.8s (activity
  // beat) before the third frame arrives.
  it("streams an AGENT_ACTIVITY poke whose refetch sees the new activity row", { timeout: 10_000 }, async () => {
    const transport = createFakeTransport();
    const ws = createClient(WorkspaceService, transport);
    const before = await ws.listWorkspaceActivity({ id: "refactor-bot-cfg" });

    const abort = new AbortController();
    const frames: { hasEvent: boolean; type?: WorkspaceEventType }[] = [];
    for await (const frame of ws.watchWorkspace(
      { id: "refactor-bot-cfg" },
      { signal: abort.signal },
    )) {
      frames.push({ hasEvent: frame.event !== undefined, type: frame.event?.type });
      if (frames.length === 3) {
        abort.abort();
        break;
      }
    }
    expect(frames[2].hasEvent).toBe(true);
    expect(frames[2].type).toBe(WorkspaceEventType.AGENT_ACTIVITY);

    const after = await ws.listWorkspaceActivity({ id: "refactor-bot-cfg" });
    expect(after.events.length).toBe(before.events.length + 1);
    expect(after.events[0].detail).toBe("commerce/checkout-api/config_test.go");
    expect(after.events[0].sessionId).toBe("sess-live");
  });
});
