import type { ChangeSummary } from "../gen/runko/v1/common_pb";

// Client-side mirror of GetChangeStack's derived relation
// (proto/runko/v1/changes.proto): Change B is stacked on Change A iff
// B.baseSha == A.headSha. Used by the changes list, which already holds
// every open ChangeSummary and shouldn't issue one stack RPC per change.
//
// Each returned stack is ordered trunk-most first. A fork (two changes
// based on the same head) yields one stack per leaf, sharing the prefix -
// linear rendering per path, the same simplification Graphite's list
// makes.
export function groupIntoStacks(changes: ChangeSummary[]): ChangeSummary[][] {
  const byHead = new Map<string, ChangeSummary>();
  for (const c of changes) byHead.set(c.headSha, c);

  const childrenOf = new Map<string, ChangeSummary[]>();
  const roots: ChangeSummary[] = [];
  for (const c of changes) {
    const parent = byHead.get(c.baseSha);
    if (parent && parent.id !== c.id) {
      const kids = childrenOf.get(parent.id);
      if (kids) kids.push(c);
      else childrenOf.set(parent.id, [c]);
    } else {
      roots.push(c);
    }
  }

  const stacks: ChangeSummary[][] = [];
  const walk = (prefix: ChangeSummary[], c: ChangeSummary) => {
    const chain = [...prefix, c];
    const kids = [...(childrenOf.get(c.id) ?? [])].sort(byNumberAsc);
    if (kids.length === 0) {
      stacks.push(chain);
      return;
    }
    for (const k of kids) walk(chain, k);
  };
  for (const r of roots) walk([], r);

  // Newest activity first: order stacks by their highest change number.
  stacks.sort((a, b) => Number(maxNumber(b) - maxNumber(a)));
  return stacks;
}

function byNumberAsc(a: ChangeSummary, b: ChangeSummary): number {
  return Number(a.number - b.number);
}

function maxNumber(stack: ChangeSummary[]): bigint {
  let max = 0n;
  for (const c of stack) if (c.number > max) max = c.number;
  return max;
}
