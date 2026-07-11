// Diff folding policy (stage 15 dogfood: "for a large change, the diff
// overwhelms the screen"): which files start collapsed, the GitHub-shaped
// rules. Pure - vitest-pinned in difffold.test.ts; DiffView owns the state.
import type { FileDiff } from "../gen/runko/v1/changes_pb";

/** A single file whose rendered diff exceeds this many lines starts
 * collapsed (GitHub's "large diffs are not rendered by default"). */
export const LARGE_FILE_LINES = 400;

/** A change touching more than this many files starts fully collapsed -
 * per-file size rules can't save a 50-file change from wall-of-diff. */
export const MANY_FILES = 20;

/** Rendered line count: what actually hits the DOM (hunk lines, all
 * types), not additions+deletions - context lines overwhelm screens too. */
export function diffLineCount(f: FileDiff): number {
  return f.hunks.reduce((n, h) => n + h.lines.length, 0);
}

/** Whether one file's diff counts as large (drives the chip + the fold). */
export function isLargeDiff(f: FileDiff): boolean {
  return diffLineCount(f) > LARGE_FILE_LINES;
}

/** The initial fold state per path. Files carrying review threads at the
 * current head NEVER start collapsed, whatever their size - hiding a
 * reviewer's comment behind a fold is worse than a long page (§13.4.1's
 * conversation outranks screen economy). Manual collapse still works. */
export function initialFolds(files: FileDiff[], threadPaths: ReadonlySet<string>): Record<string, boolean> {
  const manyFiles = files.length > MANY_FILES;
  const folds: Record<string, boolean> = {};
  for (const f of files) {
    folds[f.path] = !threadPaths.has(f.path) && (manyFiles || isLargeDiff(f));
  }
  return folds;
}

/** The paths review threads anchor to - byFile keys plus the path segment
 * of byLine keys (lineKey's `${path}|${side}|${line}` shape). */
export function threadPathSet(
  byLine: ReadonlyMap<string, unknown>,
  byFile: ReadonlyMap<string, unknown>,
): Set<string> {
  const s = new Set<string>();
  for (const path of byFile.keys()) s.add(path);
  for (const key of byLine.keys()) {
    const cut = key.lastIndexOf("|", key.lastIndexOf("|") - 1);
    if (cut > 0) s.add(key.slice(0, cut));
  }
  return s;
}
