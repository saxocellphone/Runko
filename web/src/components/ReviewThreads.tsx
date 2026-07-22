// Review conversation rendering (§13.4.1, stage 16b): one-level threads
// with the agent badge, resolved state, outdated marking, reply and
// resolve controls. Anchoring into the diff is DiffView's job; this file
// renders a thread wherever it's been anchored.
import { useState } from "react";
import { authUser, publicBrowse } from "../api/client";
import { CommentSide, type Comment } from "../gen/runko/v1/common_pb";
import type { Thread } from "../lib/comments";
import { AuthorChip } from "./ui";

export interface ReviewActions {
  /** Create a comment; anchor fields empty for change-level/replies. */
  onComment: (body: string, anchor: { path?: string; side?: CommentSide; line?: number; parentId?: string }) => Promise<void>;
  onResolve: (commentId: string, resolved: boolean) => Promise<void>;
}

export function ThreadCard({
  thread,
  outdated,
  actions,
  busy,
}: {
  thread: Thread;
  outdated?: boolean;
  actions: ReviewActions;
  busy: boolean;
}) {
  const [replying, setReplying] = useState(false);
  const root = thread.root;
  const canAct = !publicBrowse;
  return (
    <div className={`thread${root.resolved ? " thread-resolved" : ""}`}>
      <CommentRow comment={root} outdated={outdated} />
      {thread.replies.map((r) => (
        <CommentRow key={r.id} comment={r} reply />
      ))}
      {canAct && (
        <div className="thread-actions">
          {replying ? (
            <CommentComposer
              placeholder="Reply…"
              busy={busy}
              onCancel={() => setReplying(false)}
              onSubmit={async (body) => {
                await actions.onComment(body, { parentId: root.id });
                setReplying(false);
              }}
            />
          ) : (
            <>
              <button className="btn btn-sm" disabled={busy} onClick={() => setReplying(true)}>
                Reply
              </button>
              <button
                className="btn btn-sm"
                disabled={busy}
                title={
                  root.resolved
                    ? "reopen this thread"
                    : "resolvable by the thread author, the change author, or an owner of the commented path"
                }
                onClick={() => void actions.onResolve(root.id, !root.resolved)}
              >
                {root.resolved ? "Reopen" : "Resolve"}
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}

function CommentRow({
  comment,
  reply,
  outdated,
}: {
  comment: Comment;
  reply?: boolean;
  outdated?: boolean;
}) {
  return (
    <div className={`comment${reply ? " comment-reply" : ""}`}>
      <div className="comment-head">
        <AuthorChip author={comment.author} />
        {outdated && (
          <span
            className="chip chip-amber"
            title="written against an earlier version of this change - the line it pointed at may have moved or vanished"
          >
            outdated
          </span>
        )}
        {!reply && comment.resolved && <span className="chip chip-green">resolved</span>}
        {comment.path && (
          <span className="mono comment-anchor" title={comment.path}>
            {comment.path}
            {comment.line > 0 ? `:${comment.line}` : ""}
          </span>
        )}
      </div>
      <div className="comment-body">{comment.body}</div>
    </div>
  );
}

export function CommentComposer({
  placeholder,
  busy,
  onSubmit,
  onCancel,
}: {
  placeholder: string;
  busy: boolean;
  onSubmit: (body: string) => Promise<void>;
  onCancel?: () => void;
}) {
  const [body, setBody] = useState("");
  const submit = async () => {
    const text = body.trim();
    if (!text) return;
    await onSubmit(text);
    setBody("");
  };
  return (
    <div className="comment-composer">
      <textarea
        value={body}
        placeholder={placeholder}
        rows={2}
        onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") void submit();
        }}
      />
      <div className="chip-row">
        <button className="btn btn-sm btn-primary" disabled={busy || !body.trim()} onClick={() => void submit()}>
          Comment{authUser ? ` as ${authUser}` : ""}
        </button>
        {onCancel && (
          <button className="btn btn-sm" disabled={busy} onClick={onCancel}>
            Cancel
          </button>
        )}
      </div>
    </div>
  );
}
