<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NEmpty, NSpin } from 'naive-ui'
import * as echarts from 'echarts/core'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import {
  GridComponent,
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

echarts.use([BarChart, LineChart, PieChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

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
}>()

const chartEl = ref<HTMLDivElement | null>(null)
const chart = ref<ECharts | null>(null)
const { isDark } = useThemePreference()

let chartThemeFrame: number | undefined

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

onMounted(() => {
  if (!chartEl.value) {
    return
  }
  chart.value = echarts.init(chartEl.value, isDark.value ? 'dark' : undefined)
  chart.value.setOption(buildCurrentOption())
  window.addEventListener('resize', resize)
})

watch(
  () => props.option,
  () => {
    chart.value?.setOption(buildCurrentOption(), true)
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
    chart.value.setOption(buildCurrentOption())
    chartThemeFrame = undefined
  })
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
  if (chartThemeFrame !== undefined) {
    window.cancelAnimationFrame(chartThemeFrame)
  }
  chart.value?.dispose()
})
</script>

<template>
  <section class="panel chart-panel">
    <div class="chart-heading">
      <h2>{{ title }}</h2>
      <span class="chart-more" aria-hidden="true">...</span>
    </div>
    <NSpin :show="loading ?? false">
      <div class="chart-body">
        <div ref="chartEl" class="chart-surface" :class="{ 'is-empty': empty }" />
        <div v-if="empty" class="chart-empty">
          <NEmpty description="暂无数据" />
        </div>
      </div>
    </NSpin>
  </section>
</template>

<style scoped>
.chart-panel {
  min-height: 270px;
}

.chart-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 18px 18px 12px;
  border-bottom: 1px solid var(--cpa-border);
}

h2 {
  margin: 0;
  color: var(--cpa-text-strong);
  font-size: 15px;
  font-weight: 750;
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
  height: 222px;
}

.chart-body {
  position: relative;
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
</style>
