import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChangeState, ChangeSummarySchema, type ChangeSummary } from "../gen/runko/v1/common_pb";
import { branchesForWorkspace, buildStackForest, buildWorkspaceCards, changesByOrigin, layoutForest, layoutStack, railCells, retainedAbandoned, stackOrigin, stackSize, TRUNK_NODE_ID } from "./stacks";
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

// §12.2 provenance: workspace branch ↔ stack.
describe("stackOrigin / changesByOrigin", () => {
  const withOrigin = (id: string, base: string, head: string, number: number, ws: string, branch: string) =>
    create(ChangeSummarySchema, {
      id,
      state: ChangeState.OPEN,
      baseSha: base,
      headSha: head,
      number: BigInt(number),
      originWorkspace: ws,
      originBranch: branch,
    });

  it("stackOrigin walks past provenance-less changes to name the stack's workspace", () => {
    const [root] = buildStackForest([
      c("bottom", "T", "A", 1), // plain-git push, no provenance
      withOrigin("top", "A", "B", 2, "sku-validation", "head"),
    ]);
    expect(stackOrigin(root!)).toEqual({ workspace: "sku-validation", branch: "head" });
  });

  it("stackOrigin is undefined when nothing in the stack carries provenance", () => {
    const [root] = buildStackForest([c("only", "T", "A", 1)]);
    expect(stackOrigin(root!)).toBeUndefined();
  });

  it("changesByOrigin groups per workspace branch, base-most first, and omits the provenance-less", () => {
    const groups = changesByOrigin([
      withOrigin("fork", "A", "F", 4, "sku-validation", "inline-errors"),
      withOrigin("top", "A", "B", 3, "sku-validation", "head"),
      withOrigin("bottom", "T", "A", 1, "sku-validation", "head"),
      c("plain", "T", "P", 2),
    ]);
    expect([...groups.keys()].sort()).toEqual([
      "sku-validation/head",
      "sku-validation/inline-errors",
    ]);
    expect(groups.get("sku-validation/head")!.map((ch) => ch.map((x) => x.id))).toEqual([
      ["bottom", "top"],
    ]);
    expect(groups.get("sku-validation/inline-errors")!.map((ch) => ch.map((x) => x.id))).toEqual([
      ["fork"],
    ]);
  });

  it("changesByOrigin splits UNRELATED lines under one branch into separate chains - matching the inbox", () => {
    const groups = changesByOrigin([
      withOrigin("line1", "T", "A", 1, "ws", "head"),
      withOrigin("line2", "T2", "B", 2, "ws", "head"), // fresh trunk base: no chain to line1
      withOrigin("line2-child", "B", "C", 3, "ws", "head"),
    ]);
    const chains = groups.get("ws/head")!.map((ch) => ch.map((x) => x.id));
    expect(chains).toEqual([["line1"], ["line2", "line2-child"]]);
  });
});

describe("branchesForWorkspace", () => {
  it("unions refs-derived branches with origin-claimed ones, head first", () => {
    const stacks = new Map<string, unknown>([
      ["ws-a/head", []],
      ["ws-a/perf", []],
      ["ws-b/head", []],
    ]);
    // A fresh workspace: no snapshot pushed yet, but a change already
    // claims head - the branch must show so its stack has a row to live on.
    expect(branchesForWorkspace([], stacks, "ws-a")).toEqual(["head", "perf"]);
    // Refs-derived branches without open changes still show.
    expect(branchesForWorkspace(["head", "idle-line"], stacks, "ws-b")).toEqual([
      "head",
      "idle-line",
    ]);
  });
});

describe("workspace cards (one card per workspace, abandoned retained while depended upon)", () => {
  const ws = (id: string, base: string, head: string, n: number, workspace: string, branch: string, opts: Partial<ChangeSummary> = {}) => {
    const m = create(ChangeSummarySchema, {
      id, baseSha: base, headSha: head, number: BigInt(n),
      state: ChangeState.OPEN, originWorkspace: workspace, originBranch: branch,
      baseOnTrunk: false,
    });
    Object.assign(m, opts);
    return m;
  };

  it("retains an abandoned ancestor only while an open change depends on it", () => {
    const abandonedMid = ws("mid", "T", "M", 1, "w", "head", { state: ChangeState.ABANDONED, baseOnTrunk: true });
    const openLeaf = ws("leaf", "M", "L", 2, "w", "head");
    const abandonedLoner = ws("loner", "T", "X", 3, "w", "head", { state: ChangeState.ABANDONED, baseOnTrunk: true });
    expect(retainedAbandoned([openLeaf], [abandonedMid, abandonedLoner]).map((x) => x.id)).toEqual(["mid"]);
    // chains through SEVERAL abandoned ancestors survive too
    const abandonedTop = ws("mid2", "M", "M2", 4, "w", "head", { state: ChangeState.ABANDONED });
    const openTop = ws("leaf2", "M2", "L2", 5, "w", "head");
    expect(
      retainedAbandoned([openTop], [abandonedMid, abandonedTop]).map((x) => x.id).sort(),
    ).toEqual(["mid", "mid2"]);
  });

  it("groups one card per workspace with branches as forest roots; the abandoned mid reconnects the chain", () => {
    const a = ws("a", "T", "A", 1, "w", "head", { baseOnTrunk: true, state: ChangeState.ABANDONED });
    const b = ws("b", "A", "B", 2, "w", "head");
    const side = ws("s", "T", "S", 3, "w", "side", { baseOnTrunk: true });
    const other = ws("o", "T", "O", 4, "v", "head", { baseOnTrunk: true });
    const cards = buildWorkspaceCards([b, side, other], [a]);
    expect(cards.length).toBe(2);
    const wCard = cards.find((x) => x.workspace === "w")!;
    expect(wCard.roots.length).toBe(2); // head chain (a<-b) + side
    expect(wCard.stranded.length).toBe(0);
    const headRoot = wCard.roots.find((r) => r.change.id === "a")!;
    expect(headRoot.children.map((n) => n.change.id)).toEqual(["b"]);
  });

  it("stranded roots (base unreachable, parent NOT retained) split out of the shared anchor", () => {
    const stranded = ws("x", "gone", "X", 1, "w", "head"); // baseOnTrunk false, no abandoned parent
    const fine = ws("y", "T", "Y", 2, "w", "head", { baseOnTrunk: true });
    const [card] = buildWorkspaceCards([stranded, fine], []);
    expect(card!.roots.map((r) => r.change.id)).toEqual(["y"]);
    expect(card!.stranded.map((r) => r.change.id)).toEqual(["x"]);
  });

  it("workspace-less changes fall back to one card per ancestry tree", () => {
    const l1 = c("l1", "T", "L1", 1);
    const l2 = c("l2", "T", "L2", 2);
    const cards = buildWorkspaceCards([l1, l2], []);
    expect(cards.length).toBe(2);
    expect(cards.every((x) => x.workspace === undefined)).toBe(true);
  });

  it("layoutForest hangs every root off one virtual trunk row", () => {
    const r1 = buildStackForest([c("a", "T", "A", 1)])[0]!;
    const r2 = buildStackForest([c("b", "T", "B", 2)])[0]!;
    const layout = layoutForest([r1, r2]);
    const last = layout.rows[layout.rows.length - 1]!;
    expect(last.change.id).toBe(TRUNK_NODE_ID);
    expect(last.lane).toBe(0);
    expect(layout.lanes).toBe(2);
    // both roots edge into the trunk row
    const trunkRow = layout.rows.length - 1;
    expect(layout.edges.filter((e) => e.toRow === trunkRow).length).toBe(2);
  });
});
