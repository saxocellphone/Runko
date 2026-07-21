import { describe, expect, it } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { runBulk, selectAllState, toggled, visibleSelection } from "./bulk";

describe("runBulk", () => {
  it("attempts every id past a refusal and partitions the outcome", async () => {
    const seen: string[] = [];
    const result = await runBulk(["a", "b", "c"], async (id) => {
      seen.push(id);
      if (id === "b") {
        throw new ConnectError("workspace_has_open_changes: b still has open changes", Code.FailedPrecondition);
      }
    });

    // The refusal must not short-circuit "c" - that's the whole point.
    expect(seen).toEqual(["a", "b", "c"]);
    expect(result.done).toEqual(["a", "c"]);
    expect(result.failed).toEqual([
      { id: "b", message: "workspace_has_open_changes: b still has open changes" },
    ]);
  });

  it("runs strictly in order, one at a time", async () => {
    const log: string[] = [];
    await runBulk(["a", "b"], async (id) => {
      log.push(`start ${id}`);
      await Promise.resolve();
      log.push(`end ${id}`);
    });
    expect(log).toEqual(["start a", "end a", "start b", "end b"]);
  });

  it("reports non-Connect throws rather than rejecting the run", async () => {
    const result = await runBulk(["a"], () => Promise.reject(new Error("network down")));
    expect(result.done).toEqual([]);
    expect(result.failed[0].id).toBe("a");
    expect(result.failed[0].message).toContain("network down");
  });

  it("is a no-op on an empty selection", async () => {
    await expect(runBulk([], () => Promise.reject(new Error("unreachable")))).resolves.toEqual({
      done: [],
      failed: [],
    });
  });
});

describe("selection helpers", () => {
  it("derives the select-all tri-state", () => {
    const present = ["a", "b"];
    expect(selectAllState(new Set(), present)).toBe("none");
    expect(selectAllState(new Set(["a"]), present)).toBe("some");
    expect(selectAllState(new Set(["a", "b"]), present)).toBe("all");
  });

  it("ignores selected ids that are no longer on screen", () => {
    // A deleted row's id lingering in state must not read as "all
    // selected", nor inflate the confirm count.
    const present = ["a", "b"];
    expect(visibleSelection(new Set(["a", "gone"]), present)).toEqual(["a"]);
    expect(selectAllState(new Set(["a", "gone"]), present)).toBe("some");
    expect(selectAllState(new Set(["a", "b", "gone"]), present)).toBe("all");
  });

  it("returns the selection in list order, which is delete order", () => {
    expect(visibleSelection(new Set(["c", "a"]), ["a", "b", "c"])).toEqual(["a", "c"]);
  });

  it("treats an empty list as nothing selected", () => {
    expect(selectAllState(new Set(["a"]), [])).toBe("none");
  });

  it("toggles immutably", () => {
    const first = new Set(["a"]);
    const second = toggled(first, "b");
    expect([...second]).toEqual(["a", "b"]);
    expect([...first]).toEqual(["a"]); // untouched
    expect([...toggled(second, "a")]).toEqual(["b"]);
  });
});
