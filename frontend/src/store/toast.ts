import type {MessageInstance} from 'antd/es/message/interface'

export type ToastTone = 'success' | 'error' | 'info' | 'warning'

interface ToastOptions {
  tone: ToastTone
  title: string
  description?: string
}

// Antd's `App.useApp().message` must be called from inside a React component,
// but the existing call sites trigger toasts from non-component code (event
// handlers, async utilities like copyToClipboard, etc.). `MessageHolder`
// captures the message API once on mount and stores it here so the static
// `toast` helpers below can dispatch from anywhere.
let messageApi: MessageInstance | null = null

export function bindMessageApi(api: MessageInstance | null) {
  messageApi = api
}

function show({tone, title, description}: ToastOptions): void {
  const api = messageApi
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