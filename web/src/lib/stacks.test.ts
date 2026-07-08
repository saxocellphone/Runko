import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChangeState, ChangeSummarySchema, type ChangeSummary } from "../gen/runko/v1/common_pb";
import { buildStackForest, layoutStack, railCells, stackSize } from "./stacks";
import { createFakeTransport } from "../api/fake/transport";
import { createClient } from "@connectrpc/connect";
import { ChangeService } from "../gen/runko/v1/changes_pb";
import { RepoService, TreeEntryType } from "../gen/runko/v1/repo_pb";
import {
  stackBottom,
  stackFork,
  stackMiddle,
  stackTop,
  soloChange,
} from "../api/fake/fixtures";

const c = (id: string, base: string, head: string, number: number): ChangeSummary =>
  create(ChangeSummarySchema, {
    id,
    state: ChangeState.OPEN,
    baseSha: base,
    headSha: head,
    number: BigInt(number),
  });

describe("buildStackForest", () => {
  it("keeps independent changes as trees of one, newest first", () => {
    const forest = buildStackForest([c("a", "T", "A", 1), c("b", "T", "B", 2)]);
    expect(forest.map((n) => n.change.id)).toEqual(["b", "a"]);
    expect(forest.every((n) => n.children.length === 0)).toBe(true);
  });

  it("chains base->head links regardless of input order", () => {
    const [root] = buildStackForest([
      c("top", "B", "C", 3),
      c("bottom", "T", "A", 1),
      c("middle", "A", "B", 2),
    ]);
    expect(root!.change.id).toBe("bottom");
    expect(root!.children.map((n) => n.change.id)).toEqual(["middle"]);
    expect(stackSize(root!)).toBe(3);
  });

  it("a fork is ONE tree with sibling children - never a duplicated prefix", () => {
    const forest = buildStackForest([
      c("root", "T", "A", 1),
      c("left", "A", "L", 2),
      c("right", "A", "R", 3),
    ]);
    expect(forest).toHaveLength(1);
    expect(forest[0]!.children.map((n) => n.change.id)).toEqual(["left", "right"]);
  });
});

describe("layoutStack + railCells", () => {
  const fork = () =>
    buildStackForest([
      c("root", "T", "A", 1),
      c("left", "A", "L", 2),
      c("right", "A", "R", 3),
    ])[0]!;

  it("puts the last-rendered child straight above its parent, forks in outer lanes", () => {
    const layout = layoutStack(fork());
    expect(layout.rows.map((r) => `${r.change.id}@${r.lane}`)).toEqual([
      "left@1",
      "right@0",
      "root@0",
    ]);
    expect(layout.lanes).toBe(2);
  });

  it("draws pass-through verticals and a merge corner that line up", () => {
    const layout = layoutStack(fork());
    // left's row: dot in lane 1, nothing in lane 0 yet.
    expect(railCells(layout, 0)).toEqual([
      { kind: "empty" },
      { kind: "dot", up: false, down: true, right: false },
    ]);
    // right's row: dot in lane 0, left's line PASSES THROUGH lane 1.
    expect(railCells(layout, 1)).toEqual([
      { kind: "dot", up: false, down: true, right: false },
      { kind: "v" },
    ]);
    // root's row: dot with the straight child from above + the fork
    // merging in with a corner.
    expect(railCells(layout, 2)).toEqual([
      { kind: "dot", up: true, down: true, right: true },
      { kind: "corner", right: false },
    ]);
  });
});

describe("fake transport stacks", () => {
  const client = () => createClient(ChangeService, createFakeTransport());

  it("GetChangeStack returns the FULL tree from any member, parents first", async () => {
    // The demo scene forks at stackBottom: middle->top on one line,
    // stackFork (a workspace-branch parallel approach) on the other.
    const want = [stackBottom.id, stackMiddle.id, stackTop.id, stackFork.id];
    for (const [id, pos] of [
      [stackBottom.id, 0],
      [stackTop.id, 2],
      [stackFork.id, 3],
    ] as const) {
      const res = await client().getChangeStack({ changeId: id });
      expect(res.changes.map((x) => x.id)).toEqual(want);
      expect(res.position).toBe(pos);
    }
  });

  it("GetChangeStack returns a tree of one for an unstacked change", async () => {
    const res = await client().getChangeStack({ changeId: soloChange.id });
    expect(res.changes.map((x) => x.id)).toEqual([soloChange.id]);
    expect(res.position).toBe(0);
  });

  it("landing is gated on the same mergeable bool merge-requirements reports", async () => {
    const cl = client();
    const before = await cl.getMergeRequirements({ changeId: stackMiddle.id });
    expect(before.requirements!.mergeable).toBe(false);
    await expect(cl.landChange({ changeId: stackMiddle.id })).rejects.toThrow(/not mergeable/);

    const ready = await cl.getMergeRequirements({ changeId: stackBottom.id });
    expect(ready.requirements!.mergeable).toBe(true);
    const landed = await cl.landChange({ changeId: stackBottom.id });
    expect(landed.landed).toBe(true);
  });

  it("approve moves the owner gate and refreshes blockers", async () => {
    const cl = client();
    const res = await cl.approveChange({
      changeId: stackMiddle.id,
      ownerRef: "group:commerce",
      approvedBy: "user:demo",
    });
    expect(res.requirements!.owners!.outstanding).toEqual([]);
    expect(res.requirements!.mergeable).toBe(false);
  });

  it("approve rejects a non-required owner with the structured code", async () => {
    await expect(
      client().approveChange({
        changeId: stackMiddle.id,
        ownerRef: "group:nobody",
        approvedBy: "user:demo",
      }),
    ).rejects.toThrow(/not_a_required_owner/);
  });
});

describe("fake RepoService", () => {
  const repo = () => createClient(RepoService, createFakeTransport());

  it("lists the root with dirs before files and project tags", async () => {
    const res = await repo().getTree({ path: "" });
    const names = res.entries.map((e) => e.name);
    expect(names).toEqual(["commerce", "platform", "tools", "web", "OWNERS", "README.md"]);
    const commerce = res.entries.find((e) => e.name === "commerce")!;
    expect(commerce.type).toBe(TreeEntryType.DIR);
  });

  it("lists a nested directory and tags the owning project", async () => {
    const res = await repo().getTree({ path: "commerce/cart" });
    expect(res.entries.every((e) => e.project === "commerce/cart")).toBe(true);
    expect(res.entries.some((e) => e.name === "PROJECT.yaml")).toBe(true);
  });

  it("serves file content and flags binary", async () => {
    const cl = repo();
    const text = await cl.getBlob({ path: "commerce/cart/sku.go" });
    expect(text.content).toContain("func ParseSKU");
    expect(text.binary).toBe(false);
    const bin = await cl.getBlob({ path: "web/storefront/assets/error-icon.png" });
    expect(bin.binary).toBe(true);
    expect(bin.content).toBe("");
  });

  it("404s unknown paths", async () => {
    await expect(repo().getTree({ path: "no/such/dir" })).rejects.toThrow(/not found/);
    await expect(repo().getBlob({ path: "no/such/file.go" })).rejects.toThrow(/not found/);
  });
});
