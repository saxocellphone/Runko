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
});
