import { useState } from "react";
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
}: {
  requirements: MergeRequirements;
  state: ChangeState;
  busy: boolean;
  onApprove: (ownerRef: string, approvedBy: string) => void;
  onRerun: (checkName: string) => void;
}) {
  const [approveAs, setApproveAs] = useState("user:demo");
  const owners = requirements.owners;
  const checks = requirements.checks;
  const hasOwners = (owners?.required.length ?? 0) > 0;
  const hasChecks = (checks?.required.length ?? 0) > 0;
  const open = state === ChangeState.OPEN;

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
                {!satisfied && open && (
                  <button
                    className="btn btn-sm"
                    disabled={busy}
                    onClick={() => onApprove(o, approveAs)}
                  >
                    Approve
                  </button>
                )}
              </div>
            );
          })}
          {open && owners!.outstanding.length > 0 && (
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
            return (
              <div className="gate-row" key={name}>
                <span
                  className={`gate-icon ${passing ? "ok" : failing ? "bad" : pending ? "wait" : "due"}`}
                >
                  {passing ? "✓" : failing ? "✕" : pending ? "●" : "○"}
                </span>
                <span className="gate-name mono" title={name}>
                  {name}
                </span>
                {failing && open && (
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
