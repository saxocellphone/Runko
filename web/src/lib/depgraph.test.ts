import { describe, expect, it } from "vitest";
import {
  assignLayers,
  dependencyClosure,
  dependentClosure,
  layoutDag,
} from "./depgraph";

// The demo scene's shape: storefront -> checkout-api -> {cart, authz},
// relbot isolated.
const scene = [
  { name: "commerce/cart", deps: [] },
  { name: "commerce/checkout-api", deps: ["commerce/cart", "platform/authz"] },
  { name: "platform/authz", deps: [] },
  { name: "web/storefront", deps: ["commerce/checkout-api"] },
  { name: "tools/relbot", deps: [] },
];

describe("assignLayers", () => {
  it("puts foundations at 0 and dependents above by longest path", () => {
    const layers = assignLayers(scene);
    expect(layers.get("commerce/cart")).toBe(0);
    expect(layers.get("platform/authz")).toBe(0);
    expect(layers.get("tools/relbot")).toBe(0);
    expect(layers.get("commerce/checkout-api")).toBe(1);
    expect(layers.get("web/storefront")).toBe(2);
  });

  it("ignores unknown deps and survives cycles", () => {
    const layers = assignLayers([
      { name: "a", deps: ["b", "not-a-project"] },
      { name: "b", deps: ["a"] }, // illegal cycle - must not hang
    ]);
    expect(layers.size).toBe(2);
    for (const v of layers.values()) expect(Number.isFinite(v)).toBe(true);
  });
});

describe("layoutDag", () => {
  it("gives every node coordinates inside the canvas and one edge per known dep", () => {
    const g = layoutDag(scene);
    expect(g.nodes).toHaveLength(5);
    expect(g.edges).toHaveLength(3);
    for (const n of g.nodes) {
      expect(n.x).toBeGreaterThanOrEqual(0);
      expect(n.y).toBeGreaterThanOrEqual(0);
      expect(n.x + n.w).toBeLessThanOrEqual(g.width);
      expect(n.y + n.h).toBeLessThanOrEqual(g.height);
    }
  });

  it("draws edges downward (dependent above dependency)", () => {
    const g = layoutDag(scene);
    for (const e of g.edges) expect(e.y1).toBeLessThan(e.y2);
  });

  it("is deterministic", () => {
    expect(layoutDag(scene)).toEqual(layoutDag([...scene].reverse()));
  });
});

describe("closures", () => {
  it("dependencyClosure is transitive", () => {
    expect(dependencyClosure(scene, "web/storefront")).toEqual(
      new Set(["commerce/checkout-api", "commerce/cart", "platform/authz"]),
    );
  });

  it("dependentClosure is the affected direction", () => {
    expect(dependentClosure(scene, "commerce/cart")).toEqual(
      new Set(["commerce/checkout-api", "web/storefront"]),
    );
    expect(dependentClosure(scene, "tools/relbot")).toEqual(new Set());
  });
});

describe("consumes edges (§13.3.1)", () => {
  const items = [
    { name: "runkod", deps: ["platform"] },
    { name: "platform", deps: [] },
    { name: "mailer", deps: [], consumes: ["runkod"] },
    { name: "hybrid", deps: ["runkod"], consumes: ["runkod"] },
  ];

  it("layers a client above its provider", () => {
    const layers = assignLayers(items);
    expect(layers.get("mailer")!).toBeGreaterThan(layers.get("runkod")!);
  });

  it("emits a dashed-kind edge, deduped when a dep edge covers the pair", () => {
    const layout = layoutDag(items);
    const kinds = layout.edges.map((e) => `${e.from}->${e.to}:${e.kind}`).sort();
    expect(kinds).toContain("mailer->runkod:consumes");
    expect(kinds).toContain("hybrid->runkod:dep");
    expect(kinds).not.toContain("hybrid->runkod:consumes");
  });

  it("includes consumers in the affected-side closure and providers in the dependency closure", () => {
    expect(dependentClosure(items, "runkod").has("mailer")).toBe(true);
    expect(dependencyClosure(items, "mailer").has("runkod")).toBe(true);
    expect(dependencyClosure(items, "mailer").has("platform")).toBe(true);
  });
});
