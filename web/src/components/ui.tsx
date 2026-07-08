import type { ConnectError } from "@connectrpc/connect";
import { Link } from "react-router-dom";
import { ChangeState, type Actor, type MergeRequirements } from "../gen/runko/v1/common_pb";
import { actorLabel, changeStateLabel, isAgent } from "../lib/format";
import { checksRollup, dotStatus, reviewRollup } from "../lib/status";

// Detail pages render this above their header so there's always a visible
// way back to the list they came from (browser-back also works; this is
// the discoverable affordance).
export function BackLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <Link className="back-link" to={to}>
      <svg
        width="13"
        height="13"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden
      >
        <line x1="13" y1="8" x2="3" y2="8" />
        <polyline points="7.5,3.5 3,8 7.5,12.5" />
      </svg>
      {children}
    </Link>
  );
}

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

export function StatusDot({
  requirements,
  state = ChangeState.OPEN,
}: {
  requirements: MergeRequirements | undefined;
  state?: ChangeState;
}) {
  // A closed change's dot reflects its state, not a stale gate readout.
  if (state === ChangeState.LANDED) return <span className="dot dot-landed" title="landed" />;
  if (state === ChangeState.ABANDONED) return <span className="dot" title="abandoned" />;
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

// OriginChip names the workspace branch a Change (and so its stack) was
// pushed from - §12.2's branch ↔ stack mapping made visible. Renders
// nothing for Changes with no provenance (plain-git pushers, the web
// create-project flow, bot lanes).
export function OriginChip({
  workspace,
  branch,
  branchOnly,
}: {
  workspace: string;
  branch: string;
  branchOnly?: boolean;
}) {
  if (!workspace) return null;
  return (
    <Link
      className="chip chip-origin mono"
      to="/workspaces"
      title={`pushed from workspace ${workspace}, branch ${branch} - one workspace branch carries one stack (§12.2)`}
    >
      <WorkspaceGlyph />
      {branchOnly ? branch : `${workspace} › ${branch}`}
    </Link>
  );
}

function WorkspaceGlyph() {
  return (
    <svg
      width="11"
      height="11"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      aria-hidden
    >
      <circle cx="4.5" cy="4.5" r="2.2" />
      <circle cx="11.5" cy="11.5" r="2.2" />
      <path d="M4.5 6.7v2.1a2.7 2.7 0 0 0 2.7 2.7h2.1" />
    </svg>
  );
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

// A small "?" glyph that reveals a definition on hover/focus - for Runko
// jargon (capability, inferred deps, ...) that a first-time reader of the
// UI has no way to already know. Native `title` attributes already cover
// "here is the full value" cases (a truncated sha, a check name); this is
// for "here is what this word means" cases, which want to stay visible
// long enough to actually read.
//
// Deliberately a <button>, not a <span tabIndex>: :focus-visible's
// click-vs-keyboard heuristic is only spec-consistent across browsers for
// real interactive elements. On a span with tabIndex, some browsers (not
// Chromium, which is why this slipped through here) treat a plain mouse
// click as keyboard-style focus and pop the tooltip open with nothing to
// close it - a real click never triggers :focus-visible on a <button>.
export function InfoTip({ text }: { text: string }) {
  return (
    <button type="button" className="info-tip" aria-label={text}>
      <span className="info-tip-glyph" aria-hidden>
        ?
      </span>
      <span className="info-tip-bubble" role="tooltip">
        {text}
      </span>
    </button>
  );
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
