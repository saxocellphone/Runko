import { useCallback, useEffect, useRef, useState } from "react";
import { ConnectError } from "@connectrpc/connect";
import { usingDemoData } from "../api/client";
import { demoScene } from "../api/fake/transport";

export interface RpcState<T> {
  data: T | undefined;
  error: ConnectError | undefined;
  loading: boolean;
  reload: () => void;
}

// Minimal fetch-on-mount hook: re-runs when `key` changes, drops stale
// responses, exposes reload() for after mutations. Deliberately not a
// cache - every page owns its own data.
export function useRpc<T>(fn: () => Promise<T>, key: string): RpcState<T> {
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<ConnectError | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [generation, setGeneration] = useState(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    let stale = false;
    setLoading(true);
    setError(undefined);
    fnRef
      .current()
      .then((result) => {
        if (stale) return;
        setData(result);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (stale) return;
        setError(ConnectError.from(err));
        setLoading(false);
      });
    return () => {
      stale = true;
    };
  }, [key, generation]);

  // Under the demo the "Watch me work" director mutates the fake store on
  // a timeline; its bus poke makes every mounted page refetch, so the
  // whole app stays live wherever the visitor happens to be. Debounced
  // like useWatch's stream pokes so a burst of beats refetches once.
  useEffect(() => {
    if (!usingDemoData) return;
    const scene = demoScene();
    if (!scene) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onMutate = () => {
      clearTimeout(timer);
      timer = setTimeout(() => setGeneration((g) => g + 1), 400);
    };
    scene.bus.addEventListener("mutate", onMutate);
    return () => {
      clearTimeout(timer);
      scene.bus.removeEventListener("mutate", onMutate);
    };
  }, []);

  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  return { data, error, loading, reload };
}
