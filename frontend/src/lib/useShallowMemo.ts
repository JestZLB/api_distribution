import {useRef} from 'react'

/**
 * `useMemo`-like helper that only recomputes the factory result when
 * one of `deps` has changed by *value* (shallow equality on a flat
 * array), not by reference.
 *
 * React's built-in `useMemo` compares deps with `Object.is`, so any
 * new array / object passed as a dependency invalidates the memo on
 * every render. For example, `Object.keys(stats.requestsByHourByModel)`
 * is a fresh array on every render — even when nothing actually
 * changed inside the source data — which defeats the entire purpose of
 * memoization in the 2s stats heartbeat path.
 *
 * Usage:
 * ```ts
 * const models = useShallowMemo(
 *   () => Object.keys(stats.requestsByHourByModel ?? {}),
 *   [stats.requestsByHourByModel],
 *   (a, b) => a.length === b.length && a.every((v, i) => v === b[i]),
 * )
 * ```
 *
 * The `isEqual` predicate is optional; without it the hook falls back
 * to a shallow array compare (`length` + every-index equality) which
 * is the right behaviour for the typical "list of keys" case.
 */
export function useShallowMemo<T>(
  factory: () => T,
  deps: ReadonlyArray<unknown>,
  isEqual?: (prev: ReadonlyArray<unknown>, next: ReadonlyArray<unknown>) => boolean,
): T {
  const cacheRef = useRef<{deps: ReadonlyArray<unknown>; value: T} | null>(null)
  const prev = cacheRef.current
  const equal = prev
    ? isEqual
      ? isEqual(prev.deps, deps)
      : shallowArrayEqual(prev.deps, deps)
    : false
  if (!prev || !equal) {
    const next = factory()
    cacheRef.current = {deps, value: next}
    return next
  }
  return prev.value
}

function shallowArrayEqual(
  a: ReadonlyArray<unknown>,
  b: ReadonlyArray<unknown>,
): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false
  }
  return true
}