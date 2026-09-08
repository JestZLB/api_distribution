import {lazy, Suspense, useEffect, useMemo, useState} from 'react'
import {HashRouter, Route, Routes, Navigate} from 'react-router-dom'
import {App as AntApp, ConfigProvider} from 'antd'
import {AppShell} from '@/components/layout/AppShell'
import {Dashboard} from '@/pages/Dashboard'
import {Logs} from '@/pages/Logs'
import {Settings} from '@/pages/Settings'
import {ErrorBoundary} from '@/components/ErrorBoundary'
import {BootErrorScreen} from '@/components/BootErrorScreen'
import {useThemeStore} from '@/store/theme'
import {useLocaleStore} from '@/store/locale'
import {useConfigStore} from '@/store/config'
import * as WailsApp from '../wailsjs/go/main/App'
import {getAntdTheme} from '@/lib/antdTheme'
import {MessageHolder} from '@/components/MessageHolder'

// F-019: defer the Models / Providers bundles until the user
// navigates to them for the first time. The Dashboard is the
// landing page and already lazy-imports its own chart chunks,
// while Logs and Settings are tiny enough to keep eager.
//
// Both Models and Providers export their page components as named
// exports, so each lazy() thunk re-shapes the dynamic import into
// `{ default }` form — otherwise React.lazy rejects the module.
const Models = lazy(() => import('@/pages/Models').then((m) => ({default: m.Models})))
const Providers = lazy(() => import('@/pages/Providers').then((m) => ({default: m.Providers})))

type ResolvedMode = 'light' | 'dark'

// Determine whether the user's theme preference is currently light or dark.
// "system" follows prefers-color-scheme and re-renders when the OS
// preference changes.
function useResolvedMode(theme: 'light' | 'dark' | 'system'): ResolvedMode {
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

export default function App() {
  const theme = useThemeStore((s) => s.theme)
  const loadConfig = useConfigStore((s) => s.load)
  const resolvedMode = useResolvedMode(theme)

  useEffect(() => {
    const root = document.documentElement
    if (resolvedMode === 'dark') root.classList.add('dark')
    else root.classList.remove('dark')
  }, [resolvedMode])

  useEffect(() => {
    loadConfig().catch(() => {
      /* bootStatus already set to 'failed' by the store */
    })
  }, [loadConfig])

  // Mirror the persisted UI language to the backend on boot so the
  // OS tray menu matches the last-selected language. Fire-and-forget.
  useEffect(() => {
    const locale = useLocaleStore.getState().locale
    void WailsApp.SetLocale(locale).catch(() => {
      /* best-effort sync */
    })
  }, [])

  const antdTheme = useMemo(() => getAntdTheme(resolvedMode), [resolvedMode])

  return (
    <ConfigProvider theme={antdTheme}>
      <AntApp>
        <HashRouter>
          <ErrorBoundary>
            <BootErrorScreen />
            <MessageHolder />
            <Suspense fallback={<div className="p-6 text-fg-muted text-sm" />}>
              <Routes>
                <Route element={<AppShell/>}>
                  <Route index element={<Navigate to="/dashboard" replace/>}/>
                  <Route path="/dashboard" element={<Dashboard/>}/>
                  <Route path="/providers" element={<Providers/>}/>
                  <Route path="/models" element={<Models/>}/>
                  <Route path="/logs" element={<Logs/>}/>
                  <Route path="/settings" element={<Settings/>}/>
                </Route>
              </Routes>
            </Suspense>
          </ErrorBoundary>
        </HashRouter>
      </AntApp>
    </ConfigProvider>
  )
}