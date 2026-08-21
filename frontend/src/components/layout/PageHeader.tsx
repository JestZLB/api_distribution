import {type ReactNode} from 'react'
import {Space, Typography} from 'antd'
import {cn} from '@/lib/cn'

export interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
}

export function PageHeader({title, description, actions, className}: PageHeaderProps) {
  return (
    <div
      className={cn(
        'flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between',
        'pb-8 mb-8 border-b border-border',
        className,
      )}
    >
      <div className="min-w-0 flex-1">
        <Typography.Title
          level={3}
          className="mt-0! mb-2! text-2xl font-bold tracking-tight text-fg!"
        >
          {title}
        </Typography.Title>
        {description && (
          <Typography.Paragraph className="mt-0! mb-2! text-base text-fg-muted! max-w-3xl leading-relaxed!">
            {description}
          </Typography.Paragraph>
        )}
      </div>
      {actions && (
        <Space size="middle" wrap className="shrink-0">
          {actions}
        </Space>
      )}
    </div>
  )
}