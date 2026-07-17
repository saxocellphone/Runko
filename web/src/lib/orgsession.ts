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

// The app's own root routes that render ONE org's content. When the URL
// doesn't name an org, they fall back to the stored selection (currentOrg)
// - so the page shows an org the address bar never names. The rest
// (login, signup, admin, demo, landing, ...) are account- or
// deployment-global and legitimately live at the bare root.
const orgScopedRootRoutes = new Set([
  "",
  "changes",
  "browse",
  "projects",
  "workspaces",
  "search",
  "settings",
  "graph",
]);

/** Canonical org-scoped location for a bare-root URL, or `null` to leave it
 * be. A signed-in browser sitting on a bare-root org-scoped path (/browse,
 * /changes, /) is really viewing its stored org, but the URL doesn't say
 * which - confusing and unshareable. Rewrite it to the GitHub-style
 * /<org>/... form so the address bar always names the org. Returns `null`
 * when the URL already names an org (`pathOrg` set), when no org is known,
 * when the browser isn't signed in (anonymous/public browsing has its own
 * /<org> redirect in App's AnonGate), or when the path is a global route. */
export function canonicalOrgPath(opts: {
  pathOrg: string;
  currentOrg: string;
  signedIn: boolean;
  pathname: string;
  search: string;
  hash: string;
}): string | null {
  const { pathOrg, currentOrg, signedIn, pathname, search, hash } = opts;
  if (!signedIn || pathOrg || !currentOrg) return null;
  const seg = pathname.split("/")[1] ?? "";
  if (!orgScopedRootRoutes.has(seg)) return null;
  const rest = pathname === "/" ? "" : pathname;
  return `/${currentOrg}${rest}${search}${hash}`;
}
