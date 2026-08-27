import {useMemo, type JSX} from 'react'
import {Line} from '@ant-design/charts'
import {ChartCard} from '@/components/charts/ChartCard'
import {ChartEmpty} from '@/components/charts/ChartEmpty'
import {formatNumber} from '@/lib/format'
import type {HourBucket} from '@/types'

/**
 * One row for the G2 v5 Line component. `hour` is an epoch-ms number
 * so the X scale can be a real time axis (not categorical), and `model`
 * carries the line identifier for the color series.
 *
 * NOTE: `hour` MUST stay numeric — the G2 v5 default `d3Time` tick
 * generator parses its input as a Date and silently returns `NaN` Date
 * objects when handed an ISO string, which is what produces `NaN:00`
 * axis labels. The numeric ms avoids that code path entirely.
 */
type TrendRow = {
  hour: number
  model: string
  count: number
  /** Total tokens (input + output) for the alias in this hour. Only
   * populated when `metric === 'tokens'`; always 0 for missing hours. */
  tokens: number
}

/**
 * The Wails-generated `HourBucket` hasn't been regenerated with the
 * new token fields yet (see optimize-model-trend-tokens spec). We read
 * them through an optional-safe extension so the chart compiles today
 * and stays correct once `wails build` refreshes
 * `wailsjs/go/models.ts`. Every field here is optional and defaults to
 * 0 / undefined on older payloads, so old daily JSON stays safe.
 */
type TokenHourBucket = HourBucket & {
  inputTokens?: number
  outputTokens?: number
  byModelTokens?: Record<string, number>
}

/**
 * antd's default chart palette. Models are mapped to colours in
 * declaration order, cycling back to the start once we run out.
 */
const PALETTE = [
  '#1677ff',
  '#52c41a',
  '#faad14',
  '#722ed1',
  '#13c2c2',
  '#fa541c',
  '#eb2f96',
  '#a0d911',
  '#2f54eb',
  '#f5222d',
] as const

export interface ModelTrendChartProps {
  /** Raw hourly buckets keyed by alias name. Undefined is treated as
   * an empty object so the parent Dashboard can render this safely
   * before the first stats payload arrives. */
  data?: Record<string, HourBucket[]>
  /** Which metric drives the Y axis and tooltip. Defaults to 'tokens'
   * per the optimize-model-trend-tokens spec — operators care about
   * token burn (cost), not raw request counts. */
  metric?: 'requests' | 'tokens'
  /** 'all' shows every model line; 'single' shows just selectedModel. */
  mode: 'all' | 'single'
  /** Required when mode === 'single'. */
  selectedModel?: string
  /** 24 → last 24 hours; 168 → last 7 days; 720 → last 30 days.
   *  All windows are rendered at hourly granularity. */
  windowHours: 24 | 168 | 720
  /** Card title. The chart wrapper is only rendered when this is set. */
  title: string
  /** Optional sub-title shown under the card title. */
  description?: string
  /** Optional node rendered on the right side of the card header. Use
   * this for `Segmented` / `Select` controls. */
  extra?: JSX.Element
  /** Forwarded to the wrapping Card. */
  className?: string
}

/** antd `--ant-*` tokens live on the ConfigProvider root, not on
 * `:root`. Reading them at render time lets the chart's axis / grid
 * pick up the current light or dark theme so it never mismatches.
 *
 * G2 v5 paints into a `<canvas>` so CSS-var values on the color
 * range are not resolved by the canvas context — canvas only takes
 * literal hex/rgb. We resolve the tokens to concrete strings here. */
function readToken(name: string, fallback: string): string {
  const root = document.querySelector('.api-distribution') as HTMLElement | null
  if (!root) return fallback
  const v = getComputedStyle(root).getPropertyValue(name).trim()
  return v || fallback
}

/**
 * ModelTrendChart renders a per-model request trend Line chart.
 *
 * The backend only returns buckets for hours that had traffic, so a
 * sparse dataset would otherwise collapse the chart to a handful of
 * isolated points. The X-axis is re-anchored to a continuous rolling
 * `windowHours` window ending at the current hour, with missing hours
 * defaulting to count=0 so the line stays connected across gaps.
 */
export function ModelTrendChart({
  data,
  metric = 'tokens',
  mode,
  selectedModel,
  windowHours,
  title,
  description,
  extra,
  className,
}: ModelTrendChartProps): JSX.Element {
  const safeData = data ?? {}

  // Resolve the chart's theme tokens ONCE per render. Without this
  // memoization, `readToken` would call `getComputedStyle` on every
  // axis/grid label, which is the single biggest perf cost during
  // the 2s stats heartbeat.
  const themeTokens = useMemo(
    () => ({
      axis: readToken('--ant-color-text-tertiary', 'rgba(0, 0, 0, 0.35)'),
      grid: readToken('--ant-color-fill-secondary', 'rgba(0, 0, 0, 0.06)'),
    }),
    [],
  )

  // Precompute the continuous rolling window of hour slots. The slot
  // array is reused across the per-model loop and every 2s heartbeat
  // re-render (the date arithmetic below would otherwise run
  // `windowHours` times per alias on every stats tick).
  //
  // `hourAnchor` is the index of the current hour and only changes once
  // per hour, so the memo recomputes exactly at each hour boundary. A
  // stable `[windowHours]` dep alone would pin the window to the hour
  // the chart mounted and silently drop the newest hour's data point
  // once the clock rolled past it.
  const hourAnchor = Math.floor(Date.now() / 3_600_000)
  const hourSlots = useMemo(() => {
    const anchor = new Date()
    anchor.setMinutes(0, 0, 0)
    // Use raw ms (not ISO strings) so the G2 Time scale domain is
    // a clean numeric axis — the default `d3Time` tick generator parses
    // its input as Date and silently produces `NaN` Date objects when
    // it can't, which is what produces `NaN:00` axis labels when the
    // data points carry ISO strings.
    const slots: {hour: number; key: number}[] = []
    for (let i = windowHours - 1; i >= 0; i--) {
      const d = new Date(anchor.getTime() - i * 60 * 60 * 1000)
      slots.push({
        hour: d.getTime(),
        key: Math.floor(d.getTime() / 1000),
      })
    }
    return slots
  }, [windowHours, hourAnchor])

  const {rows, models, isEmpty} = useMemo(() => {
    // The generated HourBucket may not carry token fields yet; widen
    // to TokenHourBucket so reads are optional-safe (defaults to 0).
    const tokenData = safeData as unknown as Record<string, TokenHourBucket[]>

    // Pick which aliases to render. In single mode we still always
    // emit a row for the selected model even when it has no entries
    // so the empty-state check below can decide whether to render.
    const aliases =
      mode === 'single'
        ? selectedModel
          ? [selectedModel]
          : []
        : Object.keys(tokenData)

    // Per-model hour → bucket map keyed by unix seconds, matching the
    // backend so lookups always resolve.
    const byHourPerModel = new Map<string, Map<number, TokenHourBucket>>()
    for (const alias of aliases) {
      const map = new Map<number, TokenHourBucket>()
      for (const b of tokenData[alias] ?? []) {
        map.set(b.hour, b)
      }
      byHourPerModel.set(alias, map)
    }

    // Realize a continuous rolling window ending at the current hour,
    // reusing the precomputed hourSlots so the date math runs once.
    const out: TrendRow[] = []
    for (const alias of aliases) {
      const byHour = byHourPerModel.get(alias) ?? new Map<number, TokenHourBucket>()
      for (const {hour, key} of hourSlots) {
        const bucket = byHour.get(key)
        out.push({
          hour,
          model: alias,
          count: bucket?.count ?? 0,
          tokens: bucket?.byModelTokens?.[alias] ?? 0,
        })
      }
    }

    // Empty-state logic: in single mode, empty when the selected
    // alias is missing or has no buckets; in all mode, empty when
    // there are no aliases at all or every alias is empty.
    const empty =
      mode === 'single'
        ? !selectedModel || (safeData[selectedModel] ?? []).length === 0
        : aliases.length === 0 ||
          aliases.every((alias) => (safeData[alias] ?? []).length === 0)

    return {
      rows: out,
      models: aliases,
      isEmpty: empty,
    }
  }, [safeData, mode, selectedModel, windowHours])

  // Per-line colour, cycled through the antd default palette. When
  // mode === 'single' this collapses to a single colour.
  const colorRange = useMemo(
    () => models.map((_, idx) => PALETTE[idx % PALETTE.length]),
    [models],
  )

  // Tooltip — G2 v5's `tooltip.title` is a *channel field reference*, not
  // a render function: passing a function makes G2 display the function
  // itself / an inconsistent value instead of the hovered hour. The only
  // reliable way to format the title (and render items with localised
  // metric labels) is `customContent`, which we use below.
  //
  // `items` here are deprecated in favour of `customContent`; G2 passes
  // every hovered series' {name, value, color, ...} to the callback.
  const tooltipConfig = useMemo(
    () => ({
      shared: true,
      showCrosshairs: true,
      customContent: (title: string, items: unknown[]) => {
        const rows = (items ?? []) as {
          name?: string
          value?: number
          color?: string
          data?: {hour?: number | string}
        }[]
        // `hour` is epoch ms (number) since we switched the row shape
        // off ISO strings. Fall back to G2's default title (which it
        // passes as the second arg) for safety.
        const hourRow = rows[0]?.data
        const raw = hourRow?.hour ?? title
        const dt = raw !== undefined && raw !== null && raw !== '' ? new Date(raw) : null
        const hourLabel = dt && !Number.isNaN(dt.getTime())
          ? windowHours === 24
            ? `${String(dt.getHours()).padStart(2, '0')}:00`
            : `${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')} ${String(dt.getHours()).padStart(2, '0')}:00`
          : '—'
        const body = rows
          .map(
            (it) => `<li class="g2-tooltip-list-item" style="display:flex;align-items:center;gap:6px;line-height:2em;justify-content:space-between;white-space:nowrap;">
  <span style="display:flex;align-items:center;max-width:220px;">
    <span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${it.color ?? '#1677ff'};margin-right:6px;flex-shrink:0;"></span>
    <span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${it.name ?? '—'}</span>
  </span>
  <span style="margin-left:24px;text-align:right;flex-shrink:0;">${formatNumber(it.value ?? 0)}</span>
</li>`,
          )
          .join('')
        return `<div class="g2-tooltip">
  <div class="g2-tooltip-title">${hourLabel}</div>
  <ul class="g2-tooltip-list" style="margin:0;padding:0;list-style:none;">${body}</ul>
</div>`
      },
    }),
    [metric, windowHours],
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
        showCrosshairs: true,
        showContent: true,
      },
      elementHighlight: {
        background: true,
      },
    }),
    [themeTokens.axis],
  )

  // Animation: default G2 enter is 800ms which makes a 2s heartbeat
  // feel like the chart is constantly repainting. 300ms keeps the
  // animation perceptible while staying out of the way.
  const animateConfig = useMemo(() => ({enter: {duration: 300}}), [])

  return (
    <ChartCard
      title={title}
      description={description}
      {...(extra ? {extra} : {})}
      className={className}
    >
      {isEmpty ? (
        <ChartEmpty />
      ) : (
        <div className="mt-3 h-56 rounded-lg border border-border bg-bg-subtle/40 px-1 py-2">
          <Line
            data={rows}
            xField="hour"
            yField={metric === 'tokens' ? 'tokens' : 'count'}
            colorField="model"
            theme="classic"
            scale={{
              color: {
                domain: models,
                range: colorRange,
              },
              x: {
                type: 'time',
                // Hour-aligned ticks for the 24h view (every HH:00) and
                // day-aligned MM-DD ticks for 7d / 30d, via timeTicks.
                // labelAutoHide below only drops labels that physically
                // overlap on narrow widths, keeping every tick on axis.
                tickMethod: (min: number, max: number) =>
                  timeTicks(min, max, windowHours),
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
                // Rotate when labels crowd; let G2 auto-hide only the
                // labels that physically overlap while keeping every
                // hour/day tick on the axis.
                labelAutoRotate: true,
                labelAutoHide: true,
                labelFontSize: 10,
                labelFill: themeTokens.axis,
                labelFormatter: (v: unknown) => {
                  const d = new Date(v as string)
                  // 7d / 30d ticks are day-aligned, so a date label is
                  // enough; the tooltip still shows the exact hour.
                  if (windowHours !== 24) {
                    const mm = String(d.getMonth() + 1).padStart(2, '0')
                    const dd = String(d.getDate()).padStart(2, '0')
                    return `${mm}-${dd}`
                  }
                  return `${String(d.getHours()).padStart(2, '0')}:00`
                },
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
            tooltip={tooltipConfig}
            interaction={interactionConfig}
            animate={animateConfig}
            height={200}
            autoFit
            aria-label={title}
          />
        </div>
      )}
    </ChartCard>
  )
}

/**
 * timeTicks returns hour-aligned (24h window) or day-aligned (7d / 30d)
 * tick timestamps for the time X-axis, so the chart reads as a genuine
 * per-hour timeline rather than G2's default "nice" ticks (which collapse
 * a 24h span to ~every 2h and make hourly data look non-hourly).
 *
 * CRITICAL: G2's `@antv/scale` Time scale calls the configured tickMethod
 * with (min, max) as `Date` objects (the domain is Date-typed) and expects
 * the result to be a `Date[]` — the built-in `d3Time` returns `Date[]`.
 * Returning epoch-millisecond numbers instead makes G2 resolve tick
 * positions against the wrong coordinate space, so the X axis no longer
 * lines up with the per-hour data points. Every element is therefore
 * wrapped in `new Date(...)` before being returned.
 */
function timeTicks(min: number, max: number, windowHours: number): Date[] {
  const mn = new Date(min).getTime()
  const mx = new Date(max).getTime()
  if (!Number.isFinite(mn) || !Number.isFinite(mx) || mx <= mn) return [new Date(mn)]

  if (windowHours <= 24) {
    const step = 60 * 60 * 1000 // 1 hour
    // ceil() snaps the first tick up to the start of its hour, keeping
    // ticks on exact hour boundaries even if the data domain is padded.
    const start = Math.ceil(mn / step) * step
    const ticks: Date[] = []
    for (let t = start; t <= mx + 1; t += step) ticks.push(new Date(t))
    return ticks
  }

  // 7d / 30d: one tick per local calendar day so the long span stays
  // legible. Hourly data still drives the line; only the labels coarsen.
  const dayStep = 24 * 60 * 60 * 1000
  const sd = new Date(mn)
  sd.setHours(0, 0, 0, 0)
  let t = sd.getTime()
  if (t < mn) t += dayStep
  const ticks: Date[] = []
  for (; t <= mx + 1; t += dayStep) ticks.push(new Date(t))
  return ticks
}

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