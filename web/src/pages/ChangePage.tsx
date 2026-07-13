import { useState } from "react";
import { useParams } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { authUser, publicBrowse, changesClient } from "../api/client";
import { ChangeState, CommentSide, type MergeRequirements } from "../gen/runko/v1/common_pb";
import type { LandChangeResponse, SyncChangeResponse } from "../gen/runko/v1/changes_pb";
import { groupThreads, partitionThreads } from "../lib/comments";
import { absoluteTime, changeNumberLabel, shortSha, timeAgo } from "../lib/format";
import { useRpc } from "../lib/useRpc";
import { DiffView } from "../components/DiffView";
import { Markdown } from "../components/Markdown";
import { MergeGates } from "../components/MergeGates";
import { CommentComposer, ThreadCard, type ReviewActions } from "../components/ReviewThreads";
import { StackRail } from "../components/StackRail";
import { AuthorChip, BackLink, ErrorNote, InfoTip, OriginChip, Spinner, StateBadge } from "../components/ui";

export function ChangePage() {
  const { changeId = "" } = useParams();
  const [busy, setBusy] = useState(false);
  const [landResult, setLandResult] = useState<LandChangeResponse | undefined>();
  const [syncResult, setSyncResult] = useState<SyncChangeResponse | undefined>();
  const [actionError, setActionError] = useState<ConnectError | undefined>();

  const { data, error, loading, reload } = useRpc(async () => {
    const [change, stack, diff, reqs, comments] = await Promise.all([
      changesClient.getChange({ changeId }),
      changesClient.getChangeStack({ changeId }),
      changesClient.getChangeDiff({ changeId }),
      changesClient.getMergeRequirements({ changeId }),
      // Review conversation (§13.4.1): unpaginated - a change's own
      // threads are bounded by review attention, not history.
      changesClient.listComments({ changeId }),
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
      comments: comments.comments,
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

  const { change, stack, diff, requirements, requirementsById, comments } = data;
  const open = change.state === ChangeState.OPEN;

  // §13.4.1: threads partition by anchor against the CURRENT head -
  // current line/file threads anchor into the diff, change-level ones form
  // the conversation, and anything written against an older head is
  // grouped as outdated (marked, never floated).
  const threads = partitionThreads(groupThreads(comments), change.headSha);
  const reviewActions: ReviewActions = {
    onComment: (body, anchor) =>
      act(() =>
        changesClient.createComment({
          changeId,
          body,
          path: anchor.path ?? "",
          side: anchor.side ?? CommentSide.UNSPECIFIED,
          line: anchor.line ?? 0,
          parentId: anchor.parentId ?? "",
          // A signed-in principal comments as itself (the server derives
          // the author from the credential); the anonymous deploy token -
          // the operator dev loop - asserts one, the approve-as precedent.
          author: authUser ? "" : "operator",
        }),
      ),
    onResolve: (commentId, resolved) =>
      act(() => changesClient.resolveComment({ changeId, commentId, resolved })),
  };

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
            <span className="mono" title={change.landedAt > 0n ? absoluteTime(change.landedAt) : undefined}>
              landed as {shortSha(change.landedSha)}
              {change.landedAt > 0n && <> · {timeAgo(change.landedAt)}</>}
            </span>
          )}
        </div>
        {change.description ? (
          <Markdown className="change-description" text={change.description} />
        ) : (
          change.state === ChangeState.OPEN && (
            // §8.6: the UI prompts when the summary is empty.
            <p className="change-description change-description-empty">
              No description yet — <code>runko change describe --description "…"</code> adds the
              what-and-why blurb (it also feeds release changelogs).
            </p>
          )
        )}
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
      {syncResult?.synced && (
        <div className="land-banner land-banner-ok">
          Stack synced — rebased onto the current trunk tip. Checks are re-running against the
          rebased heads.
        </div>
      )}
      {syncResult?.alreadyInSync && (
        <div className="land-banner land-banner-warn">
          Already in sync — the stack is based on the current trunk tip.
        </div>
      )}
      {syncResult && syncResult.conflictChangeId !== "" && (
        <div className="land-banner land-banner-err">
          Sync conflict in {syncResult.conflictChangeId.slice(0, 13)}…:{" "}
          {syncResult.conflicts.join(", ")} — nothing was rebased. Resolve in your workspace
          (<code>runko workspace sync</code>) and re-push the stack.
        </div>
      )}
      {actionError && <ErrorNote error={actionError} />}

      <div className="change-layout">
        <div>
          <DiffView
            files={diff}
            review={{ byLine: threads.byLine, byFile: threads.byFile, actions: reviewActions, busy }}
          />

          <section className="card conversation-card">
            <h2>
              Conversation
              <InfoTip text="Change-level review threads (§13.4.1). Line comments live on the diff above; comments written against an earlier version collect under 'outdated' - they keep their original anchor rather than guessing a new one." />
            </h2>
            {threads.conversation.length === 0 && threads.outdated.length === 0 && (
              <p className="muted">No comments yet.</p>
            )}
            {threads.conversation.map((t) => (
              <ThreadCard key={t.root.id} thread={t} actions={reviewActions} busy={busy} />
            ))}
            {threads.outdated.length > 0 && (
              <div className="outdated-threads">
                <p className="gate-title">Outdated — written against an earlier version</p>
                {threads.outdated.map((t) => (
                  <ThreadCard key={t.root.id} thread={t} outdated actions={reviewActions} busy={busy} />
                ))}
              </div>
            )}
            {open && !publicBrowse && (
              <CommentComposer
                placeholder="Comment on this change…"
                busy={busy}
                onSubmit={(body) => reviewActions.onComment(body, {})}
              />
            )}
          </section>
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
                onRequestReview={(reviewer) =>
                  act(() => changesClient.requestReview({ changeId, reviewer }))
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
                      setSyncResult(undefined);
                      setLandResult(await changesClient.landChange({ changeId }));
                    })
                  }
                >
                  Land
                </button>
                <button
                  className="btn"
                  disabled={busy}
                  title="Rebase this change's whole stack onto the current trunk tip, server-side (design.md 13.5). All-or-nothing: a conflict in any member is reported and nothing moves. Rebased heads re-run their required checks."
                  onClick={() =>
                    act(async () => {
                      setLandResult(undefined);
                      setSyncResult(await changesClient.syncChange({ changeId }));
                    })
                  }
                >
                  Sync
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
                {!requirements?.mergeable && !change.automerge && (
                  <button
                    className="btn"
                    disabled={busy}
                    title="Arm the when-ready land (§13.5): the server lands this change automatically the moment its merge gates go green. Survives amends - the gates reset and re-gate on their own."
                    onClick={() => act(() => changesClient.setAutomerge({ changeId, enabled: true }))}
                  >
                    Auto-land when ready
                  </button>
                )}
                {change.automerge && (
                  <button
                    className="btn"
                    disabled={busy}
                    title={`Armed by ${change.automergeBy || "the deploy token"} - lands itself once the gates go green. Click to disarm.`}
                    onClick={() => act(() => changesClient.setAutomerge({ changeId, enabled: false }))}
                  >
                    ⏻ Auto-land armed — disarm
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
