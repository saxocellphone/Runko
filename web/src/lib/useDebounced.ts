import { useEffect, useState } from "react";

// Returns `value` after it has stayed unchanged for `ms` - the standard
// debounce for as-you-type server calls (the create-project preview).
export function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return debounced;
}
