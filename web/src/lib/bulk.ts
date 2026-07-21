import { ConnectError } from "@connectrpc/connect";

// Bulk actions over a list page's selection. Every mutation these drive
// is per-item server-enforced - workspace deletion refuses while a
// workspace still has open changes and is owner-only (§12.2) - so a bulk
// run is EXPECTED to partially fail. The contract is "attempt all, report
// each", never "stop at the first refusal", and never a client-side
// pre-judgement of which items the server would have taken.

export interface BulkFailure {
  id: string;
  message: string;
}

export interface BulkResult {
  done: string[];
  failed: BulkFailure[];
}

// runBulk applies op to every id IN ORDER, continuing past refusals, and
// partitions the outcome. Sequential on purpose: these are mutations
// against one registry, and a bounded, predictable sequence beats a
// fan-out that races the server for no user-visible gain.
export async function runBulk(
  ids: readonly string[],
  op: (id: string) => Promise<unknown>,
): Promise<BulkResult> {
  const done: string[] = [];
  const failed: BulkFailure[] = [];
  for (const id of ids) {
    try {
      await op(id);
      done.push(id);
    } catch (err) {
      // The server's own §6.5 message, per item - that's what tells the
      // user WHICH workspace refused and what to do about it.
      failed.push({ id, message: ConnectError.from(err).rawMessage });
    }
  }
  return { done, failed };
}

// Tri-state for a select-all box; "some" is the indeterminate dash.
export type SelectAllState = "none" | "some" | "all";

export function selectAllState(
  selected: ReadonlySet<string>,
  present: readonly string[],
): SelectAllState {
  const picked = visibleSelection(selected, present).length;
  if (picked === 0) return "none";
  return picked === present.length ? "all" : "some";
}

// A selection is only meaningful against the rows currently on screen: a
// reload drops deleted rows, so intersect rather than carry ids that no
// longer exist into a confirm count or a delete run. Returns them in the
// list's own order, which is the order the bulk run then applies.
export function visibleSelection(
  selected: ReadonlySet<string>,
  present: readonly string[],
): string[] {
  return present.filter((id) => selected.has(id));
}

// toggled returns a new selection with id flipped - selection state is
// held immutably so React re-renders on a pick.
export function toggled(selected: ReadonlySet<string>, id: string): Set<string> {
  const next = new Set(selected);
  if (!next.delete(id)) next.add(id);
  return next;
}
