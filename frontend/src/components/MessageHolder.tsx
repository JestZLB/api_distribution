import {App} from 'antd'
import {useEffect} from 'react'
import type {MessageInstance} from 'antd/es/message/interface'
import {bindMessageApi} from '@/store/toast'

// Module-level ref that exposes the bound antd message API to non-hook
// contexts (e.g., ErrorBoundary's componentDidCatch) without forcing them
// to be rewritten as React components or to consume a Context.
export const messageApiRef: {current: MessageInstance | null} = {current: null}

// Captures the antd `App.useApp().message` instance once and binds it to the
// module-level singleton in `@/store/toast`. Must be rendered inside <App />
// from antd so the context is available.
export function MessageHolder() {
  const {message} = App.useApp()
  useEffect(() => {
    messageApiRef.current = message
    bindMessageApi(message)
    return () => {
      messageApiRef.current = null
      bindMessageApi(null)
    }
  }, [message])
  return null
}