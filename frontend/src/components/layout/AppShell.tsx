import {useState} from 'react'
import {Outlet} from 'react-router-dom'
import {cn} from '@/lib/cn'
import {Sidebar} from './Sidebar'
import {TopBar} from './TopBar'

export function AppShell() {
  // Controls whether the left sidebar is collapsed to an icon-only rail.
  const [collapsed, setCollapsed] = useState(false)

  return (
    <div className="h-screen w-full flex">
      <aside
        className={cn(
          'shrink-0 flex-col glass-surface border-r border-border transition-[width] duration-200',
          collapsed ? 'w-16' : 'w-58',
        )}
      >
        <Sidebar collapsed={collapsed} />
      </aside>
      <div className="flex-1 flex flex-col min-w-0">
        {/* TopBar renders the only <header> in the chrome. */}
        <TopBar collapsed={collapsed} onToggle={() => setCollapsed((v) => !v)} />
        <main className="flex-1 overflow-y-auto px-6 py-8 sm:px-10 sm:py-12 lg:px-16 lg:py-14">
          <div className="mx-auto w-full max-w-7xl animate-[fadeInUp_250ms_ease-out]">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
