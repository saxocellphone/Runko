import { useEffect, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import { liveTokenMissing, onDemoRoute, setRunkoToken, usingDemoData } from "../api/client";

const nav = [
  { to: "/changes", label: "Changes", icon: ChangesIcon },
  { to: "/browse", label: "Browse", icon: BrowseIcon },
  { to: "/projects", label: "Projects", icon: ProjectsIcon },
  { to: "/graph", label: "Graph", icon: GraphIcon },
  { to: "/workspaces", label: "Workspaces", icon: WorkspacesIcon },
  { to: "/search", label: "Search", icon: SearchIcon },
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
        {nav.map(({ to, label, icon: Icon }) => (
          <NavLink key={to} to={to} className="nav-link">
            <Icon />
            {label}
          </NavLink>
        ))}
        <div className="sidebar-foot">
          {onDemoRoute ? (
            <div className="demo-badge">
              Demo data — <a href="/changes">exit demo</a>
            </div>
          ) : usingDemoData ? (
            <div className="demo-badge">
              Demo data — set VITE_RUNKO_URL to connect to a runkod
            </div>
          ) : (
            <div className="demo-badge">
              {liveTokenMissing ? (
                <>
                  Live, no token — <a href="/demo/changes">view demo</a>
                </>
              ) : (
                <>
                  Live — <a href="/demo/changes">view demo</a>
                </>
              )}
            </div>
          )}
          {!onDemoRoute && !usingDemoData && (
            // Per-browser deploy token (localStorage): the public bundle
            // ships without one - see client.ts. prompt() keeps this a
            // one-liner until a real settings surface exists.
            <button
              className="btn btn-sm theme-toggle"
              onClick={() => {
                const t = window.prompt(
                  "runkod deploy token (stored only in this browser; empty clears)",
                );
                if (t !== null) setRunkoToken(t.trim());
              }}
            >
              {liveTokenMissing ? "Set token" : "Change token"}
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

function GraphIcon() {
  return (
    <svg {...iconProps} aria-hidden>
      <circle cx="8" cy="3" r="1.8" />
      <circle cx="4" cy="13" r="1.8" />
      <circle cx="12" cy="13" r="1.8" />
      <line x1="7" y1="4.6" x2="4.8" y2="11.4" />
      <line x1="9" y1="4.6" x2="11.2" y2="11.4" />
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
