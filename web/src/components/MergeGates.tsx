import { useState } from "react";
import type { MergeRequirements } from "../gen/runko/v1/common_pb";

// The §13.5 merge gates, rendered the way GET merge-requirements reports
// them: required owners (satisfied/outstanding) and required checks
// (passing/failing/pending), plus the plain-language blockers. Approve and
// rerun act through the callbacks; the caller refreshes from the response.
export function MergeGates({
  requirements,
  busy,
  onApprove,
  onRerun,
}: {
  requirements: MergeRequirements;
  busy: boolean;
  onApprove: (ownerRef: string, approvedBy: string) => void;
  onRerun: (checkName: string) => void;
}) {
  const [approveAs, setApproveAs] = useState("user:demo");
  const owners = requirements.owners;
  const checks = requirements.checks;
  const hasOwners = (owners?.required.length ?? 0) > 0;
  const hasChecks = (checks?.required.length ?? 0) > 0;

  return (
    <div>
      <div
        className={`mergeable-banner ${requirements.mergeable ? "mergeable-yes" : "mergeable-no"}`}
      >
        {requirements.mergeable ? "✓ Ready to land" : "Blocked from landing"}
      </div>

      {hasOwners && (
        <div className="gate-section">
          <p className="gate-title">Owners</p>
          {owners!.required.map((o) => {
            const satisfied = owners!.satisfied.includes(o);
            return (
              <div className="gate-row" key={o}>
                <span className={`gate-icon ${satisfied ? "ok" : "due"}`}>
                  {satisfied ? "✓" : "○"}
                </span>
                <span className="gate-name mono">{o}</span>
                {!satisfied && (
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
          {owners!.outstanding.length > 0 && (
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
          <p className="gate-title">Checks</p>
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
                {failing && (
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

      {requirements.blockers.length > 0 && (
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
