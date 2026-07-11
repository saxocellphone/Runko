import { describe, expect, it } from "vitest";
import { createClient, ConnectError } from "@connectrpc/connect";
import { createFakeTransport } from "./transport";
import { ChangeService } from "../../gen/runko/v1/changes_pb";
import { agentChange, stackMiddle } from "./fixtures";

// The fake transport mirrors runkod's review-conversation semantics
// (§13.4.1-13.4.2): one-level threads, the head_sha binding stamped
// server-side, root-only resolve, and the DERIVED attention set moving as
// people respond - so the playground demonstrates the same contract the
// live ChangePage drives.
describe("fake review conversation", () => {
  it("serves the fixture threads and stamps new comments with the current head", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    const before = await changes.listComments({ changeId: stackMiddle.id });
    expect(before.comments.length).toBe(3);

    const created = await changes.createComment({
      changeId: stackMiddle.id,
      body: "also: add a test for the empty-SKU case",
      author: "sam",
    });
    expect(created.comment?.headSha).toBe(stackMiddle.headSha);
    const after = await changes.listComments({ changeId: stackMiddle.id });
    expect(after.comments.length).toBe(4);
  });

  it("refuses a reply to a reply (threads are one level deep)", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    // cmt-102 is the fixture reply under cmt-101.
    await expect(
      changes.createComment({ changeId: stackMiddle.id, body: "nested", author: "sam", parentId: "cmt-102" }),
    ).rejects.toSatisfy((err: unknown) =>
      ConnectError.from(err).rawMessage.includes("thread_depth_exceeded"),
    );
  });

  it("resolves thread roots only", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    await expect(
      changes.resolveComment({ changeId: stackMiddle.id, commentId: "cmt-102", resolved: true }),
    ).rejects.toSatisfy((err: unknown) =>
      ConnectError.from(err).rawMessage.includes("not_a_thread_root"),
    );
    const resolved = await changes.resolveComment({
      changeId: stackMiddle.id,
      commentId: "cmt-101",
      resolved: true,
    });
    expect(resolved.comment?.resolved).toBe(true);
  });

  it("derives attention: a requested reviewer enters until they respond at the current head", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    // Fixture state: priya was requested AND commented at the current head,
    // so attention sits with the author (val) and the outstanding owner.
    const initial = await changes.getMergeRequirements({ changeId: stackMiddle.id });
    expect(initial.requirements?.attentionSet).toContain("val");
    expect(initial.requirements?.attentionSet).not.toContain("priya");

    await changes.requestReview({ changeId: stackMiddle.id, reviewer: "sam" });
    const requested = await changes.getMergeRequirements({ changeId: stackMiddle.id });
    expect(requested.requirements?.attentionSet).toContain("sam");

    await changes.createComment({ changeId: stackMiddle.id, body: "LGTM overall", author: "sam" });
    const responded = await changes.getMergeRequirements({ changeId: stackMiddle.id });
    expect(responded.requirements?.attentionSet).not.toContain("sam");
  });

  it("keeps the agent fixture comment attributed to the bot (badge material)", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    const res = await changes.listComments({ changeId: agentChange.id });
    expect(res.comments[0]?.author?.id).toBe("refactor-bot");
  });
});
