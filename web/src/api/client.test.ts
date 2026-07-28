// Which base a REST call resolves against is a correctness property, not a
// style one: this control plane is ORG-LESS (no root-mounted org since
// 2026-07-17), so the root serves the hub's own APIs and nothing else. An
// org-scoped path built on the bare `baseUrl` reaches a router that answers
// "this control plane has no root-mounted org - every org lives at
// /o/<name>/", which is exactly what the change page's Acknowledge button
// did from the org-less flip until 2026-07-28 - one `baseUrl` where every
// sibling call used the org-scoped base.
//
// A unit test cannot catch it (the URL is built inside a fetch from
// module-level state), so this reads the source, the way uicopy.test.ts
// guards user-visible copy.

import { describe, expect, it } from "vitest";

const source = (
  import.meta.glob("./client.ts", { query: "?raw", import: "default", eager: true }) as Record<
    string,
    string
  >
)["./client.ts"];

/** Endpoints the HUB itself serves at the root - the org registry, signup,
 * the auth probe, invite intake. These legitimately take `baseUrl`; every
 * other api/ path belongs to an org. */
const HUB_ENDPOINTS = [
  "api/orgs",
  "api/signup",
  "api/auth/config",
  "api/invite-requests",
  "api/admin",
];

describe("REST base selection", () => {
  it("builds only hub-level endpoints on the bare baseUrl", () => {
    const offenders: string[] = [];
    source.split("\n").forEach((line, i) => {
      const m = line.match(/new URL\(\s*[`"']([^`"']*api\/[^`"']*)/);
      if (!m) return;
      // The org-scoped bases are the safe ones: transportBase, or a path
      // that spells out o/<org>/ itself.
      if (!/,\s*baseUrl\s*\)/.test(line)) return;
      const path = m[1];
      if (path.startsWith("o/")) return;
      if (HUB_ENDPOINTS.some((h) => path.startsWith(h))) return;
      offenders.push(`client.ts:${i + 1}: ${line.trim()}`);
    });
    expect(
      offenders,
      `org-scoped REST calls must resolve against transportBase (or an explicit o/<org>/ path):\n${offenders.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps the ack-policy endpoint org-scoped", () => {
    const line = source.split("\n").find((l) => l.includes("/ack-policy"));
    expect(line).toBeDefined();
    expect(line).toContain("transportBase");
  });
});
