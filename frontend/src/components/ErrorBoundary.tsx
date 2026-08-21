import {Component, type ErrorInfo, type ReactNode} from 'react'
import {Button, Card, Typography} from 'antd'
import {LuTriangleAlert, LuRefreshCw} from 'react-icons/lu'
// ErrorBoundary is a class component and cannot call App.useApp(), so it
// reads the message API from a module-level ref populated by MessageHolder.
import {messageApiRef} from './MessageHolder'
import {useLocaleStore} from '@/store/locale'
import {messages} from '@/i18n/locales'

interface Props {
  children: ReactNode
}

interface State {
  hasError: boolean
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = {hasError: false, error: null}
  }

  static getDerivedStateFromError(error: Error): State {
    return {hasError: true, error}
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    // Log to console for development debugging; a production
    // telemetry endpoint could be added here in the future.
    console.error('[ErrorBoundary]', error, errorInfo)
    messageApiRef.current?.error('Render error')
  }

  private handleRetry = () => {
    // Reset internal state and force a full page reload so that
    // stale module-level state (stores, timers, etc.) is cleared.
    window.location.reload()
  }

  render() {
    if (!this.state.hasError) return this.props.children

    const {error} = this.state
    // Read locale from store (outside React hooks) for class component.
    const locale = useLocaleStore.getState().locale
    const t = (key: string) =>
      messages[locale]?.[key] ?? messages['en-US'][key] ?? key

    return (
      <div className="flex items-center justify-center min-h-[60vh] p-6">
        <Card className="w-full! max-w-md!">
          <div className="flex items-center gap-3">
            <div className="size-10 rounded-lg bg-danger-soft flex items-center justify-center shrink-0">
              <LuTriangleAlert className="size-5 text-danger" />
            </div>
            <Typography.Title level={5} className="mt-0! mb-0! text-fg!">
              {t('error.title')}
            </Typography.Title>
          </div>
          <div className="space-y-4 mt-3!">
            <Typography.Paragraph className="mt-0! text-sm text-fg-muted!">
              {t('error.description')}
            </Typography.Paragraph>
            {error?.message && (
              <div className="rounded-md bg-bg-subtle border border-border px-3 py-2">
                <p className="text-xs font-mono text-fg-muted! break-all!">
                  {error.message}
                </p>
              </div>
            )}
            <Button
              type="primary"
              block
              onClick={this.handleRetry}
              icon={<LuRefreshCw className="size-4" />}
            >
              {t('error.retry')}
            </Button>
          </div>
        </Card>
      </div>
    )
  }
}