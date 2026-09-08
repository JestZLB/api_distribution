import {useEffect, useMemo, useState} from 'react'
import {useThemeStore, type Theme} from '@/store/theme'

type ResolvedMode = 'light' | 'dark'

/**
 * Resolve the user's theme preference into a concrete light/dark mode.
 * `system` follows `prefers-color-scheme` and re-renders when the OS
 * preference changes (otherwise a Settings switch from light→system
 * would leave the chart painted in the old palette).
 */
function useResolvedMode(theme: Theme): ResolvedMode {
  const [resolved, setResolved] = useState<ResolvedMode>('light')
  useEffect(() => {
    if (theme !== 'system') {
      setResolved(theme)
      return
    }
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const update = () => setResolved(mq.matches ? 'dark' : 'light')
    update()
    mq.addEventListener('change', update)
    return () => mq.removeEventListener('change', update)
  }, [theme])
  return resolved
}

/**
 * Resolve a single `--ant-*` CSS variable on the ConfigProvider root
 * to a concrete string. Returns `fallback` when the variable is unset
 * or the root element is not yet mounted.
 *
 * G2 v5 paints into a `<canvas>`, so CSS-var values are NOT resolved
 * by the canvas context — canvas only takes literal hex/rgb. Charts
 * therefore must resolve tokens to concrete strings here, not embed
 * `var(--ant-color-…)` in the chart config.
 */
function readToken(name: string, fallback: string): string {
  const root = document.querySelector('.api-distribution') as HTMLElement | null
  if (!root) return fallback
  const v = getComputedStyle(root).getPropertyValue(name).trim()
  return v || fallback
}

export interface DesignTokens {
  /** antd success token (green) — used for chart series / markers. */
  successHex: string
  /** antd error token (red) — used for chart series / markers. */
  errorHex: string
  /** Axis / label fill colour. */
  axis: string
  /** Grid line stroke colour. */
  grid: string
}

/**
 * useDesignTokens subscribes to the active theme (and the OS
 * `prefers-color-scheme` when the theme is `system`) and returns the
 * concrete chart-friendly tokens for the current light/dark palette.
 *
 * Re-running `getComputedStyle` on theme change is intentional: the
 * previous inline `useMemo(..., [])` snapshot would have kept stale
 * `--ant-*` values after a Settings theme switch until the chart
 * unmounted, leaving axis / grid / series colours visibly out of sync
 * with the rest of the UI.
 */
export function useDesignTokens(): DesignTokens {
  const theme = useThemeStore((s) => s.theme)
  const resolved = useResolvedMode(theme)
  return useMemo(
    () => ({
      successHex: readToken('--ant-color-success', '#52c41a'),
      errorHex: readToken('--ant-color-error', '#ff4d4f'),
      axis: readToken('--ant-color-text-tertiary', 'rgba(0, 0, 0, 0.35)'),
      grid: readToken('--ant-color-fill-secondary', 'rgba(0, 0, 0, 0.06)'),
    }),
    [resolved],
  )
}
