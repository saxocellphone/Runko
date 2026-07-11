import { useState } from "react";
import { authUser, publicBrowse } from "../api/client";
import { ChangeState, type MergeRequirements } from "../gen/runko/v1/common_pb";
import { InfoTip } from "./ui";

// The §13.5 merge gates, rendered the way GET merge-requirements reports
// them: required owners (satisfied/outstanding) and required checks
// (passing/failing/pending), plus the plain-language blockers. Approve and
// rerun act through the callbacks; the caller refreshes from the response.
// For a landed/abandoned change the card is a historical record: the
// banner says so and every action control disappears - "Ready to land" on
// a change that already landed is a lie.
export function MergeGates({
  requirements,
  state,
  busy,
  onApprove,
  onRerun,
  onRequestReview,
}: {
  requirements: MergeRequirements;
  state: ChangeState;
  busy: boolean;
  onApprove: (ownerRef: string, approvedBy: string) => void;
  onRerun: (checkName: string) => void;
  onRequestReview?: (reviewer: string) => void;
}) {
  const [approveAs, setApproveAs] = useState("user:demo");
  const [reviewer, setReviewer] = useState("");
  const owners = requirements.owners;
  const checks = requirements.checks;
  const hasOwners = (owners?.required.length ?? 0) > 0;
  const hasChecks = (checks?.required.length ?? 0) > 0;
  const open = state === ChangeState.OPEN;
  // Anonymous read-only browsing (§15.2): the gate STATUS is public
  // information; the controls act through authenticated RPCs and vanish.
  const actionable = open && !publicBrowse;

  const banner =
    state === ChangeState.LANDED ? (
      <div className="mergeable-banner mergeable-landed">✓ Landed</div>
    ) : state === ChangeState.ABANDONED ? (
      <div className="mergeable-banner mergeable-off">
        Abandoned — merge gates no longer apply
      </div>
    ) : requirements.mergeable ? (
      <div className="mergeable-banner mergeable-yes">✓ Ready to land</div>
    ) : (
      <div className="mergeable-banner mergeable-no">Blocked from landing</div>
    );

  return (
    <div>
      {banner}

      {hasOwners && (
        <div className="gate-section">
          <p className="gate-title">
            Owners
            <InfoTip text="Required because this change touches paths these owners are responsible for - computed from the touched paths, not from every project the change might affect. An agent can never satisfy this on its own; a human approval is always required." />
          </p>
          {owners!.required.map((o) => {
            const satisfied = owners!.satisfied.includes(o);
            return (
              <div className="gate-row" key={o}>
                <span className={`gate-icon ${satisfied ? "ok" : "due"}`}>
                  {satisfied ? "✓" : "○"}
                </span>
                <span className="gate-name mono">{o}</span>
                {!satisfied && actionable && (
                  <button
                    className="btn btn-sm"
                    disabled={busy}
                    // A signed-in principal approves as itself: the server
                    // derives the approver from the credential and rejects
                    // a mismatched approved_by, so send none. The free-text
                    // field exists only for the anonymous deploy token.
                    onClick={() => onApprove(o, authUser ? "" : approveAs)}
                  >
                    Approve
                  </button>
                )}
              </div>
            );
          })}
          {actionable && !authUser && owners!.outstanding.length > 0 && (
            <div className="approve-as">
              <input
                type="text"
                value={approveAs}
                onChange={(e) => setApproveAs(e.target.value)}
                aria-label="approve as"
                title="recorded as the approver (client-asserted until real AuthN)"
              />
            </div>
          )}
        </div>
      )}

      {hasChecks && (
        <div className="gate-section">
          <p className="gate-title">
            Checks
            <InfoTip text="Required checks, bound to this change's exact head commit. Rebasing onto a new base makes them stale and they must re-run - unless the trunk delta doesn't touch anything this change affects, in which case landing skips straight through (optimistic revalidation, §13.5)." />
          </p>
          {checks!.required.map((name) => {
            const failing = checks!.failing.includes(name);
            const pending = checks!.pending.includes(name);
            const passing = checks!.passing.includes(name);
            // The reporter's details_url deep-links to the CI run page
            // (runko-ci report-check --details-url) - a check that has
            // reported becomes clickable, one that hasn't stays plain.
            const url = checks!.detailsUrls[name];
            return (
              <div className="gate-row" key={name}>
                <span
                  className={`gate-icon ${passing ? "ok" : failing ? "bad" : pending ? "wait" : "due"}`}
                >
                  {passing ? "✓" : failing ? "✕" : pending ? "●" : "○"}
                </span>
                {url ? (
                  <a
                    className="gate-name mono gate-link"
                    href={url}
                    target="_blank"
                    rel="noreferrer"
                    title={`${name} — open the CI run`}
                  >
                    {name} ↗
                  </a>
                ) : (
                  <span className="gate-name mono" title={name}>
                    {name}
                  </span>
                )}
                {failing && actionable && (
                  <button className="btn btn-sm" disabled={busy} onClick={() => onRerun(name)}>
                    Rerun
                  </button>
                )}
              </div>
            );
          })}
        </div>
      )}

      {!hasOwners && !hasChecks && (
        <p className="gate-title">No policy resolved for this change.</p>
      )}

      {open && (requirements.attentionSet.length > 0 || (actionable && onRequestReview)) && (
        <div className="gate-section">
          <p className="gate-title">
            Attention
            <InfoTip text="Whose turn it is (§13.4.2), derived - never hand-managed: requested reviewers and required owners who haven't approved or commented on the current version, plus the author once a reviewer has responded. An amend re-derives the whole set." />
          </p>
          {requirements.attentionSet.map((name) => (
            <div className="gate-row" key={name}>
              <span className="gate-icon due">●</span>
              <span className={`gate-name mono${name === authUser || name === `user:${authUser}` ? " attention-you" : ""}`}>
                {name}
                {(name === authUser || name === `user:${authUser}`) && " (you)"}
              </span>
            </div>
          ))}
          {actionable && onRequestReview && (
            <div className="request-review-row">
              <input
                type="text"
                value={reviewer}
                placeholder="principal or group:name"
                aria-label="request review from"
                onChange={(e) => setReviewer(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && reviewer.trim()) {
                    onRequestReview(reviewer.trim());
                    setReviewer("");
                  }
                }}
              />
              <button
                className="btn btn-sm"
                disabled={busy || !reviewer.trim()}
                onClick={() => {
                  onRequestReview(reviewer.trim());
                  setReviewer("");
                }}
              >
                Request review
              </button>
            </div>
          )}
        </div>
      )}

      {open && requirements.blockers.length > 0 && (
        <div className="gate-section">
          <p className="gate-title">Blockers</p>
          {requirements.blockers.map((b) => (
            <div className="gate-row" key={b}>
              <span className="gate-icon wait">!</span>
              <span>{b}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
