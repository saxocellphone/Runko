import { ActorType, ChangeState, ProjectType, type Actor } from "../gen/runko/v1/common_pb";

export function shortSha(sha: string): string {
  return sha.slice(0, 8);
}

/** Compact relative time for history/blame rows ("3h ago", "2y ago"). */
export function timeAgo(unixSeconds: number | bigint): string {
  const s = Math.max(0, Math.floor(Date.now() / 1000) - Number(unixSeconds));
  if (s < 60) return "just now";
  const units: [number, string][] = [
    [60, "m"],
    [60, "h"],
    [24, "d"],
    [30, "mo"],
    [12, "y"],
  ];
  let v = s / 60;
  let label = "m";
  for (let i = 1; i < units.length; i++) {
    if (v < units[i]![0]) break;
    v /= units[i]![0];
    label = units[i]![1];
  }
  return `${Math.floor(v)}${label} ago`;
}

export function shortChangeId(id: string): string {
  // Change-Ids are Gerrit-style I<40 hex> (§7.4); show the I + 8 hex.
  return id.startsWith("I") ? id.slice(0, 9) : id.slice(0, 8);
}

export function changeStateLabel(state: ChangeState): string {
  switch (state) {
    case ChangeState.OPEN:
      return "Open";
    case ChangeState.LANDED:
      return "Landed";
    case ChangeState.ABANDONED:
      return "Abandoned";
    default:
      return "Unknown";
  }
}

export function projectTypeLabel(type: ProjectType): string {
  switch (type) {
    case ProjectType.LIBRARY:
      return "library";
    case ProjectType.SERVICE:
      return "service";
    case ProjectType.APP:
      return "app";
    case ProjectType.JOB:
      return "job";
    case ProjectType.OTHER:
      return "other";
    default:
      return "unspecified";
  }
}

export function actorLabel(actor: Actor | undefined): string {
  if (!actor || !actor.id) return "anonymous";
  return actor.displayName || actor.id;
}

export function isAgent(actor: Actor | undefined): boolean {
  return actor?.type === ActorType.AGENT;
}

export function changeNumberLabel(n: bigint): string {
  return n > 0n ? `#${n}` : "";
}
