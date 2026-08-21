import {useMemo, type JSX} from 'react'
import {Line} from '@ant-design/charts'
import {Card, Typography} from 'antd'
import {LuActivity} from 'react-icons/lu'
import {EmptyState} from '@/components/ui/EmptyState'
import {formatNumber} from '@/lib/format'
import type {HourBucket} from '@/types'

/**
 * One row for the G2 v5 Line component. `hour` is an ISO timestamp so
 * the X scale can be a real time axis (not categorical), and `model`
 * carries the line identifier for the color series.
 */
type TrendRow = {
  hour: string
  model: string
  count: number
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
  /** 'all' shows every model line; 'single' shows just selectedModel. */
  mode: 'all' | 'single'
  /** Required when mode === 'single'. */
  selectedModel?: string
  /** 24 → last 24 hours; 168 → last 7 days. */
  windowHours: 24 | 168
  /** Card title. The chart wrapper is only rendered when this is set. */
  title: string
  /** Optional sub-title shown under the card title. */
  description?: string
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
  mode,
  selectedModel,
  windowHours,
  title,
  description,
  className,
}: ModelTrendChartProps): JSX.Element {
  const safeData = data ?? {}

  const {rows, models, themeColors, isEmpty} = useMemo(() => {
    // Pick which aliases to render. In single mode we still always
    // emit a row for the selected model even when it has no entries
    // so the empty-state check below can decide whether to render.
    const aliases =
      mode === 'single'
        ? selectedModel
          ? [selectedModel]
          : []
        : Object.keys(safeData)

    // Per-model hour → bucket map keyed by unix seconds, matching the
    // backend so lookups always resolve.
    const byHourPerModel = new Map<string, Map<number, HourBucket>>()
    for (const alias of aliases) {
      const map = new Map<number, HourBucket>()
      for (const b of safeData[alias] ?? []) {
        map.set(b.hour, b)
      }
      byHourPerModel.set(alias, map)
    }

    // Realize a continuous rolling window ending at the current hour.
    const anchor = new Date()
    anchor.setMinutes(0, 0, 0)
    const out: TrendRow[] = []
    for (const alias of aliases) {
      const byHour = byHourPerModel.get(alias) ?? new Map<number, HourBucket>()
      for (let i = windowHours - 1; i >= 0; i--) {
        const d = new Date(anchor.getTime() - i * 60 * 60 * 1000)
        const key = Math.floor(d.getTime() / 1000)
        const bucket = byHour.get(key)
        out.push({
          hour: d.toISOString(),
          model: alias,
          count: bucket?.count ?? 0,
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

    const axis = readToken('--ant-color-text-tertiary', 'rgba(0, 0, 0, 0.35)')
    const grid = readToken(
      '--ant-color-fill-secondary',
      'rgba(0, 0, 0, 0.06)',
    )

    return {
      rows: out,
      models: aliases,
      themeColors: {axis, grid},
      isEmpty: empty,
    }
  }, [safeData, mode, selectedModel, windowHours])

  // Per-line colour, cycled through the antd default palette. When
  // mode === 'single' this collapses to a single colour.
  const colorRange = useMemo(
    () => models.map((_, idx) => PALETTE[idx % PALETTE.length]),
    [models],
  )

  // Tooltip — keep G2 defaults, just point the per-item label at the
  // model name so each line shows its own label in the crosshair.
  const tooltipConfig = useMemo(
    () => ({
      shared: true,
      items: [
        {
          name: (d: {model: string}) => d.model,
          field: 'count',
        },
      ],
    }),
    [],
  )

  const interactionConfig = useMemo(
    () => ({
      tooltip: {
        crosshairs: {
          type: 'x' as const,
          lineStroke: themeColors.axis,
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
    [themeColors.axis],
  )

  // Honour the "render only if title is provided" contract — an empty
  // title means the parent decided not to surface this card.
  if (!title) {
    return <></>
  }

  return (
    <Card title={title} variant="outlined" className={className}>
      {description && (
        <Typography.Text type="secondary" className="block">
          {description}
        </Typography.Text>
      )}

      {isEmpty ? (
        <div className="mt-5">
          <EmptyState
            icon={<LuActivity className="size-10 mx-auto text-fg-subtle" />}
            title="暂无模型趋势数据"
            description="No model trend data yet"
          />
        </div>
      ) : (
        <div className="mt-3 h-56 rounded-lg border border-border bg-bg-subtle/40 px-1 py-2">
          <Line
            data={rows}
            xField="hour"
            yField="count"
            colorField="model"
            scale={{
              color: {
                domain: models,
                range: colorRange,
              },
              x: {
                type: 'time',
                tickCount: windowHours === 168 ? 12 : 24,
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
                labelAutoHide: false,
                labelFontSize: 10,
                labelFill: themeColors.axis,
                labelFormatter: (v: unknown) => {
                  const d = new Date(v as string)
                  if (windowHours === 168) {
                    const mm = String(d.getMonth() + 1).padStart(2, '0')
                    const dd = String(d.getDate()).padStart(2, '0')
                    const hh = String(d.getHours()).padStart(2, '0')
                    return `${mm}-${dd} ${hh}:00`
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
                labelFill: themeColors.axis,
                gridStroke: themeColors.grid,
                gridStrokeWidth: 1,
                gridLineDash: [3, 4],
                lineStroke: 'transparent',
                tickStroke: 'transparent',
              },
            }}
            tooltip={tooltipConfig}
            interaction={interactionConfig}
            height={200}
            autoFit
          />
        </div>
      )}
    </Card>
  )
}

/**
 * integerTicks — same algorithm as RecentTrafficChart. The default
 * G2 tick generator happily emits fractional values (0.2/0.4/0.6)
 * for small maxima, which looks wrong on a request-count axis. We
 * pick a step of 1/2/5 * power of ten, capped to ~4 ticks, then
 * round every returned value.
 */
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