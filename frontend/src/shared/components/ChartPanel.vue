<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { NEmpty, NSpin } from 'naive-ui'
import * as echarts from 'echarts/core'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import {
  AxisPointerComponent,
  DatasetComponent,
  GridComponent,
  GridSimpleComponent,
  LegendComponent,
  TooltipComponent,
  type GridComponentOption,
  type LegendComponentOption,
  type TooltipComponentOption,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { BarSeriesOption, LineSeriesOption, PieSeriesOption } from 'echarts/charts'
import type { ComposeOption, ECharts } from 'echarts/core'

import { useThemePreference } from '@/shared/composables/useThemePreference'
import { useI18n } from '@/shared/i18n'

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  AxisPointerComponent,
  DatasetComponent,
  GridComponent,
  GridSimpleComponent,
  LegendComponent,
  TooltipComponent,
  CanvasRenderer,
])

export type ChartOption = ComposeOption<
  | BarSeriesOption
  | LineSeriesOption
  | PieSeriesOption
  | GridComponentOption
  | LegendComponentOption
  | TooltipComponentOption
>

const props = defineProps<{
  title: string
  option: ChartOption
  empty: boolean
  loading?: boolean
  compactFooter?: boolean
  /**
   * Plot height in px. Omit to keep the shared default; taller feature charts
   * (a log axis plus a legend and two axis names) pass an explicit value
   * instead of copying the panel to escape the fixed height.
   */
  height?: number
}>()

const chartEl = ref<HTMLDivElement | null>(null)
// ECharts keeps identity-sensitive internal models; never deep-proxy its instance.
const chart = shallowRef<ECharts | null>(null)
const { isDark } = useThemePreference()
const { t } = useI18n()

// An inline height wins over the class rules below, so the shared defaults stay
// intact for every existing caller.
const bodyStyle = computed(() =>
  props.height === undefined ? undefined : { height: `${props.height}px` },
)

let chartThemeFrame: number | undefined
let applyFrame: number | undefined

/**
 * Apply a new option to the live chart.
 *
 * `notMerge` stays: every caller passes a complete option, and charts whose
 * series list shrinks (the radar history chart) rely on the old series being
 * dropped. `scheduleApply` coalesces reactive updates into one apply per frame.
 */
function applyOption() {
  chart.value?.setOption(buildCurrentOption(), true)
}

/** Coalesce a burst of reactive updates into a single apply per frame. */
function scheduleApply() {
  if (applyFrame !== undefined) {
    window.cancelAnimationFrame(applyFrame)
  }
  applyFrame = window.requestAnimationFrame(() => {
    applyFrame = undefined
    applyOption()
  })
}

function getChartTextColor(): string {
  return (
    getComputedStyle(document.documentElement).getPropertyValue('--cpa-text').trim() ||
    (isDark.value ? '#dfe8ea' : '#172026')
  )
}

function getChartMutedColor(): string {
  return (
    getComputedStyle(document.documentElement).getPropertyValue('--cpa-text-muted').trim() ||
    (isDark.value ? '#93a8ae' : '#667981')
  )
}

function buildCurrentOption(): ChartOption {
  return {
    backgroundColor: 'transparent',
    textStyle: {
      fontFamily: 'Aptos, Segoe UI, Microsoft YaHei UI, sans-serif',
      color: getChartTextColor(),
    },
    tooltip: {
      backgroundColor: isDark.value ? 'rgba(22, 34, 39, 0.96)' : 'rgba(255, 255, 255, 0.96)',
      borderColor: isDark.value ? 'rgba(160, 190, 196, 0.22)' : 'rgba(116, 146, 151, 0.22)',
      textStyle: {
        color: getChartTextColor(),
      },
      extraCssText: 'box-shadow: 0 16px 32px rgba(26, 50, 57, 0.12); border-radius: 8px;',
    },
    legend: {
      textStyle: {
        color: getChartMutedColor(),
      },
    },
    ...props.option,
  }
}

function resize() {
  chart.value?.resize()
}

function highlightSeries(name: string | null) {
  chart.value?.dispatchAction({ type: 'downplay' })
  if (name !== null) chart.value?.dispatchAction({ type: 'highlight', seriesName: name })
}

defineExpose({ highlightSeries })

onMounted(() => {
  if (!chartEl.value) {
    return
  }
  chart.value = echarts.init(chartEl.value, isDark.value ? 'dark' : undefined)
  applyOption()
  window.addEventListener('resize', resize)
})

watch(
  () => props.option,
  () => {
    scheduleApply()
  },
  { deep: true },
)

watch(isDark, () => {
  if (chartThemeFrame !== undefined) {
    window.cancelAnimationFrame(chartThemeFrame)
  }
  chartThemeFrame = window.requestAnimationFrame(() => {
    if (!chartEl.value) {
      chartThemeFrame = undefined
      return
    }
    chart.value?.dispose()
    chart.value = echarts.init(chartEl.value, isDark.value ? 'dark' : undefined)
    applyOption()
    chartThemeFrame = undefined
  })
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
  if (chartThemeFrame !== undefined) {
    window.cancelAnimationFrame(chartThemeFrame)
  }
  if (applyFrame !== undefined) {
    window.cancelAnimationFrame(applyFrame)
  }
  chart.value?.dispose()
})
</script>

<template>
  <section
    class="panel chart-panel"
    :class="{ 'has-chart-footer': $slots.default, 'has-compact-footer': props.compactFooter }"
  >
    <div class="chart-heading">
      <div class="chart-heading-title">
        <slot name="title">
          <h2>{{ title }}</h2>
        </slot>
      </div>
      <div class="chart-heading-extra">
        <slot name="header-extra">
          <span class="chart-more" aria-hidden="true">...</span>
        </slot>
      </div>
    </div>
    <div v-if="$slots.controls" class="chart-controls">
      <slot name="controls" />
    </div>
    <NSpin :show="loading ?? false">
      <div class="chart-body" :style="bodyStyle">
        <div ref="chartEl" class="chart-surface" :class="{ 'is-empty': empty }" />
        <div v-if="empty" class="chart-empty">
          <NEmpty :description="t('暂无数据', 'No data')" />
        </div>
      </div>
      <div v-if="$slots.default" class="chart-footer">
        <slot />
      </div>
    </NSpin>
  </section>
</template>

<style scoped>
.chart-panel {
  min-height: 270px;
}

.chart-panel.has-chart-footer {
  min-height: 318px;
}

.chart-panel.has-chart-footer.has-compact-footer {
  min-height: 278px;
}

.chart-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 18px 18px 12px;
  border-bottom: 1px solid var(--cpa-border);
}

.chart-heading-title {
  display: flex;
  flex: 1 1 auto;
  min-width: 0;
  align-items: center;
}

h2 {
  min-width: 0;
  margin: 0;
  color: var(--cpa-text-strong);
  font-size: 15px;
  font-weight: 750;
}

/* Filters that change what the chart draws belong inside the same panel, above
   the plot, so they cannot read as a separate section. */
.chart-controls {
  padding: 12px 18px;
  border-bottom: 1px solid var(--cpa-border);
}

.chart-heading-extra {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: flex-end;
}

.chart-more {
  color: var(--cpa-text-muted);
  font-size: 18px;
  font-weight: 750;
  line-height: 1;
  letter-spacing: 0;
}

.chart-body,
.chart-surface,
.chart-empty {
  width: 100%;
  height: 100%;
}

.chart-panel.has-chart-footer .chart-body {
  height: 160px;
}

.chart-body {
  position: relative;
  height: 222px;
  background: transparent;
}

.chart-surface.is-empty {
  visibility: hidden;
}

.chart-empty {
  display: grid;
  position: absolute;
  inset: 0;
  place-items: center;
}

.chart-footer {
  padding: 0 18px 16px;
}
</style>
