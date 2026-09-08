import {useState} from 'react'
import {LuCircleAlert, LuRefreshCw} from 'react-icons/lu'
import {App as AntApp, Button, Card, Typography} from 'antd'
import {useConfigStore} from '@/store/config'
import {useT} from '@/i18n/useT'

/**
 * Always-mounted sentinel that renders nothing. Subscribes to nothing,
 * allocates nothing — its only purpose is to occupy the slot next to
 * the rest of the app shell so the (more expensive) `BootErrorDetail`
 * can be conditionally mounted in its place.
 */
function BootErrorPlaceholder(): null {
  return null
}

/**
 * Heavy child: only mounted when `BootErrorScreen` (its parent) decides
 * an error UI is actually needed. Owns the original UI plus the 5
 * atomic selectors that drive the gating decision + the data selectors
 * the UI consumes. In the steady-state `bootStatus === 'ready'` path
 * this component is never mounted, so its subscriptions and DOM are
 * not paid for.
 */
function BootErrorDetail({
  showFatal,
  showDegraded,
}: {
  showFatal: boolean
  showDegraded: boolean
}) {
  const {message} = AntApp.useApp()
  const bootError = useConfigStore((s) => s.bootError)
  const lastLoadedAt = useConfigStore((s) => s.lastLoadedAt)
  const load = useConfigStore((s) => s.load)
  const t = useT()
  const [retrying, setRetrying] = useState(false)
  const isDev =
    typeof window !== 'undefined' && !window.location.protocol.startsWith('wails')

  async function handleRetry() {
    setRetrying(true)
    try {
      await load()
      message.success(t('boot.retry'))
    } catch (e) {
      message.error(`${t('boot.retry')}: ${String(e)}`)
    } finally {
      setRetrying(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-bg p-6">
        <Card className="w-full max-w-lg!">
        <div className="flex items-center gap-3">
          <div className="size-10 rounded-lg bg-danger-soft flex items-center justify-center shrink-0">
            <LuCircleAlert className="size-5 text-danger" />
          </div>
          <Typography.Title level={5} className="mt-0! mb-0! text-fg!">
            {showFatal ? t('boot.fatal.title') : t('boot.degraded.title')}
          </Typography.Title>
        </div>
        <div className="space-y-4 mt-3!">
          <Typography.Paragraph className="mt-0! text-sm text-fg-muted!">
            {showFatal ? t('boot.fatal.desc') : t('boot.degraded.desc')}
          </Typography.Paragraph>
          {bootError && showFatal && (
            <pre className="rounded-md bg-bg-subtle border border-border px-3 py-2 text-xs font-mono text-fg whitespace-pre-wrap break-all">
              {bootError}
            </pre>
          )}
          {lastLoadedAt && showDegraded && (
            <Typography.Text type="secondary" className="text-xs!">
              {t('boot.lastSuccess', {
                time: new Date(lastLoadedAt).toLocaleTimeString(),
              })}
            </Typography.Text>
          )}
          <div className="flex flex-col sm:flex-row sm:items-center gap-2 pt-2! border-t border-border!">
            <Button
              type="primary"
              loading={retrying}
              onClick={handleRetry}
              icon={<LuRefreshCw className="size-4" />}
            >
              {t('boot.retry')}
            </Button>
            {isDev && (
              <Typography.Text type="secondary" className="text-xs sm:ml-auto">
                {t('boot.devHint')}
              </Typography.Text>
            )}
          </div>
        </div>
      </Card>
    </div>
  )
}

/**
 * Main entry. Subscribes to the 5 atomic gating selectors
 * (`bootStatus` + 4 per-slice refresh error flags) so transient errors
 * in any one slice do not invalidate unrelated subscribers, then
 * decides between the always-mounted (no-op) `BootErrorPlaceholder` and
 * the heavy `BootErrorDetail`. The detail component is only mounted
 * when an error UI is actually needed.
 */
export function BootErrorScreen() {
  const bootStatus = useConfigStore((s) => s.bootStatus)
  const refreshErrorStats = useConfigStore((s) => s.refreshErrorStats)
  const refreshErrorLogs = useConfigStore((s) => s.refreshErrorLogs)
  const refreshErrorServer = useConfigStore((s) => s.refreshErrorServer)
  const refreshErrorLoad = useConfigStore((s) => s.refreshErrorLoad)

  // Aggregate per-slice refresh errors into a single boolean so the
  // transient banner can surface without forcing the placeholder to
  // subscribe to a single combined object (which would invalidate on
  // every slice's flip).
  const anyRefreshError =
    !!refreshErrorStats || !!refreshErrorLogs || !!refreshErrorServer || !!refreshErrorLoad
  const showFatal = bootStatus === 'failed'
  const showDegraded = bootStatus === 'degraded' || (bootStatus === 'ready' && anyRefreshError)

  if (!showFatal && !showDegraded) return <BootErrorPlaceholder />
  return <BootErrorDetail showFatal={showFatal} showDegraded={showDegraded} />
}
