import type {ReactNode} from 'react'
import {useMemo} from 'react'
import {LuChartLine} from 'react-icons/lu'
import {EmptyState} from '@/components/ui/EmptyState'
import {useT} from '@/i18n/useT'

/**
 * Shared empty-state for the dashboard's charts.
 *
 * Both ModelTrendChart and RecentTrafficChart should render this
 * component when their respective data slice is empty so the user
 * gets the same icon, layout and copy across the page. The component
 * reads `chart.empty.title` / `chart.empty.desc` from the i18n
 * catalog so a single translation covers both charts.
 *
 * Callers can override the icon / title / description per-chart when
 * they want to be more specific (e.g. trend vs. traffic wording),
 * but the defaults are identical on purpose.
 */
export interface ChartEmptyProps {
  /** Optional override for the icon shown above the title. */
  icon?: ReactNode
  /** Optional override for the primary title. Defaults to the
   * localized `chart.empty.title`. */
  title?: string
  /** Optional override for the secondary description. Defaults to
   * the localized `chart.empty.desc`. */
  description?: string
  /** Extra class names for the outer container. */
  className?: string
}

export function ChartEmpty({
  icon,
  title,
  description,
  className,
}: ChartEmptyProps): ReactNode {
  const t = useT()
  // Cache the i18n lookups once per locale so the component doesn't
  // re-read the catalog every heartbeat. The hook already memoizes
  // the function reference, but pulling the strings out of the
  // render path keeps the prop set flat and obvious.
  const strings = useMemo(
    () => ({
      defaultTitle: t('chart.empty.title'),
      defaultDesc: t('chart.empty.desc'),
    }),
    [t],
  )
  return (
    <div className={className ?? 'mt-5'}>
      <EmptyState
        icon={icon ?? <LuChartLine className="size-10 mx-auto text-fg-subtle" />}
        title={title ?? strings.defaultTitle}
        description={description ?? strings.defaultDesc}
      />
    </div>
  )
}