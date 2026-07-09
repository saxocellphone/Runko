import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import {
  authUser,
  createOrg,
  currentOrg,
  fetchOrgs,
  onDemoRoute,
  signedIn,
  signOut,
  switchOrg,
  usingDemoData,
  type OrgInfo,
} from "../api/client";

const nav = [
  { to: "/changes", label: "Changes", icon: ChangesIcon },
  { to: "/browse", label: "Browse", icon: BrowseIcon },
  { to: "/projects", label: "Projects", icon: ProjectsIcon },
  { to: "/workspaces", label: "Workspaces", icon: WorkspacesIcon },
  { to: "/search", label: "Search", icon: SearchIcon },
  { to: "/settings", label: "Settings", icon: SettingsIcon },
];

export function Layout() {
  const [theme, setTheme] = useState<string>(
    () => document.documentElement.dataset.theme ?? "light",
  );
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("runko-theme", theme);
  }, [theme]);

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <BrandMark />
          Runko
        </div>
        {!usingDemoData && signedIn && <OrgSwitcher />}
        {nav.map(({ to, label, icon: Icon }) => (
          <NavLink key={to} to={to} className="nav-link">
            <Icon />
            {label}
          </NavLink>
        ))}
        <div className="sidebar-foot">
          {onDemoRoute ? (
            <div className="demo-badge">
              Playground — sample data, in-browser · <a href="/changes">exit</a>
            </div>
          ) : usingDemoData ? (
            <div className="demo-badge">
              Playground data — set VITE_RUNKO_URL to connect to a runkod
            </div>
          ) : (
            <div className="demo-badge">
              {authUser ? (
                <>
                  Signed in as <strong>{authUser}</strong>
                </>
              ) : (
                <>Live{signedIn ? ", anonymous" : ""}</>
              )}
            </div>
          )}
          {!onDemoRoute && !usingDemoData && signedIn && (
            <button className="btn btn-sm theme-toggle" onClick={() => signOut()}>
              Sign out
            </button>
          )}
          <button
            className="btn btn-sm theme-toggle"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          >
            {theme === "dark" ? "Light" : "Dark"} theme
          </button>
        </div>
      </aside>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}

// OrgSwitcher: pick which org this browser works in (multi-org,
// runkod/orghub.go - each org owns its own repo, mounted at /o/<name>/).
// "" = the shared default org at the root mount. Switching reloads so
// every Connect client rebinds its transport base.
function OrgSwitcher() {
  const [orgs, setOrgs] = useState<OrgInfo[]>([]);
  useEffect(() => {
    void fetchOrgs().then(setOrgs);
  }, []);

  // Stale selection (org gone / membership revoked): keep it visible so
  // the user can switch away rather than being stuck. Sessions are
  // org-scoped: the listing is exactly this account's memberships.
  const known = orgs.some((o) => o.name === currentOrg);

  // One org and you're in it: nothing to switch, no selector (org
  // creation lives on the sign-up form). It appears when a second
  // membership does.
  if (orgs.length === 0 || (orgs.length === 1 && known)) return null;

  const onChange = async (value: string) => {
    if (value === "__new__") {
      const name = window.prompt("New org name (lowercase letters, digits, dashes):");
      if (!name) return;
      try {
        const created = await createOrg(name.trim());
        switchOrg(created.name);
      } catch (err) {
        window.alert(err instanceof Error ? err.message : String(err));
      }
      return;
    }
    switchOrg(value);
  };

  return (
    <select
      className="org-select"
      aria-label="Organization"
      value={currentOrg}
      onChange={(e) => void onChange(e.target.value)}
    >
      {orgs.map((o) => (
        <option key={o.name} value={o.name}>
          {o.name}
        </option>
      ))}
      {!known && currentOrg && <option value={currentOrg}>{currentOrg} (unavailable?)</option>}
      <option value="__new__">＋ New org…</option>
    </select>
  );
}

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
