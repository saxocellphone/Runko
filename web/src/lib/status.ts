import type { MergeRequirements } from "../gen/runko/v1/common_pb";

// Roll-ups of the two §13.5 gates for compact display (list rows, stack
// rail dots). The MergeRequirements shape itself stays the source of
// truth; these never feed back into any mergeability decision.

export type ChecksRollup = "passing" | "failing" | "pending" | "none";

export function checksRollup(r: MergeRequirements | undefined): ChecksRollup {
  const g = r?.checks;
  if (!g || g.required.length === 0) return "none";
  if (g.failing.length > 0) return "failing";
  if (g.pending.length > 0) return "pending";
  return "passing";
}

export type ReviewRollup = "approved" | "outstanding" | "none";

export function reviewRollup(r: MergeRequirements | undefined): ReviewRollup {
  const g = r?.owners;
  if (!g || g.required.length === 0) return "none";
  return g.outstanding.length === 0 ? "approved" : "outstanding";
}

// One dot color per change, Graphite-style: red beats yellow beats
// "waiting on review" beats green.
export type DotStatus = "failing" | "pending" | "review" | "ready" | "unknown";

export function dotStatus(r: MergeRequirements | undefined): DotStatus {
  if (!r) return "unknown";
  if (r.mergeable) return "ready";
  const checks = checksRollup(r);
  if (checks === "failing") return "failing";
  if (checks === "pending") return "pending";
  if (reviewRollup(r) === "outstanding") return "review";
  return "pending";
}
