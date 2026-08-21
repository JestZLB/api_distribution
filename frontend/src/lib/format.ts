export function formatNumber(n: number): string {
  if (!Number.isFinite(n)) return '—'
  if (Math.abs(n) >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (Math.abs(n) >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

export function formatLatency(ms: number): string {
  if (!ms || ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

export function formatBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`
}

type TranslateFn = (key: string, params?: Record<string, string | number>) => string

export function formatRelativeTime(unixNano: number, t?: TranslateFn): string {
  if (!unixNano) return ''
  const now = Date.now()
  const timeMs = unixNano / 1_000_000
  const diff = now - timeMs
  if (diff < 0) return t ? t('format.future') : 'in the future'
  const s = Math.floor(diff / 1000)
  if (s < 5) return t ? t('format.justNow') : 'just now'
  if (s < 60) return t ? t('format.secondsAgo', {s}) : `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return t ? t('format.minutesAgo', {m}) : `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return t ? t('format.hoursAgo', {h}) : `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 7) return t ? t('format.daysAgo', {d}) : `${d}d ago`
  return new Date(timeMs).toLocaleDateString()
}

export function formatDateTime(unixNano: number): string {
  if (!unixNano) return ''
  return new Date(unixNano / 1_000_000).toLocaleString()
}

export function maskKey(key: string, prefix = 'gw-'): string {
  if (!key) return ''
  if (key.length <= 8) return key
  return `${prefix}${key.slice(3, 7)}…${key.slice(-4)}`
}

// redactKey fully masks a secret for safe export / logging. Unlike
// maskKey (which keeps a small visible window for in-app UI display),
// redactKey never leaves any original key character on screen. Use this
// when writing secrets to a downloaded file or any place the result
// might be saved, screenshotted, or pasted elsewhere.
export function redactKey(key: string): string {
  if (!key) return ''
  return '*'.repeat(Math.max(8, key.length))
}