import { useState } from "react";
import { useParams } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { publicBrowse, changesClient } from "../api/client";
import { ChangeState, type MergeRequirements } from "../gen/runko/v1/common_pb";
import type { LandChangeResponse } from "../gen/runko/v1/changes_pb";
import { changeNumberLabel, shortSha } from "../lib/format";
import { useRpc } from "../lib/useRpc";
import { DiffView } from "../components/DiffView";
import { MergeGates } from "../components/MergeGates";
import { StackRail } from "../components/StackRail";
import { AuthorChip, BackLink, ErrorNote, InfoTip, OriginChip, Spinner, StateBadge } from "../components/ui";

export function ChangePage() {
  const { changeId = "" } = useParams();
  const [busy, setBusy] = useState(false);
  const [landResult, setLandResult] = useState<LandChangeResponse | undefined>();
  const [actionError, setActionError] = useState<ConnectError | undefined>();

  const { data, error, loading, reload } = useRpc(async () => {
    const [change, stack, diff, reqs] = await Promise.all([
      changesClient.getChange({ changeId }),
      changesClient.getChangeStack({ changeId }),
      changesClient.getChangeDiff({ changeId }),
      changesClient.getMergeRequirements({ changeId }),
    ]);
    // Requirements for stack siblings power the rail's status dots.
    const requirementsById = new Map<string, MergeRequirements>();
    if (reqs.requirements) requirementsById.set(changeId, reqs.requirements);
    await Promise.all(
      stack.changes
        .filter((c) => c.id !== changeId)
        .map(async (c) => {
          try {
            const r = await changesClient.getMergeRequirements({ changeId: c.id });
            if (r.requirements) requirementsById.set(c.id, r.requirements);
          } catch {
            // Dot degrades to "unknown".
          }
        }),
    );
    return {
      change: change.change!,
      stack: stack.changes,
      diff: diff.files,
      requirements: reqs.requirements,
      requirementsById,
    };
  }, changeId);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setActionError(undefined);
    try {
      await fn();
      reload();
    } catch (err) {
      setActionError(ConnectError.from(err));
    } finally {
      setBusy(false);
    }
  };

  const back = <BackLink to="/changes">Changes</BackLink>;
  if (loading) return <div className="page">{back}<Spinner /></div>;
  if (error) return <div className="page">{back}<ErrorNote error={error} /></div>;
  if (!data) return null;

  const { change, stack, diff, requirements, requirementsById } = data;
  const open = change.state === ChangeState.OPEN;

  return (
    <div className="page">
      {back}
      <header className="change-header">
        <h1>
          {change.title}
          <StateBadge state={change.state} />
        </h1>
        <div className="change-meta-row">
          <span>{changeNumberLabel(change.number)}</span>
          <span className="mono" title={change.id}>
            {change.id.slice(0, 13)}…
          </span>
          <AuthorChip author={change.authoredBy} />
          {change.originWorkspace && (
            <>
              <OriginChip workspace={change.originWorkspace} branch={change.originBranch} />
              <InfoTip text="The workspace branch this change was pushed from (§12.2): one workspace branch carries one stack. Recorded at push time from the worktree's own runko.workspace/runko.branch config, validated against the registry." />
            </>
          )}
          <span className="mono" title={`base ${change.baseSha}`}>
            base {shortSha(change.baseSha)}
          </span>
          <InfoTip text="The merge-base with trunk as of this version's push. Landing recomputes trunk's delta since this commit - if it doesn't intersect what this change affects, it lands without re-running checks; otherwise checks re-run (§13.5)." />
          <span className="mono" title={`head ${change.headSha}`}>
            head {shortSha(change.headSha)}
          </span>
          {change.state === ChangeState.LANDED && change.landedSha && (
            <span className="mono">landed as {shortSha(change.landedSha)}</span>
          )}
        </div>
        {change.description && <p className="change-description">{change.description}</p>}
      </header>

      {landResult?.landed && (
        <div className="land-banner land-banner-ok">
          Landed as {shortSha(landResult.landedSha)}
        </div>
      )}
      {landResult && !landResult.landed && landResult.requiresRevalidation && (
        <div className="land-banner land-banner-warn">
          Trunk moved under this change and intersects its affected set — required checks must
          re-run before landing (§13.5).
        </div>
      )}
      {landResult && !landResult.landed && landResult.conflicts.length > 0 && (
        <div className="land-banner land-banner-err">
          Rebase conflicts: {landResult.conflicts.join(", ")}
        </div>
      )}
      {landResult && !landResult.landed && landResult.raceRetry && (
        <div className="land-banner land-banner-warn">
          Lost a land race — try again.
        </div>
      )}
      {actionError && <ErrorNote error={actionError} />}

      <div className="change-layout">
        <div>
          <DiffView files={diff} />
        </div>
        <aside>
          <section className="card side-card">
            <h2>
              Stack
              <InfoTip text="Changes stacked on one another: each one's base is the previous one's head. They land independently, bottom-up - a change can't land until everything below it in the stack already has." />
            </h2>
            <StackRail stack={stack} currentId={change.id} requirementsById={requirementsById} />
          </section>

          {requirements && (
            <section className="card side-card">
              <h2>Merge requirements</h2>
              <MergeGates
                requirements={requirements}
                state={change.state}
                busy={busy}
                onApprove={(ownerRef, approvedBy) =>
                  act(() => changesClient.approveChange({ changeId, ownerRef, approvedBy }))
                }
                onRerun={(checkName) =>
                  act(() => changesClient.rerunCheck({ changeId, checkName }))
                }
              />
            </section>
          )}

          {open && !publicBrowse && (
            <section className="card side-card">
              <h2>Actions</h2>
              <div className="chip-row">
                <button
                  className="btn btn-primary"
                  disabled={busy || !requirements?.mergeable}
                  title={requirements?.mergeable ? "" : requirements?.blockers.join("; ")}
                  onClick={() =>
                    act(async () => {
                      setLandResult(await changesClient.landChange({ changeId }));
                    })
                  }
                >
                  Land
                </button>
                {!requirements?.mergeable && (
                  <button
                    className="btn btn-danger"
                    disabled={busy}
                    title="Admin override (design.md 13.5): bypasses owner/check gates, audited as landed_forced. The server refuses non-admin callers."
                    onClick={() => {
                      const blockers = requirements?.blockers.join("\n") ?? "";
                      if (
                        window.confirm(
                          `Force land, bypassing merge gates?\n\nBlockers being overridden:\n${blockers}\n\nThis is audited (landed_forced) and only admins may do it.`,
                        )
                      ) {
                        void act(async () => {
                          setLandResult(await changesClient.landChange({ changeId, force: true }));
                        });
                      }
                    }}
                  >
                    Force land
                  </button>
                )}
                <button
                  className="btn btn-danger"
                  disabled={busy}
                  onClick={() => {
                    if (window.confirm("Abandon this change?")) {
                      void act(() => changesClient.abandonChange({ changeId, reason: "" }));
                    }
                  }}
                >
                  Abandon
                </button>
              </div>
            </section>
          )}
        </aside>
      </div>
    </div>
  );
}
