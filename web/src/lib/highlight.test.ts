import { describe, expect, it } from "vitest";
import { highlightLines, languageFor } from "./highlight";

describe("languageFor", () => {
  it("maps extensions and well-known basenames, refuses the unknown", () => {
    expect(languageFor("platform/affected/compute.go")).toBe("go");
    expect(languageFor("web/src/App.tsx")).toBe("typescript");
    expect(languageFor("PROJECT.yaml")).toBe("yaml");
    expect(languageFor("proto/runko/v1/common.proto")).toBe("protobuf");
    expect(languageFor("MODULE.bazel")).toBe("python");
    expect(languageFor("platform/index/BUILD.bazel")).toBe("python");
    expect(languageFor("Dockerfile")).toBe("dockerfile");
    expect(languageFor("Makefile")).toBe("makefile");
    expect(languageFor("LICENSE")).toBeUndefined();
    expect(languageFor(".gitignore")).toBeUndefined(); // dotfile, not extension
    expect(languageFor("weird.xyz")).toBeUndefined();
  });
});

describe("highlightLines", () => {
  it("returns exactly one token array per line, reassembling to the source", () => {
    const src = 'package main\n\nfunc main() {\n\tprintln("hi")\n}\n';
    const lines = highlightLines(src, "main.go")!;
    expect(lines).not.toBeNull();
    expect(lines.length).toBe(src.split("\n").length);
    const rebuilt = lines.map((ts) => ts.map((t) => t.text).join("")).join("\n");
    expect(rebuilt).toBe(src);
  });

  it("keeps a token's class on every line a multiline token spans", () => {
    const src = "/* first\nsecond */\nvar x = 1\n";
    const lines = highlightLines(src, "a.go")!;
    // Both comment lines carry comment-classed tokens.
    for (const i of [0, 1]) {
      expect(lines[i]!.some((t) => t.cls?.includes("hljs-comment"))).toBe(true);
    }
    // The code line does not.
    expect(lines[2]!.some((t) => t.cls?.includes("hljs-comment"))).toBe(false);
  });

  it("actually classifies tokens (keyword + string in Go)", () => {
    const lines = highlightLines('func f() string { return "s" }\n', "f.go")!;
    const classes = lines[0]!.map((t) => t.cls ?? "").join(" ");
    expect(classes).toContain("hljs-keyword");
    expect(classes).toContain("hljs-string");
  });

  it("returns null for unmapped paths and oversized content", () => {
    expect(highlightLines("plain text\n", "notes.txt")).toBeNull();
    expect(highlightLines("x".repeat(600 * 1024), "big.go")).toBeNull();
  });
});
