import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChangeState, ChangeSummarySchema, type ChangeSummary } from "../gen/runko/v1/common_pb";
import { groupIntoStacks } from "./stacks";
import { createFakeTransport } from "../api/fake/transport";
import { createClient } from "@connectrpc/connect";
import { ChangeService } from "../gen/runko/v1/changes_pb";
import { RepoService, TreeEntryType } from "../gen/runko/v1/repo_pb";
import { stackBottom, stackMiddle, stackTop, soloChange } from "../api/fake/fixtures";

const c = (id: string, base: string, head: string, number: number): ChangeSummary =>
  create(ChangeSummarySchema, {
    id,
    state: ChangeState.OPEN,
    baseSha: base,
    headSha: head,
    number: BigInt(number),
  });

describe("groupIntoStacks", () => {
  it("keeps independent changes as stacks of one", () => {
    const stacks = groupIntoStacks([c("a", "T", "A", 1), c("b", "T", "B", 2)]);
    expect(stacks.map((s) => s.map((x) => x.id))).toEqual([["b"], ["a"]]);
  });

  it("chains base->head links trunk-most first regardless of input order", () => {
    const stacks = groupIntoStacks([
      c("top", "B", "C", 3),
      c("bottom", "T", "A", 1),
      c("middle", "A", "B", 2),
    ]);
    expect(stacks).toHaveLength(1);
    expect(stacks[0]!.map((x) => x.id)).toEqual(["bottom", "middle", "top"]);
  });

  it("emits one stack per leaf on a fork, sharing the prefix", () => {
    const stacks = groupIntoStacks([
      c("root", "T", "A", 1),
      c("left", "A", "L", 2),
      c("right", "A", "R", 3),
    ]);
    expect(stacks.map((s) => s.map((x) => x.id))).toEqual([
      ["root", "right"],
      ["root", "left"],
    ]);
  });

  it("orders stacks by newest change number first", () => {
    const stacks = groupIntoStacks([
      c("old", "T", "A", 1),
      c("newRoot", "T", "B", 2),
      c("newTop", "B", "C", 5),
    ]);
    expect(stacks.map((s) => s[0]!.id)).toEqual(["newRoot", "old"]);
  });
});

describe("fake transport", () => {
  const client = () => createClient(ChangeService, createFakeTransport());

  it("GetChangeStack returns the full chain from any member, trunk-most first", async () => {
    for (const [id, wantPos] of [
      [stackBottom.id, 0],
      [stackMiddle.id, 1],
      [stackTop.id, 2],
    ] as const) {
      const res = await client().getChangeStack({ changeId: id });
      expect(res.changes.map((x) => x.id)).toEqual([
        stackBottom.id,
        stackMiddle.id,
        stackTop.id,
      ]);
      expect(res.position).toBe(wantPos);
    }
  });

  it("GetChangeStack returns a stack of one for an unstacked change", async () => {
    const res = await client().getChangeStack({ changeId: soloChange.id });
    expect(res.changes.map((x) => x.id)).toEqual([soloChange.id]);
    expect(res.position).toBe(0);
  });

  it("landing is gated on the same mergeable bool merge-requirements reports", async () => {
    const cl = client();
    const before = await cl.getMergeRequirements({ changeId: stackMiddle.id });
    expect(before.requirements!.mergeable).toBe(false);
    await expect(cl.landChange({ changeId: stackMiddle.id })).rejects.toThrow(
      /not mergeable/,
    );

    const ready = await cl.getMergeRequirements({ changeId: stackBottom.id });
    expect(ready.requirements!.mergeable).toBe(true);
    const landed = await cl.landChange({ changeId: stackBottom.id });
    expect(landed.landed).toBe(true);
    expect(landed.landedSha).not.toBe("");
  });

  it("approve moves the owner gate and refreshes blockers", async () => {
    const cl = client();
    const res = await cl.approveChange({
      changeId: stackMiddle.id,
      ownerRef: "group:commerce",
      approvedBy: "user:demo",
    });
    const owners = res.requirements!.owners!;
    expect(owners.outstanding).toEqual([]);
    expect(owners.satisfied).toContain("group:commerce");
    // Still blocked: the bazel check is pending.
    expect(res.requirements!.mergeable).toBe(false);
    expect(res.requirements!.blockers.some((b) => b.includes("still running"))).toBe(true);
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
