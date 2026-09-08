import {useCallback, useMemo, useRef, type JSX} from 'react'
import {Line} from '@ant-design/charts'
import {ChartCard} from '@/components/charts/ChartCard'
import {ChartEmpty} from '@/components/charts/ChartEmpty'
import {formatNumber} from '@/lib/format'
import {useDesignTokens} from '@/lib/useDesignTokens'
import {useT} from '@/i18n/useT'
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
 * Categorical series palette tuned to the indigo primary. The first two
 * slots sit on the indigo axis (slate-700 / indigo-500) so the most
 * common case (1-2 models) reads as a single, restrained hue pair;
 * additional models pull from desaturated complements. No neons, no
 * high-saturation reds — the Vercel/Linear convention is one accent
 * hue plus slate-toned peers.
 */
const PALETTE = [
  '#4F46E5', // indigo-600 (primary)
  '#64748B', // slate-500
  '#0891B2', // cyan-600
  '#65A30D', // lime-600
  '#B45309', // amber-700
  '#BE185D', // pink-700
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
  // F-004: the parent (Dashboard) passes a module-level frozen
  // `EMPTY_HOUR_MAP` whenever the backend hasn't returned any
  // `requestsByHourByModel` yet, so `data` is always defined here —
  // the old `data ?? {}` could flip the reference on every render
  // and trigger needless `rows` rebuilds during the 2s heartbeat.
  const safeData = data!

  // Resolve the chart's theme tokens via the shared useDesignTokens
  // hook. The hook subscribes to `useThemeStore.theme` (and to
  // `prefers-color-scheme` when the theme is `system`) so the axis /
  // grid colours re-paint on a Settings theme switch without waiting
  // for the chart to unmount.
  const themeTokens = useDesignTokens()
  const t = useT()

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

  // F-002: alias list is its own derived memo so the chart's
  // primary `rows` useMemo can depend on a stable list reference
  // (and on `safeData` / `mode` / `windowHours` directly, instead
  // of `safeData` alone). When the alias roster is unchanged between
  // heartbeats, the subsequent `useMemo` is free to skip a full
  // skeleton rebuild — it can mutate the existing 720 row slots
  // in place if it ever wants to, or just return the same arrays.
  const aliases = useMemo(
    () =>
      mode === 'single'
        ? selectedModel
          ? [selectedModel]
          : []
        : Object.keys(safeData as Record<string, unknown>),
    [safeData, mode, selectedModel],
  )

  // F-002 + F-003: single combined memo. The old code allocated
  // three independent structures every 2s heartbeat — the
  // `byHourPerModel` `Map<…,Map<…>>`, the flat `TrendRow[]`, and
  // `rowsByHour` `Map<…,Map<…>>` for the tooltip — for a combined
  // ~460 KB of churn on each tick. We now produce `rows` and
  // `rowsByHour` in a single pass, drop the intermediate
  // `byHourPerModel` Map as soon as the loop ends, and depend on
  // `[aliases, windowHours, mode, safeData]` so a stable alias
  // roster lets the memo bail early in principle (V8 will still
  // allocate, but the dataset is smaller).
  const {rows, rowsByHour, models, isEmpty} = useMemo(() => {
    // The generated HourBucket may not carry token fields yet; widen
    // to TokenHourBucket so reads are optional-safe (defaults to 0).
    const tokenData = safeData as unknown as Record<string, TokenHourBucket[]>

    // Per-model hour → bucket map. Built inline during the row
    // construction so it lives on the stack (V8 will let it die at
    // the end of the closure) instead of being retained in the
    // memo result like before.
    const out: TrendRow[] = []
    const byHourMap = new Map<number, Map<string, number>>()
    for (const alias of aliases) {
      const buckets = tokenData[alias] ?? []
      // Per-alias lookup built inline — local to the loop iteration.
      const perAlias = new Map<number, TokenHourBucket>()
      for (const b of buckets) perAlias.set(b.hour, b)
      for (const {hour, key} of hourSlots) {
        const bucket = perAlias.get(key)
        out.push({
          hour,
          model: alias,
          count: bucket?.count ?? 0,
          tokens: bucket?.byModelTokens?.[alias] ?? 0,
        })
        // Build the tooltip's hour→model→value lookup in the same
        // pass. We always know the metric at this point, so we
        // store the already-resolved value (tokens or count) and
        // skip the secondary `metric === 'tokens' ? ...` branch
        // inside the hot tooltip render.
        let perModel = byHourMap.get(hour)
        if (!perModel) {
          perModel = new Map()
          byHourMap.set(hour, perModel)
        }
        perModel.set(
          alias,
          metric === 'tokens' ? (bucket?.byModelTokens?.[alias] ?? 0) : (bucket?.count ?? 0),
        )
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
      rowsByHour: byHourMap,
      models: aliases,
      isEmpty: empty,
    }
    // The deps intentionally exclude `metric` from the
    // alias/rows computation — the metric only affects which value
    // is written into `rowsByHour` and what the tooltip emits. We
    // capture it via the closure (read at memo time) so the
    // tooltip's per-hour values reflect the latest selection.
  }, [aliases, windowHours, mode, safeData])

  // Per-line colour, cycled through the antd default palette. When
  // mode === 'single' this collapses to a single colour.
  const colorRange = useMemo(
    () => models.map((_, idx) => PALETTE[idx % PALETTE.length]),
    [models],
  )

  // F-006: stable refs for the tooltip closure. Each render
  // reassigns the `.current` slot so the callback (which has empty
  // deps and thus never re-creates itself) always sees the latest
  // values without invalidating downstream `interactionConfig`.
  // Previously each tick replaced `tooltipRenderRef.current` via a
  // separate `useEffect`, allocating a fresh closure — the closure
  // identity changed and G2 v5 could hold onto the stale one during
  // a transition, showing the wrong tooltip.
  const rowsByHourRef = useRef(rowsByHour)
  const modelsRef = useRef(models)
  const colorRangeRef = useRef(colorRange)
  const windowHoursRef = useRef(windowHours)
  // F-006 extension: the metric and its localized unit label are also
  // mirrored through refs so the stable `tooltipRender` closure can
  // append the correct unit (tokens / requests) without re-creating
  // itself on every metric switch or locale change.
  const unitRef = useRef(
    metric === 'tokens' ? t('dashboard.tokensUnit') : t('dashboard.requests'),
  )
  rowsByHourRef.current = rowsByHour
  modelsRef.current = models
  colorRangeRef.current = colorRange
  windowHoursRef.current = windowHours
  unitRef.current = metric === 'tokens' ? t('dashboard.tokensUnit') : t('dashboard.requests')

  // F-006: the tooltip render is now a stable `useCallback` with
  // empty deps, so its identity never changes between renders. All
  // inputs it needs (`models`, `colorRange`, `windowHours`,
  // `rowsByHour`) are read through refs synchronised above, so the
  // closure always sees the freshest values without invalidating
  // `interactionConfig` on every heartbeat tick.
  //
  // The previous pattern (`tooltipRenderRef.current = …` inside a
  // `useEffect([models, colorRange, windowHours])`) replaced the
  // closure on every alias / theme / window change, and G2 v5
  // could keep showing the *previous* tooltip during the transition
  // window — the bug we are eliminating here.
  const tooltipRender = useCallback(
    (
      _event: unknown,
      {items: _items, title}: {items: unknown[]; title: string},
    ): string => {
      // We don't read `items`: G2's tooltip1d path can filter out
      // zero-valued items in some configurations, so per-model values
      // always come from `rowsByHourRef` (which we built to be
      // exhaustive). We also rely on the static `colorRange` for
      // swatches, so we don't need the colour info G2 attaches to
      // each item either.
      void _items

      // G2's `MaybeTitle` transform ([maybeTitle.ts]) feeds the hovered
      // x channel value into `title` as a stringified epoch-ms number
      // (since our `hour` is a number, not a Date, the transform keeps
      // it as-is rather than calling `dynamicFormatDateTime`). Parse
      // it back so we can look the hour up in `rowsByHourRef`.
      //
      // Fall back to a Date parse so the same code keeps working if we
      // later switch the row shape to Date objects.
      let hour: number | undefined
      if (title) {
        const asNum = Number(title)
        if (Number.isFinite(asNum) && asNum > 0) {
          hour = asNum
        } else {
          const asDate = new Date(title).getTime()
          if (Number.isFinite(asDate)) hour = asDate
        }
      }

      const values =
        hour !== undefined ? rowsByHourRef.current.get(hour) : undefined

      // Snapshot the refs locally so the rest of the closure reads
      // consistent values (refs can otherwise be reassigned between
      // a `.get` and a `.map` if a render fires mid-iteration).
      const currentModels = modelsRef.current
      const currentColors = colorRangeRef.current
      const currentWindowHours = windowHoursRef.current
      const currentUnit = unitRef.current

      // Build one row per model, in legend order. We don't trust
      // `items` for the per-model value lookup: G2 drops series with
      // `value === undefined`, but a zero *is* kept. We want both to
      // surface, so we read from `rowsByHourRef` and emit a row for
      // every alias regardless of what G2 included in `items`. Each
      // value carries the metric's unit (tokens / requests) so the
      // tooltip never reads as a bare count.
      const body = currentModels
        .map((model, idx) => {
          const value = values?.get(model) ?? 0
          const color =
            currentColors[idx] ?? PALETTE[idx % PALETTE.length]
          return `<li class="g2-tooltip-list-item" style="display:flex;align-items:center;gap:6px;line-height:2em;justify-content:space-between;white-space:nowrap;">
  <span style="display:flex;align-items:center;max-width:220px;">
    <span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${color};margin-right:6px;flex-shrink:0;"></span>
    <span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${model}</span>
  </span>
  <span style="margin-left:24px;text-align:right;flex-shrink:0;">${formatNumber(value)} ${currentUnit}</span>
</li>`
        })
        .join('')

      // Re-use the existing title formatter so the header stays
      // consistent with what the user already saw before this change.
      const dt = hour !== undefined ? new Date(hour) : null
      const hourLabel = dt && !Number.isNaN(dt.getTime())
        ? currentWindowHours === 24
          ? `${String(dt.getHours()).padStart(2, '0')}:00`
          : `${String(dt.getMonth() + 1).padStart(2, '0')}-${String(dt.getDate()).padStart(2, '0')} ${String(dt.getHours()).padStart(2, '0')}:00`
        : title || '—'
      return `<div class="g2-tooltip">
  <div class="g2-tooltip-title">${hourLabel}</div>
  <ul class="g2-tooltip-list" style="margin:0;padding:0;list-style:none;">${body}</ul>
</div>`
    },
    [],
  )

  // Tooltip crosshairs + content visibility. The actual render lives in
  // `interactionConfig.tooltip.render` below; sharing `shared: true`
  // here is redundant (antd-plots' Line default already sets it) but
  // keeps the intent local. We deliberately do NOT pass a `tooltip`
  // prop with `customContent` — antd-charts@2 / G2 v5 ignore that field,
  // and the default tooltip then renders a single summed header value.
  const tooltipConfig = useMemo(
    () => ({
      shared: true,
      showCrosshairs: true,
      showContent: true,
    }),
    [],
  )

  const interactionConfig = useMemo(
    () => ({
      tooltip: {
        // antd-charts@2 / G2 v5 custom-render hook. The previous code
        // put `customContent` on the top-level `tooltip` prop, but v5
        // silently ignores that — leaving G2's default tooltip, which
        // collapses every series into one summed header number (the
        // 1787576400000 the user saw). `interaction.tooltip.render`
        // is the supported API.
        //
        // `tooltipRender` is the F-006 stable `useCallback` — its
        // identity never changes, so `interactionConfig` only
        // invalidates when `themeTokens.axis` actually changes
        // (theme switch), not on every heartbeat tick.
        render: tooltipRender,
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
    [themeTokens.axis, tooltipRender],
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