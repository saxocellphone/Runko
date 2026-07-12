import { describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createFakeTransport } from "./transport";
import { ChangeService } from "../../gen/runko/v1/changes_pb";
import { stackMiddle } from "./fixtures";

// The fake mirrors runkod's syncChangeCore shape: the WHOLE stack rebases
// together (bases re-chain parent-head -> child-base), and a second sync
// finds it already based on the tip.
describe("fake stack sync", () => {
  it("rebases the whole stack once, then reports already-in-sync", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    const before = await changes.getChangeStack({ changeId: stackMiddle.id });

    const first = await changes.syncChange({ changeId: stackMiddle.id });
    expect(first.synced).toBe(true);
    expect(first.alreadyInSync).toBe(false);
    expect(first.conflictChangeId).toBe("");

    const after = await changes.getChangeStack({ changeId: stackMiddle.id });
    expect(after.changes.length).toBe(before.changes.length);
    for (let i = 1; i < after.changes.length; i++) {
      expect(after.changes[i]!.baseSha).toBe(after.changes[i - 1]!.headSha);
    }

    const second = await changes.syncChange({ changeId: stackMiddle.id });
    expect(second.synced).toBe(false);
    expect(second.alreadyInSync).toBe(true);
  });
});
