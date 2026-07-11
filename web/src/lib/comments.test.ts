import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { CommentSchema, CommentSide, type Comment } from "../gen/runko/v1/common_pb";
import { groupThreads, inAttention, lineKey, partitionThreads, threadOutdated } from "./comments";

const HEAD = "head-v2";

function comment(over: {
  id?: string;
  parentId?: string;
  path?: string;
  side?: CommentSide;
  line?: number;
  headSha?: string;
}): Comment {
  return create(CommentSchema, { headSha: HEAD, ...over });
}

describe("groupThreads", () => {
  it("keeps roots in arrival order with replies attached in arrival order", () => {
    const threads = groupThreads([
      comment({ id: "a" }),
      comment({ id: "b" }),
      comment({ id: "a1", parentId: "a" }),
      comment({ id: "a2", parentId: "a" }),
    ]);
    expect(threads.map((t) => t.root.id)).toEqual(["a", "b"]);
    expect(threads[0].replies.map((r) => r.id)).toEqual(["a1", "a2"]);
    expect(threads[1].replies).toEqual([]);
  });

  it("renders an orphaned reply as its own root rather than dropping it", () => {
    const threads = groupThreads([comment({ id: "x", parentId: "gone" })]);
    expect(threads).toHaveLength(1);
    expect(threads[0].root.id).toBe("x");
  });
});

describe("partitionThreads", () => {
  it("splits current-head threads by anchor and quarantines outdated ones", () => {
    const threads = groupThreads([
      comment({ id: "line", path: "a/b.go", side: CommentSide.HEAD, line: 3 }),
      comment({ id: "file", path: "a/b.go" }),
      comment({ id: "chg" }),
      comment({ id: "old", path: "a/b.go", side: CommentSide.HEAD, line: 3, headSha: "head-v1" }),
    ]);
    const p = partitionThreads(threads, HEAD);
    expect(p.byLine.get(lineKey("a/b.go", CommentSide.HEAD, 3))?.map((t) => t.root.id)).toEqual([
      "line",
    ]);
    expect(p.byFile.get("a/b.go")?.map((t) => t.root.id)).toEqual(["file"]);
    expect(p.conversation.map((t) => t.root.id)).toEqual(["chg"]);
    // §13.4.1: outdated is marked, never floated back onto the diff.
    expect(p.outdated.map((t) => t.root.id)).toEqual(["old"]);
  });

  it("treats a missing head_sha as outdated (fail closed, the 0011 NULL rule)", () => {
    const [t] = groupThreads([comment({ id: "pre", headSha: "" })]);
    expect(threadOutdated(t, HEAD)).toBe(true);
  });
});

describe("inAttention", () => {
  it("matches plain principal names and user: owner refs, never groups", () => {
    expect(inAttention(["alice", "group:eng"], "alice")).toBe(true);
    expect(inAttention(["user:bob"], "bob")).toBe(true);
    expect(inAttention(["group:eng"], "eng")).toBe(false);
    expect(inAttention(["alice"], null)).toBe(false);
  });
});
