import {NavLink, type NavLinkRenderProps} from 'react-router-dom'
import {LuServer} from 'react-icons/lu'
import {useConfigStore} from '@/store/config'
import {useT} from '@/i18n/useT'
import {cn} from '@/lib/cn'
import {navEntries} from '@/config/nav'
import {StatusDot} from '@/components/ui/StatusDot'

function navClass({isActive}: NavLinkRenderProps): string {
  return cn(
    'relative flex items-center gap-3 h-9 px-3 rounded-md text-sm whitespace-nowrap',
    'transition-colors duration-150 cursor-pointer',
    'min-w-0',
    isActive
      ? 'bg-bg-subtle text-fg'
      : 'text-fg-muted hover:bg-bg-subtle/60 hover:text-fg',
    // Left accent indicator for the active item — a 2px indigo bar
    // instead of a coloured pill background, matching Linear/Vercel's
    // rail cue.
    isActive && 'after:absolute after:left-0 after:top-1/2 after:-translate-y-1/2 after:h-4 after:w-0.5 after:rounded-full after:bg-primary',
  )
}

interface SidebarProps {
  /** When true the sidebar collapses to an icon-only rail. */
  collapsed?: boolean
}

export function Sidebar({collapsed = false}: SidebarProps) {
  const serverStatus = useConfigStore((s) => s.serverStatus)
  const t = useT()
  const running = serverStatus?.running ?? false
  const port = serverStatus?.port

  return (
    <div className="w-full h-full flex flex-col">
      {/* Brand */}
      <div
        className={cn(
          'flex items-center gap-3 h-16 px-4 border-b border-border min-w-0',
          collapsed && 'justify-center px-0',
        )}
      >
        <div className="size-9 rounded-md border border-border bg-bg-elevated text-fg-muted flex items-center justify-center shrink-0">
          <LuServer className="size-5" aria-hidden />
        </div>
        {!collapsed && (
          <div className="flex flex-col leading-tight min-w-0">
            <span className="text-sm font-bold text-fg tracking-tight truncate">
              API Distribution
            </span>
            <span className="text-xs text-fg-subtle truncate font-medium">{t('brand.gateway')}</span>
          </div>
        )}
      </div>

      {/* Nav */}
      <nav className={cn('flex-1 py-4 space-y-1 min-w-0', collapsed ? 'px-2' : 'px-3')}>
        {navEntries.map((entry) => {
          const Icon = entry.icon
          return (
            <NavLink
              key={entry.to}
              to={entry.to}
              className={(props) => cn(navClass(props), collapsed && 'justify-center px-0')}
              title={collapsed ? t(entry.labelKey) : undefined}
            >
              <Icon className="size-5 shrink-0" aria-hidden />
              {!collapsed && <span className="truncate">{t(entry.labelKey)}</span>}
            </NavLink>
          )
        })}
      </nav>

      {/* Server status */}
      <div className={cn('pb-4 pt-2', collapsed ? 'px-2' : 'px-3')}>
        <div
          className={cn(
            'flex items-center gap-2.5 px-2.5 py-2 rounded-md min-w-0 text-xs',
            collapsed && 'justify-center px-0',
          )}
        >
          <StatusDot tone={running ? 'online' : 'offline'} pulsing={running} />
          {!collapsed && (
            <div className="flex flex-1 items-baseline justify-between min-w-0">
              <span className="font-medium text-fg truncate">
                {running ? t('sidebar.running') : t('sidebar.stopped')}
              </span>
              {port && (
                <span className="font-mono text-fg-subtle text-[11px] shrink-0">
                  :{port}
                </span>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
