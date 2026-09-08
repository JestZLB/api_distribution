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
  // Only the (truncated) `message` is retained in component state.
  // The full `error.stack` is captured into `lastCrashRef` (below)
  // for telemetry / debugging, so the React tree can drop the
  // original `Error` reference as soon as `componentDidCatch` runs
  // and the GC can reclaim the stack frames on the next idle cycle.
  message: string
}

// Module-level crash slot. The boundary writes the full stack here
// during `componentDidCatch` and exposes it via `getLastCrash()` so
// a future telemetry hook (Sentry / Datadog / OTel) can ship it
// without each consumer having to wrap the class component. The
// next crash overwrites the previous entry, so the slot holds at
// most one stack — typically freed as soon as the boundary
// re-renders or the page unloads.
let lastCrashRef: {stack: string; componentStack: string; ts: number} | null = null

/** Read the most recent error captured by `<ErrorBoundary />`.
 * Returns `null` if no crash has been recorded since module load. */
export function getLastCrash(): {stack: string; componentStack: string; ts: number} | null {
  return lastCrashRef
}

const MAX_MESSAGE_LEN = 1024

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = {hasError: false, message: ''}
  }

  static getDerivedStateFromError(error: Error): State {
    // Capture only the truncated message so `state` does not pin the
    // full `Error` (and its `stack` / captured-frame chain) in
    // memory for the lifetime of the error UI. The full stack is
    // recorded separately in `componentDidCatch` below.
    const message =
      typeof error?.message === 'string'
        ? error.message.slice(0, MAX_MESSAGE_LEN)
        : ''
    return {hasError: true, message}
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    // Capture the full stack + component trace here (where they
    // cannot leak into React state). Wrapped in try/catch so a
    // pathological `error.stack` getter that itself throws cannot
    // take the boundary down — losing one telemetry entry is
    // strictly better than letting the boundary crash.
    try {
      const stack = typeof error?.stack === 'string' ? error.stack : ''
      lastCrashRef = {
        stack,
        componentStack: errorInfo?.componentStack ?? '',
        ts: Date.now(),
      }
    } catch {
      // Defensive: never let a telemetry failure unmount the tree.
    }

    // Local-only logging for development debugging; a production
    // telemetry endpoint could replace this in the future.
    // eslint-disable-next-line no-console
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
            {this.state.message && (
              <div className="rounded-md bg-bg-subtle border border-border px-3 py-2">
                <p className="text-xs font-mono text-fg-muted! break-all!">
                  {this.state.message}
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