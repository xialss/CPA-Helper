<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NRadioButton, NRadioGroup } from 'naive-ui'
import type { TopLevelFormatterParams } from 'echarts/types/dist/shared'
import RadarChartLegend from '@/features/ai-radar/components/RadarChartLegend.vue'

import {
  compareRadarModels,
  compareRadarTiers,
  defaultRadarSeriesSelection,
  escapeRadarTooltipText,
  formatRadarMetric,
  isRadarModelVisible,
  radarEffortOrder,
  radarHistoryMetrics,
  radarModelMeta,
  radarSeriesKey,
  radarSeriesLabel,
  type RadarHistoryMetric,
  type RadarHistoryWindow,
} from '@/features/ai-radar/utils/radarMetrics'
import ChartPanel, { type ChartOption } from '@/shared/components/ChartPanel.vue'
import { useI18n } from '@/shared/i18n'
import type { AIRadarEffort, AIRadarHistorySeries } from '@/shared/types/api'
import { BEIJING_TIME_ZONE, parseDisplayDate } from '@/shared/utils/format'

const props = defineProps<{
  series: AIRadarHistorySeries[]
  window: RadarHistoryWindow
  loading: boolean
}>()

const emit = defineEmits<{
  (event: 'update:window', value: RadarHistoryWindow): void
}>()

const { t } = useI18n()
const panel = ref<InstanceType<typeof ChartPanel> | null>(null)
const visibleSeries = computed(() => props.series.filter((item) => isRadarModelVisible(item.model)))

const historyWindows: RadarHistoryWindow[] = ['24h', '7d', '30d', 'all']

const metric = ref<RadarHistoryMetric>('iq')

const windowOptions = computed<Array<{ value: RadarHistoryWindow; label: string }>>(() => [
  { value: '24h', label: t('24 小时', '24h') },
  { value: '7d', label: t('7 天', '7d') },
  { value: '30d', label: t('30 天', '30d') },
  { value: 'all', label: t('全部', 'All') },
])

const metricOptions = computed<Array<{ value: RadarHistoryMetric; label: string }>>(() => [
  { value: 'iq', label: 'IQ' },
  { value: 'price', label: t('费用', 'Cost') },
  { value: 'minutes', label: t('耗时', 'Duration') },
  { value: 'agent_steps', label: t('Agent steps', 'Agent steps') },
])

const metricLabel = computed(
  () => metricOptions.value.find((item) => item.value === metric.value)?.label ?? '',
)

/**
 * A model × reasoning-tier matrix instead of a dropdown. Four models against six
 * tiers is a table, not a list: every series is visible at once, the row header
 * says which model a cell belongs to, and the columns use the width a dropdown
 * wastes on two-letter labels.
 */
interface SeriesMatrixCell {
  effort: AIRadarEffort
  /** Null where this model has no series for the tier. */
  key: string | null
}

interface SeriesMatrixRow {
  model: string
  label: string
  color: string
  /** One entry per column, in `effortColumns` order. */
  cells: SeriesMatrixCell[]
}

/** Deepest tier first, matching the card wall. */
const effortColumns = [...radarEffortOrder].reverse()

const seriesMatrix = computed<SeriesMatrixRow[]>(() => {
  const buckets = new Map<string, AIRadarHistorySeries[]>()
  for (const item of visibleSeries.value) {
    const bucket = buckets.get(item.model)
    if (bucket) {
      bucket.push(item)
    } else {
      buckets.set(item.model, [item])
    }
  }
  return [...buckets.entries()]
    .sort(([left], [right]) => compareRadarModels(left, right))
    .map(([model, items]) => {
      const byEffort = new Map(items.map((item) => [item.effort, item]))
      return {
        model,
        label: radarModelMeta(model).label,
        color: radarModelMeta(model).color,
        cells: effortColumns.map((effort) => {
          const item = byEffort.get(effort)
          return { effort, key: item ? radarSeriesKey(item) : null }
        }),
      }
    })
})

function toggleSeries(key: string) {
  const next = new Set(selectedKeys.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  selectedKeys.value = [...next]
}

const selectedKeys = ref<string[]>([])

// Keep the selection valid across window changes: drop keys that are gone and
// fall back to each model's strongest observed tier when nothing survives.
watch(
  visibleSeries,
  (series) => {
    const available = new Set(series.map((item) => radarSeriesKey(item)))
    const kept = selectedKeys.value.filter((key) => available.has(key))
    selectedKeys.value = kept.length > 0 ? kept : defaultRadarSeriesSelection(series)
  },
  { immediate: true },
)

const selectedSeries = computed(() =>
  visibleSeries.value
    .filter((item) => selectedKeys.value.includes(radarSeriesKey(item)))
    .sort((left, right) => compareRadarModels(left.model, right.model) || compareRadarTiers(left, right)),
)

interface RadarHistoryPlot {
  name: string
  color: string
  data: Array<[number, number]>
}

/**
 * The plotted rows are resolved once per metric change, so the empty state and
 * the series array are derived from exactly the same values. A backfilled frame
 * whose selected metric is null produces no row rather than a zero.
 */
const plot = computed<RadarHistoryPlot[]>(() => {
  const definition = radarHistoryMetrics[metric.value]
  return selectedSeries.value.map((item) => ({
    name: radarSeriesLabel(item),
    color: radarModelMeta(item.model).color,
    data: item.points.flatMap((point) => {
      const timestamp = radarTimestamp(point.observed_at)
      const value = definition.value(point)
      if (timestamp === null || value === null || !Number.isFinite(value)) return []
      return [[timestamp, value] as [number, number]]
    }),
  }))
})

const chartEmpty = computed(() => plot.value.every((item) => item.data.length === 0))

const option = computed<ChartOption>(() => ({
  legend: { show: false },
  // containLabel handles ticks only; reserve space above them for the axis title.
  // Reserve horizontal room for the rotated y-axis title as well as tick
  // labels; containLabel only accounts for the latter.
  grid: { left: 40, right: 22, top: 48, bottom: 12, containLabel: true },
  tooltip: {
    trigger: 'axis',
    // A multi-series readout is tall, and `.panel` clips its overflow while
    // ECharts draws the tooltip inside the chart container. Without `confine`
    // the box is cut off, which reads as "hover shows nothing".
    confine: true,
    axisPointer: { type: 'cross' },
    formatter: tooltipFormatter,
  },
  xAxis: {
    type: 'time',
    axisLabel: { fontSize: 11, formatter: (value: number) => formatRadarTime(value, false) },
  },
  yAxis: {
    type: 'value',
    name: metricLabel.value,
    nameGap: 16,
    nameTextStyle: { fontSize: 11, lineHeight: 16 },
    axisLabel: { fontSize: 11 },
    splitLine: { show: true, lineStyle: { opacity: 0.3 } },
  },
  series: plot.value.map((item) => ({
    name: item.name,
    type: 'line',
    showSymbol: item.data.length === 1,
    symbolSize: 6,
    lineStyle: { width: 2, color: item.color },
    itemStyle: { color: item.color },
    emphasis: { focus: 'series' },
    data: item.data,
  })),
}))

function handleWindowChange(value: string | number | boolean) {
  const match = historyWindows.find((item) => item === value)
  if (match) emit('update:window', match)
}

/** Backend times carry an explicit Asia/Shanghai offset; keep that zone. */
function radarTimestamp(value: string): number | null {
  return parseDisplayDate(value)?.getTime() ?? null
}

function formatRadarTime(value: number, withYear: boolean): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const formatOptions: Intl.DateTimeFormatOptions = {
    timeZone: BEIJING_TIME_ZONE,
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }
  if (withYear) formatOptions.year = 'numeric'
  return new Intl.DateTimeFormat('en-CA', formatOptions).format(date)
}

function tooltipFormatter(params: TopLevelFormatterParams): string {
  const items = Array.isArray(params) ? params : [params]
  const blocks: string[] = []
  for (const item of items) {
    const raw = item.value
    if (!Array.isArray(raw) || raw.length < 2) continue
    const timestamp = Number(raw[0])
    const value = Number(raw[1])
    const label = typeof item.seriesName === 'string' ? item.seriesName : ''
    blocks.push(
      [
        `<strong>${escapeRadarTooltipText(label)}</strong>`,
        escapeRadarTooltipText(formatRadarTime(timestamp, true)),
        escapeRadarTooltipText(
          `${metricLabel.value}: ${formatRadarMetric(metric.value, value, t(' 分钟', ' min'))}`,
        ),
      ].join('<br/>'),
    )
  }
  return blocks.join('<br/><br/>')
}
</script>

<template>
  <ChartPanel
    ref="panel"
    :title="t('IQ 历史数据', 'IQ history')"
    :option="option"
    :empty="chartEmpty"
    :loading="loading"
    :height="380"
  >
    <template #controls>
      <div class="radar-history-row">
        <NRadioGroup :value="props.window" size="small" @update:value="handleWindowChange">
          <NRadioButton v-for="item in windowOptions" :key="item.value" :value="item.value">
            {{ item.label }}
          </NRadioButton>
        </NRadioGroup>
        <NRadioGroup v-model:value="metric" size="small">
          <NRadioButton v-for="item in metricOptions" :key="item.value" :value="item.value">
            {{ item.label }}
          </NRadioButton>
        </NRadioGroup>
      </div>
      <table class="radar-series-matrix">
        <caption>
          {{
            t(
              '点击格子切换是否绘制；填色格子与图中折线同色',
              'Click a cell to toggle its line; a filled cell uses that model\u2019s line colour',
            )
          }}
        </caption>
        <thead>
          <tr>
            <th scope="col">{{ t('模型', 'Model') }}</th>
            <th v-for="effort in effortColumns" :key="effort" scope="col">
              {{ effort.toUpperCase() }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in seriesMatrix" :key="row.model">
            <th scope="row">
              <span class="radar-series-dot" :style="{ background: row.color }" aria-hidden="true" />
              {{ row.label }}
            </th>
            <td v-for="cell in row.cells" :key="cell.effort">
              <button
                v-if="cell.key"
                type="button"
                class="radar-series-cell"
                :class="{ 'is-selected': selectedKeys.includes(cell.key) }"
                :style="selectedKeys.includes(cell.key) ? { color: row.color } : undefined"
                :aria-pressed="selectedKeys.includes(cell.key)"
                :aria-label="`${row.label} · ${cell.effort.toUpperCase()}`"
                :title="`${row.label} · ${cell.effort.toUpperCase()}`"
                @click="toggleSeries(cell.key)"
              />
              <span v-else class="radar-series-cell is-absent" aria-hidden="true" />
            </td>
          </tr>
        </tbody>
      </table>
      <RadarChartLegend :items="plot" @highlight="panel?.highlightSeries($event)" />
    </template>
  </ChartPanel>
</template>

<style scoped>
.radar-history-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.radar-series-matrix {
  width: 100%;
  margin-top: 12px;
  border-collapse: collapse;
  table-layout: fixed;
}

.radar-series-matrix caption {
  padding-bottom: 8px;
  color: var(--cpa-text-muted);
  font-size: 11px;
  text-align: left;
}

.radar-series-matrix th {
  color: var(--cpa-text-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
}

.radar-series-matrix thead th {
  padding: 0 0 6px;
  text-align: center;
}

.radar-series-matrix thead th:first-child {
  width: 148px;
  text-align: left;
}

.radar-series-matrix tbody th {
  display: flex;
  align-items: center;
  gap: 7px;
  padding-right: 10px;
  color: var(--cpa-text);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0;
  text-align: left;
}

.radar-series-matrix td {
  padding: 2px 3px;
}

.radar-series-dot {
  width: 9px;
  height: 9px;
  flex: none;
  border-radius: 50%;
}

.radar-series-cell {
  display: flex;
  width: 100%;
  height: 20px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: transparent;
}

/* A selected cell carries a dot in the model's line colour (inherited through
   `color`), matching the row dot and the chart. Filling the whole cell turned
   the table into a slab of colour that fought the rest of the page. */
.radar-series-cell.is-selected::after {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentcolor;
  content: '';
}

button.radar-series-cell {
  cursor: pointer;
}

button.radar-series-cell:hover {
  border-color: var(--cpa-primary);
}

button.radar-series-cell:focus-visible {
  outline: 2px solid var(--cpa-primary);
  outline-offset: 1px;
}

button.radar-series-cell.is-selected {
  border-color: currentcolor;
}

.radar-series-cell.is-absent {
  border-color: transparent;
}

@media (max-width: 720px) {
  .radar-series-matrix thead th:first-child {
    width: 104px;
  }

  .radar-series-matrix tbody th {
    font-size: 11px;
  }
}
</style>
