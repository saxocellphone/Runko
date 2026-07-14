import { describe, expect, it } from "vitest";
import { ACTIVITY_KINDS, countByKind, kindMeta, normalizeKind, parseStoredKinds } from "./activity";

describe("activity kinds (§12.6.1)", () => {
  it("has a glyph and label for every kind", () => {
    for (const k of ACTIVITY_KINDS) {
      expect(kindMeta[k].glyph).not.toBe("");
      expect(kindMeta[k].label).toBe(k);
    }
  });

  it("normalizes unknown kinds to note, mirroring server ingest", () => {
    expect(normalizeKind("edit")).toBe("edit");
    expect(normalizeKind("tool_use")).toBe("note");
    expect(normalizeKind("")).toBe("note");
  });

  it("counts per kind with unknown folded into note", () => {
    const counts = countByKind([
      { kind: "read" },
      { kind: "read" },
      { kind: "command" },
      { kind: "something-new" },
    ]);
    expect(counts).toEqual({ read: 2, edit: 0, command: 1, search: 0, note: 1 });
  });

  it("falls back to all-visible on a first visit or garbage storage", () => {
    expect(parseStoredKinds(null)).toEqual([...ACTIVITY_KINDS]);
    expect(parseStoredKinds("{not json")).toEqual([...ACTIVITY_KINDS]);
    expect(parseStoredKinds('"read"')).toEqual([...ACTIVITY_KINDS]);
    expect(parseStoredKinds('{"read": true}')).toEqual([...ACTIVITY_KINDS]);
  });

  it("respects a stored valid selection, including empty, dropping strays", () => {
    expect(parseStoredKinds('["read","note"]')).toEqual(["read", "note"]);
    expect(parseStoredKinds("[]")).toEqual([]);
    expect(parseStoredKinds('["bogus","edit"]')).toEqual(["edit"]);
  });
});
