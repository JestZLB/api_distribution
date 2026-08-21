import { useCallback, useEffect, useMemo, useState, Suspense, lazy } from 'react'
import {
  Alert,
  Button,
  Card,
  Segmented,
  Select,
  Skeleton,
  Space,
  Statistic,
  Tag,
  Typography,
} from 'antd'
import {
  LuBox,
  LuCirclePlus,
  LuCopy,
  LuKeyRound,
  LuPlay,
  LuRotateCw,
  LuServer,
  LuSparkles,
  LuSquare,
  LuTriangleAlert,
  LuZap,
} from 'react-icons/lu'
import { PageHeader } from '@/components/layout/PageHeader'
import { StatusDot } from '@/components/ui/StatusDot'
import { EmptyState } from '@/components/ui/EmptyState'
// `@ant-design/charts` is the largest third-party dependency in the
// app (~600kB gzip). Dynamically importing it defers the chart code
// off the first-paint critical path so the Dashboard renders with
// only the KPI cards / lists, and the chart loads in parallel.
const ModelTrendChart = lazy(() =>
  import('@/components/charts/ModelTrendChart').then((m) => ({
    default: m.ModelTrendChart,
  })),
)
import { useConfigStore } from '@/store/config'
import { toast } from '@/store/toast'
import { formatNumber } from '@/lib/format'
import { copyToClipboard } from '@/lib/clipboard'
import { useT } from '@/i18n/useT'
import type { HourBucket } from '@/types'

interface QuickStartStep {
  key: string
  to?: string
  label: string
}

export function Dashboard() {
  const stats = useConfigStore((s) => s.stats)
  const statsWC = useConfigStore((s) => s.statsWithComparison)
  const serverStatus = useConfigStore((s) => s.serverStatus)
  const modelAliases = useConfigStore((s) => s.modelAliases)
  const providers = useConfigStore((s) => s.providers)
  const bootStatus = useConfigStore((s) => s.bootStatus)
  const startServer = useConfigStore((s) => s.startServer)
  const stopServer = useConfigStore((s) => s.stopServer)
  const restartServer = useConfigStore((s) => s.restartServer)
  const statsError = useConfigStore((s) => s.refreshErrors.stats)
  const t = useT()

  // Subscribe to backend stats:changed events emitted every ~2s.
  useEffect(() => {
    const unsubscribe = useConfigStore.getState().subscribeStatsEvents()
    return unsubscribe
  }, [])

  const previousRequests = statsWC?.prevRequests ?? 0
  const previousInput = statsWC?.prevInputTokens ?? 0
  const previousOutput = statsWC?.prevOutputTokens ?? 0

  const enabledAliases = useMemo(
    () => modelAliases.filter((a) => a.enabled),
    [modelAliases],
  )

  const providerById = useMemo(() => {
    return new Map(providers.map((p) => [p.id, p]))
  }, [providers])

  const traffic = stats?.requestsByHour ?? []

  const totalRequests = stats?.totalRequests ?? 0
  const totalErrors = useMemo(
    () => traffic.reduce((sum, b) => sum + b.errors, 0),
    [traffic],
  )
  const errorRate = totalRequests > 0 ? (totalErrors / totalRequests) * 100 : 0

  const sparklineData = useMemo(() => traffic.map((b) => b.count), [traffic])

  // Use the i18n catalog when the key exists, otherwise fall back to the
  // hard-coded English message so we don't depend on locale changes for
  // a single error-string.
  const statsStaleMessage = useMemo(() => {
    const translated = t('dashboard.stats.stale')
    return translated === 'dashboard.stats.stale'
      ? 'Statistics may be out of date'
      : translated
  }, [t])

  const quickStart = useMemo<QuickStartStep[]>(() => {
    const steps: QuickStartStep[] = []
    if (providers.length === 0) {
      steps.push({ key: 'provider', to: '/providers', label: t('dashboard.quickStart.addProvider') })
    }
    if (modelAliases.length === 0) {
      steps.push({ key: 'alias', to: '/models', label: t('dashboard.quickStart.addAlias') })
    }
    if (!serverStatus?.running) {
      steps.push({ key: 'start', label: t('dashboard.quickStart.start') })
    }
    return steps
  }, [providers.length, modelAliases.length, serverStatus?.running, t])

  async function handleStart() {
    try {
      await startServer()
      toast.success(t('toast.serverStarted'))
    } catch (e) {
      toast.error(t('toast.serverStartFailed'), String(e))
    }
  }

  async function handleStop() {
    try {
      await stopServer()
      toast.info(t('toast.serverStopped'))
    } catch (e) {
      toast.error(t('toast.serverStopFailed'), String(e))
    }
  }

  async function handleRestart() {
    try {
      await restartServer()
      toast.success(t('toast.serverRestarted'))
    } catch (e) {
      toast.error(t('toast.serverRestartFailed'), String(e))
    }
  }

  const running = serverStatus?.running ?? false
  const baseUrl = serverStatus?.baseUrl ?? ''
  const isInitial = bootStatus === 'initializing' && !stats

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('pages.title.dashboard')}
        description={t('pages.desc.dashboard')}
      />

      {/* Stat row */}
      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-5">
        {isInitial ? (
          <>
            <Skeleton active className="h-32" />
            <Skeleton active className="h-32" />
            <Skeleton active className="h-32" />
            <Skeleton active className="h-32" />
          </>
        ) : (
          <>
            <Card variant="outlined" styles={{ body: { padding: '24px' } }}>
              <Statistic
                title={t('dashboard.totalRequests')}
                value={formatNumber(totalRequests)}
                prefix={<LuZap className="size-4 text-accent" aria-hidden />}
              />
              {previousRequests !== 0 && (
                <Typography.Text type="secondary" className="mt-2! block text-xs">
                  {(((totalRequests - previousRequests) / Math.max(previousRequests, 1)) * 100).toFixed(1)}%
                </Typography.Text>
              )}
              {sparklineData.length > 0 && (
                <div className="mt-3 text-xs text-fg-muted">
                  peak: {formatNumber(Math.max(...sparklineData))}
                </div>
              )}
            </Card>
            <Card variant="outlined" styles={{ body: { padding: '24px' } }}>
              <Statistic
                title={t('dashboard.inputTokens')}
                value={formatNumber(stats?.totalInputTokens ?? 0)}
                prefix={<LuCopy className="size-4 text-accent" aria-hidden />}
              />
              {previousInput !== 0 && (
                <Typography.Text type="secondary" className="mt-2! block text-xs">
                  {(((stats?.totalInputTokens ?? 0) - previousInput) / Math.max(previousInput, 1) * 100).toFixed(1)}%
                </Typography.Text>
              )}
            </Card>
            <Card variant="outlined" styles={{ body: { padding: '24px' } }}>
              <Statistic
                title={t('dashboard.outputTokens')}
                value={formatNumber(stats?.totalOutputTokens ?? 0)}
                prefix={<LuCopy className="size-4 text-success" aria-hidden />}
              />
              {previousOutput !== 0 && (
                <Typography.Text type="secondary" className="mt-2! block text-xs">
                  {(((stats?.totalOutputTokens ?? 0) - previousOutput) / Math.max(previousOutput, 1) * 100).toFixed(1)}%
                </Typography.Text>
              )}
            </Card>
            <Card variant="outlined" styles={{ body: { padding: '24px' } }}>
              <Statistic
                title={t('dashboard.errorRate')}
                value={errorRate === 0 ? '0%' : `${errorRate.toFixed(1)}%`}
                prefix={<LuTriangleAlert
                  className={`size-4 ${errorRate > 5 ? 'text-danger' : errorRate > 1 ? 'text-warning' : 'text-fg-subtle'}`}
                  aria-hidden
                />}
              />
            </Card>
          </>
        )}
      </div>

      {statsError && (
        <Alert type="warning" showIcon message={statsStaleMessage} />
      )}

      {/* Quick start */}
      {quickStart.length > 0 && (
        <Card variant="outlined" className="border-accent/30 bg-accent-soft/40">
          <div className="flex items-center gap-2">
            <LuSparkles className="size-4 text-accent" aria-hidden />
            <div className="text-base font-semibold text-fg">{t('dashboard.quickStart.title')}</div>
          </div>
          <Typography.Text type="secondary" className="block mt-3">
            {t('dashboard.quickStart.desc')}
          </Typography.Text>
          <ol className="mt-5 space-y-2.5">
            {quickStart.map((step, idx) => (
              <li
                key={step.key}
                className="flex items-center gap-3 rounded-lg border border-border bg-bg-elevated px-4 py-3"
              >
                <span className="inline-flex size-7 items-center justify-center rounded-full bg-accent text-fg-on-accent text-xs font-semibold shrink-0">
                  {idx + 1}
                </span>
                <span className="flex-1 text-sm text-fg">{step.label}</span>
                {step.to ? (
                  <Button
                    size="small"
                    onClick={() => (window.location.hash = `#${step.to}`)}
                    icon={<LuCirclePlus className="size-3.5" />}
                  >
                    {t('dashboard.quickStart.go')}
                  </Button>
                ) : (
                  <Button
                    size="small"
                    type="primary"
                    onClick={handleStart}
                    icon={<LuPlay className="size-3.5" />}
                  >
                    {t('dashboard.quickStart.go')}
                  </Button>
                )}
              </li>
            ))}
          </ol>
        </Card>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Server card — split into three clearly-separated regions
         * (hero status / connection details / actions) with muted
         * dividers so the eye scans top-to-bottom instead of
         * bouncing around a grid. */}
        <Card
          title={
            <div className="flex items-center gap-2.5">
              <span className="inline-flex size-7 items-center justify-center rounded-md bg-accent-soft text-accent">
                <LuServer className="size-4" aria-hidden />
              </span>
              <span className="text-base font-semibold">{t('dashboard.serverStatus')}</span>
              {running ? (
                <Tag color="success" className="!ml-auto !mr-0">
                  {t('dashboard.badge.live')}
                </Tag>
              ) : (
                <Tag className="!ml-auto !mr-0">{t('dashboard.badge.idle')}</Tag>
              )}
            </div>
          }
          variant="outlined"
          className="lg:col-span-1"
        >
          {/* Hero status block: large pulsing dot + status word. */}
          <div className="flex items-center gap-3 rounded-lg border border-border bg-bg-subtle/40 px-4 py-3.5">
            <StatusDot tone={running ? 'online' : 'offline'} pulsing={running} />
            <div className="flex flex-col leading-tight">
              <span className="text-xs uppercase tracking-wider text-fg-muted">
                {t('dashboard.serverStatus')}
              </span>
              <span
                className={`mt-0.5 text-base font-semibold ${running ? 'text-success' : 'text-fg-muted'
                  }`}
              >
                {running ? t('dashboard.running') : t('dashboard.stopped')}
              </span>
            </div>
          </div>

          {/* Divider */}
          <div className="my-5 h-px bg-border" />

          {/* Connection details — labelled key/value rows for a
           * scannable, settings-page feel. */}
          <div className="space-y-3.5">
            <div className="flex items-baseline gap-3">
              <span className="w-16 shrink-0 text-xs uppercase tracking-wider text-fg-muted">
                {t('settings.host')}
              </span>
              <Typography.Text code className="text-sm">
                {serverStatus?.host ?? '\u2014'}
              </Typography.Text>
            </div>
            <div className="flex items-baseline gap-3">
              <span className="w-16 shrink-0 text-xs uppercase tracking-wider text-fg-muted">
                {t('settings.port')}
              </span>
              <Typography.Text code className="text-sm">
                {serverStatus?.port ?? '\u2014'}
              </Typography.Text>
            </div>
            <div>
              <span className="mb-1.5 block text-xs uppercase tracking-wider text-fg-muted">
                {t('providers.form.baseUrl')}
              </span>
              <div className="flex items-center gap-2">
                <Typography.Text
                  code
                  className="flex-1 truncate rounded-md border border-border bg-bg-subtle/40 px-3 py-2 text-sm"
                >
                  {baseUrl || '\u2014'}
                </Typography.Text>
                <Button
                  size="small"
                  onClick={() =>
                    baseUrl && copyToClipboard(baseUrl, t('providers.form.baseUrl'))
                  }
                  disabled={!baseUrl}
                  icon={<LuCopy className="size-3.5" />}
                  aria-label={t('common.copy')}
                >
                  {t('common.copy')}
                </Button>
              </div>
            </div>
          </div>

          {/* Divider */}
          <div className="my-5 h-px bg-border" />

          {/* Actions. The colour mapping follows the affordance of
           * each operation: Start = primary (green) since it brings
           * the gateway up; Stop = danger (red) since it tears down
           * in-flight streams; Restart = outlined neutral so it sits
           * visually behind the primary action. */}
          <div className="flex items-center gap-2">
            {!running ? (
              <Button
                type="primary"
                onClick={handleStart}
                icon={<LuPlay className="size-3.5" />}
              >
                {t('dashboard.start')}
              </Button>
            ) : (
              <Button
                danger
                type="primary"
                onClick={handleStop}
                icon={<LuSquare className="size-3.5" />}
              >
                {t('dashboard.stop')}
              </Button>
            )}
            <Button
              type="default"
              onClick={handleRestart}
              icon={<LuRotateCw className="size-3.5" />}
            >
              {t('dashboard.restart')}
            </Button>
          </div>
        </Card>

        {/* Model trend — per-model request volume over a 24h or 7d
         * rolling window. Lazy-loaded like the traffic chart so the
         * @ant-design/charts bundle never blocks first paint. */}
        <ModelTrendChartContainer />
      </div>

      {/* Traffic by client key — sorted by total traffic so the busiest
       * client surfaces first, with a per-row key chip, recent-24h
       * substat, and a right-aligned big total. */}
      <div className='mt-4'>
        <Card
          title={
            <div className="flex items-center gap-2.5">
              <span className="inline-flex size-7 items-center justify-center rounded-md bg-accent-soft text-accent">
                <LuKeyRound className="size-4" aria-hidden />
              </span>
              <span className="text-base font-semibold">
                {t('dashboard.byClientKey')}
              </span>
            </div>
          }
          variant="outlined"
        >
          {Object.keys(stats?.requestsByClientKey ?? {}).length === 0 ? (
            <EmptyState
              icon={<LuKeyRound className="size-10 mx-auto text-fg-subtle" />}
              title={t('dashboard.noClientKeyTraffic')}
              description={t('dashboard.noClientKeyTrafficDesc')}
            />
          ) : (
            <ul className="space-y-2">
            {Object.entries(stats?.requestsByClientKey ?? {})
              .sort(([, a], [, b]) => b - a)
              .map(([label, count], idx) => {
                const recent = stats?.requestsByClientKeyRecent?.[label] ?? 0
                // Activity status derived from the relationship
                // between lifetime total and the 24h subset:
                //  - recent === 0          → stale (no traffic in 24h)
                //  - recent === total>0    → fresh (all activity in 24h)
                //  - 0 < recent < total    → mixed (some history)
                // Plus a single percentage so the operator sees
                // recency at a glance instead of two raw numbers.
                const isTop = idx === 0
                const isStale = recent === 0
                const isFresh = recent > 0 && recent >= count
                const ratio = count > 0 ? Math.round((recent / count) * 100) : 0
                let statusTone: string
                let statusText: string
                if (isStale) {
                  statusTone = 'text-fg-subtle'
                  statusText = t('dashboard.clientKey.stale')
                } else if (isFresh) {
                  statusTone = 'text-success'
                  statusText = t('dashboard.clientKey.fresh')
                } else {
                  statusTone = 'text-warning'
                  statusText = t('dashboard.clientKey.mixed', {
                    pct: ratio,
                    recent: formatNumber(recent),
                  })
                }
                return (
                  <li
                    key={label}
                    className={`flex items-center gap-3 rounded-lg border px-3.5 py-3 transition-colors ${
                      isTop
                        ? 'border-accent/40 bg-accent-soft/40'
                        : 'border-border bg-bg-subtle/30'
                    }`}
                  >
                    {/* Rank badge */}
                    <span
                      className={`inline-flex size-8 shrink-0 items-center justify-center rounded-md text-xs font-semibold ${
                        isTop
                          ? 'bg-accent text-fg-on-accent'
                          : 'bg-bg-elevated text-fg-muted'
                      }`}
                    >
                      #{idx + 1}
                    </span>
                    <div className="flex flex-1 flex-col min-w-0">
                      <Typography.Text code className="self-start text-sm">
                        {label}
                      </Typography.Text>
                      <div
                        className={`mt-1 text-xs ${statusTone}`}
                        aria-label={statusText}
                      >
                        {statusText}
                      </div>
                    </div>
                    <div className="text-right">
                      <div
                        className={`text-lg font-semibold tabular-nums leading-tight ${
                          isTop ? 'text-accent' : 'text-fg'
                        }`}
                      >
                        {formatNumber(count)}
                      </div>
                      <div className="mt-0.5 text-[11px] uppercase tracking-wider text-fg-subtle">
                        {t('dashboard.totalRequestsUnit')}
                      </div>
                    </div>
                  </li>
                )
              })}
          </ul>
          )}
        </Card>
      </div>

      {/* Models in use — each enabled alias becomes a small "routing
       * card" with the public name, a Provider → model flow line,
       * and a status tag. Wider gap between rows mirrors the byClientKey
       * card for visual consistency. */}
      <div className='mt-4'>
        <Card
          title={
            <div className="flex items-center gap-2.5">
              <span className="inline-flex size-7 items-center justify-center rounded-md bg-accent-soft text-accent">
                <LuBox className="size-4" aria-hidden />
              </span>
              <span className="text-base font-semibold">
                {t('dashboard.modelsInUse')}
              </span>
              <Tag className="!ml-auto !mr-0">{enabledAliases.length}</Tag>
            </div>
          }
          variant="outlined"
        >
          {enabledAliases.length === 0 ? (
            <EmptyState
              icon={<LuZap className="size-10 mx-auto text-fg-subtle" />}
              title={t('dashboard.noAliases')}
              description={t('dashboard.noAliasesDesc')}
            />
          ) : (
            <ul className="space-y-2">
              {enabledAliases.map((alias) => {
                const provider = providerById.get(alias.providerId)
                return (
                  <li
                    key={alias.id}
                    className="flex items-center gap-3 rounded-lg border border-border bg-bg-subtle/30 px-3.5 py-3"
                  >
                    {/* Provider dot + name */}
                    <span
                      className="inline-flex size-8 shrink-0 items-center justify-center rounded-md bg-bg-elevated text-fg-muted"
                      aria-hidden
                    >
                      <LuBox className="size-4" />
                    </span>
                    <div className="flex flex-1 flex-col min-w-0">
                      <Typography.Text code className="self-start text-sm">
                        {alias.alias}
                      </Typography.Text>
                      <div className="mt-1.5 flex items-center gap-1.5 text-xs text-fg-muted min-w-0">
                        <span className="truncate">
                          {provider?.name ?? t('dashboard.unknownProvider')}
                        </span>
                        <span className="shrink-0 text-fg-subtle">→</span>
                        <span className="truncate font-mono text-fg-secondary">
                          {alias.providerModel}
                        </span>
                      </div>
                    </div>
                    <Tag
                      color={alias.enabled ? 'success' : 'default'}
                      className="shrink-0"
                    >
                      {alias.enabled
                        ? t('dashboard.badge.active')
                        : t('dashboard.badge.disabled')}
                    </Tag>
                  </li>
                )
              })}
            </ul>
          )}
        </Card>
      </div>
    </div>
  )
}

/**
 * ModelTrendChartContainer wraps the lazy `ModelTrendChart` with the
 * Segmented / Select controls that drive its `mode`, `selectedModel`,
 * and `windowHours` props. Owning the state here (rather than inside
 * the chart) keeps the chart presentational and lets the Dashboard
 * pull values directly from the store.
 *
 * The backend returns per-alias hourly buckets as
 * `Record<alias, HourBucket[]>`; the regenerated Wails `Stats` type
 * hasn't been bumped yet, so we read it through `any` until the
 * `wails build` regenerates `wailsjs/go/models.ts` with the new field.
 */
function ModelTrendChartContainer() {
  const stats = useConfigStore((s) => s.stats)
  const modelAliases = useConfigStore((s) => s.modelAliases)
  const t = useT()

  const [mode, setMode] = useState<'all' | 'single'>('all')
  const [windowHours, setWindowHours] = useState<24 | 168>(24)
  const [selectedModel, setSelectedModel] = useState<string | undefined>(undefined)

  const enabledAliases = useMemo(
    () => modelAliases.filter((a) => a.enabled),
    [modelAliases],
  )

  // Fallback to `any` cast: the regenerated Wails `Stats` type has
  // not yet picked up `requestsByHourByModel`, but the backend
  // already returns `Record<alias, HourBucket[]>` over the wire.
  const data = useMemo(
    () =>
      ((stats as unknown as {requestsByHourByModel?: Record<string, HourBucket[]>})
        ?.requestsByHourByModel ?? {}) as Record<string, HourBucket[]>,
    [stats],
  )

  const handleModeChange = useCallback((value: 'all' | 'single') => {
    setMode(value)
  }, [])

  const handleWindowChange = useCallback((value: 24 | 168) => {
    setWindowHours(value)
  }, [])

  const handleModelChange = useCallback((value: string) => {
    setSelectedModel(value)
  }, [])

  // In single mode the Select drives the chart; if the user hasn't
  // picked anything yet we fall back to the first enabled alias so
  // the chart always has a target to render against.
  const effectiveSelected =
    mode === 'single' ? selectedModel ?? enabledAliases[0]?.alias : undefined

  return (
    <Card variant="outlined" className="lg:col-span-2">
      <Space size="middle" wrap className="mb-3">
        <Segmented
          value={windowHours}
          onChange={(v) => handleWindowChange(v as 24 | 168)}
          options={[
            {label: t('dashboard.modelTrend.window.24h'), value: 24},
            {label: t('dashboard.modelTrend.window.7d'), value: 168},
          ]}
        />
        <Segmented
          value={mode}
          onChange={(v) => handleModeChange(v as 'all' | 'single')}
          options={[
            {label: t('dashboard.modelTrend.mode.all'), value: 'all'},
            {label: t('dashboard.modelTrend.mode.single'), value: 'single'},
          ]}
        />
        {mode === 'single' && enabledAliases.length > 0 && (
          <Select
            value={effectiveSelected}
            onChange={handleModelChange}
            placeholder={t('dashboard.modelTrend.selectModel')}
            options={enabledAliases.map((a) => ({
              label: a.alias,
              value: a.alias,
            }))}
            style={{minWidth: 180}}
          />
        )}
      </Space>
      <Suspense fallback={<Skeleton active />}>
        <ModelTrendChart
          data={data}
          mode={mode}
          selectedModel={effectiveSelected}
          windowHours={windowHours}
          title={t('dashboard.modelTrend.title')}
          description={t('dashboard.modelTrend.desc')}
          className="lg:col-span-2"
        />
      </Suspense>
    </Card>
  )
}