import type {MessageInstance} from 'antd/es/message/interface'
import {messageApiRef} from '@/components/MessageHolder'

export type ToastTone = 'success' | 'error' | 'info' | 'warning'

interface ToastOptions {
  tone: ToastTone
  title: string
  description?: string
}

// Antd's `App.useApp().message` must be called from inside a React component,
// but the existing call sites trigger toasts from non-component code (event
// handlers, async utilities like copyToClipboard, etc.). The `MessageHolder`
// component captures the message API once on mount and writes it into the
// shared `messageApiRef` so the static `toast` helpers below can dispatch
// from anywhere — including class components (ErrorBoundary) that can't call
// the React `App.useApp()` hook.
//
// Earlier revisions kept a parallel module-level `messageApi` slot that was
// updated alongside `messageApiRef`. That double storage could drift (e.g.
// if a future caller updated one but not the other) and added two writes
// on every mount/unmount. The single `messageApiRef` source of truth is
// enough — all consumers below read it through `messageApiRef.current`.

// Kept for backwards compatibility with the previous two-storage layout:
// `MessageHolder` still calls `bindMessageApi(api)` on mount, and
// `bindMessageApi` now just routes that value into `messageApiRef` so any
// external caller that wired its own bind through this entry point keeps
// working. New code should read `messageApiRef.current` directly.
export function bindMessageApi(api: MessageInstance | null) {
  messageApiRef.current = api
}

function show({tone, title, description}: ToastOptions): void {
  const api = messageApiRef.current
  if (!api) return
  const content = description ? `${title}: ${description}` : title
  switch (tone) {
    case 'success':
      api.success(content)
      break
    case 'error':
      api.error(content)
      break
    case 'info':
      api.info(content)
      break
    case 'warning':
      api.warning(content)
      break
  }
}

export const toast = {
  success: (title: string, description?: string) =>
    show({tone: 'success', title, description}),
  error: (title: string, description?: string) =>
    show({tone: 'error', title, description}),
  info: (title: string, description?: string) =>
    show({tone: 'info', title, description}),
  warning: (title: string, description?: string) =>
    show({tone: 'warning', title, description}),
}