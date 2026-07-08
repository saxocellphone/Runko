import { describe, expect, it } from "vitest";
import { createClient } from "@connectrpc/connect";
import { createFakeTransport } from "./transport";
import { ProjectService } from "../../gen/runko/v1/projects_pb";
import { ChangeService } from "../../gen/runko/v1/changes_pb";
import { ChangeState } from "../../gen/runko/v1/common_pb";

// The fake mirrors the daemon's create-project semantics (runkod/
// createproject_test.go): creation opens a Change, the project only
// exists once that Change lands.
describe("fake create project", () => {
  const clients = () => {
    const t = createFakeTransport();
    return {
      projects: createClient(ProjectService, t),
      changes: createClient(ChangeService, t),
    };
  };
  const intent = { name: "payments-api", type: "service", owners: ["group:commerce"] };

  it("previews the generated files without applying anything", async () => {
    const { projects } = clients();
    const res = await projects.previewCreateProject({ intent });
    expect(res.path).toBe("payments-api");
    expect(res.files.map((f) => f.path)).toContain("PROJECT.yaml");
    const list = await projects.listProjects({});
    expect(list.projects.some((p) => p.name === intent.name)).toBe(false);
  });

  it("creates as an open change; landing it makes the project real", async () => {
    const { projects, changes } = clients();
    const created = await projects.createProject({ intent });
    const change = created.change!;
    expect(change.state).toBe(ChangeState.OPEN);
    expect(change.title).toBe("Create project payments-api");

    // Not a project yet - only a change with the plan as its diff.
    expect(
      (await projects.listProjects({})).projects.some((p) => p.name === intent.name),
    ).toBe(false);
    const diff = await changes.getChangeDiff({ changeId: change.id });
    expect(diff.files.map((f) => f.path)).toContain("payments-api/PROJECT.yaml");

    // Gated on the declared owner, then lands, then exists.
    await expect(changes.landChange({ changeId: change.id })).rejects.toThrow(/not mergeable/);
    await changes.approveChange({
      changeId: change.id,
      ownerRef: "group:commerce",
      approvedBy: "user:demo",
    });
    const landed = await changes.landChange({ changeId: change.id });
    expect(landed.landed).toBe(true);
    expect(
      (await projects.listProjects({})).projects.some((p) => p.name === intent.name),
    ).toBe(true);
  });

  it("rejects duplicates and invalid intents with the daemon's codes", async () => {
    const { projects } = clients();
    await expect(
      projects.previewCreateProject({ intent: { name: "commerce/cart", path: "commerce/cart", type: "library" } }),
    ).rejects.toThrow(/already_exists|invalid_format/);
    await expect(
      projects.previewCreateProject({ intent: { name: "", type: "service" } }),
    ).rejects.toThrow(/invalid_intent/);
    await expect(
      projects.previewCreateProject({ intent: { name: "xy", type: "microservice" } }),
    ).rejects.toThrow(/invalid_intent/);
  });
});
