import {memo, useMemo} from 'react'
import {LuArrowRight, LuPlug} from 'react-icons/lu'
import {useNavigate} from 'react-router-dom'
import {Button, Card, Tag, Typography} from 'antd'
import {EmptyState} from '@/components/ui/EmptyState'
import {maskKey} from '@/lib/format'
import {useT} from '@/i18n/useT'
import type {Provider} from '@/types'

interface ModelConnectionsCardProps {
  providers: Provider[]
}

function ModelConnectionsCardImpl({providers}: ModelConnectionsCardProps) {
  const navigate = useNavigate()
  const t = useT()

  // Show the most recent 5 providers as a summary.
  const recent = useMemo(() => providers.slice(-5).reverse(), [providers])
  const enabledCount = useMemo(() => providers.filter((p) => p.enabled).length, [providers])

  return (
    <Card
      title={t('settings.modelConnections')}
      variant="outlined"
      extra={
        <Button
          size="small"
          onClick={() => navigate('/providers')}
          icon={<LuArrowRight className="size-3.5" />}
        >
          {t('settings.manageProviders')}
        </Button>
      }
    >
      <Typography.Text type="secondary" className="block">
        {t('settings.modelConnections.desc')}
      </Typography.Text>
      <div className="mt-4">
        {providers.length === 0 ? (
          <EmptyState
            icon={<LuPlug className="size-10 mx-auto text-fg-subtle" />}
            title={t('providers.empty.title')}
            description={t('providers.empty.desc')}
            action={
              <Button
                size="small"
                type="primary"
                onClick={() => navigate('/providers')}
                icon={<LuArrowRight className="size-3.5" />}
              >
                {t('settings.manageProviders')}
              </Button>
            }
          />
        ) : (
          <div className="space-y-3">
            <div className="flex items-center gap-3 text-sm text-fg-muted">
              <span>
                {t('providers.form.total', {n: providers.length})}
              </span>
              <span className="text-fg-subtle">·</span>
              <span>
                {enabledCount} {t('providers.form.enabled').toLowerCase()}
              </span>
            </div>
            <ul className="space-y-1.5">
              {recent.map((p) => (
                <li
                  key={p.id}
                  className="flex items-center gap-2 px-3 py-2 rounded-lg border border-border bg-bg-subtle"
                >
                  <Tag color="cyan">{p.type}</Tag>
                  <span className="text-sm font-medium text-fg truncate">
                    {p.name || t('providers.form.recent.untitled')}
                  </span>
                  <Typography.Text code className="hidden md:inline-flex truncate max-w-40 text-xs">
                    {p.apiKey ? maskKey(p.apiKey) : p.baseUrl}
                  </Typography.Text>
                  <Tag
                    color={p.enabled ? 'green' : 'default'}
                    className="ml-auto"
                  >
                    {p.enabled ? t('providers.form.enabled') : t('dashboard.badge.disabled')}
                  </Tag>
                </li>
              ))}
            </ul>
            {providers.length > 5 && (
              <p className="text-xs text-fg-subtle text-center">
                + {providers.length - 5} {t('providers.form.moreProviders')}
              </p>
            )}
          </div>
        )}
      </div>
    </Card>
  )
}

export const ModelConnectionsCard = memo(ModelConnectionsCardImpl)