import {useCallback, useEffect, useMemo, useRef, useState} from 'react'
import {
  LuChevronDown,
  LuChevronRight,
  LuPause,
  LuPlay,
  LuRefreshCw,
  LuScrollText,
  LuTrash2,
  LuWaves,
} from 'react-icons/lu'
import {App as AntApp, Alert, Badge, Button, Card, Checkbox, Input, Select, Space, Table, Tag, Typography} from 'antd'
import type {ColumnsType, ExpandableConfig} from 'antd/es/table/interface'
import {PageHeader} from '@/components/layout/PageHeader'
import {EmptyState} from '@/components/ui/EmptyState'
import {useConfigStore} from '@/store/config'
import {formatLatency, formatNumber, formatRelativeTime} from '@/lib/format'
import {useDebouncedValue} from '@/lib/useDebouncedValue'
import {cn} from '@/lib/cn'
import type {LogEntry} from '@/types'
import {useT} from '@/i18n/useT'

type StatusFilter = 'all' | '2xx' | '4xx' | '5xx' | 'error'

function statusBadge(code: number, error: string, t: (key: string) => string): {tone: 'success' | 'warning' | 'error' | 'default' | 'processing'; label: string} {
  if (error) return {tone: 'error', label: t('logs.status.error')}
  if (code >= 500) return {tone: 'error', label: String(code)}
  if (code >= 400) return {tone: 'warning', label: String(code)}
  if (code >= 300) return {tone: 'processing', label: String(code)}
  if (code >= 200) return {tone: 'success', label: String(code)}
  return {tone: 'default', label: '—'}
}

function matchesStatus(code: number, error: string, filter: StatusFilter): boolean {
  if (filter === 'all') return true
  if (filter === 'error') return !!error || code >= 400
  if (filter === '2xx') return !error && code >= 200 && code < 300
  if (filter === '4xx') return code >= 400 && code < 500
  if (filter === '5xx') return code >= 500 && code < 600
  return true
}

export function Logs() {
  const {message, modal} = AntApp.useApp()
  const logs = useConfigStore((s) => s.logs)
  const refreshLogs = useConfigStore((s) => s.refreshLogs)
  const lastLoadedAt = useConfigStore((s) => s.lastLoadedAt)
  const logsError = useConfigStore((s) => s.refreshErrorLogs)
  const t = useT()
  const clearLogs = useConfigStore((s) => s.clearLogs)

  const [paused, setPaused] = useState(false)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [streamingOnly, setStreamingOnly] = useState(false)
  const [expandedRowKeys, setExpandedRowKeys] = useState<React.Key[]>([])

  // Debounce the search query so we don't re-filter 200 logs on every
  // keystroke. The input keeps responding instantly; only the
  // filtered list / table re-renders after a 200ms pause.
  const debouncedQuery = useDebouncedValue(query, 200)

  const tableBodyRef = useRef<HTMLElement | null>(null)
  const lastLogCount = useRef(logs.length)
  const stickyTop = useRef(true)
  const pollTimerRef = useRef<number | null>(null)
  const pollCancelledRef = useRef(false)
  const pollInFlightRef = useRef(false)

  // Poll every 3s while not paused. The timer id lives in a ref so the cleanup
  // function can clear it even if `tick` has already scheduled the next run
  // while paused/visibility cleanup was racing with an in-flight refresh.
  useEffect(() => {
    const clearTimer = () => {
      if (pollTimerRef.current !== null) {
        window.clearTimeout(pollTimerRef.current)
        pollTimerRef.current = null
      }
    }

    if (paused) {
      pollCancelledRef.current = true
      clearTimer()
      return
    }

    pollCancelledRef.current = false

    const tick = async () => {
      // Skip if cleanup happened, polling was paused, or a request is still in flight.
      if (pollCancelledRef.current || pollInFlightRef.current) return
      pollInFlightRef.current = true
      try {
        await refreshLogs()
      } catch {
        // Backend may be transiently unavailable — ignore silently.
      } finally {
        pollInFlightRef.current = false
      }
      if (pollCancelledRef.current) return
      pollTimerRef.current = window.setTimeout(tick, 3000)
    }

    pollTimerRef.current = window.setTimeout(tick, 3000)

    const handleVisibility = () => {
      if (typeof document === 'undefined') return
      if (document.visibilityState === 'hidden') {
        // Stop scheduling new requests while the tab is hidden. Any request
        // already in flight will still resolve naturally (we don't abort it),
        // but its next tick is suppressed.
        pollCancelledRef.current = true
        clearTimer()
      } else if (document.visibilityState === 'visible' && !paused) {
        pollCancelledRef.current = false
        if (pollTimerRef.current === null) {
          pollTimerRef.current = window.setTimeout(tick, 3000)
        }
      }
    }
    document.addEventListener('visibilitychange', handleVisibility)

    return () => {
      pollCancelledRef.current = true
      clearTimer()
      document.removeEventListener('visibilitychange', handleVisibility)
    }
  }, [paused, refreshLogs])

  // Detect whether the user is at the top so we can auto-scroll on new logs.
  // With the virtual Table the scroll container is `.ant-table-body`,
  // not a wrapper we own, so we capture it via the Table `onScroll`
  // callback in `handleTableScroll` rather than attaching a listener
  // directly to a DOM node we control.
  useEffect(() => {
    if (!paused && stickyTop.current && logs.length > lastLogCount.current) {
      tableBodyRef.current?.scrollTo({top: 0, behavior: 'smooth'})
    }
    lastLogCount.current = logs.length
  }, [logs, paused])

  const handleTableScroll = useCallback(
    (info: {currentTarget?: HTMLElement; scrollLeft?: number}) => {
      const node = info.currentTarget
      if (!node) return
      tableBodyRef.current = node
      stickyTop.current = node.scrollTop < 16
    },
    [],
  )

  const filtered = useMemo(() => {
    const q = debouncedQuery.trim().toLowerCase()
    return logs.filter((l) => {
      if (!matchesStatus(l.statusCode, l.error, statusFilter)) return false
      if (streamingOnly && !l.streaming) return false
      if (!q) return true
      const haystack = `${l.alias} ${l.providerName} ${l.providerModel} ${l.path} ${l.method} ${l.clientIp}`.toLowerCase()
      return haystack.includes(q)
    })
  }, [logs, debouncedQuery, statusFilter, streamingOnly])

  async function handleClear() {
    try {
      await clearLogs()
      message.success(t('toast.logs.cleared'))
      setExpandedRowKeys([])
    } catch (e) {
      message.error(`${t('toast.logs.clearFailed')}: ${String(e)}`)
    }
  }

  function promptClear() {
    modal.confirm({
      title: t('logs.modal.clearTitle'),
      content: (
        <Space orientation="vertical" size={0}>
          <span>{t('logs.modal.clearDesc')}</span>
          <span>{t('logs.modal.clearBody')}</span>
        </Space>
      ),
      okText: t('logs.modal.clearButton'),
      cancelText: t('common.cancel'),
      okButtonProps: {danger: true},
      onOk: handleClear,
    })
  }

  const columns: ColumnsType<LogEntry> = useMemo(() => [
    {
      title: t('logs.col.status'),
      dataIndex: 'statusCode',
      key: 'status',
      width: 80,
      render: (_v, entry) => {
        const {tone, label} = statusBadge(entry.statusCode, entry.error, t)
        return <Badge status={tone as any} text={label} />
      },
    },
    {
      title: t('logs.col.time'),
      dataIndex: 'timestamp',
      key: 'time',
      width: 130,
      render: (_v, entry) => formatRelativeTime(entry.timestamp, t),
    },
    {
      title: t('logs.col.method'),
      dataIndex: 'method',
      key: 'method',
      width: 80,
      render: (v: string) => <Typography.Text code>{v}</Typography.Text>,
    },
    {
      title: t('logs.col.path'),
      dataIndex: 'path',
      key: 'path',
      ellipsis: true,
      render: (v: string) => (
        <Typography.Text className="font-mono text-xs!">{v}</Typography.Text>
      ),
    },  
    {
      title: t('logs.col.aliasProvider'),
      key: 'aliasProvider',
      render: (_v, entry) => (
        <Space size={4} wrap>
          <Typography.Text code>{entry.alias || '—'}</Typography.Text>
          {entry.streaming && (
            <Tag color="processing" icon={<LuWaves className="size-3" aria-hidden />}>
              {t('logs.badge.stream')}
            </Tag>
          )}
          <Typography.Text type="secondary" className="text-xs!" ellipsis>
            {entry.providerName} / {entry.providerModel}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t('logs.col.latency'),
      dataIndex: 'latencyMs',
      key: 'latency',
      width: 90,
      align: 'right',
      render: (v: number) => formatLatency(v),
    },
    {
      title: t('logs.col.tokens'),
      key: 'tokens',
      width: 110,
      align: 'right',
      render: (_v, entry) => {
        const hasIn = entry.inputTokens > 0
        const hasOut = entry.outputTokens > 0
        return hasIn || hasOut ? `${formatNumber(entry.inputTokens)} / ${formatNumber(entry.outputTokens)}` : '—'
      },
    },
    {
      title: t('logs.col.client'),
      dataIndex: 'clientIp',
      key: 'client',
      width: 130,
      render: (v: string) => <Typography.Text code>{v || '—'}</Typography.Text>,
    },
  ], [t])

  const expandable: ExpandableConfig<LogEntry> = useMemo(() => ({
    expandedRowKeys,
    onExpand: (expanded, record) => {
      setExpandedRowKeys((prev) => {
        const next = new Set(prev)
        if (expanded) next.add(record.id)
        else next.delete(record.id)
        return Array.from(next)
      })
    },
    expandIcon: ({expanded, onExpand: trigger, record}) => (
      <Button
        type="text"
        size="small"
        onClick={(e) => {
          e.stopPropagation()
          trigger(record, e)
        }}
        aria-expanded={expanded}
        aria-label={expanded ? t('logs.row.collapse') : t('logs.row.expand')}
        icon={expanded ? <LuChevronDown className="size-3.5" /> : <LuChevronRight className="size-3.5" />}
      />
    ),
    expandedRowRender: (record: LogEntry) => {
      const {tone, label} = statusBadge(record.statusCode, record.error, t)
      return (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4 text-xs">
          <Detail label={t('logs.detail.method')} value={<Typography.Text code>{record.method}</Typography.Text>} />
          <Detail label={t('logs.detail.path')} value={<Typography.Text code>{record.path}</Typography.Text>} />
          <Detail label={t('logs.detail.status')} value={<Badge status={tone as any} text={label} />} />
          <Detail label={t('logs.detail.alias')} value={<Typography.Text code>{record.alias || '—'}</Typography.Text>} />
          <Detail label={t('logs.detail.provider')} value={<Typography.Text code>{record.providerName}</Typography.Text>} />
          <Detail label={t('logs.detail.model')} value={<Typography.Text code>{record.providerModel || '—'}</Typography.Text>} />
          <Detail label={t('logs.detail.latency')} value={formatLatency(record.latencyMs)} />
          <Detail label={t('logs.detail.tokens')} value={`${formatNumber(record.inputTokens)} / ${formatNumber(record.outputTokens)}`} />
          <Detail label={t('logs.detail.clientIp')} value={<Typography.Text code>{record.clientIp || '—'}</Typography.Text>} />
          <Detail label={t('logs.detail.apiKeySuffix')} value={<Typography.Text code>{record.apiKeySuffix || '—'}</Typography.Text>} />
          <Detail label={t('logs.detail.streaming')} value={record.streaming ? t('logs.detail.yes') : t('logs.detail.no')} />
          <Detail label={t('logs.detail.time')} value={new Date(record.timestamp / 1e6).toLocaleString()} />
          {record.error && (
            <div className="col-span-full">
              <div className="text-xs font-medium text-danger mb-1">{t('logs.detail.error')}</div>
              <pre className={cn('rounded-md border border-danger/20 bg-danger/10 px-3 py-2 text-xs font-mono text-danger whitespace-pre-wrap break-all')}>
                {record.error}
              </pre>
            </div>
          )}
        </div>
      )
    },
  }), [expandedRowKeys, t])

  return (
    <div className="flex flex-col gap-8">
      <PageHeader
        title={t('pages.title.logs')}
        description={t('pages.desc.logs')}
        actions={
          <Space>
            <Button
              onClick={() => setPaused((v) => !v)}
              icon={paused ? <LuPlay className="size-4" /> : <LuPause className="size-4" />}
            >
              {paused ? t('logs.resume') : t('logs.pause')}
            </Button>
            <Button
              onClick={promptClear}
              disabled={logs.length === 0}
              danger
              icon={<LuTrash2 className="size-4" />}
            >
              {t('logs.clear')}
            </Button>
          </Space>
        }
      />

      {logsError && (
        <Alert
          type="warning"
          showIcon
          message={t('logs.banner.stale')}
          description={
            <Space orientation="vertical" size={0}>
              <span>{logsError}</span>
              <span className="text-fg-muted!">
                {lastLoadedAt
                  ? t('logs.banner.lastSuccess', {
                      time: new Date(lastLoadedAt).toLocaleTimeString(),
                    })
                  : t('logs.banner.neverLoaded')}
              </span>
            </Space>
          }
          action={
            <Button
              size="small"
              onClick={() => {
                refreshLogs().catch(() => {})
              }}
              icon={<LuRefreshCw className="size-3.5" />}
            >
              {t('logs.banner.retry')}
            </Button>
          }
        />
      )}

      <Card>
        <div className="flex flex-col sm:flex-row sm:items-center gap-4">
          <Input
            prefix={<span className="text-fg-subtle">⌕</span>}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('logs.search.placeholder')}
            allowClear
            className="flex-1!"
            size="large"
          />
          <Space size={8}>
            <Select<StatusFilter>
              value={statusFilter}
              onChange={(v) => setStatusFilter(v)}
              style={{width: 140}}
              options={[
                {value: 'all', label: t('logs.filter.all')},
                {value: '2xx', label: '2xx'},
                {value: '4xx', label: '4xx'},
                {value: '5xx', label: '5xx'},
                {value: 'error', label: t('logs.filter.errors')},
              ]}
            />
            <Checkbox checked={streamingOnly} onChange={(e) => setStreamingOnly(e.target.checked)}>
              {t('logs.streamingOnly')}
            </Checkbox>
          </Space>
        </div>
      </Card>

      <div>
        {filtered.length === 0 ? (
          <Card>
            <EmptyState
              icon={<LuScrollText className="size-10 mx-auto text-fg-subtle" aria-hidden />}
              title={logs.length === 0 ? t('logs.empty.title') : t('logs.empty.noMatch')}
              description={logs.length === 0 ? t('logs.empty.desc') : t('logs.empty.noMatchDesc')}
            />
          </Card>
        ) : (
          <Table<LogEntry>
            rowKey="id"
            columns={columns}
            dataSource={filtered}
            pagination={false}
            size="middle"
            // `virtual` swaps the `<tbody>` for `rc-virtual-list`,
            // rendering only the rows in view + a small buffer. Without
            // this, expanding any row triggers a full-table reflow over
            // every rendered row, which is the main source of the
            // expansion lag on a 300-row buffer.
            virtual
            scroll={{x: 1000, y: 380}}
            expandable={expandable}
            onScroll={handleTableScroll}
          />
        )}
      </div>
    </div>
  )
}

function Detail({label, value}: {label: string; value: React.ReactNode}) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs uppercase tracking-wider text-fg-muted">{label}</span>
      <span className="text-fg">{value}</span>
    </div>
  )
}