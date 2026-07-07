import { useCallback, useEffect, useRef, useState } from "react";
import { ConnectError } from "@connectrpc/connect";

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

  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  return { data, error, loading, reload };
}
