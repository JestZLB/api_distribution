import {useState} from 'react'
import {LuCircleAlert, LuRefreshCw} from 'react-icons/lu'
import {App as AntApp, Button, Card, Typography} from 'antd'
import {useConfigStore} from '@/store/config'
import {useT} from '@/i18n/useT'

export function BootErrorScreen() {
  const {message} = AntApp.useApp()
  const bootStatus = useConfigStore((s) => s.bootStatus)
  const bootError = useConfigStore((s) => s.bootError)
  const lastLoadedAt = useConfigStore((s) => s.lastLoadedAt)
  const load = useConfigStore((s) => s.load)
  const refreshErrors = useConfigStore((s) => s.refreshErrors)
  const t = useT()
  const [retrying, setRetrying] = useState(false)
  const isDev =
    typeof window !== 'undefined' && !window.location.protocol.startsWith('wails')

  // While we are still attempting to load, show nothing — pages render
  // their own skeletons / states. Only render the full-page error when
  // bootStatus has resolved to 'failed' or 'degraded'.
  const showFatal = bootStatus === 'failed'
  const showDegraded =
    bootStatus === 'degraded' || (bootStatus === 'ready' && Object.values(refreshErrors).some(Boolean))

  if (!showFatal && !showDegraded) return null

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