// Thread grouping for the review conversation (§13.4.1, stage 16b).
// Comments arrive flat and oldest-first from ListComments; the UI renders
// them as one-level threads anchored into the diff. All pure functions -
// vitest-pinned in comments.test.ts.
import { CommentSide, type Comment } from "../gen/runko/v1/common_pb";

export interface Thread {
  root: Comment;
  replies: Comment[];
}

/** Group a flat comment list into threads: roots in arrival order, each
 * with its replies in arrival order. A reply whose root is missing (can't
 * happen server-side; defensive) renders as its own root rather than
 * vanishing. */
export function groupThreads(comments: Comment[]): Thread[] {
  const byId = new Map<string, Thread>();
  const out: Thread[] = [];
  for (const c of comments) {
    if (!c.parentId) {
      const t = { root: c, replies: [] };
      byId.set(c.id, t);
      out.push(t);
    }
  }
  for (const c of comments) {
    if (!c.parentId) continue;
    const t = byId.get(c.parentId);
    if (t) t.replies.push(c);
    else out.push({ root: c, replies: [] });
  }
  return out;
}

/** §13.4.1: a comment binds to the head it was written against; a
 * differing (or missing) head means outdated - marked, never floated. */
export function threadOutdated(t: Thread, currentHeadSha: string): boolean {
  return t.root.headSha === "" || t.root.headSha !== currentHeadSha;
}

/** Stable key for a line-level anchor, matching DiffView's row identity. */
export function lineKey(path: string, side: CommentSide, line: number): string {
  return `${path}|${side === CommentSide.BASE ? "base" : "head"}|${line}`;
}

export interface PartitionedThreads {
  /** Current-head line-level threads, keyed by lineKey - rendered inline
   * under their diff row. */
  byLine: Map<string, Thread[]>;
  /** Current-head file-level threads (path, no line) - rendered at the top
   * of the file's diff card. */
  byFile: Map<string, Thread[]>;
  /** Current-head change-level threads - the conversation card. */
  conversation: Thread[];
  /** Threads written against an older head, whatever their anchor -
   * grouped separately and marked, never repositioned (§13.4.1). */
  outdated: Thread[];
}

export function partitionThreads(threads: Thread[], currentHeadSha: string): PartitionedThreads {
  const out: PartitionedThreads = {
    byLine: new Map(),
    byFile: new Map(),
    conversation: [],
    outdated: [],
  };
  for (const t of threads) {
    if (threadOutdated(t, currentHeadSha)) {
      out.outdated.push(t);
      continue;
    }
    const { path, line, side } = t.root;
    if (path && line > 0) {
      const key = lineKey(path, side, line);
      const list = out.byLine.get(key);
      if (list) list.push(t);
      else out.byLine.set(key, [t]);
    } else if (path) {
      const list = out.byFile.get(path);
      if (list) list.push(t);
      else out.byFile.set(path, [t]);
    } else {
      out.conversation.push(t);
    }
  }
  return out;
}

/** Whose turn: true when the signed-in principal appears in the derived
 * attention set, either by plain name or as a user: owner ref (§13.4.2).
 * Group refs never match - membership isn't resolvable client-side. */
export function inAttention(attentionSet: string[], user: string | null): boolean {
  if (!user) return false;
  return attentionSet.includes(user) || attentionSet.includes(`user:${user}`);
}
