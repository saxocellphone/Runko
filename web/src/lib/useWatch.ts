import { useEffect, useRef, useState } from "react";
import { workspacesClient } from "../api/client";

export type WatchState = "connecting" | "live" | "offline";

// useWatch subscribes to WatchWorkspace (§12.6) - this surface's one
// server-streaming RPC. Frames are pokes, never data: every event frame
// (and every successful REconnect - anything could have happened while
// offline) fires onPoke, debounced, and the caller refetches through its
// own useRpc hooks. The stream carrying no state is what keeps useRpc's
// no-cache philosophy intact - kill this hook and the page degrades to
// plain fetch-on-mount. Reconnects with exponential backoff (1s -> 30s);
// the returned state drives the live dot. An empty workspaceId disables
// the hook (the public-browse page renders its auth error without a
// reconnect loop hammering 401s).
export function useWatch(workspaceId: string, onPoke: () => void): WatchState {
  const [state, setState] = useState<WatchState>(workspaceId ? "connecting" : "offline");
  const onPokeRef = useRef(onPoke);
  onPokeRef.current = onPoke;

  useEffect(() => {
    if (!workspaceId) return;
    const abort = new AbortController();
    let stopped = false;
    let backoff = 1000;
    let debounce: ReturnType<typeof setTimeout> | undefined;
    let everConnected = false;

    // One trailing-edge debounce across pokes: a land (change_landed +
    // workspace_closed in quick succession) refetches once, not twice.
    const poke = () => {
      if (debounce) return;
      debounce = setTimeout(() => {
        debounce = undefined;
        if (!stopped) onPokeRef.current();
      }, 400);
    };

    const run = async () => {
      while (!stopped) {
        try {
          setState("connecting");
          let first = true;
          for await (const frame of workspacesClient.watchWorkspace(
            { id: workspaceId },
            { signal: abort.signal },
          )) {
            if (first) {
              first = false;
              backoff = 1000;
              setState("live");
              if (everConnected) poke(); // refetch what the gap may have hidden
              everConnected = true;
            }
            if (frame.event) poke();
          }
        } catch {
          // Aborted (unmount) or transport failure - the loop below decides.
        }
        if (stopped) return;
        setState("offline");
        await new Promise((resolve) => setTimeout(resolve, backoff));
        backoff = Math.min(backoff * 2, 30_000);
      }
    };
    void run();

    return () => {
      stopped = true;
      if (debounce) clearTimeout(debounce);
      abort.abort();
    };
  }, [workspaceId]);

  return state;
}
