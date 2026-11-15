import { useEffect, useState } from "react";

/** `value` after it has stopped changing for `ms`; an empty string passes at once (clearing is not typing). */
export function useDebounced(value: string, ms: number): string {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), value === "" ? 0 : ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return value === "" ? "" : settled;
}
