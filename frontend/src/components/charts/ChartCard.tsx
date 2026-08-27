import type {ReactNode} from 'react'
import {Card, Typography} from 'antd'

/**
 * Shared wrapper around antd `Card` for chart surfaces.
 *
 * The wrapper fixes:
 *   - body padding (24px, matching the KPI row)
 *   - title font-weight (600) and size (base)
 *   - description slot rendered as a secondary `Typography.Text`
 *   - `extra` slot rendered on the right side of the card header so
 *     `Segmented` controls, model pickers, etc. sit consistently
 *
 * Both ModelTrendChart and RecentTrafficChart share this wrapper so
 * the dashboard's chart column reads as one shape, not two
 * near-duplicates.
 */
export interface ChartCardProps {
  /** Card title rendered in the header. Pass an empty string to
   * suppress the card from rendering — matches the legacy contract
   * from ModelTrendChart. */
  title: string
  /** Optional secondary description rendered under the title. */
  description?: string
  /** Optional node shown on the right side of the card header. Use
   * this for `Segmented` / `Select` controls. */
  extra?: ReactNode
  /** Forwarded to the wrapping Card. */
  className?: string
  /** Card body content. */
  children?: ReactNode
}

export function ChartCard({
  title,
  description,
  extra,
  className,
  children,
}: ChartCardProps): ReactNode {
  if (!title) return null
  return (
    <Card
      title={<span className="text-base font-semibold">{title}</span>}
      variant="outlined"
      className={className}
      {...(extra ? {extra} : {})}
      styles={{body: {padding: '24px'}}}
    >
      {description && (
        <Typography.Text type="secondary" className="block">
          {description}
        </Typography.Text>
      )}
      {children}
    </Card>
  )
}