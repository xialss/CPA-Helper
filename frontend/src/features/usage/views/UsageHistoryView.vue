<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NButton,
  NDataTable,
  NDatePicker,
  NSelect,
  NSpin,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { CircleDollarSign, ClipboardList, Download, Layers3, Upload, Users } from 'lucide-vue-next'

import { getUsageOverview } from '@/features/usage/api/usageApi'
import ChartPanel, { type ChartOption } from '@/features/usage/components/ChartPanel.vue'
import type {
  DistributionItem,
  RankingItem,
  TrendPoint,
  UsageDistributionsResponse,
  UsageFilters,
  UsageOverviewResponse,
  UsageOptionsResponse,
  UsageRankingsResponse,
  UsageSummary,
} from '@/shared/types/api'
import {
  BEIJING_TIME_ZONE,
  formatCompact,
  formatDateTime,
  formatInteger,
  formatLocalDateTimeParam,
  formatUsd,
} from '@/shared/utils/format'

type FailedFilter = 'all' | 'success' | 'failed'
type QuickRangeKey = 'today' | 'last24h' | 'last3d' | 'last7d' | 'last30d'
type UsageScope = 'admin' | 'account'

interface RefreshOptions {
  silent?: boolean
}

interface Props {
  scope: UsageScope
}

const AUTO_REFRESH_INTERVAL_MS = 5000
const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS
const quickRangeOptions: Array<{ key: QuickRangeKey; label: string }> = [
  { key: 'today', label: '今日' },
  { key: 'last24h', label: '近24小时' },
  { key: 'last3d', label: '近3日' },
  { key: 'last7d', label: '近7日' },
  { key: 'last30d', label: '近30日' },
]

interface MetricCardConfig {
  key: string
  label: string
  value: string
  icon: Component
  tone: string
}

interface DistributionLegendItem {
  key: string
  label: string
  recordsText: string
  percentText: string
  colorIndex: number
}

const DISTRIBUTION_CHART_COLORS = [
  { token: '--cpa-chart-1', fallback: '#009aa8' },
  { token: '--cpa-chart-2', fallback: '#1d8dff' },
  { token: '--cpa-chart-3', fallback: '#7e66f2' },
  { token: '--cpa-chart-4', fallback: '#f58a2f' },
  { token: '--cpa-chart-5', fallback: '#18a058' },
] as const

const route = useRoute()
const router = useRouter()
const message = useMessage()
const props = defineProps<Props>()
const isLoading = ref(false)
const isAutoRefreshing = ref(false)
const autoRefreshError = ref<string | null>(null)
const lastRefreshedAt = ref<Date | null>(null)
const summary = ref<UsageSummary | null>(null)
const trends = ref<TrendPoint[]>([])
const userRanking = ref<RankingItem[]>([])
const modelRanking = ref<RankingItem[]>([])
const distributions = ref<UsageDistributionsResponse>({ providers: [], models: [], endpoints: [] })
const options = ref<UsageOptionsResponse>({
  users: [],
  api_key_descriptions: [],
  providers: [],
  models: [],
  endpoints: [],
})

function normalizeUsageOptions(nextOptions: UsageOptionsResponse): UsageOptionsResponse {
  return {
    users: nextOptions.users ?? [],
    api_key_descriptions: nextOptions.api_key_descriptions ?? [],
    providers: nextOptions.providers ?? [],
    models: nextOptions.models ?? [],
    endpoints: nextOptions.endpoints ?? [],
  }
}

function normalizeUsageDistributions(
  nextDistributions: Partial<UsageDistributionsResponse> | undefined,
): UsageDistributionsResponse {
  return {
    providers: nextDistributions?.providers ?? [],
    models: nextDistributions?.models ?? [],
    endpoints: nextDistributions?.endpoints ?? [],
  }
}

function emptyRanking(groupBy: UsageRankingsResponse['group_by']): UsageRankingsResponse {
  return { group_by: groupBy, items: [] }
}

function descriptionRanking(overview: UsageOverviewResponse): UsageRankingsResponse {
  return (
    overview.api_key_description_ranking ??
    overview.api_key_ranking ??
    emptyRanking('api_key_description')
  )
}

function initialRange(): [number, number] | null {
  const startQuery = typeof route.query.start === 'string' ? route.query.start : ''
  const endQuery = typeof route.query.end === 'string' ? route.query.end : ''
  if (startQuery && endQuery) {
    return [new Date(startQuery).getTime(), new Date(endQuery).getTime()]
  }
  return null
}

function todayRange(): [number, number] {
  const now = new Date()
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const tomorrow = new Date(start)
  tomorrow.setDate(start.getDate() + 1)
  return [start.getTime(), tomorrow.getTime()]
}

function isTodayRange(range: [number, number] | null): boolean {
  if (!range) {
    return false
  }
  const [todayStart, tomorrowStart] = todayRange()
  return range[0] === todayStart && range[1] === tomorrowStart
}

function failedFromQuery(): FailedFilter {
  if (route.query.failed === 'true') {
    return 'failed'
  }
  if (route.query.failed === 'false') {
    return 'success'
  }
  return 'all'
}

function numberFromQuery(value: unknown): number | null {
  if (typeof value !== 'string' || !value) {
    return null
  }
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : null
}

const initialDateRange = initialRange()
const dateRange = ref<[number, number] | null>(initialDateRange)
const activeQuickRange = ref<QuickRangeKey | null>(
  initialDateRange === null || isTodayRange(initialDateRange) ? 'today' : null,
)
const filterForm = reactive({
  user_id: numberFromQuery(route.query.user_id),
  api_key_description:
    typeof route.query.api_key_description === 'string' ? route.query.api_key_description : null,
  provider: typeof route.query.provider === 'string' ? route.query.provider : null,
  model: typeof route.query.model === 'string' ? route.query.model : null,
  endpoint: typeof route.query.endpoint === 'string' ? route.query.endpoint : null,
  failed: failedFromQuery(),
})

const selectOptions = computed(() => ({
  users: options.value.users
    .filter((item) => item.user_id !== null)
    .map((item) => ({ label: item.label, value: item.user_id as number })),
  apiKeyDescriptions: options.value.api_key_descriptions.map((item) => ({
    label: item.label,
    value: item.key,
  })),
  providers: options.value.providers.map((item) => ({ label: item, value: item })),
  models: options.value.models.map((item) => ({ label: item, value: item })),
  endpoints: options.value.endpoints.map((item) => ({ label: item, value: item })),
}))

const isAccountScope = computed(() => props.scope === 'account')
const pageTitle = computed(() => (isAccountScope.value ? '我的用量' : '历史用量'))
const pageSubtitle = computed(() =>
  isAccountScope.value
    ? '仅聚合当前登录账号自己的本地用量记录'
    : '按本地 SQLite 历史记录实时聚合，费用按当前模型价格估算',
)
const rankingTitle = computed(() => (isAccountScope.value ? 'KEY 描述排行' : '用户排行'))

const refreshStatusText = computed(() => {
  const lastRefreshTime = lastRefreshedAt.value
  if (!lastRefreshTime) {
    return autoRefreshError.value ? '自动刷新异常 · 尚无成功同步' : '每 5 秒自动刷新 · 等待首次同步'
  }
  const lastRefreshText = new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(lastRefreshTime)
  if (autoRefreshError.value) {
    return `自动刷新异常 · 最近成功 ${lastRefreshText}`
  }
  return `每 5 秒自动刷新 · 最近 ${lastRefreshText}`
})

const metricRangeLabel = computed(() => {
  const activeRange = quickRangeOptions.find((option) => option.key === activeQuickRange.value)
  if (activeRange) {
    return activeRange.label
  }
  const range = dateRange.value
  if (!range) {
    return '默认范围'
  }
  if (isTodayRange(range)) {
    return '今日'
  }
  return `${formatMetricRangeTime(range[0])} - ${formatMetricRangeTime(range[1])}`
})

function formatMetricRangeTime(value: number): string {
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: BEIJING_TIME_ZONE,
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(value))
}

function buildFilters(): UsageFilters {
  const failed =
    filterForm.failed === 'all' ? undefined : filterForm.failed === 'failed' ? true : false
  return {
    scope: props.scope,
    start: dateRange.value ? formatLocalDateTimeParam(dateRange.value[0]) : undefined,
    end: dateRange.value ? formatLocalDateTimeParam(dateRange.value[1]) : undefined,
    user_id: isAccountScope.value ? undefined : (filterForm.user_id ?? undefined),
    api_key_description: filterForm.api_key_description ?? undefined,
    provider: filterForm.provider ?? undefined,
    model: filterForm.model ?? undefined,
    endpoint: filterForm.endpoint ?? undefined,
    failed,
  }
}

function filtersToQuery(filters: UsageFilters): Record<string, string> {
  const query: Record<string, string> = {}
  Object.entries(filters).forEach(([key, value]) => {
    if (key !== 'scope' && value !== undefined && value !== '') {
      query[key] = String(value)
    }
  })
  return query
}

function buildQuickRange(key: QuickRangeKey): [number, number] {
  switch (key) {
    case 'today':
      return todayRange()
    case 'last24h': {
      const end = Date.now()
      return [end - 24 * HOUR_MS, end]
    }
    case 'last3d': {
      const end = Date.now()
      return [end - 3 * DAY_MS, end]
    }
    case 'last7d': {
      const end = Date.now()
      return [end - 7 * DAY_MS, end]
    }
    case 'last30d': {
      const end = Date.now()
      return [end - 30 * DAY_MS, end]
    }
  }
}

function normalizeRangeValue(value: unknown): [number, number] | null {
  if (
    Array.isArray(value) &&
    value.length === 2 &&
    typeof value[0] === 'number' &&
    typeof value[1] === 'number'
  ) {
    return [value[0], value[1]]
  }
  return null
}

function normalizeSelectValue(value: unknown): string | null {
  if (value === null || value === undefined || value === '') {
    return null
  }
  return String(value)
}

function refreshAfterFilterChange() {
  void refresh()
}

function handleCustomRangeChange(value: unknown) {
  dateRange.value = normalizeRangeValue(value)
  activeQuickRange.value = null
  refreshAfterFilterChange()
}

function handleApiKeyChange(value: unknown) {
  filterForm.api_key_description = normalizeSelectValue(value)
  refreshAfterFilterChange()
}

function handleUserChange(value: unknown) {
  filterForm.user_id = typeof value === 'number' ? value : null
  refreshAfterFilterChange()
}

function handleProviderChange(value: unknown) {
  filterForm.provider = normalizeSelectValue(value)
  refreshAfterFilterChange()
}

function handleModelChange(value: unknown) {
  filterForm.model = normalizeSelectValue(value)
  refreshAfterFilterChange()
}

function handleEndpointChange(value: unknown) {
  filterForm.endpoint = normalizeSelectValue(value)
  refreshAfterFilterChange()
}

function handleFailedChange(value: unknown) {
  filterForm.failed = value === 'success' || value === 'failed' ? value : 'all'
  refreshAfterFilterChange()
}

async function applyQuickRange(key: QuickRangeKey) {
  activeQuickRange.value = key
  dateRange.value = buildQuickRange(key)
  await refresh()
}

let queuedRefresh: RefreshOptions | null = null

function queueRefresh(options: RefreshOptions) {
  if (options.silent) {
    return
  }
  queuedRefresh = { silent: false }
}

async function refresh({ silent = false }: RefreshOptions = {}) {
  if (isLoading.value || isAutoRefreshing.value) {
    queueRefresh({ silent })
    return
  }
  if (activeQuickRange.value) {
    dateRange.value = buildQuickRange(activeQuickRange.value)
  }
  if (silent) {
    isAutoRefreshing.value = true
  } else {
    isLoading.value = true
  }
  try {
    const filters = buildFilters()
    const usedServerDefaultRange = filters.start === undefined && filters.end === undefined
    const overview = await getUsageOverview(filters)
    summary.value = overview.summary
    if (usedServerDefaultRange) {
      dateRange.value = [
        new Date(overview.summary.start).getTime(),
        new Date(overview.summary.end).getTime(),
      ]
    }
    trends.value = overview.trends
    userRanking.value = isAccountScope.value
      ? descriptionRanking(overview).items
      : (overview.user_ranking ?? emptyRanking('user')).items
    modelRanking.value = (overview.model_ranking ?? emptyRanking('model')).items
    distributions.value = normalizeUsageDistributions(overview.distributions)
    options.value = normalizeUsageOptions(overview.options)
    void router.replace({
      query: filtersToQuery(
        usedServerDefaultRange
          ? { ...filters, start: overview.summary.start, end: overview.summary.end }
          : filters,
      ),
    })
    autoRefreshError.value = null
    lastRefreshedAt.value = new Date()
  } catch (error) {
    const errorMessage = error instanceof Error ? error.message : '加载历史用量失败'
    if (silent) {
      autoRefreshError.value = errorMessage
    } else {
      message.error(errorMessage)
    }
  } finally {
    if (silent) {
      isAutoRefreshing.value = false
    } else {
      isLoading.value = false
    }
    const nextRefresh = queuedRefresh
    queuedRefresh = null
    if (nextRefresh) {
      void refresh(nextRefresh)
    }
  }
}

function goRecords(extra: UsageFilters = {}) {
  const filters = { ...buildFilters(), ...extra }
  void router.push({
    name: isAccountScope.value ? 'account-records' : 'admin-records',
    query: filtersToQuery(filters),
  })
}

function cssVar(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback
}

function distributionChartColors(): string[] {
  return DISTRIBUTION_CHART_COLORS.map((color) => cssVar(color.token, color.fallback))
}

function distributionMarkerStyle(index: number): Record<string, string> {
  const color = DISTRIBUTION_CHART_COLORS[index % DISTRIBUTION_CHART_COLORS.length]
  if (!color) {
    return {
      '--distribution-color': '#009aa8',
    }
  }

  return {
    '--distribution-color': `var(${color.token}, ${color.fallback})`,
  }
}

function distributionLegendItems(items: DistributionItem[]): DistributionLegendItem[] {
  const totalRecords = items.reduce((sum, item) => sum + item.records, 0)

  return items.map((item, index) => ({
    key: item.key,
    label: item.label,
    recordsText: formatCompact(item.records),
    percentText: totalRecords > 0 ? `${Math.round((item.records / totalRecords) * 100)}%` : '0%',
    colorIndex: index,
  }))
}

const metricCards = computed<MetricCardConfig[]>(() => [
  {
    key: 'requests',
    label: '请求数',
    value: formatInteger(summary.value?.total_records ?? 0),
    icon: Users,
    tone: 'teal',
  },
  {
    key: 'failed',
    label: '失败数',
    value: formatInteger(summary.value?.failed_records ?? 0),
    icon: ClipboardList,
    tone: 'purple',
  },
  {
    key: 'total_tokens',
    label: '总 Token',
    value: formatCompact(summary.value?.total_tokens ?? 0),
    icon: Layers3,
    tone: 'blue',
  },
  {
    key: 'input_tokens',
    label: '输入 Token',
    value: formatCompact(summary.value?.input_tokens ?? 0),
    icon: Download,
    tone: 'teal',
  },
  {
    key: 'output_tokens',
    label: '输出 Token',
    value: formatCompact(summary.value?.output_tokens ?? 0),
    icon: Upload,
    tone: 'orange',
  },
  {
    key: 'cost',
    label: '估算费用',
    value: formatUsd(summary.value?.estimated_cost_usd ?? 0),
    icon: CircleDollarSign,
    tone: 'green',
  },
])

const trendOption = computed<ChartOption>(() => ({
  tooltip: { trigger: 'axis' },
  legend: { show: false },
  grid: { left: 40, right: 18, top: 20, bottom: 34 },
  xAxis: {
    type: 'category',
    data: trends.value.map((item) => item.bucket),
    axisLabel: {
      hideOverlap: true,
      color: cssVar('--cpa-text-muted', '#6a7d87'),
      formatter: (value: string) => formatDateTime(value).slice(0, 11),
    },
    axisLine: { lineStyle: { color: cssVar('--cpa-chart-grid', 'rgba(120,146,151,.22)') } },
    axisTick: { show: false },
  },
  yAxis: {
    type: 'value',
    axisLabel: { formatter: (value: number) => formatCompact(value) },
    splitLine: { lineStyle: { color: cssVar('--cpa-chart-grid', 'rgba(120,146,151,.22)') } },
  },
  series: [
    {
      name: 'Token',
      type: 'line',
      smooth: true,
      showSymbol: false,
      data: trends.value.map((item) => item.total_tokens),
      areaStyle: { opacity: 0.18, color: cssVar('--cpa-primary', '#009aa8') },
      lineStyle: { color: cssVar('--cpa-primary', '#009aa8'), width: 3 },
      itemStyle: { color: cssVar('--cpa-primary', '#009aa8') },
    },
  ],
}))

function pieOption(items: DistributionItem[], name: string): ChartOption {
  const totalRecords = items.reduce((sum, item) => sum + item.records, 0)
  const surfaceColor = cssVar('--cpa-surface', '#ffffff')
  const textColor = cssVar('--cpa-text-strong', '#172026')
  const mutedColor = cssVar('--cpa-text-muted', '#667981')

  return {
    tooltip: {
      trigger: 'item',
      formatter: `${name}：{b}<br/>请求：{c} ({d}%)`,
    },
    color: distributionChartColors(),
    legend: {
      show: false,
    },
    series: [
      {
        name,
        type: 'pie',
        radius: ['48%', '73%'],
        center: ['50%', '53%'],
        startAngle: 92,
        minAngle: 4,
        avoidLabelOverlap: true,
        label: { show: false },
        labelLine: { show: false },
        itemStyle: {
          borderColor: surfaceColor,
          borderWidth: 3,
          borderRadius: 5,
        },
        emphasis: {
          scaleSize: 3,
          itemStyle: {
            shadowBlur: 10,
            shadowColor: 'rgba(0, 154, 168, 0.18)',
          },
        },
        data: items.map((item, index) => ({
          name: item.label,
          value: item.records,
          label:
            index === 0
              ? {
                  show: true,
                  position: 'center',
                  formatter: `{total|${formatInteger(totalRecords)}}\n{caption|请求}`,
                  rich: {
                    total: {
                      color: textColor,
                      fontSize: 22,
                      fontWeight: 750,
                      lineHeight: 28,
                    },
                    caption: {
                      color: mutedColor,
                      fontSize: 12,
                      lineHeight: 18,
                    },
                  },
                }
              : { show: false },
        })),
      },
    ],
  }
}

const modelDistributionLegend = computed(() => distributionLegendItems(distributions.value.models))
const endpointDistributionLegend = computed(() => distributionLegendItems(distributions.value.endpoints))

const rankingColumns = computed<DataTableColumns<RankingItem>>(() => [
  {
    title: isAccountScope.value ? 'KEY 描述' : '用户昵称',
    key: 'label',
    width: isAccountScope.value ? 116 : 96,
    ellipsis: { tooltip: true },
  },
  {
    title: '请求',
    key: 'records',
    width: 58,
    render: (row) => formatInteger(row.records),
  },
  {
    title: '总 Token',
    key: 'total_tokens',
    width: 78,
    render: (row) => formatCompact(row.total_tokens),
  },
  {
    title: '费用',
    key: 'estimated_cost_usd',
    width: 86,
    render: (row) => formatUsd(row.estimated_cost_usd),
  },
  {
    title: '',
    key: 'actions',
    width: 52,
    render: (row) =>
      h(
        NButton,
        {
          size: 'small',
          quaternary: true,
          onClick: () =>
            goRecords(
              !isAccountScope.value && row.user_id
                ? { user_id: row.user_id }
                : row.api_key_description
                  ? { api_key_description: row.api_key_description }
                  : {},
            ),
        },
        { default: () => '明细' },
      ),
  },
])

const modelColumns: DataTableColumns<RankingItem> = [
  { title: '模型', key: 'label', minWidth: 180 },
  {
    title: '请求',
    key: 'records',
    width: 90,
    render: (row) => formatInteger(row.records),
  },
  {
    title: '总 Token',
    key: 'total_tokens',
    width: 110,
    render: (row) => formatCompact(row.total_tokens),
  },
  {
    title: '',
    key: 'actions',
    width: 86,
    render: (row) =>
      h(
        NButton,
        {
          size: 'small',
          quaternary: true,
          onClick: () => {
            const [provider, model] = row.key.split('::')
            const filters: UsageFilters = {}
            if (provider) {
              filters.provider = provider
            }
            if (model) {
              filters.model = model
            }
            goRecords(filters)
          },
        },
        { default: () => '明细' },
      ),
  },
]

let autoRefreshTimer: number | undefined

onMounted(() => {
  void refresh()
  autoRefreshTimer = window.setInterval(() => {
    void refresh({ silent: true })
  }, AUTO_REFRESH_INTERVAL_MS)
})

onBeforeUnmount(() => {
  if (autoRefreshTimer !== undefined) {
    window.clearInterval(autoRefreshTimer)
  }
})
</script>

<template>
  <section class="page" :aria-busy="isLoading">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ pageTitle }}</h1>
        <p class="page-subtitle">{{ pageSubtitle }}</p>
      </div>
      <div class="header-actions">
        <span class="refresh-status" :class="{ 'is-error': autoRefreshError }">
          {{ refreshStatusText }}
        </span>
        <NButton secondary @click="goRecords()">明细</NButton>
      </div>
    </div>

    <section class="panel">
      <div class="panel-inner filter-toolbar">
        <div class="time-row">
          <div class="quick-ranges" role="group" aria-label="快捷时间范围">
            <NButton
              v-for="option in quickRangeOptions"
              :key="option.key"
              class="quick-range-button"
              size="small"
              secondary
              :type="activeQuickRange === option.key ? 'primary' : 'default'"
              @click="applyQuickRange(option.key)"
            >
              {{ option.label }}
            </NButton>
          </div>
          <NDatePicker
            :value="dateRange"
            class="range-picker"
            type="datetimerange"
            clearable
            @update:value="handleCustomRangeChange"
          />
        </div>
        <div class="field-row" :class="{ 'is-account-scope': isAccountScope }">
          <NSelect
            v-if="!isAccountScope"
            :value="filterForm.user_id"
            :options="selectOptions.users"
            clearable
            filterable
            placeholder="用户昵称"
            @update:value="handleUserChange"
          />
          <NSelect
            :value="filterForm.api_key_description"
            :options="selectOptions.apiKeyDescriptions"
            clearable
            filterable
            placeholder="KEY 描述"
            @update:value="handleApiKeyChange"
          />
          <NSelect
            :value="filterForm.provider"
            :options="selectOptions.providers"
            clearable
            filterable
            placeholder="服务商"
            @update:value="handleProviderChange"
          />
          <NSelect
            :value="filterForm.model"
            :options="selectOptions.models"
            clearable
            filterable
            placeholder="模型"
            @update:value="handleModelChange"
          />
          <NSelect
            :value="filterForm.endpoint"
            :options="selectOptions.endpoints"
            clearable
            filterable
            placeholder="接口"
            @update:value="handleEndpointChange"
          />
          <div class="status-actions">
            <NSelect
              :value="filterForm.failed"
              class="status-select"
              :options="[
                { label: '全部', value: 'all' },
                { label: '成功', value: 'success' },
                { label: '失败', value: 'failed' },
              ]"
              @update:value="handleFailedChange"
            />
            <NButton secondary :loading="isLoading" @click="refresh()">筛选</NButton>
          </div>
        </div>
      </div>
    </section>

    <NSpin :show="isLoading">
      <div class="metric-grid">
        <div v-for="metric in metricCards" :key="metric.key" class="metric-card" :class="`is-${metric.tone}`">
          <div class="metric-icon" aria-hidden="true">
            <component :is="metric.icon" :size="20" :stroke-width="2.2" />
          </div>
          <div class="metric-label">{{ metric.label }}</div>
          <div class="metric-value">{{ metric.value }}</div>
          <div class="metric-footnote usage-metric-footnote" :title="metricRangeLabel">
            {{ metricRangeLabel }}
          </div>
        </div>
      </div>
    </NSpin>

    <div class="grid-two">
      <ChartPanel
        title="用量趋势"
        :option="trendOption"
        :empty="trends.length === 0"
        :loading="isLoading"
      />
      <ChartPanel
        title="模型分布"
        :option="pieOption(distributions.models, '模型')"
        :empty="distributions.models.length === 0"
        :loading="isLoading"
        :compact-footer="modelDistributionLegend.length === 1"
      >
        <ol
          class="distribution-legend"
          :class="{ 'is-single': modelDistributionLegend.length === 1 }"
          aria-label="模型分布图例"
        >
          <li
            v-for="item in modelDistributionLegend"
            :key="item.key"
            class="distribution-legend-item"
          >
            <span
              class="distribution-marker"
              :style="distributionMarkerStyle(item.colorIndex)"
              aria-hidden="true"
            />
            <span class="distribution-label" :title="item.label">{{ item.label }}</span>
            <span class="distribution-count">{{ item.recordsText }}</span>
            <span class="distribution-percent">{{ item.percentText }}</span>
          </li>
        </ol>
      </ChartPanel>
    </div>

    <div class="grid-two">
      <div class="ranking-pair">
        <section class="panel">
          <div class="panel-inner">
            <h2 class="section-title">{{ rankingTitle }}</h2>
            <NDataTable
              size="small"
              :columns="rankingColumns"
              :data="userRanking"
              :loading="isLoading"
              :pagination="{ pageSize: 8 }"
              :scroll-x="isAccountScope ? 390 : 370"
            />
          </div>
        </section>
        <section class="panel">
          <div class="panel-inner">
            <h2 class="section-title">模型排行</h2>
            <NDataTable
              size="small"
              :columns="modelColumns"
              :data="modelRanking"
              :loading="isLoading"
              :pagination="{ pageSize: 8 }"
              :scroll-x="520"
            />
          </div>
        </section>
      </div>
      <ChartPanel
        title="接口分布"
        :option="pieOption(distributions.endpoints, '接口')"
        :empty="distributions.endpoints.length === 0"
        :loading="isLoading"
        :compact-footer="endpointDistributionLegend.length === 1"
      >
        <ol
          class="distribution-legend"
          :class="{ 'is-single': endpointDistributionLegend.length === 1 }"
          aria-label="接口分布图例"
        >
          <li
            v-for="item in endpointDistributionLegend"
            :key="item.key"
            class="distribution-legend-item"
          >
            <span
              class="distribution-marker"
              :style="distributionMarkerStyle(item.colorIndex)"
              aria-hidden="true"
            />
            <span class="distribution-label" :title="item.label">{{ item.label }}</span>
            <span class="distribution-count">{{ item.recordsText }}</span>
            <span class="distribution-percent">{{ item.percentText }}</span>
          </li>
        </ol>
      </ChartPanel>
    </div>
  </section>
</template>

<style scoped>
.status-select {
  min-width: 96px;
}

.filter-toolbar {
  display: grid;
  gap: 12px;
  padding-block: 14px;
}

.time-row {
  display: grid;
  grid-template-columns: auto minmax(280px, 460px);
  gap: 12px;
  align-items: center;
  min-width: 0;
}

.field-row {
  display: grid;
  grid-template-columns: repeat(5, minmax(120px, 1fr)) auto;
  gap: 10px;
  align-items: end;
  min-width: 0;
}

.field-row.is-account-scope {
  grid-template-columns: repeat(4, minmax(120px, 1fr)) auto;
}

.usage-metric-footnote {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.range-picker {
  min-width: 0;
}

.quick-ranges {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  min-width: 0;
}

.quick-range-button {
  flex: 0 0 auto;
  min-width: 64px;
  border-radius: 999px;
  font-weight: 750;
}

.status-actions {
  display: flex;
  gap: 8px;
  min-width: 0;
}

.status-actions :deep(.n-button) {
  min-width: 82px;
}

.header-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
}

.refresh-status {
  color: var(--cpa-text-muted);
  font-size: 12px;
  white-space: nowrap;
}

.refresh-status.is-error {
  color: var(--cpa-danger);
}

.section-title {
  margin: 0 0 12px;
}

.ranking-pair {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  min-width: 0;
}

.distribution-legend {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 6px 10px;
  max-height: 112px;
  margin: 0;
  overflow: auto;
  padding: 0;
  list-style: none;
  scrollbar-width: thin;
}

.distribution-legend.is-single {
  grid-template-columns: minmax(0, 300px);
  justify-content: center;
  max-height: none;
  overflow: visible;
}

.distribution-legend-item {
  display: grid;
  grid-template-columns: 10px minmax(0, 1fr) auto auto;
  gap: 8px;
  align-items: center;
  min-width: 0;
  padding: 6px 8px;
  border: 1px solid color-mix(in srgb, var(--cpa-border) 68%, transparent);
  border-radius: 6px;
  background: color-mix(in srgb, var(--cpa-surface-muted) 72%, transparent);
}

.distribution-marker {
  width: 9px;
  height: 9px;
  border-radius: 3px;
  background: var(--distribution-color);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--distribution-color) 14%, transparent);
}

.distribution-label {
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text);
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.distribution-count {
  color: var(--cpa-text-strong);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  font-weight: 750;
  line-height: 18px;
}

.distribution-percent {
  min-width: 36px;
  color: var(--cpa-text-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  line-height: 18px;
  text-align: right;
}

@media (max-width: 1180px) {
  .field-row,
  .field-row.is-account-scope {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .ranking-pair {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 720px) {
  .field-row,
  .field-row.is-account-scope {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .filter-toolbar {
    gap: 8px;
    padding-block: 10px;
  }

  .time-row {
    grid-template-columns: 1fr;
    align-items: stretch;
    gap: 8px;
  }

  .field-row,
  .field-row.is-account-scope {
    gap: 8px;
  }

  .status-actions {
    display: grid;
    grid-column: 1 / -1;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: stretch;
  }

  .status-actions :deep(.n-select) {
    min-width: 0;
  }

  .header-actions {
    width: 100%;
    align-items: flex-start;
    justify-content: space-between;
  }

  .refresh-status {
    white-space: normal;
  }
}
</style>
