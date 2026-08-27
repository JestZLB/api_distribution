import {useMemo, type JSX, type ReactNode} from 'react'
import {Column} from '@ant-design/charts'
import {BorderBeam} from 'antd'
import {LuZap} from 'react-icons/lu'
import {ChartCard} from '@/components/charts/ChartCard'
import {ChartEmpty} from '@/components/charts/ChartEmpty'
import {formatNumber} from '@/lib/format'
import {useT} from '@/i18n/useT'
import type {HourBucket} from '@/types'

/**
 * One pre-aggregated column row used directly by the stacked chart.
 *
 * Because a stacked Column only renders one segment per datum, we carry
 * the whole-bar metrics (total / errors) on every row so the tooltip can
 * show accurate totals without reaching across segments.
 */
type BarRow = {
  hour: string
  status: string
  count: number
  total: number
  errors: number
}

interface RecentTrafficChartProps {
  /** Raw hourly buckets (may only include hours that had traffic). */
  traffic: HourBucket[]
  /** Extra classes to pass through to the wrapping Card. */
  className?: string
}

/** antd `--ant-*` tokens are declared on the ConfigProvider root, not
 * `:root`. Reading them here at render time lets the chart's axis / grid
 * pick up the current light or dark theme so it never mismatches.
 *
 * IMPORTANT: G2 v5 paints into a `<canvas>`, so CSS-var values
 * (`var(--color-...)`) on the color range are NOT resolved by the
 * canvas context — canvas only takes literal hex/rgb. We therefore
 * resolve the tokens to concrete strings here and pass them down. */
function readToken(name: string, fallback: string): string {
  const root = document.querySelector('.api-distribution') as HTMLElement | null
  if (!root) return fallback
  const v = getComputedStyle(root).getPropertyValue(name).trim()
  return v || fallback
}

/**
 * RecentTrafficChart renders the "近期流量" stacked column chart.
 *
 * The backend only returns buckets for hours that actually had traffic
 * (last 7 days), so a sparse dataset would otherwise collapse the chart
 * to a handful of isolated bars. The X-axis is re-anchored to a
 * continuous rolling 24-hour window with empty hours defaulting to 0,
 * so the chart always shows the full 24h shape.
 */
export function RecentTrafficChart({
  traffic,
  className,
}: RecentTrafficChartProps): JSX.Element {
  const t = useT()

  // Resolve all canvas colors and axis/grid tokens ONCE per mount.
  // Without this memoization the chart would call getComputedStyle
  // four times every heartbeat and force a full re-paint.
  const themeTokens = useMemo(
    () => ({
      successHex: readToken('--ant-color-success', '#52c41a'),
      errorHex: readToken('--ant-color-error', '#ff4d4f'),
      axis: readToken('--ant-color-text-tertiary', 'rgba(0, 0, 0, 0.35)'),
      grid: readToken('--ant-color-fill-secondary', 'rgba(0, 0, 0, 0.06)'),
    }),
    [],
  )

  // Translate the success/error labels once; the rows are emitted in
  // both languages (we keep them distinct strings inside each row so
  // G2 can match them via the domain/range pairs).
  const labels = useMemo(
    () => ({
      success: t('dashboard.status.success'),
      error: t('dashboard.status.error'),
    }),
    [t],
  )

  const {barData, summary, totalByKey} = useMemo(() => {
    const byHour = new Map<number, HourBucket>()
    for (const b of traffic) byHour.set(b.hour, b)

    // Realize a continuous rolling 24h window ending at the current hour.
    const anchor = new Date()
    anchor.setMinutes(0, 0, 0)
    const rows: BarRow[] = []
    let total = 0
    let errors = 0
    let peak = 0
    let peakHour = ''
    for (let i = 23; i >= 0; i--) {
      // Buckets are keyed by unix seconds truncated to the hour; this
      // matches the backend so index lookups always resolve.
      const d = new Date(anchor.getTime() - i * 60 * 60 * 1000)
      const key = Math.floor(d.getTime() / 1000)
      const bucket = byHour.get(key)
      const t = bucket?.count ?? 0
      const e = bucket?.errors ?? 0
      const success = Math.max(t - e, 0)
      const hour = d.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})
      // Two rows per hour so the column chart can stack the two series.
      rows.push({hour, status: labels.success, count: success, total: t, errors: e})
      rows.push({hour, status: labels.error, count: e, total: t, errors: e})
      total += t
      errors += e
      if (t > peak) {
        peak = t
        peakHour = hour
      }
    }

    // O(1) lookup map for the tooltip's customContent renderer. The
    // tooltip needs both the success count and the error count for
    // the hovered hour, so we store them together.
    const totalByKey = new Map<string, {success: number; errors: number}>()
    for (let i = 0; i < rows.length; i += 2) {
      const successRow = rows[i]
      const errorRow = rows[i + 1]
      totalByKey.set(successRow.hour, {
        success: successRow.count,
        errors: errorRow.count,
      })
    }

    return {
      barData: rows,
      summary: {total, errors, peak, peakHour},
      totalByKey,
    }
  }, [traffic, labels.success, labels.error])

  // tooltipConfig wires the G2 v5 stacked-column tooltip.
//
// G2 v5's built-in per-item filtering (combining `series: true` with
// a custom `value` callback) is unreliable for stacked columns: when
// `field` is set, G2 ignores the `value` callback and reads d.count
// directly, so all items ended up showing the same number. When
// `field` is omitted, the item gets hidden entirely (which is why
// the "总请求数" row vanished). We sidestep both bugs by using
// `customContent`, which lets us render the tooltip HTML ourselves
// from the underlying per-hour totals and the i18n labels.
const tooltipConfig = useMemo(
    () => ({
      shared: false,
      showCrosshairs: true,
      title: (d: {hour: string}) => d.hour,
      customContent: (title: string, _items: unknown[]) => {
        const entry = totalByKey.get(title)
        if (!entry) {
          return `<div class="g2-tooltip-title">${title}</div>`
        }
        // Each entry stores {success, errors}; recompute the total
        // here so it always agrees with the per-series rows below.
        const success = entry.success
        const errors = entry.errors
        const total = success + errors
        return `
<div class="g2-tooltip">
  <div class="g2-tooltip-title">${title}</div>
  <ul class="g2-tooltip-list" style="margin:0;padding:0;list-style:none;">
    <li class="g2-tooltip-list-item" style="display:flex;align-items:center;gap:6px;">
      <span class="g2-tooltip-marker" style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${themeTokens.successHex};"></span>
      <span style="flex:1;">${labels.success}</span>
      <span>${formatNumber(success)}</span>
    </li>
    <li class="g2-tooltip-list-item" style="display:flex;align-items:center;gap:6px;">
      <span class="g2-tooltip-marker" style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${themeTokens.errorHex};"></span>
      <span style="flex:1;">${labels.error}</span>
      <span>${formatNumber(errors)}</span>
    </li>
    <li class="g2-tooltip-list-item" style="display:flex;align-items:center;gap:6px;margin-top:4px;border-top:1px solid rgba(0,0,0,0.08);padding-top:4px;">
      <span style="flex:1;font-weight:600;">${t('dashboard.totalRequests')}</span>
      <span style="font-weight:600;">${formatNumber(total)}</span>
    </li>
  </ul>
</div>`
      },
    }),
    [totalByKey, labels.success, labels.error, themeTokens.successHex, themeTokens.errorHex, t],
  )

  const interactionConfig = useMemo(
    () => ({
      tooltip: {
        crosshairs: {
          type: 'x' as const,
          lineStroke: themeTokens.axis,
          lineStrokeOpacity: 0.45,
          lineLineDash: [3, 3],
          lineWidth: 1,
        },
        // The `shared` and `items` flags live on tooltipConfig; the
        // interaction config owns the crosshair / domStyles only.
        showCrosshairs: true,
        showContent: true,
        domStyles: {
          'g2-tooltip': {
            backgroundColor: 'var(--ant-color-bg-elevated)',
            boxShadow: '0 6px 20px rgba(0,0,0,0.12)',
            borderRadius: '8px',
            border: '1px solid var(--ant-color-border)',
            padding: '8px 12px',
            fontSize: '12px',
            color: 'var(--ant-color-text)',
          },
          'g2-tooltip-title': {
            color: 'var(--ant-color-text)',
            fontWeight: 600,
            marginBottom: '6px',
          },
          'g2-tooltip-list-item': {
            color: 'var(--ant-color-text)',
          },
          'g2-tooltip-marker': {
            width: '8px',
            height: '8px',
            borderRadius: '50%',
          },
        },
      },
      // Use only one highlight interaction — stacking two causes
      // G2 to re-evaluate every bar in the chart on each hover,
      // which is the dominant frame-time cost.
      elementHighlight: {
        background: true,
        offset: 1,
        borderRadius: 4,
      },
    }),
    [themeTokens.axis],
  )

  // Animation: default G2 enter is 800ms which makes a 2s heartbeat
  // feel like the chart is constantly repainting. 300ms keeps the
  // animation perceptible while staying out of the way.
  const animateConfig = useMemo(() => ({enter: {duration: 300}}), [])

  const title = t('dashboard.recentTraffic')
  const description = t('dashboard.recentTrafficDesc')

  if (traffic.length === 0) {
    return (
      <ChartCard title={title} description={description} className={className}>
        <ChartEmpty
          icon={<LuZap className="size-10 mx-auto text-fg-subtle" />}
          title={t('dashboard.noTraffic')}
          description={t('dashboard.noTrafficDesc')}
        />
      </ChartCard>
    )
  }

  return (
    <ChartCard title={title} description={description} className={className}>
      {/* KPI strip — three cells with a soft muted background and a tiny
       * accent dot at the top edge. Reads as a metrics strip rather than
       * a stat row, so the chart below isn't fighting for hierarchy. */}
      <div className="mt-4 grid grid-cols-3 gap-2">
        <MetricCell
          label={t('dashboard.totalRequests')}
          value={formatNumber(summary.total)}
          accent={themeTokens.successHex}
        />
        <MetricCell
          label={t('logs.detail.error')}
          value={formatNumber(summary.errors)}
          accent={themeTokens.errorHex}
          muted={summary.errors === 0}
        />
        <MetricCell
          label={t('dashboard.peak24h')}
          value={summary.peak > 0 ? `${formatNumber(summary.peak)}` : '—'}
          sub={summary.peak > 0 ? `at ${summary.peakHour}` : undefined}
          accent="var(--ant-color-primary)"
        />
      </div>

      {/* Column chart — soft inner panel so the bars have a balanced
       * background to live against. autoFit + the wrapping `h-56`
       * keeps the canvas right-sized. */}
      <div className="mt-3 h-56 rounded-lg border border-border bg-bg-subtle/40 px-1 py-2">
        <Column
          data={barData}
          xField="hour"
          yField="count"
          stack
          colorField="status"
          theme="classic"
          scale={{
            color: {
              domain: [labels.success, labels.error],
              // G2 v5 paints into a canvas, so CSS var() values are NOT
              // resolved by the canvas context. Pass literal hex
              // strings resolved from the live antd theme tokens.
              range: [themeTokens.successHex, themeTokens.errorHex],
            },
            y: {
              domainMin: 0,
              nice: true,
              tickMethod: (_min: number, max: number) => integerTicks(0, max),
            },
          }}
          axis={{
            x: {
              title: false,
              labelAutoRotate: false,
              labelAutoHide: true,
              labelFontSize: 10,
              labelFill: themeTokens.axis,
              lineStroke: 'transparent',
              tickStroke: 'transparent',
            },
            y: {
              title: false,
              labelFormatter: (v: number) => formatNumber(Math.round(v)),
              labelFontSize: 10,
              labelFill: themeTokens.axis,
              gridStroke: themeTokens.grid,
              gridStrokeWidth: 1,
              gridLineDash: [3, 4],
              lineStroke: 'transparent',
              tickStroke: 'transparent',
            },
          }}
          legend={false}
          tooltip={tooltipConfig}
          interaction={interactionConfig}
          animate={animateConfig}
          style={{
            maxWidth: 18,
            radiusTopLeft: 4,
            radiusTopRight: 4,
          }}
          height={200}
          autoFit
          aria-label={title}
        />
      </div>

      {/* Inline legend — sits below the chart instead of floating in the
       * legend corner, so we never collide with the y-axis labels. */}
      <div className="mt-3 flex items-center gap-5 text-xs text-fg-muted">
        <LegendDot color={themeTokens.successHex} label={labels.success} />
        <LegendDot color={themeTokens.errorHex} label={labels.error} />
        <span className="ml-auto">{t('dashboard.recent24h')}</span>
      </div>
    </ChartCard>
  )
}

function MetricCell({
  label,
  value,
  sub,
  accent,
  muted = false,
}: {
  label: string
  value: string
  sub?: string
  accent: string
  muted?: boolean
}): ReactNode {
  return (
    // BorderBeam needs a single DOM child whose position:relative
    // provides the host context, so we wrap the cell body in one
    // <div>. `accent` is forwarded as the beam colour so each KPI
    // card gets a colour-coded flow. The legacy top accent strip was
    // removed once BorderBeam shipped — the beam now owns the
    // colour-coded edge.
    <BorderBeam duration={6} lineWidth={2} size={80} color={accent}>
      <div className="relative rounded-md border border-border bg-bg-subtle/40 px-3 py-2.5 overflow-hidden">
        <div className="text-[11px] font-medium uppercase tracking-wider text-fg-muted">
          {label}
        </div>
        <div
          className={`mt-1 text-lg font-semibold tabular-nums leading-tight ${
            muted ? 'text-fg-muted' : 'text-fg'
          }`}
        >
          {value}
        </div>
        {sub && (
          <div className="mt-0.5 text-[11px] text-fg-muted">
            {sub}
          </div>
        )}
      </div>
    </BorderBeam>
  )
}

function LegendDot({color, label}: {color: string; label: string}): ReactNode {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        className="inline-block size-2.5 rounded-full"
        style={{background: color}}
        aria-hidden
      />
      <span>{label}</span>
    </span>
  )
}

// integerTicks returns evenly-spaced whole-number tick values spanning
// [min, max]. The default G2 tick generator happily emits fractional
// values (0.2/0.4/0.6/0.8) for small maxima, which looks wrong on a
// request-count axis. We pick a step of 1/2/5 * power of ten, capped to
// ~4 ticks, then round every returned value.
function integerTicks(min: number, max: number): number[] {
  if (!Number.isFinite(min) || !Number.isFinite(max) || max <= 0) return [0]
  if (max === min) return [Math.round(min)]

  const targetTicks = 4
  const span = max - min
  const rawStep = span / targetTicks
  const magnitude = Math.pow(10, Math.floor(Math.log10(rawStep)))
  const step =
    [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => span / s <= targetTicks) ??
    magnitude * 10

  const ticks: number[] = []
  for (let v = Math.max(0, Math.floor(min / step) * step); v <= max + 1e-9; v += step) {
    ticks.push(Math.round(v))
  }
  return ticks
}