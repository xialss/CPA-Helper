<script setup lang="ts">
import { computed, ref } from 'vue'
import { NRadioButton, NRadioGroup } from 'naive-ui'
import type { TopLevelFormatterParams } from 'echarts/types/dist/shared'
import RadarChartLegend from '@/features/ai-radar/components/RadarChartLegend.vue'

import {
  compareRadarModels,
  escapeRadarTooltipText,
  formatRadarIQ,
  formatRadarIndex,
  formatRadarMetric,
  isRadarModelVisible,
  normalizeRadarValues,
  radarChartMetrics,
  radarLogExtent,
  radarEffortRank,
  radarModelMeta,
  type RadarChartMetric,
} from '@/features/ai-radar/utils/radarMetrics'
import ChartPanel, { type ChartOption } from '@/shared/components/ChartPanel.vue'
import { useI18n } from '@/shared/i18n'
import type { AIRadarPoint } from '@/shared/types/api'

const props = defineProps<{
  points: AIRadarPoint[]
  loading: boolean
}>()

const { t } = useI18n()
const panel = ref<InstanceType<typeof ChartPanel> | null>(null)

/**
 * `combined` matches the source site's default: the power-law blend of price
 * and duration that answers "what does one point of IQ cost".
 */
const metric = ref<RadarChartMetric>('combined')

const metricOptions = computed<Array<{ value: RadarChartMetric; label: string }>>(() => [
  { value: 'combined', label: t('综合成本', 'Combined cost') },
  { value: 'minutes', label: t('耗时', 'Duration') },
  { value: 'price', label: t('费用', 'Cost') },
])

const metricLabel = computed(
  () => metricOptions.value.find((item) => item.value === metric.value)?.label ?? '',
)

interface RadarPlotRow {
  effort: AIRadarPoint['effort']
  point: AIRadarPoint
  /** Position on the x axis: normalized for the combined index, raw otherwise. */
  axis: number
  iq: number
}

interface RadarPlotSeries {
  key: string
  label: string
  color: string
  rows: RadarPlotRow[]
}

/**
 * Only the combined index is normalized. It spans ~5.5 orders of magnitude, and
 * the source site's own rule is "highest plotted value reads 100". Duration and
 * cost span about one order, so they stay at their real minutes and dollars —
 * an axis a reader can compare against the cards instead of an opaque 0-100.
 */
const normalizes = computed(() => metric.value === 'combined')

/** Identity axis: keeps the value, still rejecting anything a log axis cannot take. */
function rawAxisValue(value: number | null): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null
}

/**
 * One line per model, walked in reasoning-tier order: the trajectory a model
 * traces across the cost/IQ plane as thinking effort rises.
 */
const plotSeries = computed<RadarPlotSeries[]>(() => {
  const definition = radarChartMetrics[metric.value]
  const candidates: Array<{
    model: string
    effort: AIRadarPoint['effort']
    raw: number
    iq: number
    point: AIRadarPoint
  }> = []
  for (const point of props.points) {
    if (!isRadarModelVisible(point.model)) continue
    const raw = definition.value(point)
    const iq = point.iq
    // A non-positive or missing axis value cannot be placed on a log axis.
    if (typeof raw !== 'number' || !Number.isFinite(raw) || raw <= 0) continue
    if (typeof iq !== 'number' || !Number.isFinite(iq)) continue
    candidates.push({ model: point.model, effort: point.effort, raw, iq, point })
  }

  // Normalization applies to the plotted set only, exactly as the source site
  // does while drawing; it never touches stored values.
  const toAxis = normalizes.value
    ? normalizeRadarValues(candidates.map((item) => item.raw))
    : rawAxisValue

  const buckets = new Map<string, RadarPlotSeries>()
  for (const item of candidates) {
    const axis = toAxis(item.raw)
    if (axis === null) continue
    let bucket = buckets.get(item.model)
    if (!bucket) {
      const meta = radarModelMeta(item.model)
      bucket = { key: item.model, label: meta.label, color: meta.color, rows: [] }
      buckets.set(item.model, bucket)
    }
    bucket.rows.push({ effort: item.effort, point: item.point, axis, iq: item.iq })
  }

  return [...buckets.values()]
    .map((bucket) => ({
      ...bucket,
      rows: [...bucket.rows].sort(
        (left, right) => radarEffortRank(left.effort) - radarEffortRank(right.effort),
      ),
    }))
    .sort((left, right) => compareRadarModels(left.key, right.key))
})

const yMax = computed(() => {
  const values = plotSeries.value.flatMap((series) => series.rows.map((row) => row.iq))
  if (values.length === 0) return 150
  const max = Math.max(...values)
  const step = max <= 50 ? 10 : max <= 120 ? 20 : 50
  return Math.min(150, Math.ceil(max / step) * step)
})

const xAxisName = computed(() => {
  if (!normalizes.value) {
    const unit = metric.value === 'minutes' ? t('分钟', 'min') : 'USD'
    return `${metricLabel.value} (${unit})`
  }
  return `${metricLabel.value} (${t('图中最高值归一为 100', 'highest plotted value normalized to 100')})`
})

/**
 * Explicit, legend-independent bounds for the log axis. ECharts does not draw a
 * blank axis, and a scale goes blank once no visible series contributes data, so
 * hiding the last series through the legend used to strip the x axis entirely.
 * Deriving the domain from every row also keeps it from rescaling on each toggle.
 *
 * A fallback is always supplied. Before the first payload arrives the chart has
 * no series at all, and handing ECharts a `log` axis with neither data nor
 * bounds leaves its scale on a degenerate path — the same one that later makes
 * the axis blank and stops the series rendering.
 */
const xExtent = computed(
  () =>
    radarLogExtent(plotSeries.value.flatMap((series) => series.rows.map((row) => row.axis))) ?? {
      min: 0.0001,
      max: 100,
    },
)

const chartEmpty = computed(() => plotSeries.value.length === 0)
const legendItems = computed(() => plotSeries.value.map((series) => ({ name: series.label, color: series.color })))

const option = computed<ChartOption>(() => ({
  legend: { show: false },
  // `containLabel` reserves room for the tick labels but not for the axis name,
  // so the top and bottom insets also cover the axis names.
  grid: { left: 40, right: 22, top: 48, bottom: 24, containLabel: true },
  tooltip: {
    trigger: 'item',
    triggerOn: 'mousemove|click',
    // The panel clips its overflow, and ECharts draws the tooltip inside the
    // chart container, so it must be confined to stay visible.
    confine: true,
    formatter: tooltipFormatter,
  },
  xAxis: {
    type: 'log',
    // Explicit, legend-independent bounds; see `xExtent`.
    min: xExtent.value.min,
    max: xExtent.value.max,
    name: xAxisName.value,
    nameLocation: 'middle',
    nameGap: 30,
    nameTextStyle: { fontSize: 11 },
    axisLabel: { fontSize: 11, formatter: (value: number) => formatAxisValue(value) },
    splitLine: { show: true, lineStyle: { opacity: 0.3 } },
  },
  yAxis: {
    type: 'value',
    min: 0,
    max: yMax.value,
    name: 'IQ',
    nameGap: 16,
    nameTextStyle: { fontSize: 11, lineHeight: 16 },
    axisLabel: { fontSize: 11 },
    splitLine: { show: true, lineStyle: { opacity: 0.3 } },
  },
  series: plotSeries.value.map((series) => ({
    name: series.label,
    type: 'line',
    showSymbol: true,
    showAllSymbol: true,
    symbolSize: 8,
    lineStyle: { width: 2, color: series.color },
    itemStyle: { color: series.color },
    emphasis: { focus: 'series' },
    data: series.rows.map((row) => [row.axis, row.iq]),
  })),
}))

/** Log-axis ticks span ~5 orders of magnitude, so keep 2-3 significant digits. */
function formatAxisValue(value: number): string {
  if (!Number.isFinite(value)) return ''
  return String(Number(value.toPrecision(value >= 1 ? 3 : 2)))
}

function tooltipFormatter(params: TopLevelFormatterParams): string {
  const items = Array.isArray(params) ? params : [params]
  const blocks: string[] = []
  for (const item of items) {
    const series = item.seriesIndex === undefined ? undefined : plotSeries.value[item.seriesIndex]
    const row = series?.rows[item.dataIndex ?? -1]
    if (!series || !row) continue
    const lines = [
      `<strong>${escapeRadarTooltipText(`${series.label} · ${row.effort}`)}</strong>`,
      ...metricOptions.value.map(({ value, label }) =>
        escapeRadarTooltipText(
          `${label}: ${formatRadarMetric(value, radarChartMetrics[value].value(row.point), t(' 分钟', ' min'))}`,
        ),
      ),
    ]
    // Only the combined index has a second, normalized reading to report.
    if (normalizes.value) {
      lines.push(
        escapeRadarTooltipText(`${t('归一化', 'Normalized')}: ${formatRadarIndex(row.axis)}`),
      )
    }
    lines.push(escapeRadarTooltipText(`IQ: ${formatRadarIQ(row.iq)}`))
    blocks.push(lines.join('<br/>'))
  }
  return blocks.join('<br/><br/>')
}
</script>

<template>
  <ChartPanel
    ref="panel"
    class="radar-pk-panel"
    :title="t('效能 PK（综合成本 × IQ）', 'Efficiency PK (cost vs IQ)')"
    :option="option"
    :empty="chartEmpty"
    :loading="loading"
    :height="380"
  >
    <template #controls>
      <RadarChartLegend :items="legendItems" @highlight="panel?.highlightSeries($event)" />
    </template>
    <template #header-extra>
      <div class="radar-pk-controls">
        <span class="radar-pk-hint">{{ t('越靠左上越高效', 'Upper-left is more efficient') }}</span>
        <NRadioGroup v-model:value="metric" size="small">
          <NRadioButton
            v-for="item in metricOptions"
            :key="item.value"
            :value="item.value"
          >
            {{ item.label }}
          </NRadioButton>
        </NRadioGroup>
      </div>
    </template>
  </ChartPanel>
</template>

<style scoped>
.radar-pk-controls {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}

.radar-pk-hint {
  color: var(--cpa-text-muted);
  font-size: 12px;
  white-space: nowrap;
}

@media (max-width: 860px) {
  .radar-pk-hint {
    display: none;
  }
}
</style>
