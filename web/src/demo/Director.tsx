// The "Watch me work" director (playground-only): a floating card that
// plays showcase.ts's beat timeline against the live fake store. It
// narrates, keeps a progress bar, and offers a per-beat "watch this"
// link — but it NEVER navigates for the visitor; the whole app stays
// theirs to click while the show runs (useRpc's demo-bus subscription
// keeps every mounted page current).
import { useEffect, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { demoScene } from "../api/fake/transport";
import { SHOWCASE } from "./showcase";

// Runtime: ?t=<seconds> adjusts the whole show (beats are fractions);
// the 5-minute default is the guided-tour cut.
const DEFAULT_SECONDS = 300;
const TICK_MS = 250;
const SPEEDS = [1, 2, 4];

function runtimeMs(): number {
  const t = Number(new URLSearchParams(window.location.search).get("t"));
  const seconds = Number.isFinite(t) && t >= 5 && t <= 3600 ? t : DEFAULT_SECONDS;
  return seconds * 1000;
}

export function ShowcaseDirector() {
  const [phase, setPhase] = useState<"idle" | "running" | "done">("idle");
  const [paused, setPaused] = useState(false);
  const [speedIdx, setSpeedIdx] = useState(0);
  const [minimized, setMinimized] = useState(false);
  const [beatIdx, setBeatIdx] = useState(-1); // last applied beat
  const [progress, setProgress] = useState(0); // 0..1 of the runtime
  const elapsedRef = useRef(0); // virtual ms into the show
  const appliedRef = useRef(0); // beats applied so far
  const pausedRef = useRef(false);
  const speedRef = useRef(1);
  pausedRef.current = paused;
  speedRef.current = SPEEDS[speedIdx] ?? 1;

  // ?watch=1 auto-starts - on first load AND when an in-app link lands
  // on the /watch entry later (the Director never remounts across
  // navigation; it lives in the layout).
  const location = useLocation();
  useEffect(() => {
    if (phase === "idle" && new URLSearchParams(location.search).has("watch")) {
      setPhase("running");
    }
  }, [location.search, phase]);

  useEffect(() => {
    if (phase !== "running") return;
    const scene = demoScene();
    if (!scene) return;
    const total = runtimeMs();
    const timer = setInterval(() => {
      if (pausedRef.current) return;
      elapsedRef.current = Math.min(total, elapsedRef.current + TICK_MS * speedRef.current);
      const frac = elapsedRef.current / total;
      while (appliedRef.current < SHOWCASE.length && SHOWCASE[appliedRef.current]!.at <= frac) {
        const beat = SHOWCASE[appliedRef.current]!;
        scene.mutate(beat.apply);
        appliedRef.current++;
        setBeatIdx(appliedRef.current - 1);
      }
      setProgress(frac);
      if (appliedRef.current >= SHOWCASE.length) {
        setPhase("done");
      }
    }, TICK_MS);
    return () => clearInterval(timer);
  }, [phase]);

  if (phase === "idle") {
    return (
      <button className="showcase-pill" onClick={() => setPhase("running")}>
        <PlayGlyph /> Watch me work
      </button>
    );
  }

  const beat = beatIdx >= 0 ? SHOWCASE[beatIdx] : undefined;
  const speed = SPEEDS[speedIdx] ?? 1;

  if (minimized) {
    return (
      <button
        className="showcase-pill"
        onClick={() => setMinimized(false)}
        title="Reopen the showcase card"
      >
        <PlayGlyph /> {phase === "done" ? "Show finished" : `Watching… ${Math.round(progress * 100)}%`}
      </button>
    );
  }

  return (
    <aside className="showcase-card" aria-live="polite" aria-label="Watch me work showcase">
      <div className="showcase-head">
        <span className="showcase-title">
          <PlayGlyph /> Watch me work
        </span>
        <span className="showcase-count">
          {beatIdx + 1}/{SHOWCASE.length}
        </span>
        <button
          className="showcase-iconbtn"
          onClick={() => setMinimized(true)}
          title="Minimize (the show keeps running)"
          aria-label="Minimize"
        >
          –
        </button>
      </div>
      <div className="showcase-progress" role="progressbar" aria-valuenow={Math.round(progress * 100)} aria-valuemin={0} aria-valuemax={100}>
        <div className="showcase-progress-fill" style={{ width: `${progress * 100}%` }} />
      </div>
      {beat && (
        <>
          <p className="showcase-beat-title">{beat.caption.title}</p>
          <p className="showcase-beat-body">{beat.caption.body}</p>
          {beat.watchAt && phase === "running" && (
            <Link className="showcase-watchlink" to={beat.watchAt}>
              watch this →
            </Link>
          )}
        </>
      )}
      {!beat && <p className="showcase-beat-body">Starting…</p>}
      <div className="showcase-controls">
        {phase === "running" ? (
          <>
            <button className="btn btn-sm" onClick={() => setPaused((p) => !p)}>
              {paused ? "Resume" : "Pause"}
            </button>
            <button
              className="btn btn-sm"
              onClick={() => setSpeedIdx((i) => (i + 1) % SPEEDS.length)}
              title="Playback speed"
            >
              ×{speed}
            </button>
          </>
        ) : (
          <a className="btn btn-sm" href="/demo/changes?watch=1">
            ▶ Replay
          </a>
        )}
      </div>
    </aside>
  );
}

function PlayGlyph() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" aria-hidden>
      <path d="M4 2.5v11l9-5.5z" fill="currentColor" />
    </svg>
  );
}
