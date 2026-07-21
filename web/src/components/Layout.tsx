import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import {
  authUser,
  onDemoRoute,
  probeSearchAvailable,
  publicBrowse,
  signedIn,
  signOut,
  usingDemoData,
} from "../api/client";

const nav = [
  { to: "/changes", label: "Changes", icon: ChangesIcon },
  { to: "/browse", label: "Browse", icon: BrowseIcon },
  { to: "/projects", label: "Projects", icon: ProjectsIcon },
  { to: "/workspaces", label: "Workspaces", icon: WorkspacesIcon },
  { to: "/search", label: "Search", icon: SearchIcon },
  { to: "/settings", label: "Settings", icon: SettingsIcon },
];

// GitHub-style shell: a top header - brand/status row, then a horizontal
// tab row with the active-tab underline - and the page below. .main stays
// the app's scroll container.
export function Layout() {
  const [theme, setTheme] = useState<string>(
    () => document.documentElement.dataset.theme ?? "light",
  );
  // Hide Search when this org's backend answers 503 (no Zoekt wired).
  const [searchOk, setSearchOk] = useState(true);
  useEffect(() => {
    void probeSearchAvailable().then(setSearchOk);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("runko-theme", theme);
  }, [theme]);

  return (
    <div className="app">
      <header className="topbar">
        <div className="topbar-row">
          {/* In the playground the brand is the way out: the logo-goes-home
              reflex is the one every visitor already has, and the playground
              is the only place in the app where "home" is the product page
              rather than a signed-in surface. Nothing here links off the
              deployment otherwise, so the live app keeps a plain wordmark. */}
          <div className="topbar-brand">
            {onDemoRoute ? (
              <a className="topbar-brand-link" href="/" title="Leave the playground">
                <BrandMark />
                Runko
              </a>
            ) : (
              <>
                <BrandMark />
                Runko
              </>
            )}
          </div>
          <div className="topbar-actions">
            {onDemoRoute ? (
              <div className="demo-badge" title="Playground — sample data, in-browser">
                Playground — sample data, in-browser
              </div>
            ) : usingDemoData ? (
              <div
                className="demo-badge"
                title="Playground data — set VITE_RUNKO_URL to connect to a runkod"
              >
                Playground data — set VITE_RUNKO_URL to connect to a runkod
              </div>
            ) : publicBrowse ? (
              <div className="demo-badge">Browsing read-only</div>
            ) : (
              <div className="user-badge" title={authUser ? `Signed in as ${authUser}` : undefined}>
                {authUser ? (
                  <>
                    Signed in as <strong>{authUser}</strong>
                  </>
                ) : (
                  <>Live{signedIn ? ", anonymous" : ""}</>
                )}
              </div>
            )}
            {/* The way out of the playground, as a button in the same row as
                every other action - the old affordance was the word "exit"
                inside the status badge, small enough to read as part of the
                label. It also pointed at /changes, which is the APP: for a
                visitor carrying a stored org that canonicalises to /<org>,
                so "exit" landed on a source browser rather than the page
                they arrived from. "/" is the product landing page (nginx
                path-matches the bare root; the SPA never sees it). */}
            {onDemoRoute && (
              <a className="btn btn-sm" href="/" title="Leave the playground">
                ← Back to Runko
              </a>
            )}
            {publicBrowse && (
              <button
                className="btn btn-sm"
                onClick={() => {
                  // Stay on the CURRENT path: nginx gives the bare "/" to the
                  // product landing page (location = /, path-matched - the
                  // query string doesn't save it), so "/?signin=1" would serve
                  // the pitch instead of the gate. Any SPA route + ?signin=1
                  // reaches AnonGate, which forces the sign-in page.
                  window.location.href = `${window.location.pathname}?signin=1`;
                }}
              >
                Sign in
              </button>
            )}
            {!onDemoRoute && !usingDemoData && signedIn && (
              <button className="btn btn-sm" onClick={() => signOut()}>
                Sign out
              </button>
            )}
            <button
              className="btn btn-sm btn-icon"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
              title={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
              aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
            >
              {theme === "dark" ? <SunIcon /> : <MoonIcon />}
            </button>
          </div>
        </div>
        <nav className="topnav" aria-label="Primary">
          {nav
            // Anonymous read-only browsing (§15.2): workspaces and settings
            // are not on the public allowlist - hide what would only 401.
            // Search hides when the org has no search backend wired.
            .filter(({ to }) => {
              if (publicBrowse && (to === "/workspaces" || to === "/settings")) return false;
              if (to === "/search" && !searchOk) return false;
              return true;
            })
            .map(({ to, label, icon: Icon }) => (
              <NavLink key={to} to={to} className="topnav-tab">
                <span className="tab-pill">
                  <Icon />
                  {label}
                </span>
              </NavLink>
            ))}
        </nav>
      </header>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}

// The org drop-down switcher is gone (2026-07-17, user direction:
// "even the operator should get the same treatment as everyone else"):
// orgs are navigated by their own /<org> URLs, GitHub-style - the app
// binds to the URL's org (client.ts pathOrg/currentOrg), sign-in lands
// inside the org that authenticated (lib/orgsession.ts), and org
// creation lives on the sign-up form and the CLI. Operator-wide org
// administration is its own operator-only surface, not a topbar
// affordance.

function BrandMark() {
  return (
    <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden>
      <rect width="32" height="32" rx="7" fill="var(--accent)" />
      <line x1="16" y1="7" x2="16" y2="25" stroke="#fff" strokeWidth="2.5" strokeLinecap="round" opacity="0.55" />
      <circle cx="16" cy="7.5" r="3.4" fill="#fff" />
      <circle cx="16" cy="16" r="3.4" fill="#fff" />
      <circle cx="16" cy="24.5" r="3.4" fill="#fff" />
    </svg>
  );
}

const iconProps = {
  width: 15,
  height: 15,
  viewBox: "0 0 16 16",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.5,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

function ChangesIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <circle cx="8" cy="3" r="1.8" />
      <circle cx="8" cy="13" r="1.8" />
      <line x1="8" y1="4.8" x2="8" y2="11.2" />
    </svg>
  );
}

function BrowseIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <path d="M5.5 2v12M2 5.5h3.5M9 8h5M9 8a3.5 3.5 0 0 1-3.5-3.5M9 12.5h5M9 12.5A3.5 3.5 0 0 1 5.5 9" />
    </svg>
  );
}

function ProjectsIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <path d="M2 4.5c0-.8.7-1.5 1.5-1.5h3l1.5 2h4.5c.8 0 1.5.7 1.5 1.5v6c0 .8-.7 1.5-1.5 1.5h-9C2.7 14 2 13.3 2 12.5v-8z" />
    </svg>
  );
}

function WorkspacesIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <rect x="2" y="2" width="5" height="5" rx="1" />
      <rect x="9" y="2" width="5" height="5" rx="1" />
      <rect x="2" y="9" width="5" height="5" rx="1" />
      <rect x="9" y="9" width="5" height="5" rx="1" />
    </svg>
  );
}

function SearchIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <circle cx="7" cy="7" r="4.5" />
      <line x1="10.5" y1="10.5" x2="14" y2="14" />
    </svg>
  );
}

function SettingsIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <circle cx="8" cy="8" r="2.2" />
      <path d="M8 1.8v2M8 12.2v2M1.8 8h2M12.2 8h2M3.6 3.6l1.4 1.4M11 11l1.4 1.4M12.4 3.6L11 5M5 11l-1.4 1.4" />
    </svg>
  );
}

function SunIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <circle cx="8" cy="8" r="3" />
      <path d="M8 1.2v1.6M8 13.2v1.6M1.2 8h1.6M13.2 8h1.6M3.2 3.2l1.1 1.1M11.7 11.7l1.1 1.1M12.8 3.2l-1.1 1.1M4.3 11.7l-1.1 1.1" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <path d="M13.3 9.9A5.7 5.7 0 0 1 6.1 2.7a5.7 5.7 0 1 0 7.2 7.2z" />
    </svg>
  );
}
