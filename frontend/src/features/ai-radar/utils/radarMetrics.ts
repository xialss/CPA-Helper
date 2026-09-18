import type {
  AIRadarEffort,
  AIRadarHistorySeries,
  AIRadarHistoryWindow,
  AIRadarPoint,
} from '@/shared/types/api'

/**
 * Missing measurements always render as an em dash. Upstream omits fields for
 * a handful of points and a substituted zero would read as a measured value.
 */
export const radarMissingLabel = '—'

/** Reasoning tiers in the order a model traverses them. */
export const radarEffortOrder = [
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
  'ultra',
] as const satisfies readonly AIRadarEffort[]

export type RadarEffort = AIRadarEffort

export interface RadarModelMeta {
  model: string
  label: string
  color: string
}

/**
 * The GPT subset this phase renders. Model ids are protocol values and keep
 * their upstream spelling; colors follow the source site so a model keeps one
 * identity across the card wall and both charts. Order is wall order.
 */
export const radarModels: RadarModelMeta[] = [
  { model: 'gpt-6-astra', label: 'GPT-6 Astra', color: '#f97316' },
  { model: 'gpt-5.6-sol', label: 'GPT-5.6 Sol', color: '#eab308' },
  { model: 'gpt-5.6-terra', label: 'GPT-5.6 Terra', color: '#60a5fa' },
  { model: 'gpt-5.6-luna', label: 'GPT-5.6 Luna', color: '#c7d2e0' },
]

const radarModelIndex = new Map(radarModels.map((item, index) => [item.model, index]))

/** Also excludes retired entries from an older server or cached response. */
export function isRadarModelVisible(model: string): boolean {
  return model.trim().toLowerCase() !== 'gpt-5.5'
}

export const radarFallbackModelColor = '#94a3b8'

export function radarModelMeta(model: string): RadarModelMeta {
  const known = radarModels.find((item) => item.model === model)
  if (known) return known
  return { model, label: model, color: radarFallbackModelColor }
}

/** Ascending rank used to walk a model's trajectory across reasoning tiers. */
export function radarEffortRank(effort: string): number {
  const index = radarEffortOrder.indexOf(effort as RadarEffort)
  return index === -1 ? radarEffortOrder.length : index
}

/**
 * Descending rank used for the card wall, so a group reads
 * ULTRA → MAX → XHIGH → HIGH → MEDIUM → LOW. An effort the source site adds
 * later is unknown to this list and therefore sorts last rather than first.
 */
export function radarEffortDisplayRank(effort: string): number {
  const index = radarEffortOrder.indexOf(effort as RadarEffort)
  return index === -1 ? -1 : index
}

export interface RadarTierGroup {
  model: string
  label: string
  color: string
  tiers: AIRadarPoint[]
}

/**
 * Groups the card wall by model and orders each group by reasoning depth. Models
 * follow the configured order and unexpected ids still render after them.
 */
export function groupRadarTiers(points: AIRadarPoint[]): RadarTierGroup[] {
  const buckets = new Map<string, AIRadarPoint[]>()
  for (const point of points) {
    if (!isRadarModelVisible(point.model)) continue
    const bucket = buckets.get(point.model)
    if (bucket) {
      bucket.push(point)
    } else {
      buckets.set(point.model, [point])
    }
  }

  return [...buckets.entries()]
    .sort(([left], [right]) => compareRadarModels(left, right))
    .map(([model, tiers]) => ({
      model,
      label: radarModelMeta(model).label,
      color: radarModelMeta(model).color,
      tiers: [...tiers].sort(compareRadarTiers),
    }))
}

/**
 * The source normalizes "the highest combined cost in this chart to 100" while
 * drawing, so the same rule is applied here and only to the values actually
 * plotted. Stored values stay unnormalized.
 */
export function normalizeRadarValues(values: Array<number | null>): (value: number | null) => number | null {
  let max = 0
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value) && value > max) {
      max = value
    }
  }
  return (value) => {
    if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0 || max <= 0) {
      return null
    }
    return (value / max) * 100
  }
}

export interface RadarMetricDefinition<T> {
  value: (point: T) => number | null
}

/**
 * Decade-aligned bounds over every plotted value, for a logarithmic axis.
 *
 * A log axis must never be left without a domain. ECharts marks a scale blank
 * when no visible series contributes data, and it does not draw a blank axis at
 * all — hiding the last series through the legend stripped the efficiency
 * chart's x axis of its labels and split lines. Passing these bounds
 * explicitly, derived from every row rather than the visible ones, keeps the
 * axis drawn and stops it rescaling under the legend.
 */
export function radarLogExtent(values: readonly number[]): { min: number; max: number } | null {
  let min = Infinity
  let max = -Infinity
  for (const value of values) {
    if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) continue
    if (value < min) min = value
    if (value > max) max = value
  }
  if (min === Infinity) return null
  const lower = 10 ** Math.floor(Math.log10(min))
  const upper = 10 ** Math.ceil(Math.log10(max))
  return {
    min: lower,
    max: upper > lower ? upper : lower * 10,
  }
}

/**
 * The three cost axes the source site offers. `combined` is the default.
 */
export const radarChartMetrics = {
  combined: { value: (point: AIRadarPoint) => point.combined_cost_index },
  minutes: { value: (point: AIRadarPoint) => point.average_minutes },
  price: { value: (point: AIRadarPoint) => point.average_price_usd },
} satisfies Record<string, RadarMetricDefinition<AIRadarPoint>>

export type RadarChartMetric = keyof typeof radarChartMetrics

export const radarHistoryMetrics = {
  iq: { value: (point: AIRadarHistorySeries['points'][number]) => point.iq },
  price: { value: (point: AIRadarHistorySeries['points'][number]) => point.average_price_usd },
  minutes: { value: (point: AIRadarHistorySeries['points'][number]) => point.average_minutes },
  agent_steps: {
    value: (point: AIRadarHistorySeries['points'][number]) => point.average_agent_steps,
  },
} satisfies Record<string, RadarMetricDefinition<AIRadarHistorySeries['points'][number]>>

export type RadarHistoryMetric = keyof typeof radarHistoryMetrics

/**
 * One presentation path per quantity, so the same measurement never reads
 * differently in two panels: costs always carry the currency symbol, durations
 * always carry the unit, missing values always render as the dash.
 */
export function formatRadarMetric(
  metric: RadarChartMetric | RadarHistoryMetric,
  value: number | null,
  minutesSuffix = ' min',
): string {
  switch (metric) {
    case 'price':
      return formatRadarUsd(value)
    case 'minutes':
      return formatRadarMinutes(value, minutesSuffix)
    case 'combined':
      return formatRadarIndex(value)
    case 'agent_steps':
      return formatRadarSteps(value)
    default:
      return formatRadarIQ(value)
  }
}

/** Orders model ids the way the card wall does; unknown ids sort last. */
export function compareRadarModels(left: string, right: string): number {
  const leftIndex = radarModelIndex.get(left)
  const rightIndex = radarModelIndex.get(right)
  if (leftIndex !== undefined && rightIndex !== undefined) return leftIndex - rightIndex
  if (leftIndex !== undefined) return -1
  if (rightIndex !== undefined) return 1
  return left.localeCompare(right)
}

/** Orders tiers deepest-first, the wall and the series picker both. */
export function compareRadarTiers(
  left: Pick<AIRadarPoint, 'effort'>,
  right: Pick<AIRadarPoint, 'effort'>,
): number {
  return radarEffortDisplayRank(right.effort) - radarEffortDisplayRank(left.effort)
}

/** The history window is a backend contract; keep one declaration. */
export type RadarHistoryWindow = AIRadarHistoryWindow

export function radarSeriesKey(series: Pick<AIRadarHistorySeries, 'model' | 'effort'>): string {
  return `${series.model}\u0000${series.effort}`
}

export function radarSeriesLabel(series: Pick<AIRadarHistorySeries, 'model' | 'effort'>): string {
  return `${radarModelMeta(series.model).label} · ${series.effort}`
}

/** ECharts' default HTML tooltip writes formatter output through innerHTML. */
export function escapeRadarTooltipText(value: string): string {
  return value.replace(/[&<>"']/g, (character) => {
    switch (character) {
      case '&':
        return '&amp;'
      case '<':
        return '&lt;'
      case '>':
        return '&gt;'
      case '"':
        return '&quot;'
      default:
        return '&#39;'
    }
  })
}

/**
 * Default selection is each model's strongest observed tier, which is the
 * comparison the history chart is normally opened for.
 */
export function defaultRadarSeriesSelection(series: AIRadarHistorySeries[]): string[] {
  const strongest = new Map<string, AIRadarHistorySeries>()
  for (const item of series) {
    if (!isRadarModelVisible(item.model)) continue
    if (item.points.length === 0) continue
    const current = strongest.get(item.model)
    if (!current || radarEffortRank(item.effort) > radarEffortRank(current.effort)) {
      strongest.set(item.model, item)
    }
  }
  return series
    .filter((item) => strongest.get(item.model) === item)
    .map((item) => radarSeriesKey(item))
}

function formatNumber(value: number | null, fractionDigits: number): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return radarMissingLabel
  }
  return value.toFixed(fractionDigits)
}

export function formatRadarIQ(value: number | null): string {
  return formatNumber(value, 2)
}

export function formatRadarIndex(value: number | null): string {
  return formatNumber(value, 2)
}

export function formatRadarUsd(value: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return radarMissingLabel
  }
  const fractionDigits = Math.abs(value) < 1 ? 4 : 2
  return `$${value.toFixed(fractionDigits)}`
}

export function formatRadarMinutes(value: number | null, suffix = ' min'): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return radarMissingLabel
  }
  return `${value.toFixed(2)}${suffix}`
}

/** Agent steps are a trajectory length, so one decimal is all that reads. */
export function formatRadarSteps(value: number | null): string {
  return formatNumber(value, 1)
}

export function formatRadarPercent(value: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return radarMissingLabel
  }
  return `${(value * 100).toFixed(1)}%`
}

export function formatRadarCount(value: number | null): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return radarMissingLabel
  }
  return new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 }).format(value)
}

export function formatRadarPassed(passed: number | null, total: number | null): string {
  if (typeof passed !== 'number' || typeof total !== 'number') {
    return radarMissingLabel
  }
  return `${formatRadarCount(passed)} / ${formatRadarCount(total)}`
}
