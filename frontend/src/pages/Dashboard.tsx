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
  LuCalendarDays,
  LuCalendarRange,
  LuCirclePlus,
  LuCopy,
  LuKeyRound,
  LuPlay,
  LuRotateCw,
  LuServer,
  LuSparkles,
  LuSquare,
  LuTrendingDown,
  LuTrendingUp,
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
// The 24h stacked traffic chart shares the @ant-design/charts bundle, so
// it is lazy-loaded alongside the trend chart to keep first paint light.
const RecentTrafficChart = lazy(() =>
  import('@/components/charts/RecentTrafficChart').then((m) => ({
    default: m.RecentTrafficChart,
  })),
)
import { useConfigStore } from '@/store/config'
import { toast } from '@/store/toast'
import { formatNumber } from '@/lib/format'
import { copyToClipboard } from '@/lib/clipboard'
import { useT } from '@/i18n/useT'
import { useLocaleStore } from '@/store/locale'
import type { HourBucket } from '@/types'

interface QuickStartStep {
  key: string
  to?: string
  label: string
}

// Module-level empty object used as a stable fallback for
// `ModelTrendChartContainer.data` whenever the backend has not
// returned `requestsByHourByModel` yet. Without this constant a fresh
// `{}` is allocated on every render, which flips `data`'s reference
// and triggers a full rebuild of the trend chart's
// `rows` / `rowsByHour` memos — exactly the heartbeat allocation
// churn the F-004 fix is meant to prevent.
const EMPTY_HOUR_MAP = Object.freeze({}) as Record<string, HourBucket[]>

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
  const statsError = useConfigStore((s) => s.refreshErrorStats)
  const locale = useLocaleStore((s) => s.locale)
  const t = useT()

  // Subscribe to backend stats:changed events emitted every ~2s.
  useEffect(() => {
    const unsubscribe = useConfigStore.getState().subscribeStatsEvents()
    return unsubscribe
  }, [])

  // Rolling-window consumption: all-time / last 7 days / last 30 days,
  // each with the previous equivalent window for period-over-period
  // deltas. Consumption is measured in total tokens (input + output).
  const allTime = statsWC?.allTime
  const week = statsWC?.week
  const prevWeek = statsWC?.prevWeek
  const month = statsWC?.month
  const prevMonth = statsWC?.prevMonth

  const allTimeTokens = (allTime?.inputTokens ?? 0) + (allTime?.outputTokens ?? 0)
  const weekTokens = (week?.inputTokens ?? 0) + (week?.outputTokens ?? 0)
  const prevWeekTokens = (prevWeek?.inputTokens ?? 0) + (prevWeek?.outputTokens ?? 0)
  const monthTokens = (month?.inputTokens ?? 0) + (month?.outputTokens ?? 0)
  const prevMonthTokens = (prevMonth?.inputTokens ?? 0) + (prevMonth?.outputTokens ?? 0)

  // Percent change vs the previous equivalent window. Null when there
  // is no baseline so the UI renders a dash instead of a misleading
  // "-100%".
  const pctChange = (cur: number, prev: number): number | null =>
    prev > 0 ? ((cur - prev) / prev) * 100 : null
  const weekDelta = pctChange(weekTokens, prevWeekTokens)
  const monthDelta = pctChange(monthTokens, prevMonthTokens)

  // 7-day error rate vs the previous 7 days.
  const weekErrorRate =
    (week?.requests ?? 0) > 0 ? ((week?.errors ?? 0) / (week?.requests ?? 0)) * 100 : 0
  const prevWeekErrorRate =
    (prevWeek?.requests ?? 0) > 0 ? ((prevWeek?.errors ?? 0) / (prevWeek?.requests ?? 0)) * 100 : 0

  const enabledAliases = useMemo(
    () => modelAliases.filter((a) => a.enabled),
    [modelAliases],
  )

  const providerById = useMemo(() => {
    return new Map(providers.map((p) => [p.id, p]))
  }, [providers])

  // byClientKey is a plain object from the backend; without memoizing
  // the sort, every 2s heartbeat allocates a fresh `Object.entries(...)`
  // array and forces the by-client-key list to re-render even when the
  // totals haven't changed. Keyed only on the source map so the list
  // re-sorts only when the underlying data actually moves.
  const sortedClientKeys = useMemo(
    () =>
      Object.entries(stats?.requestsByClientKey ?? {}).sort(
        ([, a], [, b]) => b - a,
      ),
    [stats?.requestsByClientKey],
  )

  // Use the i18n catalog when the key exists, otherwise fall back to the
  // hard-coded English message so we don't depend on locale changes for
  // a single error-string. `t` is a stable reference (see `useT`), so we
  // don't need to list it as a dep — only the active locale affects
  // the resolved string.
  const statsStaleMessage = useMemo(() => {
    const translated = t('dashboard.stats.stale')
    return translated === 'dashboard.stats.stale'
      ? 'Statistics may be out of date'
      : translated
  }, [locale])

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
    // `t` is a stable reference (useCallback with `[]` deps inside
    // useT) so we don't list it here; the memo only needs to
    // re-derive when the data driving the steps actually changes.
  }, [providers.length, modelAliases.length, serverStatus?.running])

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

      {/* Stat row — each KPI is a layered surface with a quiet depth
       * shadow and a tiny accent edge. The staggered entrance makes
       * the four cards arrive as a wave instead of a flat grid. */}
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
            <Card
              variant="outlined"
              className="card-depth-1 enter-wave lift-hover"
              styles={{ body: { padding: '24px' } }}
              style={{animationDelay: '0ms'}}
            >
              <Statistic
                title={t('dashboard.allTimeConsumption')}
                value={formatNumber(allTimeTokens)}
                valueStyle={{fontSize: 28, fontWeight: 600}}
                prefix={<LuZap className="size-4 text-accent" aria-hidden />}
                suffix={t('dashboard.tokensUnit')}
              />
              <TokenBreakdown
                input={allTime?.inputTokens ?? 0}
                output={allTime?.outputTokens ?? 0}
                unit={t('dashboard.tokensUnit')}
              />
            </Card>
            <Card
              variant="outlined"
              className="card-depth-1 enter-wave lift-hover"
              styles={{ body: { padding: '24px' } }}
              style={{animationDelay: '40ms'}}
            >
              <Statistic
                title={t('dashboard.weekConsumption')}
                value={formatNumber(weekTokens)}
                valueStyle={{fontSize: 28, fontWeight: 600}}
                prefix={<LuCalendarDays className="size-4 text-accent" aria-hidden />}
                suffix={t('dashboard.tokensUnit')}
              />
              <DeltaLine prev={prevWeekTokens} prevLabel={t('dashboard.prevWeek')} percent={weekDelta} />
              <TokenBreakdown input={week?.inputTokens ?? 0} output={week?.outputTokens ?? 0} unit={t('dashboard.tokensUnit')} />
            </Card>
            <Card
              variant="outlined"
              className="card-depth-1 enter-wave lift-hover"
              styles={{ body: { padding: '24px' } }}
              style={{animationDelay: '80ms'}}
            >
              <Statistic
                title={t('dashboard.monthConsumption')}
                value={formatNumber(monthTokens)}
                valueStyle={{fontSize: 28, fontWeight: 600}}
                prefix={<LuCalendarRange className="size-4 text-accent" aria-hidden />}
                suffix={t('dashboard.tokensUnit')}
              />
              <DeltaLine prev={prevMonthTokens} prevLabel={t('dashboard.prevMonth')} percent={monthDelta} />
              <TokenBreakdown input={month?.inputTokens ?? 0} output={month?.outputTokens ?? 0} unit={t('dashboard.tokensUnit')} />
            </Card>
            <Card
              variant="outlined"
              className="card-depth-1 enter-wave lift-hover"
              styles={{ body: { padding: '24px' } }}
              style={{animationDelay: '120ms'}}
            >
              <Statistic
                title={t('dashboard.weekErrorRate')}
                value={weekErrorRate === 0 ? '0%' : `${weekErrorRate.toFixed(1)}%`}
                valueStyle={{fontSize: 28, fontWeight: 600}}
                prefix={<LuTriangleAlert
                  className={`size-4 ${weekErrorRate > 5 ? 'text-danger' : weekErrorRate > 1 ? 'text-warning' : 'text-fg-subtle'}`}
                  aria-hidden
                />}
              />
              <Typography.Text type="secondary" className="mt-2! block text-xs">
                {t('dashboard.prevWeek')}: {prevWeekErrorRate.toFixed(1)}%
              </Typography.Text>
            </Card>
          </>
        )}
      </div>

      {statsError && (
        <Alert type="warning" showIcon message={statsStaleMessage} />
      )}

      {/* Quick start */}
      {quickStart.length > 0 && (
        <Card
          variant="outlined"
          className="card-depth-2 enter-wave"
        >
          <div className="flex items-center gap-2">
            <LuSparkles className="size-4 text-fg-muted" aria-hidden />
            <div className="text-base font-semibold text-fg">{t('dashboard.quickStart.title')}</div>
          </div>
          <Typography.Text type="secondary" className="block mt-3">
            {t('dashboard.quickStart.desc')}
          </Typography.Text>
          <ol className="mt-5 space-y-2.5">
            {quickStart.map((step, idx) => (
              <li
                key={step.key}
                className="flex items-center gap-3 rounded-lg border border-border bg-bg-subtle/40 px-4 py-3"
              >
                <span className="inline-flex size-7 items-center justify-center rounded-full bg-bg-elevated border border-border text-fg-muted text-xs font-semibold shrink-0">
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
              <span className="inline-flex size-7 items-center justify-center rounded-md bg-bg-subtle text-fg-muted border border-border">
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
          className="lg:col-span-1 card-depth-2 enter-wave"
          style={{animationDelay: '160ms'}}
        >
          {/* Hero status block: large pulsing dot + status word. */}
          <div className="flex items-center gap-3 rounded-lg border border-border bg-bg-subtle/40 px-4 py-3.5">
            <StatusDot tone={running ? 'online' : 'offline'} pulsing={running} />
            <div className="flex flex-col leading-tight">
              <span className="text-xs uppercase tracking-wider text-fg-muted">
                {t('dashboard.serverStatus')}
              </span>
              <span className="mt-0.5 text-base font-semibold text-fg">
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

      {/* Recent traffic — 24h stacked success/error column chart. The
       * component existed with full i18n coverage but was never mounted,
       * leaving the homepage without a traffic overview. Placed full-width
       * under the trend chart so the two chart surfaces read as one band. */}
      <div>
        <Suspense fallback={<Skeleton active />}>
          <RecentTrafficChart traffic={stats?.requestsByHour ?? []} />
        </Suspense>
      </div>

      {/* Traffic by client key — sorted by total traffic so the busiest
       * client surfaces first, with a per-row key chip, recent-24h
       * substat, and a right-aligned big total. */}
      <div>
        <Card
          title={
            <div className="flex items-center gap-2.5">
              <span className="inline-flex size-7 items-center justify-center rounded-md bg-bg-subtle text-fg-muted border border-border">
                <LuKeyRound className="size-4" aria-hidden />
              </span>
              <span className="text-base font-semibold">
                {t('dashboard.byClientKey')}
              </span>
            </div>
          }
          variant="outlined"
          className="card-depth-1 enter-wave"
          style={{animationDelay: '200ms'}}
        >
          {Object.keys(stats?.requestsByClientKey ?? {}).length === 0 ? (
            <EmptyState
              icon={<LuKeyRound className="size-10 mx-auto text-fg-subtle" />}
              title={t('dashboard.noClientKeyTraffic')}
              description={t('dashboard.noClientKeyTrafficDesc')}
            />
          ) : (
            <ul className="space-y-2">
            {sortedClientKeys.map(([label, count], idx) => {
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
                        ? 'border-border-strong bg-bg-subtle/60'
                        : 'border-border bg-bg-subtle/30'
                    }`}
                  >
                    {/* Rank badge */}
                    <span
                      className={`inline-flex size-8 shrink-0 items-center justify-center rounded-md text-xs font-semibold ${
                        isTop
                          ? 'bg-fg text-bg'
                          : 'bg-bg-elevated text-fg-muted border border-border'
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
                          isTop ? 'text-fg' : 'text-fg-muted'
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
       <div>
        <Card
          title={
            <div className="flex items-center gap-2.5">
              <span className="inline-flex size-7 items-center justify-center rounded-md bg-bg-subtle text-fg-muted border border-border">
                <LuBox className="size-4" aria-hidden />
              </span>
              <span className="text-base font-semibold">
                {t('dashboard.modelsInUse')}
              </span>
              <Tag className="!ml-auto !mr-0">{enabledAliases.length}</Tag>
            </div>
          }
          variant="outlined"
          className="card-depth-1 enter-wave"
          style={{animationDelay: '240ms'}}
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
  const [windowHours, setWindowHours] = useState<24 | 168 | 720>(24)
  const [selectedModel, setSelectedModel] = useState<string | undefined>(undefined)
  // Default to 'tokens' per the optimize-model-trend-tokens spec.
  const [metric, setMetric] = useState<'requests' | 'tokens'>('tokens')

  const enabledAliases = useMemo(
    () => modelAliases.filter((a) => a.enabled),
    [modelAliases],
  )

  // Fallback to `any` cast: the regenerated Wails `Stats` type has
  // not yet picked up `requestsByHourByModel`, but the backend
  // already returns `Record<alias, HourBucket[]>` over the wire.
  //
  // Re-derived from `stats` whenever the store emits a new
  // (reference-different) stats object — including the 2s heartbeat.
  // Because `statsShallowEqual` swaps the reference on `totalRequests`
  // changes, `byModel` carries the latest hourly buckets and is the
  // single source of truth for `data`.
  const byModel = useMemo(
    () =>
      (stats as unknown as {requestsByHourByModel?: Record<string, HourBucket[]>})
        ?.requestsByHourByModel,
    [stats],
  )
  // `data` follows the latest `byModel` directly. It MUST NOT be pinned
  // to the alias roster: same aliases with updated hourly buckets (new
  // requests / hour rollover) keep the key set stable, so pinning on the
  // roster froze the chart at its first frame. Depending on `byModel`
  // keeps the trend live while still avoiding needless re-renders when
  // the heartbeat doesn't change the stats reference.
  //
  // The fallback uses the module-level `EMPTY_HOUR_MAP` instead of a
  // fresh inline `{}` so the reference stays stable across renders
  // — a new `{}` each time would flip `data`'s identity and force the
  // downstream chart to re-run its `rows` useMemo on every heartbeat,
  // re-allocating ~720 `TrendRow`s even when `byModel` is briefly
  // `undefined` (e.g. a transient stats fetch error).
  const data = useMemo(
    () => byModel ?? EMPTY_HOUR_MAP,
    [byModel],
  )

  const handleModeChange = useCallback((value: 'all' | 'single') => {
    setMode(value)
  }, [])

  const handleWindowChange = useCallback((value: 24 | 168 | 720) => {
    setWindowHours(value)
  }, [])

  const handleModelChange = useCallback((value: string) => {
    setSelectedModel(value)
  }, [])

  const handleMetricChange = useCallback((value: 'requests' | 'tokens') => {
    setMetric(value)
  }, [])

  // In single mode the Select drives the chart; if the user hasn't
  // picked anything yet we fall back to the first enabled alias so
  // the chart always has a target to render against.
  const effectiveSelected =
    mode === 'single' ? selectedModel ?? enabledAliases[0]?.alias : undefined

  // The Segmented row drives the chart's `mode` / `windowHours` /
  // `metric`. Render it once and pass via `extra` so the
  // ModelTrendChart's wrapping Card header stays consistent with
  // RecentTrafficChart (same Card, same header placement).
  const controls = (
    <Space size="small" wrap>
      <Segmented
        value={metric}
        onChange={(v) => handleMetricChange(v as 'requests' | 'tokens')}
        options={[
          {label: t('dashboard.modelTrend.metric.requests'), value: 'requests'},
          {label: t('dashboard.modelTrend.metric.tokens'), value: 'tokens'},
        ]}
      />
      <Segmented
        value={windowHours}
        onChange={(v) => handleWindowChange(v as 24 | 168 | 720)}
        options={[
          {label: t('dashboard.modelTrend.window.24h'), value: 24},
          {label: t('dashboard.modelTrend.window.7d'), value: 168},
          {label: t('dashboard.modelTrend.window.30d'), value: 720},
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
  )

  return (
    <Suspense fallback={<Skeleton active />}>
      <ModelTrendChart
        data={data}
        metric={metric}
        mode={mode}
        selectedModel={effectiveSelected}
        windowHours={windowHours}
        title={t('dashboard.modelTrend.title')}
        description={t('dashboard.modelTrend.desc')}
        extra={controls}
        className="lg:col-span-2"
      />
    </Suspense>
  )
}

/**
 * DeltaLine renders the "vs previous period" comparison under a KPI
 * value: an up/down arrow with the percent change (green up / red down)
 * plus the previous period's absolute value. When there is no baseline
 * the percent is omitted and only the previous value is shown. Both
 * call sites pass token counts, so the previous-period value carries
 * the token unit too — a bare number would read as ambiguous.
 */
function DeltaLine({
  prev,
  prevLabel,
  percent,
}: {
  prev: number
  prevLabel: string
  percent: number | null
}) {
  const t = useT()
  return (
    <Typography.Text type="secondary" className="mt-2! block text-xs">
      {percent !== null ? (
        <span className={percent >= 0 ? 'text-success' : 'text-danger'}>
          {percent >= 0 ? (
            <LuTrendingUp className="mr-0.5 inline size-3.5" aria-hidden />
          ) : (
            <LuTrendingDown className="mr-0.5 inline size-3.5" aria-hidden />
          )}
          {Math.abs(percent).toFixed(1)}%
        </span>
      ) : (
        <span className="text-fg-muted">—</span>
      )}
      <span className="mx-1">·</span>
      {prevLabel}: {formatNumber(prev)} {t('dashboard.tokensUnit')}
    </Typography.Text>
  )
}

/**
 * TokenBreakdown renders the input / output token split under a
 * consumption KPI value, each with the shared token unit suffix so
 * the numbers never read as ambiguous raw counts.
 */
function TokenBreakdown({
  input,
  output,
  unit,
}: {
  input: number
  output: number
  unit: string
}) {
  const t = useT()
  return (
    <Typography.Text type="secondary" className="mt-1! block text-xs">
      {t('dashboard.inputShort')} {formatNumber(input)} {unit} · {t('dashboard.outputShort')}{' '}
      {formatNumber(output)} {unit}
    </Typography.Text>
  )
}