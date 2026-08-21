import {create} from 'zustand'
import * as App from '../../wailsjs/go/main/App'
import {locales, type Locale} from '@/i18n/locales'

interface LocaleState {
  locale: Locale
  setLocale: (l: Locale) => void
}

// Pull a previously selected locale from localStorage when present and
// valid; otherwise fall back to the closest match for the user's
// browser language. Persistence mirrors the theme store so the
// choice survives restarts.
function detectInitial(): Locale {
  if (typeof localStorage !== 'undefined') {
    const saved = localStorage.getItem('app:locale') as Locale | null
    if (saved && (locales as readonly string[]).includes(saved)) {
      return saved
    }
  }
  if (typeof navigator !== 'undefined') {
    const langs = navigator.languages ?? [navigator.language]
    for (const raw of langs) {
      if (!raw) continue
      const lower = raw.toLowerCase()
      if (lower.startsWith('zh')) return 'zh-CN'
      if (lower.startsWith('ja')) return 'ja-JP'
      if (lower.startsWith('ko')) return 'ko-KR'
      if (lower.startsWith('en')) return 'en-US'
    }
  }
  return 'en-US'
}

export const useLocaleStore = create<LocaleState>((set) => ({
  locale: detectInitial(),
  setLocale: (l) => {
    if (typeof localStorage !== 'undefined') {
      try {
        localStorage.setItem('app:locale', l)
      } catch {
        // localStorage may be unavailable (e.g. SSR), ignore.
      }
    }
    // Push the selected language to the backend so the OS tray menu
    // items are localized. Fire-and-forget: a failed call should not
    // block the UI language switch.
    void App.SetLocale(l).catch(() => {
      /* backend sync is best-effort */
    })
    set({locale: l})
  },
}));