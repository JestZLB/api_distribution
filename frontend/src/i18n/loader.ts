// Dynamic locale loader with a small LRU cache. The four per-locale
// bundles in `./locales/` are code-split so they don't bloat the
// initial chunk — only `en-US` is imported synchronously by
// `./locales.ts` to keep first paint unblocked.
//
// LRU semantics:
//   - `capacity = 2`: keep the two most-recently-resolved bundles.
//   - Touching an entry (re-requesting the same locale) promotes it
//     to "most-recently-used" so the other one becomes the eviction
//     candidate.
//   - When over capacity, drop the oldest reference so the GC can
//     reclaim the chunk. We do NOT explicitly clear the in-place
//     `messages[locale]` slot: the loader's cached `MessageMap` is
//     the only "live" reference; the in-place slot is a courtesy
//     fallback for non-React callers (`clipboard.ts`,
//     `ErrorBoundary`), and updating it again would re-introduce the
//     memory bloat this loader exists to avoid.

import {messages, locales} from './locales'
import {messages as enUSMessages} from './locales/en-US'
import type {Locale, MessageMap} from './types'

const LRU_CAPACITY = 2

// Use a `Map` so insertion order is preserved (JS Maps iterate in
// insertion order, which gives us the LRU ordering for free).
const cache = new Map<Locale, MessageMap>()

// Pre-seed the cache with the synchronously-loaded `en-US` bundle so
// it's a guaranteed cache hit and never falls out. We import the
// strict per-locale bundle directly (rather than going through the
// loose `messages['en-US']` export) so the `MessageMap` type
// matches the cache value type.
cache.set('en-US', enUSMessages)

function touch(locale: Locale, bundle: MessageMap): void {
  // Map preserves insertion order. Re-inserting moves the entry to
  // the end (most-recently-used).
  cache.delete(locale)
  cache.set(locale, bundle)
}

function evictIfNeeded(): void {
  while (cache.size > LRU_CAPACITY) {
    const oldest = cache.keys().next().value
    if (oldest === undefined) break
    cache.delete(oldest)
  }
}

export async function loadLocale(locale: Locale): Promise<MessageMap> {
  const cached = cache.get(locale)
  if (cached) {
    touch(locale, cached)
    return cached
  }

  // Dynamic import per locale — Vite code-splits each branch as a
  // separate chunk. We avoid a template-literal `import(...)` so the
  // bundler doesn't try to glob the `./locales/` directory.
  const mod = await importBundle(locale)
  const bundle = mod.messages
  cache.set(locale, bundle)
  // Also publish to the legacy synchronous slot so non-React
  // callers (clipboard.ts / ErrorBoundary) can resolve the new
  // locale without going through React state.
  messages[locale] = bundle
  evictIfNeeded()
  return bundle
}

async function importBundle(locale: Locale): Promise<{messages: MessageMap}> {
  switch (locale) {
    case 'zh-CN':
      return await import('./locales/zh-CN')
    case 'ja-JP':
      return await import('./locales/ja-JP')
    case 'ko-KR':
      return await import('./locales/ko-KR')
    case 'en-US':
    default:
      return await import('./locales/en-US')
  }
}

// Test / debug only: snapshot of the current LRU contents. Not
// referenced by the React tree; safe to ignore in production.
export function _debugLru(): Locale[] {
  return Array.from(cache.keys())
}

// Keep `locales` referenced so unused-import linters don't drop it —
// the `Locale` parameter on `loadLocale` already constrains it, but
// we keep the re-export visible in case future helper APIs need it.
void locales