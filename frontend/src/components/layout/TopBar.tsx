import {useEffect, useMemo, useState, type ReactElement} from 'react'
import {useLocation} from 'react-router-dom'
import {Button, Tooltip} from 'antd'
import {LuMenu, LuMonitor, LuMoon, LuServer, LuSun} from 'react-icons/lu'
import {useThemeStore, type Theme} from '@/store/theme'
import {useConfigStore} from '@/store/config'
import {cn} from '@/lib/cn'
import {useT} from '@/i18n/useT'
import {LanguageSelector} from '@/components/layout/LanguageSelector'
import {StatusDot} from '@/components/ui/StatusDot'

const titles: Record<string, {titleKey: string; crumbKey: string}> = {
  '/dashboard': {titleKey: 'topbar.title.dashboard', crumbKey: 'topbar.crumb.dashboard'},
  '/providers': {titleKey: 'topbar.title.providers', crumbKey: 'topbar.crumb.providers'},
  '/models': {titleKey: 'topbar.title.models', crumbKey: 'topbar.crumb.models'},
  '/logs': {titleKey: 'topbar.title.logs', crumbKey: 'topbar.crumb.logs'},
  '/settings': {titleKey: 'topbar.title.settings', crumbKey: 'topbar.crumb.settings'},
}

const cycle: Theme[] = ['light', 'dark', 'system']

const themeIcon: Record<Theme, ReactElement> = {
  light: <LuSun className="size-4" aria-hidden />,
  dark: <LuMoon className="size-4" aria-hidden />,
  system: <LuMonitor className="size-4" aria-hidden />,
}

interface TopBarProps {
  /** Whether the sidebar is currently collapsed (drives the toggle icon). */
  collapsed?: boolean
  /** Toggle the sidebar collapsed state. */
  onToggle?: () => void
}

export function TopBar({collapsed = false, onToggle}: TopBarProps) {
  const location = useLocation()
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const serverStatus = useConfigStore((s) => s.serverStatus)
  const t = useT()
  const [resolved, setResolved] = useState<'light' | 'dark'>('light')

  const running = serverStatus?.running ?? false
  const port = serverStatus?.port

  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const update = () => {
      const dark = theme === 'dark' || (theme === 'system' && mq.matches)
      setResolved(dark ? 'dark' : 'light')
    }
    update()
    mq.addEventListener('change', update)
    return () => mq.removeEventListener('change', update)
  }, [theme])

  const meta = titles[location.pathname] ?? {titleKey: 'topbar.title.dashboard', crumbKey: 'topbar.crumb.dashboard'}
  const crumb = t(meta.crumbKey)
  const title = t(meta.titleKey)

  function cycleTheme() {
    const idx = cycle.indexOf(theme)
    const next = cycle[(idx + 1) % cycle.length]
    setTheme(next)
  }

  const statusLabel = useMemo(
    () => (running ? t('sidebar.running') : t('sidebar.stopped')),
    [running, t],
  )

  return (
    <header
      className={cn(
        'flex items-center justify-between gap-4 h-16 px-6',
        'bg-bg-elevated border-b border-border',
      )}
    >
      <div className="flex items-center gap-4 min-w-0">
        <Button
          type="text"
          shape="circle"
          aria-label={t('topbar.navMenuLabel')}
          onClick={onToggle}
          icon={<LuMenu className="size-5" />}
          size="large"
        />
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-xs uppercase tracking-wider text-fg-subtle">
            {crumb}
          </span>
          <span className="text-fg-subtle/60">/</span>
          <h1 className="text-base font-semibold text-fg truncate">{title}</h1>
        </div>
      </div>

      <div className="flex items-center gap-3">
        <Tooltip
          title={`${statusLabel}${port ? ' · ' + t('sidebar.port', {port}) : ''}`}
        >
          <div
            className={cn(
              'inline-flex items-center gap-2 h-10 px-3 rounded-lg text-xs font-medium',
              running
                ? 'bg-success-soft border border-success/25 text-fg'
                : 'bg-bg-subtle border border-border text-fg-muted',
            )}
            aria-label={`Gateway ${statusLabel}${port ? `, port ${port}` : ''}`}
          >
            <LuServer className={cn('size-4', running ? 'text-success' : 'text-fg-subtle')} aria-hidden />
            <StatusDot tone={running ? 'online' : 'offline'} pulsing={running} />
            <span className="hidden sm:inline truncate">
              {port ? `:${port}` : statusLabel}
            </span>
          </div>
        </Tooltip>
        <LanguageSelector />
        <Tooltip
          title={t('theme.tooltip', {theme: t(`settings.theme.${theme}`)})}
        >
          <Button
            type="text"
            shape="circle"
            aria-label={`Switch theme (current: ${t(`settings.theme.${theme}`)})`}
            onClick={cycleTheme}
            icon={themeIcon[theme]}
            size="large"
          />
        </Tooltip>
      </div>
    </header>
  )
}