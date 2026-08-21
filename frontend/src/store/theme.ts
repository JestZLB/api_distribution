import {create} from 'zustand'

export type Theme = 'light' | 'dark' | 'system'

interface ThemeState {
  theme: Theme
  setTheme: (t: Theme) => void
}

export const useThemeStore = create<ThemeState>((set) => {
  const persisted = (typeof localStorage !== 'undefined'
    ? (localStorage.getItem('app:theme') as Theme | null)
    : null) ?? 'system'

  return {
    theme: persisted,
    setTheme: (t) => {
      localStorage.setItem('app:theme', t)
      set({theme: t})
    },
  }
})