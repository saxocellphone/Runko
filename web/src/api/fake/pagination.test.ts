import { describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createFakeTransport } from "./transport";
import { ChangeService } from "../../gen/runko/v1/changes_pb";
import { ChangeState } from "../../gen/runko/v1/common_pb";

// The fake transport mirrors runkod's ListChanges pagination (offset
// tokens, pageSize 0 = everything) so the playground exercises the same
// wire contract the real ChangesPage history tabs use.
describe("fake listChanges pagination", () => {
  it("pages tile the full listing with offset tokens", async () => {
    const changes = createClient(ChangeService, createFakeTransport());
    const full = await changes.listChanges({ state: ChangeState.OPEN });
    expect(full.nextPageToken).toBe("");
    expect(full.changes.length).toBeGreaterThan(1);

    const size = full.changes.length - 1; // forces exactly two pages
    const first = await changes.listChanges({ state: ChangeState.OPEN, pageSize: size });
    expect(first.changes.length).toBe(size);
    expect(first.nextPageToken).not.toBe("");

    const second = await changes.listChanges({
      state: ChangeState.OPEN,
      pageSize: size,
      pageToken: first.nextPageToken,
    });
    expect(second.nextPageToken).toBe("");
    expect([...first.changes, ...second.changes].map((c) => c.id)).toEqual(
      full.changes.map((c) => c.id),
    );
  });
});
