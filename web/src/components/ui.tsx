import type { ConnectError } from "@connectrpc/connect";
import { ChangeState, type Actor, type MergeRequirements } from "../gen/runko/v1/common_pb";
import { actorLabel, changeStateLabel, isAgent } from "../lib/format";
import { checksRollup, dotStatus, reviewRollup } from "../lib/status";

export function Spinner() {
  return <div className="spinner" aria-label="loading" />;
}

export function ErrorNote({ error }: { error: ConnectError }) {
  return (
    <div className="error-note" role="alert">
      {error.rawMessage || error.message}
    </div>
  );
}

export function EmptyState({ children }: { children: React.ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function StatusDot({ requirements }: { requirements: MergeRequirements | undefined }) {
  const status = dotStatus(requirements);
  const label = {
    ready: "ready to land",
    failing: "checks failing",
    pending: "checks running",
    review: "waiting on review",
    unknown: "status unknown",
  }[status];
  return <span className={`dot dot-${status}`} title={label} />;
}

export function StateBadge({ state }: { state: ChangeState }) {
  const cls =
    state === ChangeState.OPEN
      ? "badge-open"
      : state === ChangeState.LANDED
        ? "badge-landed"
        : "badge-abandoned";
  return <span className={`badge-state ${cls}`}>{changeStateLabel(state)}</span>;
}

export function AuthorChip({ author }: { author: Actor | undefined }) {
  if (isAgent(author)) {
    return (
      <span className="agent-badge" title="authored by a coding agent">
        <AgentIcon />
        {actorLabel(author)}
      </span>
    );
  }
  return <span>{actorLabel(author)}</span>;
}

export function ChecksChip({ requirements }: { requirements: MergeRequirements | undefined }) {
  const rollup = checksRollup(requirements);
  if (rollup === "none") return null;
  const checks = requirements!.checks!;
  if (rollup === "failing") {
    return <span className="chip chip-red">✕ {checks.failing.length} failing</span>;
  }
  if (rollup === "pending") {
    return (
      <span className="chip chip-amber">
        ● {checks.required.length - checks.pending.length}/{checks.required.length} checks
      </span>
    );
  }
  return <span className="chip chip-green">✓ checks</span>;
}

export function ReviewChip({ requirements }: { requirements: MergeRequirements | undefined }) {
  const rollup = reviewRollup(requirements);
  if (rollup === "none") return null;
  if (rollup === "approved") return <span className="chip chip-green">✓ approved</span>;
  return <span className="chip chip-violet">review</span>;
}

export function MergeableChip({ requirements }: { requirements: MergeRequirements | undefined }) {
  if (!requirements?.mergeable) return null;
  return <span className="chip chip-green">mergeable</span>;
}

export function AgentIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" fill="currentColor" aria-hidden>
      <rect x="3" y="5" width="10" height="8" rx="2" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <circle cx="6.2" cy="8.6" r="1" />
      <circle cx="9.8" cy="8.6" r="1" />
      <line x1="8" y1="2.5" x2="8" y2="5" stroke="currentColor" strokeWidth="1.5" />
      <circle cx="8" cy="2.2" r="1.2" />
    </svg>
  );
}

export function TrunkIcon() {
  return (
    <svg
      className="trunk-icon"
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      aria-hidden
    >
      <circle cx="8" cy="8" r="3" />
      <line x1="8" y1="1" x2="8" y2="5" />
      <line x1="8" y1="11" x2="8" y2="15" />
    </svg>
  );
}
