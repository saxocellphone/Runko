import { describe, expect, it } from "vitest";
import { graphProjects, isRootProject, rootFirst } from "./projects";

const p = (name: string, path: string) => ({ name, path });

// The index's own shape: the root project reports path "" (verified
// against a live control plane), every other project reports its folder.
const repo = p("repo", "");
const runkod = p("runkod", "runkod");
const web = p("web", "web");

describe("isRootProject", () => {
  it("keys on the empty path, not the name", () => {
    expect(isRootProject(repo)).toBe(true);
    expect(isRootProject(runkod)).toBe(false);
    // A project NAMED repo that lives in a folder is a normal project -
    // the name is this repo's convention, not a reserved word.
    expect(isRootProject(p("repo", "tools/repo"))).toBe(false);
    // ...and a root project named anything else is still the root.
    expect(isRootProject(p("monorepo-glue", ""))).toBe(true);
    // "." is the daemon's other spelling of the repo root
    // (runkod/deleteproject.go, runkod/bootstraporg.go).
    expect(isRootProject(p("repo", "."))).toBe(true);
  });
});

describe("rootFirst", () => {
  it("hoists the root and preserves the rest of the server's order", () => {
    expect(rootFirst([runkod, repo, web]).map((x) => x.name)).toEqual(["repo", "runkod", "web"]);
    expect(rootFirst([web, runkod]).map((x) => x.name)).toEqual(["web", "runkod"]);
  });

  it("does not mutate its input", () => {
    const items = [runkod, repo];
    rootFirst(items);
    expect(items.map((x) => x.name)).toEqual(["runkod", "repo"]);
  });
});

describe("graphProjects", () => {
  it("drops the root node and keeps everything else", () => {
    expect(graphProjects([repo, runkod, web]).map((x) => x.name)).toEqual(["runkod", "web"]);
  });

  it("is a no-op for a repo with no root manifest", () => {
    expect(graphProjects([runkod, web])).toHaveLength(2);
  });
});
