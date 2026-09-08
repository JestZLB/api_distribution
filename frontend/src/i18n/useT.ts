import {useCallback, useEffect, useRef, useState} from 'react'
import {useLocaleStore} from '@/store/locale'
import {messages} from '@/i18n/locales'
import {loadLocale} from '@/i18n/loader'
import type {MessageMap} from '@/i18n/types'

// Translation hook. Resolves `key` in the active locale, falls back
// to the synchronously-loaded `en-US` bundle (so the first paint
// resolves without awaiting a dynamic import), and finally returns
// the raw key when no translation is available. Placeholders of the
// form `{{name}}` in the translated string are replaced from the
// optional `params` map.
//
// Other locales are dynamic-imported via `loader.ts` (which keeps an
// LRU=2 cache so re-visiting a recently-used language doesn't pay
// the import cost twice). Until that bundle arrives, the rendered
// text degrades to English — never to the raw key.
//
// The returned `t` is a stable reference (useCallback with `[]` deps)
// so downstream `useMemo`s that take `t` as a dependency don't
// invalidate on every render / locale swap. Inside the callback we
// read `bundle` and `locale` from the latest store / state via
// `useLocaleStore.getState()` and a ref respectively, instead of
// closing over the render's values.
export function useT() {
  const locale = useLocaleStore((s) => s.locale)
  const [bundle, setBundle] = useState<MessageMap | null>(null)
  // Mirror `bundle` into a ref so the stable `t` callback can read
  // the latest bundle without needing to depend on it (and thus
  // re-create itself every time a new bundle arrives).
  const bundleRef = useRef<MessageMap | null>(null)
  bundleRef.current = bundle

  // Re-load whenever the active locale changes. `useEffect` (rather
  // than `useMemo`) keeps the `await` off the synchronous render path
  // and lets us drop an in-flight load if the user toggles locales
  // before the previous one resolves.
  useEffect(() => {
    let cancelled = false
    // Always seed the bundle for the active locale. For `en-US` the
    // loader returns the synchronously-imported object, so this is a
    // near-instant resolve; for the other three locales it's a real
    // dynamic import.
    loadLocale(locale)
      .then((m) => {
        if (!cancelled) setBundle(m)
      })
      .catch(() => {
        // On load failure (offline chunk, etc.) keep the previous
        // bundle (or null for the very first render) — the sync
        // fallback chain in `t` will degrade to English either way.
        if (!cancelled) setBundle(null)
      })
    return () => {
      cancelled = true
    }
  }, [locale])

  return useCallback((key: string, params?: Record<string, string | number>): string => {
    // Preference order:
    //   1. Async bundle for the active locale (latest translation)
    //   2. Sync-en-US bundle as a guaranteed first-paint fallback
    //   3. The raw key (last resort, only triggers if the locale
    //      somehow has neither its own bundle nor the en-US sync
    //      copy — should be unreachable now that all four locales
    //      fully cover `MessageKey`).
    //
    // `bundle` is the strict `MessageMap` so `bundle?.[key]` needs a
    // cast — keys coming from arbitrary component code aren't
    // guaranteed to be `MessageKey` literals. We read the live values
    // through the ref + getState() so this callback can keep its
    // identity stable across renders.
    const liveLocale = useLocaleStore.getState().locale
    const liveBundle = bundleRef.current
    const fromBundle = liveBundle ? (liveBundle[key as keyof MessageMap] as string | undefined) : undefined
    const raw: string =
      fromBundle ?? messages[liveLocale]?.[key] ?? messages['en-US'][key] ?? key
    if (!params) return raw
    return raw.replace(/\{\{(\w+)\}\}/g, (_match: string, name: string) => {
      const v = params[name]
      return v === undefined || v === null ? `{{${name}}}` : String(v)
    })
  }, [])
}