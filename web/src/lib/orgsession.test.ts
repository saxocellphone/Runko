import { describe, expect, it } from "vitest";

import { canonicalOrgPath, postSignInPath, wantsAuthPage } from "./orgsession";

// The reported bug (2026-07-20): "when I click on sign in, it takes me to
// /changes, not the sign in page". The landing page's Sign in CTA pointed
// at a bare /changes and relied on the app's gate to notice there is no
// credential - but this deployment's own org is public_read and signOut
// keeps "runko-org", so the signed-out visitor's boot probe succeeds and
// renders the read-only inbox. The CTA now says ?signin=1, and App
// answers that BEFORE consulting any auth state.
describe("wantsAuthPage", () => {
  it("is true for the landing page's Sign in CTA", () => {
    expect(wantsAuthPage("?signin=1")).toBe(true);
  });

  it("is true for the invite deep link (LoginPage picks the request mode)", () => {
    expect(wantsAuthPage("?invite=1")).toBe(true);
  });

  it("is true beside other params, and for the bare valueless flag", () => {
    expect(wantsAuthPage("?tab=landed&signin=1")).toBe(true);
    expect(wantsAuthPage("?signin")).toBe(true);
  });

  it("leaves ordinary app URLs alone - the gate keeps deciding for them", () => {
    expect(wantsAuthPage("")).toBe(false);
    expect(wantsAuthPage("?tab=landed")).toBe(false);
    expect(wantsAuthPage("?org=acme")).toBe(false);
  });

  it("does not fire on a look-alike param", () => {
    expect(wantsAuthPage("?signinvite=1")).toBe(false);
  });
});

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

// The reported bug (2026-07-17): visiting /browse shows "the code of the
// last org I visited" under a URL that names no org. Signed in, the bare
// root resolves currentOrg from the stored selection; canonicalOrgPath
// rewrites the URL so it always names that org.
describe("canonicalOrgPath", () => {
  const base = { pathOrg: "", currentOrg: "acme", signedIn: true, search: "", hash: "" };

  it("names the org on a bare org-scoped route", () => {
    expect(canonicalOrgPath({ ...base, pathname: "/browse" })).toBe("/acme/browse");
  });

  it("names the org on the bare root", () => {
    expect(canonicalOrgPath({ ...base, pathname: "/" })).toBe("/acme");
  });

  it("preserves deep paths, query, and hash", () => {
    expect(
      canonicalOrgPath({
        ...base,
        pathname: "/changes/I123",
        search: "?tab=diff",
        hash: "#c4",
      }),
    ).toBe("/acme/changes/I123?tab=diff#c4");
  });

  it("leaves already-org-scoped URLs alone (no double prefix, no loop)", () => {
    expect(canonicalOrgPath({ ...base, pathOrg: "acme", pathname: "/browse" })).toBeNull();
  });

  it("leaves account-/deployment-global routes at the bare root", () => {
    expect(canonicalOrgPath({ ...base, pathname: "/login" })).toBeNull();
    expect(canonicalOrgPath({ ...base, pathname: "/admin" })).toBeNull();
  });

  it("does nothing without a signed-in session (AnonGate owns public browsing)", () => {
    expect(canonicalOrgPath({ ...base, signedIn: false, pathname: "/browse" })).toBeNull();
  });

  it("does nothing when no org is known", () => {
    expect(canonicalOrgPath({ ...base, currentOrg: "", pathname: "/browse" })).toBeNull();
  });
});
