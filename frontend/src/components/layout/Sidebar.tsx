import {NavLink, type NavLinkRenderProps} from 'react-router-dom'
import {LuServer} from 'react-icons/lu'
import {useConfigStore} from '@/store/config'
import {useT} from '@/i18n/useT'
import {cn} from '@/lib/cn'
import {navEntries} from '@/config/nav'
import {StatusDot} from '@/components/ui/StatusDot'

function navClass({isActive}: NavLinkRenderProps): string {
  return cn(
    'relative flex items-center gap-3 h-11 px-3 rounded-lg text-sm whitespace-nowrap',
    'transition-colors duration-150 cursor-pointer',
    'min-w-0',
    isActive
      ? 'bg-accent/10 text-accent font-semibold'
      : 'text-fg-muted hover:bg-bg-subtle hover:text-fg',
    // Left accent indicator for the active item.
    isActive && 'after:absolute after:left-0 after:top-1/2 after:-translate-y-1/2 after:h-5 after:w-0.5 after:rounded-full after:bg-accent',
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
        <div className="size-9 rounded-lg bg-accent text-fg-on-accent flex items-center justify-center shrink-0 shadow-sm">
          <LuServer className="size-5" aria-hidden />
        </div>
        {!collapsed && (
          <div className="flex flex-col leading-tight min-w-0">
            <span className="text-sm font-bold text-fg tracking-tight truncate">
              API Distribution
            </span>
            <span className="text-xs text-fg-subtle truncate font-medium">Gateway</span>
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
            'flex items-center gap-3 px-3 py-3 rounded-lg min-w-0',
            collapsed && 'justify-center px-0',
            running
              ? 'bg-success-soft border border-success/20'
              : 'bg-bg-subtle border border-border',
          )}
        >
          <StatusDot tone={running ? 'online' : 'offline'} pulsing={running} />
          {!collapsed && (
            <div className="flex flex-col leading-tight min-w-0">
              <span className="text-xs font-semibold text-fg truncate">
                {running ? t('sidebar.running') : t('sidebar.stopped')}
              </span>
              <span className="text-xs text-fg-subtle truncate">
                {port ? t('sidebar.port', {port}) : '—'}
              </span>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
