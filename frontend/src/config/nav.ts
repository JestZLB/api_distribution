import {
  LuLayoutDashboard,
  LuPlug,
  LuBox,
  LuScrollText,
  LuSettings,
} from 'react-icons/lu'
import type {ComponentType, SVGProps} from 'react'

// react-icons/lu re-exports Lucide icons but does not export the
// `LucideIcon` type alias. Declare a structural type instead.
export type LucideIcon = ComponentType<SVGProps<SVGSVGElement>>

export interface NavEntry {
  to: string
  /** i18n key for the visible label. */
  labelKey: string
  /** Icon component (Lucide). */
  icon: LucideIcon
}

export const navEntries: NavEntry[] = [
  {to: '/dashboard', labelKey: 'nav.dashboard', icon: LuLayoutDashboard},
  {to: '/providers', labelKey: 'nav.providers', icon: LuPlug},
  {to: '/models', labelKey: 'nav.models', icon: LuBox},
  {to: '/logs', labelKey: 'nav.logs', icon: LuScrollText},
  {to: '/settings', labelKey: 'nav.settings', icon: LuSettings},
]