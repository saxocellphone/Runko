// Tooltips, titles, and on-page prose are product copy: a reader hovering
// "Whose turn it is" cannot resolve "§13.4.2", and design.md is a retired
// historical document. Comments keep their citations - that is where the
// reference still means something - so this scans only what can reach the
// screen. Sibling guards cover the CLI help and the MCP tool catalog.

import { describe, expect, it } from "vitest";

// Every source file under src/, read raw. Generated code (src/gen) and the
// tests themselves are excluded by the patterns.
const sources = import.meta.glob("../**/*.{ts,tsx}", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

/** A line is a comment when it opens with //, *, /* or {/*. */
function isComment(line: string): boolean {
  const t = line.trimStart();
  return t.startsWith("//") || t.startsWith("*") || t.startsWith("/*") || t.startsWith("{/*");
}

/** Drop a trailing // comment, which is prose about the code, not copy.
 *  The leading-space requirement keeps https:// inside strings intact. */
function code(line: string): string {
  return line.replace(/\s\/\/.*$/, "");
}

describe("user-visible copy", () => {
  it("cites no spec sections or design.md", () => {
    const offenders: string[] = [];
    for (const [path, text] of Object.entries(sources)) {
      if (path.startsWith("../gen/") || /\.test\.tsx?$/.test(path)) continue;
      text.split("\n").forEach((line, i) => {
        if (isComment(line)) return;
        const source = code(line);
        if (source.includes("§") || source.includes("design.md")) {
          offenders.push(`${path}:${i + 1}: ${line.trim()}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });
});
