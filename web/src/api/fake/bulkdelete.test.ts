import { describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createFakeTransport } from "./transport";
import { WorkspaceService } from "../../gen/runko/v1/workspaces_pb";
import { runBulk } from "../../lib/bulk";

// Bulk workspace deletion is a client-side fan-out over the single-item
// DeleteWorkspace RPC (§12.2) - there is no batch verb, precisely so that
// every item keeps its own server-side guard. This pins the behaviour the
// workspaces list depends on: a selection that mixes deletable rows with
// ones the server refuses must delete what it can, keep the rest, and
// report per workspace which was which.
describe("bulk workspace deletion over the fake transport", () => {
  const del = (ws: ReturnType<typeof createClient<typeof WorkspaceService>>) => (id: string) =>
    ws.deleteWorkspace({ id });

  it("deletes what it can and reports each refusal", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());
    const before = await ws.listWorkspaces({});
    expect(before.workspaces.map((w) => w.id)).toContain("pricing-spike");

    // sku-validation still carries open changes; pricing-spike carries none.
    const { done, failed } = await runBulk(["sku-validation", "pricing-spike"], del(ws));

    expect(done).toEqual(["pricing-spike"]);
    expect(failed).toHaveLength(1);
    expect(failed[0].id).toBe("sku-validation");
    expect(failed[0].message).toContain("workspace_has_open_changes");

    const after = await ws.listWorkspaces({});
    const ids = after.workspaces.map((w) => w.id);
    expect(ids).not.toContain("pricing-spike");
    expect(ids).toContain("sku-validation"); // a refusal leaves the row intact
  });

  it("reports an already-deleted id instead of failing the whole run", async () => {
    const ws = createClient(WorkspaceService, createFakeTransport());
    await ws.deleteWorkspace({ id: "pricing-spike" });

    // Re-deleting a row a concurrent viewer already removed is a stale
    // selection, not a broken page: it lands in `failed` like any other.
    const { done, failed } = await runBulk(["pricing-spike"], del(ws));
    expect(done).toEqual([]);
    expect(failed[0].message).toMatch(/pricing-spike/);
  });
});
