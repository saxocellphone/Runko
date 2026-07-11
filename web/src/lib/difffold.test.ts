import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { FileDiffSchema, type FileDiff } from "../gen/runko/v1/changes_pb";
import { initialFolds, isLargeDiff, LARGE_FILE_LINES, MANY_FILES, threadPathSet } from "./difffold";

function file(path: string, lines: number): FileDiff {
  return create(FileDiffSchema, {
    path,
    hunks: [{ lines: Array.from({ length: lines }, () => ({ content: "x" })) }],
  });
}

describe("initialFolds", () => {
  it("collapses only oversized files in a small change", () => {
    const folds = initialFolds([file("small.go", 10), file("big.go", LARGE_FILE_LINES + 1)], new Set());
    expect(folds["small.go"]).toBe(false);
    expect(folds["big.go"]).toBe(true);
  });

  it("collapses everything when the change touches many files", () => {
    const files = Array.from({ length: MANY_FILES + 1 }, (_, i) => file(`f${i}.go`, 5));
    const folds = initialFolds(files, new Set());
    expect(Object.values(folds).every(Boolean)).toBe(true);
  });

  it("never auto-collapses a file carrying review threads (§13.4.1)", () => {
    const files = [file("big.go", LARGE_FILE_LINES + 1)];
    const folds = initialFolds(files, new Set(["big.go"]));
    expect(folds["big.go"]).toBe(false);
  });
});

describe("threadPathSet", () => {
  it("extracts paths from byFile keys and lineKey-shaped byLine keys", () => {
    const s = threadPathSet(
      new Map([["a/b|c.go|head|42", 1]]), // path itself may contain | -only the last two separators are structural
      new Map([["plain.go", 1]]),
    );
    expect(s.has("plain.go")).toBe(true);
    expect(s.has("a/b|c.go")).toBe(true);
  });
});

describe("isLargeDiff", () => {
  it("counts rendered hunk lines, context included", () => {
    expect(isLargeDiff(file("x", LARGE_FILE_LINES))).toBe(false);
    expect(isLargeDiff(file("x", LARGE_FILE_LINES + 1))).toBe(true);
  });
});
