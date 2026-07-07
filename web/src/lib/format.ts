import { ActorType, ChangeState, ProjectType, type Actor } from "../gen/runko/v1/common_pb";

export function shortSha(sha: string): string {
  return sha.slice(0, 8);
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
