import { describe, expect, it } from "vitest";

import { postSignInPath } from "./orgsession";

// The prod repro (2026-07-16): casey has accounts named "casey" with the
// SAME password in org-x and org-y. Browsing org-y's public pages
// (/org-y/...), casey signs in with the form saying org-x. The session
// must land under /org-x - a bare reload would stay on /org-y, where the
// URL org overrides the stored one and the same combo verifies against
// org-y's DIFFERENT account.
describe("postSignInPath", () => {
  it("re-enters the authenticated org when the URL names another org", () => {
    expect(postSignInPath("org-y", "org-x")).toBe("/org-x");
  });

  it("reloads in place when the URL already names the signed-in org", () => {
    expect(postSignInPath("org-x", "org-x")).toBeNull();
  });

  it("reloads in place on org-less routes (/login, /changes, root)", () => {
    expect(postSignInPath("", "org-x")).toBeNull();
  });

  it("covers the org switcher the same way (switch while deep in another org's URL)", () => {
    // switchOrg("org-c") from /org-b/browse: without navigation the
    // reload stays bound to org-b and the switch silently no-ops.
    expect(postSignInPath("org-b", "org-c")).toBe("/org-c");
  });
});
