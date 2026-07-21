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

/** Where the browser must go after binding its session to `org` - always
 * a destination, never "stay here".
 *
 * Re-entering the authenticated org is only half the job: the auth-page
 * request itself must be spent. `?signin=1` beats every auth state by
 * design (wantsAuthPage below), INCLUDING the session that was just
 * created - so reloading the current URL verbatim re-rendered the gate,
 * and the credential the server had already accepted bought nothing. On
 * this deployment that was an unbreakable loop: sign in, land back on
 * the sign-in form, repeat (prod-observed 2026-07-21 - runkod answered
 * `GET /o/runko/api/whoami` 200 three times for the same browser, each
 * followed by the login page mounting again). Stripping the flags here
 * keeps "asking for the auth page wins" intact for arrivals while
 * letting a bound session through. */
export function postSignInPath(opts: {
  pathOrg: string;
  org: string;
  pathname: string;
  search: string;
  hash: string;
}): string {
  const { pathOrg, org, pathname, search, hash } = opts;
  // A URL naming a DIFFERENT org re-enters at that org's root: its path,
  // query, and hash all belong to the other org's content.
  if (pathOrg && pathOrg !== org) return `/${org}`;
  const params = new URLSearchParams(search);
  params.delete("signin");
  params.delete("invite");
  const rest = params.toString();
  return `${pathname}${rest ? `?${rest}` : ""}${hash}`;
}

/** Whether the URL asks for the auth page outright - `?signin=1` (every
 * "Sign in" affordance outside the app: the landing page's header/footer
 * CTAs and the read-only footer button) or `?invite=1` (the landing
 * page's "Request an invite" CTA, §15.1).
 *
 * Asking to sign in must beat every ambient auth state, exactly as
 * `?invite=1` already does. A browser that has signed in here before
 * keeps its org (signOut deliberately leaves "runko-org" behind to
 * prefill the form), so a SIGNED-OUT visitor still resolves an org - and
 * when that org is public_read (§15.2), the boot probe puts them into
 * anonymous browsing and renders the inbox instead of the gate. The
 * landing page's "Sign in" then lands on a read-only /changes list with
 * no way to authenticate: prod-observed 2026-07-20 on this very
 * deployment, whose own org IS public_read. A stale or foreign
 * credential strands the same way, one branch further along. */
export function wantsAuthPage(search: string): boolean {
  const params = new URLSearchParams(search);
  return params.has("signin") || params.has("invite");
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
