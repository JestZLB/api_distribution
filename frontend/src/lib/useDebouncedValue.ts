import {useEffect, useRef, useState} from 'react'

/**
 * Returns a value that only updates after it has remained stable for
 * `delayMs`. Useful for throttling expensive filters (e.g. searching
 * 200 logs) so each keystroke doesn't trigger a full re-filter.
 *
 * Properties:
 * - On mount, returns the initial value immediately (no flash of stale
 *   data while the first timer ticks).
 * - If the component unmounts while a debounce is pending, the
 *   pending value is NOT committed to state (avoids React's
 *   "setState on unmounted component" warning). Callers that care
 *   about the last value should read it from the source-of-truth
 *   `value` prop, not from this hook's return value, after unmount.
 * - Falls back to `globalThis.setTimeout` in non-browser environments
 *   so the hook stays safe under SSR / unit tests.
 * - A non-positive `delayMs` commits synchronously and skips the
 *   timer entirely, which is the documented "no debounce" signal.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState<T>(value)
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])
  useEffect(() => {
    if (delayMs <= 0) {
      // A non-positive delay is a legitimate "no debounce" signal.
      // Commit synchronously and skip the timer entirely.
      setDebounced(value)
      return
    }
    const setTimeoutFn: typeof setTimeout =
      typeof window === 'undefined' ? globalThis.setTimeout : window.setTimeout
    const clearTimeoutFn: typeof clearTimeout =
      typeof window === 'undefined'
        ? globalThis.clearTimeout
        : window.clearTimeout
    const timer = setTimeoutFn(() => {
      if (mountedRef.current) setDebounced(value)
    }, delayMs)
    return () => clearTimeoutFn(timer)
  }, [value, delayMs])
  return debounced
}
