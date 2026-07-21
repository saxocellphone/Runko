// The "Watch me work" director (playground-only), modeled on videogame
// tutorials: the world starts empty (showcase.ts's reset beat), the
// camera follows the action - auto-navigating to each beat's page - a
// tour cursor glides onto the element where the action lands, and a
// spotlight dims everything around it. The visitor can grab the
// controls at any time: navigating on their own drops the tour into
// free-roam (narration continues, camera stops) with a "Resume tour"
// button to hand control back. Beats mutate the live fake store, so
// every page stays real and clickable throughout.
import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { demoScene } from "../api/fake/transport";
import { SHOWCASE, type Beat } from "./showcase";

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

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

const reducedMotion = () =>
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// Poll for a beat's target element: refetches (bus-debounced ~400ms) and
// re-renders land after the mutation, so the thing to point at often
// does not exist yet when the beat fires.
async function resolveTarget(selector: string, timeoutMs: number): Promise<Element | null> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const el = document.querySelector(selector);
    if (el) return el;
    if (Date.now() >= deadline) return null;
    await sleep(150);
  }
}

export function ShowcaseDirector() {
  const [phase, setPhase] = useState<"idle" | "running" | "done">("idle");
  const [paused, setPaused] = useState(false);
  const [following, setFollowing] = useState(true);
  // Defaults to ×2 (SPEEDS[1]): the 5-minute script at conversational
  // pace - ×1 stays available for a slow walkthrough via the cycle.
  const [speedIdx, setSpeedIdx] = useState(1);
  const [minimized, setMinimized] = useState(false);
  const [beatIdx, setBeatIdx] = useState(-1); // beat whose caption shows
  const [progress, setProgress] = useState(0); // 0..1 of the runtime
  // The spotlight: a live selector (re-queried every tick - refetching
  // pages replace DOM nodes) and its current viewport rect.
  const [spotRect, setSpotRect] = useState<DOMRect | null>(null);
  const [cursorAt, setCursorAt] = useState<{ x: number; y: number } | null>(null);
  const [pulse, setPulse] = useState(0);

  const elapsedRef = useRef(0);
  const lastTickRef = useRef(0);
  const totalRef = useRef(0);
  const appliedRef = useRef(0);
  const queueRef = useRef<Promise<void>>(Promise.resolve());
  const pausedRef = useRef(false);
  const speedRef = useRef(1);
  const followRef = useRef(true);
  const spotSelectorRef = useRef<string | null>(null);
  const scrolledSelectorRef = useRef<string | null>(null);
  // Where the director just navigated (basename-relative pathname): the
  // next location change matching it is ours; anything else is the
  // visitor grabbing the controls. A counter can't do this job - beats
  // often "navigate" to the page they're already on, which produces no
  // location event and would leak the count.
  const dirTargetRef = useRef<string | null>(null);
  const pathnameRef = useRef("");
  pausedRef.current = paused;
  speedRef.current = SPEEDS[speedIdx] ?? 1;
  followRef.current = following;

  const navigate = useNavigate();
  const location = useLocation();
  pathnameRef.current = location.pathname;

  // ?watch=1 auto-starts - on first load AND when an in-app link lands
  // on the /watch entry later (the Director never remounts; it lives in
  // the layout).
  useEffect(() => {
    if (phase === "idle" && new URLSearchParams(location.search).has("watch")) {
      setPhase("running");
    }
  }, [location.search, phase]);

  // Free-roam detection: a location change we did not initiate means the
  // visitor took the controls - stop driving, keep narrating.
  useEffect(() => {
    if (dirTargetRef.current === location.pathname) {
      dirTargetRef.current = null;
      return;
    }
    if (phase === "running" && followRef.current) {
      setFollowing(false);
      spotSelectorRef.current = null;
      setSpotRect(null);
      setCursorAt(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname]);

  const dirNavigate = useCallback(
    (route: string) => {
      // Already there: navigating again would push history noise and no
      // location event (the target would go stale).
      if (pathnameRef.current === route) return;
      dirTargetRef.current = route;
      // Keep the query string: dropping it silently re-paces the show
      // (?t=) on the next clock-effect re-creation, and drops ?watch=1
      // a mid-show reload would want.
      navigate({ pathname: route, search: window.location.search });
    },
    [navigate],
  );

  const clearSpot = useCallback(() => {
    spotSelectorRef.current = null;
    setSpotRect(null);
  }, []);

  // Glide the cursor onto an element and pulse - the "look here" click.
  const cursorOnto = useCallback(async (el: Element) => {
    const r = el.getBoundingClientRect();
    setCursorAt({
      x: r.left + Math.min(r.width - 24, r.width * 0.7),
      y: r.top + Math.min(r.height - 10, r.height * 0.6),
    });
    await sleep(reducedMotion() ? 60 : 650);
    setPulse((p) => p + 1);
  }, []);

  const spotlightOnto = useCallback(
    async (selector: string, timeoutMs: number): Promise<Element | null> => {
      const el = await resolveTarget(selector, timeoutMs);
      if (!el || !followRef.current) return null;
      spotSelectorRef.current = selector;
      if (scrolledSelectorRef.current !== selector) {
        scrolledSelectorRef.current = selector;
        el.scrollIntoView({
          block: "center",
          behavior: reducedMotion() ? "auto" : "smooth",
        });
      }
      setSpotRect(el.getBoundingClientRect());
      await cursorOnto(el);
      return el;
    },
    [cursorOnto],
  );

  // One beat, played tutorial-style: move the camera, then either
  // point-then-act (pointer: "before" - clicking an existing control) or
  // act-then-point (default - look what just appeared). Camera work is
  // skipped for STALE beats - at high speed or short ?t= cuts, beats
  // fire faster than cinematics play, and the queue must never fall
  // behind the clock: superseded beats apply their mutation instantly
  // and the camera spends its time on the newest beat only.
  const playBeat = useCallback(
    async (beat: Beat, idx: number) => {
      const scene = demoScene();
      if (!scene) return;
      setBeatIdx(idx);
      const stale = () => appliedRef.current - 1 > idx;
      const focus = beat.focus;
      if (!stale() && followRef.current && focus?.route) {
        dirNavigate(focus.route);
        await sleep(300);
      }
      if (!focus?.selector) clearSpot();
      if (focus?.pointer === "before" && focus.selector && !stale() && followRef.current) {
        await spotlightOnto(focus.selector, 1200);
        await sleep(reducedMotion() ? 60 : 450);
        scene.mutate(beat.apply);
      } else {
        scene.mutate(beat.apply);
        if (focus?.selector && followRef.current && !stale()) {
          await spotlightOnto(focus.selector, 2200);
        }
      }
    },
    [dirNavigate, clearSpot, spotlightOnto],
  );

  // The clock: virtual time advances by WALL time * speed while unpaused
  // (fire counting drifts whenever paint cost or throttling delays the
  // interval - the spotlight's dim layer is not free); due beats chain
  // onto a queue so their camera work stays sequential.
  useEffect(() => {
    if (phase !== "running") return;
    // Captured ONCE per show: this effect re-creates whenever a
    // dependency's identity shifts (navigate), and re-reading the URL
    // then would re-pace a show whose query has changed.
    if (totalRef.current === 0) totalRef.current = runtimeMs();
    const total = totalRef.current;
    lastTickRef.current = Date.now();
    const timer = setInterval(() => {
      const now = Date.now();
      const wallDelta = Math.min(2000, now - lastTickRef.current);
      lastTickRef.current = now;
      if (pausedRef.current) return;
      elapsedRef.current = Math.min(total, elapsedRef.current + wallDelta * speedRef.current);
      const frac = elapsedRef.current / total;
      while (appliedRef.current < SHOWCASE.length && SHOWCASE[appliedRef.current]!.at <= frac) {
        const beat = SHOWCASE[appliedRef.current]!;
        const idx = appliedRef.current;
        appliedRef.current++;
        queueRef.current = queueRef.current.then(() => playBeat(beat, idx)).catch(() => {});
      }
      setProgress(frac);
      if (appliedRef.current >= SHOWCASE.length) {
        void queueRef.current.then(() => {
          setPhase("done");
          clearSpot();
          setCursorAt(null);
        });
      }
    }, TICK_MS);
    return () => clearInterval(timer);
  }, [phase, playBeat, clearSpot]);

  // Keep the spotlight glued to its element across re-renders, scrolls,
  // and layout shifts: re-query the live selector every tick - but only
  // publish a rect when it actually moved, or the dim layer repaints
  // every tick and the paint cost starves the timers.
  useEffect(() => {
    if (phase !== "running") return;
    const timer = setInterval(() => {
      const selector = spotSelectorRef.current;
      if (!selector) return;
      const el = document.querySelector(selector);
      if (!el) return;
      const r = el.getBoundingClientRect();
      setSpotRect((prev) =>
        prev &&
        Math.abs(prev.left - r.left) < 1 &&
        Math.abs(prev.top - r.top) < 1 &&
        Math.abs(prev.width - r.width) < 1 &&
        Math.abs(prev.height - r.height) < 1
          ? prev
          : r,
      );
    }, 200);
    return () => clearInterval(timer);
  }, [phase]);

  const skipToEnd = useCallback(() => {
    const scene = demoScene();
    if (!scene) return;
    queueRef.current = queueRef.current.then(() => {
      while (appliedRef.current < SHOWCASE.length) {
        const beat = SHOWCASE[appliedRef.current]!;
        appliedRef.current++;
        scene.mutate(beat.apply);
      }
      elapsedRef.current = totalRef.current || runtimeMs();
      setProgress(1);
      setBeatIdx(SHOWCASE.length - 1);
      setPhase("done");
      clearSpot();
      setCursorAt(null);
    });
  }, [clearSpot]);

  const resumeTour = useCallback(() => {
    setFollowing(true);
    followRef.current = true;
    const beat = beatIdx >= 0 ? SHOWCASE[beatIdx] : undefined;
    if (beat?.focus?.route) dirNavigate(beat.focus.route);
    if (beat?.focus?.selector) void spotlightOnto(beat.focus.selector, 2600);
  }, [beatIdx, dirNavigate, spotlightOnto]);

  if (phase === "idle") {
    return (
      <button className="showcase-pill" onClick={() => setPhase("running")}>
        <PlayGlyph /> Watch me work
      </button>
    );
  }

  const beat = beatIdx >= 0 ? SHOWCASE[beatIdx] : undefined;
  const speed = SPEEDS[speedIdx] ?? 1;
  const showOverlay = phase === "running" && following && spotRect !== null;

  return (
    <>
      {showOverlay && <TourDim rect={spotRect} />}
      {phase === "running" && following && cursorAt && (
        <div
          className={`tour-cursor${reducedMotion() ? " tour-cursor-instant" : ""}`}
          style={{ left: cursorAt.x, top: cursorAt.y }}
          aria-hidden
        >
          <span key={pulse} className="tour-cursor-pulse" />
          <CursorGlyph />
        </div>
      )}
      {minimized ? (
        <button
          className="showcase-pill"
          onClick={() => setMinimized(false)}
          title="Reopen the tour card"
        >
          <PlayGlyph />{" "}
          {phase === "done" ? "Tour finished" : `Watching… ${Math.round(progress * 100)}%`}
        </button>
      ) : (
        <aside className="showcase-card" aria-live="polite" aria-label="Watch me work tour">
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
              title="Minimize (the tour keeps running)"
              aria-label="Minimize"
            >
              –
            </button>
          </div>
          <div
            className="showcase-progress"
            role="progressbar"
            aria-valuenow={Math.round(progress * 100)}
            aria-valuemin={0}
            aria-valuemax={100}
          >
            <div className="showcase-progress-fill" style={{ width: `${progress * 100}%` }} />
          </div>
          {beat ? (
            <>
              <p className="showcase-beat-title">{beat.caption.title}</p>
              <p className="showcase-beat-body">{beat.caption.body}</p>
            </>
          ) : (
            <p className="showcase-beat-body">Starting…</p>
          )}
          {phase === "running" && !following && (
            <p className="showcase-freeroam">
              You have the controls — the story keeps going.{" "}
              <button className="showcase-linkbtn" onClick={resumeTour}>
                Resume tour
              </button>
            </p>
          )}
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
                <button className="btn btn-sm" onClick={skipToEnd} title="Apply the rest instantly">
                  Skip to end
                </button>
              </>
            ) : (
              <a className="btn btn-sm" href="/demo/changes?watch=1">
                ▶ Replay
              </a>
            )}
          </div>
        </aside>
      )}
    </>
  );
}

// The spotlight overlay: a border box on the target plus four dim
// panels tiling the rest of the viewport - bounded paint, unlike the
// classic one-element giant-box-shadow trick.
function TourDim({ rect }: { rect: DOMRect }) {
  const pad = 6;
  const l = rect.left - pad;
  const t = rect.top - pad;
  const w = rect.width + pad * 2;
  const h = rect.height + pad * 2;
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  return (
    <div className="tour-overlay" aria-hidden>
      <div className="tour-dim-panel" style={{ left: 0, top: 0, width: vw, height: Math.max(0, t) }} />
      <div className="tour-dim-panel" style={{ left: 0, top: t + h, width: vw, height: Math.max(0, vh - t - h) }} />
      <div className="tour-dim-panel" style={{ left: 0, top: t, width: Math.max(0, l), height: h }} />
      <div className="tour-dim-panel" style={{ left: l + w, top: t, width: Math.max(0, vw - l - w), height: h }} />
      <div className="tour-spot" style={{ left: l, top: t, width: w, height: h }} />
    </div>
  );
}

function PlayGlyph() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" aria-hidden>
      <path d="M4 2.5v11l9-5.5z" fill="currentColor" />
    </svg>
  );
}

function CursorGlyph() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden>
      <path
        d="M5 3l14 8.5-6.2 1.4L9.5 19z"
        fill="var(--accent)"
        stroke="#fff"
        strokeWidth="1.6"
        strokeLinejoin="round"
      />
    </svg>
  );
}
