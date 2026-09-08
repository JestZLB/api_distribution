// Multi-locale message catalog. The four per-locale bundles live in
// `./locales/<lang>.ts` and are loaded on demand by `./loader.ts`.
// Only `en-US` is imported synchronously here so the first paint can
// resolve translations without awaiting a dynamic chunk; the other
// three are dynamic-imported by `useT` and the LRU cache in
// `loader.ts` keeps the two most-recently-used bundles resident.

import {messages as enUSMessages} from './locales/en-US'
import type {Locale, MessageMap} from './types'

export {locales, localeLabels} from './types'
export type {Locale, MessageKey, MessageMap} from './types'

// `messages` keeps the legacy `Record<Locale, Record<string, string>>`
// shape (loose inner index signature) so non-React callers
// (`clipboard.ts`, the class-component `ErrorBoundary` render path)
// can keep their synchronous lookup chain — only `en-US` is
// populated at module load. Other locales are populated in place by
// `loader.ts` once their async bundle arrives; until then the
// fallback chain
// `messages[locale]?.[key] ?? messages['en-US'][key] ?? key`
// degrades gracefully to English.
//
// The per-locale bundles are typed strictly as
// `Record<MessageKey, string>` (see `./locales/<lang>.ts`), but we
// widen the inner index signature here so call sites that pass
// arbitrary string keys don't need per-call casts.
const _sync: Partial<Record<Locale, MessageMap>> = {'en-US': enUSMessages}
export const messages = _sync as Record<Locale, Record<string, string>>