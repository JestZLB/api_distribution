import type {ReactNode} from 'react'
import {Empty} from 'antd'
import {cn} from '@/lib/cn'

export interface EmptyStateProps {
  icon: ReactNode
  title: string
  description?: ReactNode
  action?: ReactNode
  className?: string
}

export function EmptyState({icon, title, description, action, className}: EmptyStateProps) {
  return (
    <Empty
      image={icon}
      className={className}
      description={
        <div className="space-y-1.5">
          <div className="text-base font-medium text-fg">{title}</div>
          {description && (
            <div className="text-sm text-fg-muted">{description}</div>
          )}
        </div>
      }
    >
      {action}
    </Empty>
  )
}