// Post-sign-in (and org-switch) navigation: sessions are org-scoped, but
// two inputs name an org at page load - the URL's first segment
// (client.ts pathOrg, GitHub-style /<org> routes) and the stored
// selection (localStorage "runko-org") - and the URL WINS. A bare
// location.reload() after binding a session therefore re-enters whatever
// org the URL names, not the org the credential was just validated
// against. With per-org accounts sharing one name+password combo, the
// other org's row verifies too, so the browser silently authenticates as
// a DIFFERENT account than the one that signed in - the prod-observed
// "signed in to one org, landed in another" (2026-07-16). The server is
// blameless here (runkod's TestSameNameSamePasswordAcrossOrgs pins that);
// the divergence is purely this client-side rebind.

/** Where the browser must go after binding its session to `org`:
 * `null` means reload in place (the URL either names no org or names
 * the same org, so the rebind is consistent); otherwise the returned
 * path re-enters the app under the org that actually authenticated. */
export function postSignInPath(pathOrg: string, org: string): string | null {
  if (!pathOrg || pathOrg === org) return null;
  return `/${org}`;
}
