<script setup lang="ts">
import type { CSSProperties } from 'vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NModal,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NTooltip,
  useMessage,
  type TagProps,
} from 'naive-ui'
import {
  CircleCheck,
  CircleHelp,
  CircleX,
  ChevronDown,
  ChevronUp,
  ExternalLink,
  Gauge,
  RefreshCw,
  Settings,
  Settings2,
  TriangleAlert,
  WifiOff,
  Wrench,
} from 'lucide-vue-next'

import {
  getModelMonitorProxySettings,
  getModelMonitorSettings,
  getModelMonitorSource,
  updateModelMonitorProxySettings,
  updateModelMonitorSettings,
} from '@/features/model-monitor/api/modelMonitorApi'
import {
  modelMonitorSampleTimeFormatOptions,
  modelMonitorTimestampFormatOptions,
} from '@/features/model-monitor/utils/timeFormat'
import { minuteHistoryStripItems } from '@/features/model-monitor/utils/historyStrip'
import { useI18n } from '@/shared/i18n'
import type {
  ModelMonitorBuiltInSource,
  ModelMonitorSample,
  ModelMonitorProxySettings,
  ModelMonitorProxySettingsPayload,
  ModelMonitorSettings,
  ModelMonitorSettingsPayload,
  ModelMonitorSourceStatus,
  ModelMonitorStatus,
} from '@/shared/types/api'

const refreshIntervalMs = 60_000
const proxyModalStyle: CSSProperties = { width: 'min(460px, calc(100vw - 32px))' }
const proxyModalContentStyle: CSSProperties = { padding: '16px 22px 4px' }
const proxyModalFooterStyle: CSSProperties = { padding: '12px 22px 18px' }
const sourceSettingsModalStyle: CSSProperties = { width: 'min(620px, calc(100vw - 32px))' }
const sourceSettingsModalContentStyle: CSSProperties = { padding: '16px 22px 4px' }
const sourceSettingsModalFooterStyle: CSSProperties = { padding: '12px 22px 18px' }
const modelMonitorTooltipThemeOverrides = {
  color: 'var(--cpa-surface-raised)',
  textColor: 'var(--cpa-text)',
  boxShadow: 'var(--cpa-shadow), 0 0 0 1px var(--cpa-border)',
  padding: '10px 12px',
  borderRadius: '8px',
}
const message = useMessage()
const { errorText, language, serverText, t } = useI18n()
const noUpstreamSampleLabel = computed(() => t('暂无上游样本', 'No upstream sample'))

interface ModelMonitorSourceViewState {
  source: ModelMonitorSourceStatus | null
  error: string | null
  loading: boolean
  pending: boolean
  queued: boolean
  refreshPromise: Promise<void> | null
  generation: number
}

interface ModelMonitorSourceSlot {
  definition: ModelMonitorBuiltInSource
  state: ModelMonitorSourceViewState
}

type ModelMonitorSourceHealth = ModelMonitorStatus | 'collection_error' | 'loading'

const modelMonitorSettings = ref<ModelMonitorSettings | null>(null)
const sourceStates = reactive<Record<string, ModelMonitorSourceViewState>>({})
const settingsLoading = ref(true)
const settingsLoadError = ref<string | null>(null)
const sourceSettingsModalOpen = ref(false)
const sourceSettingsLoading = ref(false)
const sourceSettingsLoadSucceeded = ref(false)
const sourceSettingsLoadError = ref<string | null>(null)
const sourceSettingsSaving = ref(false)
const sourceSettingsDraft = ref<ModelMonitorBuiltInSource[]>([])
const proxyModalOpen = ref(false)
const proxyLoading = ref(false)
const proxyLoadSucceeded = ref(false)
const proxySaving = ref(false)
const proxyForm = reactive<ModelMonitorProxySettings>({
  enabled: false,
  proxy_url: '',
})
let refreshTimer: number | undefined
let modelMonitorSettingsGeneration = 0
let modelMonitorSettingsRequestGeneration = 0
let sourceSettingsModalGeneration = 0
let proxyLoadGeneration = 0

const enabledSourceSlots = computed<ModelMonitorSourceSlot[]>(() => {
  const settings = modelMonitorSettings.value
  if (!settings) return []

  const slots: ModelMonitorSourceSlot[] = []
  for (const source of settings.sources) {
    if (!source.enabled) continue
    const state = sourceStates[source.id]
    if (state) slots.push({ definition: source, state })
  }
  return slots
})
const refreshAllPending = ref(false)
let refreshAllQueued = false
const totalCount = computed(() => enabledSourceSlots.value.length)
const failedCount = computed(
  () =>
    enabledSourceSlots.value.filter(
      (slot) => slot.state.error || slot.state.source?.collection_state === 'error',
    ).length,
)
const operationalCount = computed(
  () =>
    enabledSourceSlots.value.filter(
      (slot) =>
        !slot.state.error &&
        slot.state.source?.collection_state === 'ok' &&
        slot.state.source.overall_status === 'operational',
    ).length,
)
const abnormalCount = computed(
  () =>
    enabledSourceSlots.value.filter(
      (slot) =>
        !slot.state.error &&
        slot.state.source?.collection_state === 'ok' &&
        slot.state.source.overall_status !== 'operational',
    ).length,
)

function createSourceViewState(): ModelMonitorSourceViewState {
  return {
    source: null,
    error: null,
    loading: false,
    pending: false,
    queued: false,
    refreshPromise: null,
    generation: 0,
  }
}

function enabledSourceIDs(settings: ModelMonitorSettings | null): string[] {
  return settings?.enabled_source_ids ?? []
}

function hasSameEnabledSourceIDs(left: ModelMonitorSettings | null, right: ModelMonitorSettings): boolean {
  const leftIDs = enabledSourceIDs(left)
  const rightIDs = right.enabled_source_ids
  return leftIDs.length === rightIDs.length && leftIDs.every((id, index) => id === rightIDs[index])
}

function applyModelMonitorSettings(settings: ModelMonitorSettings): void {
  if (!hasSameEnabledSourceIDs(modelMonitorSettings.value, settings)) {
    modelMonitorSettingsGeneration++
  }

  const enabledIDs = new Set(settings.enabled_source_ids)
  for (const source of settings.sources) {
    if (!enabledIDs.has(source.id)) continue
    if (!sourceStates[source.id]) {
      sourceStates[source.id] = createSourceViewState()
    }
  }
  for (const id of Object.keys(sourceStates)) {
    if (!enabledIDs.has(id)) delete sourceStates[id]
  }

  modelMonitorSettings.value = settings
  settingsLoadError.value = null
}

function isSourceEnabled(id: string): boolean {
  return modelMonitorSettings.value?.enabled_source_ids.includes(id) ?? false
}

function sourceForSlot(slot: ModelMonitorSourceSlot): ModelMonitorSourceStatus[] {
  return slot.state.source ? [slot.state.source] : []
}

function sourceSlotHealthKey(slot: ModelMonitorSourceSlot): ModelMonitorSourceHealth {
  if (slot.state.error) return 'collection_error'
  if (!slot.state.source) return 'loading'
  return sourceHealthKey(slot.state.source)
}

function sourceSlotHealthLabel(slot: ModelMonitorSourceSlot): string {
  if (slot.state.error) return t('采集失败', 'Collection failed')
  if (!slot.state.source) return t('正在采集', 'Collecting')
  return sourceHealthLabel(slot.state.source)
}

function sourceSlotHealthIcon(slot: ModelMonitorSourceSlot) {
  if (slot.state.error) return WifiOff
  if (!slot.state.source) return RefreshCw
  return sourceHealthIcon(slot.state.source)
}

function refreshSource(id: string): Promise<void> {
  const state = sourceStates[id]
  if (!state || !isSourceEnabled(id)) return Promise.resolve()
  if (state.pending) {
    state.queued = true
    if (!state.refreshPromise) {
      throw new Error(`Missing pending refresh promise for model monitor source ${id}`)
    }
    return state.refreshPromise
  }

  const refresh = runSourceRefresh(id, state)
  state.refreshPromise = refresh
  return refresh
}

async function runSourceRefresh(id: string, state: ModelMonitorSourceViewState): Promise<void> {
  state.pending = true
  state.loading = true
  state.error = null
  const settingsGeneration = modelMonitorSettingsGeneration
  const requestGeneration = ++state.generation
  try {
    const source = await getModelMonitorSource(id)
    if (
      settingsGeneration !== modelMonitorSettingsGeneration ||
      sourceStates[id] !== state ||
      state.generation !== requestGeneration ||
      !isSourceEnabled(id)
    ) {
      return
    }
    if (source.collection_state === 'error' && state.source?.collection_state === 'ok') {
      state.error = serverText(source.collection_error ?? '', '本次采集失败', 'This collection failed')
    } else {
      state.source = source
      state.error = null
    }
  } catch (error) {
    if (
      settingsGeneration !== modelMonitorSettingsGeneration ||
      sourceStates[id] !== state ||
      state.generation !== requestGeneration ||
      !isSourceEnabled(id)
    ) {
      return
    }
    state.error = errorText(error, '加载来源状态失败', 'Failed to load source status')
  } finally {
    if (sourceStates[id] === state && state.generation === requestGeneration) {
      state.pending = false
      state.loading = false
      if (state.queued && isSourceEnabled(id)) {
        state.queued = false
        await refreshSource(id)
      }
    }
  }
}

function refreshAllSources(trigger: 'manual' | 'configuration' = 'manual'): void {
  if (refreshAllPending.value) {
    if (trigger === 'configuration') refreshAllQueued = true
    return
  }

  // Keep Refresh All available during a manual source refresh so that source
  // can queue once while the other sources begin immediately.
  refreshAllPending.value = true
  const refreshes = enabledSourceSlots.value.map((slot) => refreshSource(slot.definition.id))
  void Promise.all(refreshes).finally(() => {
    refreshAllPending.value = false
    if (refreshAllQueued) {
      refreshAllQueued = false
      refreshAllSources('configuration')
    }
  })
}

async function loadModelMonitorSettings(): Promise<void> {
  const requestGeneration = ++modelMonitorSettingsRequestGeneration
  settingsLoading.value = true
  settingsLoadError.value = null
  try {
    const settings = await getModelMonitorSettings()
    if (requestGeneration !== modelMonitorSettingsRequestGeneration) return
    applyModelMonitorSettings(settings)
    refreshAllSources('configuration')
  } catch (error) {
    if (requestGeneration !== modelMonitorSettingsRequestGeneration) return
    const localizedError = errorText(error, '加载模型监控配置失败', 'Failed to load model monitoring settings')
    settingsLoadError.value = localizedError
    message.error(localizedError)
  } finally {
    if (requestGeneration === modelMonitorSettingsRequestGeneration) settingsLoading.value = false
  }
}

function cloneSourceSettings(sources: ModelMonitorBuiltInSource[]): ModelMonitorBuiltInSource[] {
  return sources.map((source) => ({ ...source }))
}

async function openSourceSettings(): Promise<void> {
  const generation = ++sourceSettingsModalGeneration
  modelMonitorSettingsRequestGeneration++
  sourceSettingsModalOpen.value = true
  sourceSettingsLoading.value = true
  sourceSettingsLoadSucceeded.value = false
  sourceSettingsLoadError.value = null
  sourceSettingsDraft.value = []
  try {
    const settings = await getModelMonitorSettings()
    if (generation !== sourceSettingsModalGeneration || !sourceSettingsModalOpen.value) return
    const settingsChanged = !hasSameEnabledSourceIDs(modelMonitorSettings.value, settings)
    applyModelMonitorSettings(settings)
    sourceSettingsDraft.value = cloneSourceSettings(settings.sources)
    sourceSettingsLoadSucceeded.value = true
    settingsLoading.value = false
    if (settingsChanged) refreshAllSources('configuration')
  } catch (error) {
    if (generation !== sourceSettingsModalGeneration || !sourceSettingsModalOpen.value) return
    const localizedError = errorText(error, '加载来源配置失败', 'Failed to load source settings')
    sourceSettingsLoadError.value = localizedError
    if (!modelMonitorSettings.value) {
      settingsLoadError.value = localizedError
      settingsLoading.value = false
    }
    message.error(localizedError)
  } finally {
    if (generation === sourceSettingsModalGeneration && sourceSettingsModalOpen.value) {
      sourceSettingsLoading.value = false
    }
  }
}

function updateDraftSourceEnabled(id: string, enabled: boolean): void {
  const source = sourceSettingsDraft.value.find((item) => item.id === id)
  if (!source) return
  source.enabled = enabled
  sourceSettingsDraft.value = [
    ...sourceSettingsDraft.value.filter((item) => item.enabled),
    ...sourceSettingsDraft.value.filter((item) => !item.enabled),
  ]
}

function moveDraftSource(id: string, direction: -1 | 1): void {
  const index = sourceSettingsDraft.value.findIndex((source) => source.id === id)
  const targetIndex = index + direction
  const source = sourceSettingsDraft.value[index]
  const target = sourceSettingsDraft.value[targetIndex]
  if (
    index < 0 ||
    targetIndex < 0 ||
    targetIndex >= sourceSettingsDraft.value.length ||
    !source?.enabled ||
    !target?.enabled
  ) {
    return
  }
  const next = [...sourceSettingsDraft.value]
  next[index] = target
  next[targetIndex] = source
  sourceSettingsDraft.value = next
}

function canMoveDraftSource(id: string, direction: -1 | 1): boolean {
  const index = sourceSettingsDraft.value.findIndex((source) => source.id === id)
  const source = sourceSettingsDraft.value[index]
  const target = sourceSettingsDraft.value[index + direction]
  return index >= 0 && source?.enabled === true && target?.enabled === true
}

async function saveSourceSettings(): Promise<void> {
  if (
    !sourceSettingsModalOpen.value ||
    sourceSettingsLoading.value ||
    !sourceSettingsLoadSucceeded.value ||
    sourceSettingsSaving.value
  ) {
    return
  }

  const payload: ModelMonitorSettingsPayload = {
    enabled_source_ids: sourceSettingsDraft.value
      .filter((source) => source.enabled)
      .map((source) => source.id),
  }
  sourceSettingsSaving.value = true
  try {
    const saved = await updateModelMonitorSettings(payload)
    applyModelMonitorSettings(saved)
    sourceSettingsModalOpen.value = false
    message.success(t('来源配置已保存', 'Source settings saved'))
    refreshAllSources('configuration')
  } catch (error) {
    message.error(errorText(error, '保存来源配置失败', 'Failed to save source settings'))
  } finally {
    sourceSettingsSaving.value = false
  }
}

async function openProxySettings() {
  const generation = ++proxyLoadGeneration
  proxyModalOpen.value = true
  proxyLoadSucceeded.value = false
  proxyLoading.value = true
  proxyForm.enabled = false
  proxyForm.proxy_url = ''
  try {
    const settings = await getModelMonitorProxySettings()
    if (generation !== proxyLoadGeneration || !proxyModalOpen.value) return
    proxyForm.enabled = settings.enabled
    proxyForm.proxy_url = settings.proxy_url
    proxyLoadSucceeded.value = true
  } catch (error) {
    if (generation !== proxyLoadGeneration || !proxyModalOpen.value) return
    message.error(errorText(error, '加载代理配置失败', 'Failed to load proxy settings'))
  } finally {
    if (generation === proxyLoadGeneration && proxyModalOpen.value) proxyLoading.value = false
  }
}

async function saveProxySettings() {
  if (!proxyModalOpen.value || proxyLoading.value || !proxyLoadSucceeded.value || proxySaving.value) return

  const payload: ModelMonitorProxySettingsPayload = {
    enabled: proxyForm.enabled,
    proxy_url: proxyForm.proxy_url.trim(),
  }
  if (payload.enabled && !payload.proxy_url) {
    message.error(t('启用代理时必须填写代理地址', 'Proxy URL is required when proxy is enabled'))
    return
  }
  proxySaving.value = true
  try {
    const saved = await updateModelMonitorProxySettings(payload)
    proxyForm.enabled = saved.enabled
    proxyForm.proxy_url = saved.proxy_url
    proxyModalOpen.value = false
    message.success(t('代理配置已保存', 'Proxy settings saved'))
    refreshAllSources('configuration')
  } catch (error) {
    message.error(errorText(error, '保存代理配置失败', 'Failed to save proxy settings'))
  } finally {
    proxySaving.value = false
  }
}

function statusLabel(status: ModelMonitorStatus): string {
  const labels: Record<ModelMonitorStatus, [string, string]> = {
    operational: ['正常', 'Operational'],
    degraded_performance: ['性能下降', 'Degraded'],
    partial_outage: ['部分故障', 'Partial outage'],
    major_outage: ['严重故障', 'Major outage'],
    maintenance: ['维护中', 'Maintenance'],
    unknown: ['未知', 'Unknown'],
  }
  return t(...labels[status])
}

function statusTagType(status: ModelMonitorStatus): NonNullable<TagProps['type']> {
  if (status === 'operational') return 'success'
  if (status === 'degraded_performance' || status === 'maintenance') return 'warning'
  if (status === 'partial_outage' || status === 'major_outage') return 'error'
  return 'default'
}

function formatTime(value: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(
    language.value === 'zh' ? 'zh-CN' : 'en-US',
    modelMonitorTimestampFormatOptions(),
  ).format(date)
}

function formatSampleTime(sample: ModelMonitorSample, granularity: 'day' | 'minute'): string {
  const date = new Date(sample.timestamp)
  return new Intl.DateTimeFormat(
    language.value === 'zh' ? 'zh-CN' : 'en-US',
    modelMonitorSampleTimeFormatOptions(granularity),
  ).format(date)
}

function historyWindowLabel(source: { history_granularity: 'day' | 'minute' }): string {
  if (source.history_granularity === 'day') return t('最近 90 天', 'Last 90 days')
  return t('最近 60 分钟', 'Last 60 minutes')
}

function sampleRelatedIncidents(sample: ModelMonitorSample): string | null {
  if (sample.related_incidents.length === 0) return null
  return t(
    `关联事件：${sample.related_incidents.join(', ')}`,
    `Related: ${sample.related_incidents.join(', ')}`,
  )
}

function sampleTooltip(sample: ModelMonitorSample, source: ModelMonitorSourceStatus): string {
  const details = [formatSampleTime(sample, source.history_granularity === 'day' ? 'day' : 'minute')]
  details.push(statusLabel(sample.status))
  if (sample.latency_ms !== null) details.push(`${Math.round(sample.latency_ms)} ms`)
  if (sample.error) details.push(sample.error)
  const relatedIncidents = sampleRelatedIncidents(sample)
  if (relatedIncidents) details.push(relatedIncidents)
  return details.join(' · ')
}

function sampleTooltipLines(sample: ModelMonitorSample, source: ModelMonitorSourceStatus): string[] {
  if (source.history_granularity === 'minute') {
    if (!sample.error) return [sampleTooltip(sample, source)]
    const summary = [formatSampleTime(sample, 'minute'), statusLabel(sample.status)]
    if (sample.latency_ms !== null) summary.push(`${Math.round(sample.latency_ms)} ms`)
    return [summary.join(' · '), sample.error]
  }
  const lines = [`${formatSampleTime(sample, 'day')} · ${statusLabel(sample.status)}`]
  const relatedIncidents = sampleRelatedIncidents(sample)
  if (relatedIncidents) lines.push(relatedIncidents)
  return lines
}

function sourceHealthKey(source: ModelMonitorSourceStatus): ModelMonitorStatus | 'collection_error' {
  return source.collection_state === 'error' ? 'collection_error' : source.overall_status
}

function sourceHealthLabel(source: ModelMonitorSourceStatus): string {
  if (source.collection_state === 'error') return t('采集失败', 'Collection failed')
  const labels: Record<ModelMonitorStatus, [string, string]> = {
    operational: ['当前运行正常', 'Currently operational'],
    degraded_performance: ['当前性能下降', 'Currently degraded'],
    partial_outage: ['当前部分故障', 'Current partial outage'],
    major_outage: ['当前严重故障', 'Current major outage'],
    maintenance: ['当前处于维护中', 'Currently under maintenance'],
    unknown: ['当前状态未知', 'Current status unknown'],
  }
  return t(...labels[source.overall_status])
}

function sourceHealthIcon(source: ModelMonitorSourceStatus) {
  const icons = {
    operational: CircleCheck,
    degraded_performance: Gauge,
    partial_outage: TriangleAlert,
    major_outage: CircleX,
    maintenance: Wrench,
    unknown: CircleHelp,
    collection_error: WifiOff,
  }
  return icons[sourceHealthKey(source)]
}

onMounted(() => {
  void loadModelMonitorSettings()
  refreshTimer = window.setInterval(refreshAllSources, refreshIntervalMs)
})

onBeforeUnmount(() => {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer)
})

watch(proxyModalOpen, (open) => {
  if (open) return
  proxyLoadGeneration++
  proxyLoading.value = false
  proxyLoadSucceeded.value = false
})

watch(sourceSettingsModalOpen, (open) => {
  if (open) return
  const bootstrapPending = settingsLoading.value && !modelMonitorSettings.value
  sourceSettingsModalGeneration++
  sourceSettingsLoading.value = false
  sourceSettingsLoadSucceeded.value = false
  sourceSettingsLoadError.value = null
  if (bootstrapPending) void loadModelMonitorSettings()
})
</script>

<template>
  <div class="page model-monitor-page">
    <header class="page-header">
      <div>
        <h1 class="page-title">{{ t('模型监控', 'Model Monitoring') }}</h1>
        <p class="page-subtitle">
          {{ t('查看公开状态页的当前状态与上游原生历史窗口', 'View current status and native upstream history windows') }}
        </p>
      </div>
      <NSpace>
        <NButton secondary @click="openSourceSettings">
          <template #icon><NIcon><Settings /></NIcon></template>
          {{ t('来源配置', 'Source settings') }}
        </NButton>
        <NButton secondary @click="openProxySettings">
          <template #icon><NIcon><Settings2 /></NIcon></template>
          {{ t('代理配置', 'Proxy settings') }}
        </NButton>
        <NButton
          secondary
          :aria-busy="refreshAllPending"
          :disabled="settingsLoading || refreshAllPending || enabledSourceSlots.length === 0"
          @click="refreshAllSources()"
        >
          <template #icon><NIcon class="refresh-all-icon" :class="{ 'is-spinning': refreshAllPending }"><RefreshCw /></NIcon></template>
          {{ t('刷新全部', 'Refresh all') }}
        </NButton>
      </NSpace>
    </header>

    <section class="metric-grid monitor-metrics">
      <article class="metric-card is-blue"><span class="metric-label">{{ t('来源', 'Sources') }}</span><strong class="metric-value">{{ totalCount }}</strong></article>
      <article class="metric-card is-success"><span class="metric-label">{{ t('正常', 'Operational') }}</span><strong class="metric-value">{{ operationalCount }}</strong></article>
      <article class="metric-card is-warning"><span class="metric-label">{{ t('异常', 'Affected') }}</span><strong class="metric-value">{{ abnormalCount }}</strong></article>
      <article class="metric-card is-danger"><span class="metric-label">{{ t('采集失败', 'Collection errors') }}</span><strong class="metric-value">{{ failedCount }}</strong></article>
    </section>

    <NSpin :show="settingsLoading">
      <div v-if="enabledSourceSlots.length" class="source-grid">
        <article
          v-for="slot in enabledSourceSlots"
          :key="slot.definition.id"
          class="panel source-card"
          :class="`is-${sourceSlotHealthKey(slot)}`"
        >
          <div class="panel-inner">
            <div class="source-heading">
              <div>
                <div class="source-title-row">
                  <h2>{{ slot.definition.name }}</h2>
                </div>
                <p>{{ historyWindowLabel(slot.definition) }}</p>
              </div>
              <div class="source-actions">
                <NTooltip trigger="hover">
                  <template #trigger>
                    <NButton
                      quaternary
                      circle
                      size="small"
                      :loading="slot.state.pending"
                      :disabled="slot.state.pending"
                      :aria-label="t(`刷新 ${slot.definition.name}`, `Refresh ${slot.definition.name}`)"
                      @click="refreshSource(slot.definition.id)"
                    >
                      <template #icon><NIcon><RefreshCw /></NIcon></template>
                    </NButton>
                  </template>
                  {{ t('刷新此来源', 'Refresh this source') }}
                </NTooltip>
                <NButton tag="a" text :href="slot.definition.status_page_url" target="_blank" rel="noopener noreferrer">
                  <template #icon><NIcon><ExternalLink /></NIcon></template>
                  {{ t('原始状态页', 'Status page') }}
                </NButton>
              </div>
            </div>

            <div class="source-health" :class="`is-${sourceSlotHealthKey(slot)}`">
              <NIcon size="19"><component :is="sourceSlotHealthIcon(slot)" /></NIcon>
              <strong>{{ sourceSlotHealthLabel(slot) }}</strong>
            </div>

            <NSpin :show="slot.state.loading">
              <NAlert v-if="slot.state.error" type="error" :title="t('加载来源状态失败', 'Failed to load source status')">
                {{ slot.state.error }}
              </NAlert>
              <template v-for="source in sourceForSlot(slot)" :key="source.id">
                <NAlert v-if="source.collection_state === 'error'" type="error" :title="t('本次采集失败', 'This collection failed')">
                  {{ serverText(source.collection_error || '') }}
                </NAlert>

                <div class="timestamp-line">
                  <span>{{ t('来源更新', 'Source updated') }} <strong>{{ formatTime(source.source_updated_at) }}</strong></span>
                  <span>{{ t('检查', 'Checked') }} <strong>{{ formatTime(source.checked_at) }}</strong></span>
                  <span>{{ t('最后成功', 'Last success') }} <strong>{{ formatTime(source.last_success_at) }}</strong></span>
                </div>

                <template v-if="source.collection_state === 'ok'">
                  <div v-if="source.services.length" class="service-list">
                    <article v-for="service in source.services" :key="service.id" class="service-row">
                      <div class="service-header">
                        <span>{{ service.name }}</span>
                        <NTag size="small" :type="statusTagType(service.status)">{{ statusLabel(service.status) }}</NTag>
                        <span v-if="service.uptime_percent !== null" class="service-stat">{{ service.uptime_percent.toFixed(2) }}%</span>
                        <span v-if="service.last_latency_ms !== null" class="service-stat">{{ Math.round(service.last_latency_ms) }} ms</span>
                      </div>
                      <NAlert v-if="service.last_error" type="error" :show-icon="false" class="last-error">{{ service.last_error }}</NAlert>
                      <div v-if="service.samples.length && source.history_granularity === 'minute'" class="sample-strip is-minute">
                        <template v-for="item in minuteHistoryStripItems(service.samples, source.history_granularity)" :key="item.key">
                          <NTooltip v-if="item.sample" trigger="hover" :theme-overrides="modelMonitorTooltipThemeOverrides">
                            <template #trigger><span class="sample-bar" :class="`is-${item.sample.status}`" role="img" :aria-label="sampleTooltip(item.sample, source)" /></template>
                            <div class="sample-tooltip-content">
                              <span v-for="(line, lineIndex) in sampleTooltipLines(item.sample, source)" :key="lineIndex">{{ line }}</span>
                            </div>
                          </NTooltip>
                          <NTooltip v-else trigger="hover" :theme-overrides="modelMonitorTooltipThemeOverrides">
                            <template #trigger><span class="sample-bar is-empty" role="img" :aria-label="noUpstreamSampleLabel" /></template>
                            {{ noUpstreamSampleLabel }}
                          </NTooltip>
                        </template>
                      </div>
                      <div v-else-if="service.samples.length" class="sample-strip" :class="`is-${source.history_granularity}`">
                        <NTooltip v-for="(sample, sampleIndex) in service.samples" :key="`${sample.timestamp}-${sampleIndex}`" trigger="hover" :theme-overrides="modelMonitorTooltipThemeOverrides">
                          <template #trigger><span class="sample-bar" :class="`is-${sample.status}`" role="img" :aria-label="sampleTooltip(sample, source)" /></template>
                          <div class="sample-tooltip-content">
                            <span v-for="(line, lineIndex) in sampleTooltipLines(sample, source)" :key="lineIndex">{{ line }}</span>
                          </div>
                        </NTooltip>
                      </div>
                    </article>
                  </div>

                  <NCollapse v-if="source.groups.length" class="group-list" arrow-placement="right">
                    <NCollapseItem v-for="group in source.groups" :key="group.id" :name="group.id">
                      <template #header>
                        <div class="group-summary">
                          <div class="service-header">
                            <strong>{{ group.name }}</strong>
                            <NTag size="small" :type="statusTagType(group.status)">{{ statusLabel(group.status) }}</NTag>
                            <span class="service-stat">{{ t(`${group.services.length} 个组件`, `${group.services.length} components`) }}</span>
                            <span v-if="group.uptime_percent !== null" class="service-stat">{{ group.uptime_percent.toFixed(2) }}%</span>
                          </div>
                          <div v-if="group.samples.length" class="sample-strip is-day">
                            <NTooltip v-for="sample in group.samples" :key="sample.timestamp" trigger="hover" :theme-overrides="modelMonitorTooltipThemeOverrides">
                              <template #trigger><span class="sample-bar" :class="`is-${sample.status}`" role="img" :aria-label="sampleTooltip(sample, source)" /></template>
                              <div class="sample-tooltip-content">
                                <span v-for="(line, lineIndex) in sampleTooltipLines(sample, source)" :key="lineIndex">{{ line }}</span>
                              </div>
                            </NTooltip>
                          </div>
                        </div>
                      </template>
                      <div class="group-services">
                        <article v-for="service in group.services" :key="service.id" class="service-row">
                          <div class="service-header">
                            <span>{{ service.name }}</span>
                            <NTag size="small" :type="statusTagType(service.status)">{{ statusLabel(service.status) }}</NTag>
                            <span v-if="service.uptime_percent !== null" class="service-stat">{{ service.uptime_percent.toFixed(2) }}%</span>
                          </div>
                          <div v-if="service.samples.length" class="sample-strip is-day">
                            <NTooltip v-for="sample in service.samples" :key="sample.timestamp" trigger="hover" :theme-overrides="modelMonitorTooltipThemeOverrides">
                              <template #trigger><span class="sample-bar" :class="`is-${sample.status}`" role="img" :aria-label="sampleTooltip(sample, source)" /></template>
                              <div class="sample-tooltip-content">
                                <span v-for="(line, lineIndex) in sampleTooltipLines(sample, source)" :key="lineIndex">{{ line }}</span>
                              </div>
                            </NTooltip>
                          </div>
                        </article>
                      </div>
                    </NCollapseItem>
                  </NCollapse>
                  <NEmpty v-if="!source.services.length && !source.groups.length" size="small" :description="t('上游未返回组件或模型', 'The upstream returned no components or models')" />

                  <section v-if="source.id === 'openai' || source.id === 'anthropic' || source.id === 'deepseek'" class="incidents">
                    <h3>{{ t('当前事件', 'Current incidents') }}</h3>
                    <NEmpty v-if="source.incidents.length === 0" size="small" :description="t('当前没有事件', 'No current incidents')" />
                    <a v-for="incident in source.incidents" :key="incident.id" class="incident-row" :href="incident.url || undefined" :target="incident.url ? '_blank' : undefined" rel="noopener noreferrer">
                      <span><strong>{{ incident.name }}</strong><small>{{ incident.status }} · {{ incident.impact || t('影响未知', 'Unknown impact') }}</small></span>
                      <time>{{ formatTime(incident.updated_at) }}</time>
                    </a>
                  </section>
                </template>
              </template>
              <div v-if="!slot.state.source && slot.state.loading" class="source-loading-placeholder">
                {{ t('正在采集此来源的状态', 'Collecting this source') }}
              </div>
              <NEmpty
                v-else-if="!slot.state.source && !slot.state.error"
                size="small"
                :description="t('尚未获取来源状态', 'Source status has not been collected yet')"
              />
            </NSpin>
          </div>
        </article>
      </div>
      <NAlert
        v-else-if="!settingsLoading && settingsLoadError"
        class="aggregate-load-error"
        type="error"
        :title="t('模型监控配置加载失败', 'Failed to load model monitoring settings')"
      >
        <div class="aggregate-load-error-content">
          <span>{{ settingsLoadError }}</span>
          <NButton size="small" type="error" secondary @click="loadModelMonitorSettings">
            <template #icon><NIcon><RefreshCw /></NIcon></template>
            {{ t('重试', 'Retry') }}
          </NButton>
        </div>
      </NAlert>
      <NEmpty
        v-else-if="!settingsLoading"
        :description="t('暂无已启用的模型监控来源', 'No model monitoring sources are enabled')"
      />
    </NSpin>

    <NModal
      v-model:show="sourceSettingsModalOpen"
      preset="card"
      :title="t('模型监控来源配置', 'Model monitoring source settings')"
      :style="sourceSettingsModalStyle"
      :content-style="sourceSettingsModalContentStyle"
      :footer-style="sourceSettingsModalFooterStyle"
      :mask-closable="!sourceSettingsSaving"
      :close-on-esc="!sourceSettingsSaving"
      :closable="!sourceSettingsSaving"
      class="source-settings-modal"
    >
      <NSpin :show="sourceSettingsLoading">
        <NAlert v-if="sourceSettingsLoadError" type="error" :title="t('来源配置加载失败', 'Failed to load source settings')">
          {{ sourceSettingsLoadError }}
        </NAlert>
        <div v-else class="source-settings-list">
          <article
            v-for="source in sourceSettingsDraft"
            :key="source.id"
            class="source-settings-row"
            :class="{ 'is-disabled': !source.enabled }"
          >
            <div class="source-settings-main">
              <div class="source-settings-copy">
                <strong>{{ source.name }}</strong>
                <span>{{ historyWindowLabel(source) }}</span>
              </div>
              <NSwitch
                :value="source.enabled"
                :disabled="sourceSettingsLoading || sourceSettingsSaving"
                :aria-label="t(`启用 ${source.name}`, `Enable ${source.name}`)"
                @update:value="(value) => updateDraftSourceEnabled(source.id, value)"
              />
            </div>
            <div v-if="source.enabled" class="source-order-actions">
              <NTooltip trigger="hover">
                <template #trigger>
                  <NButton
                    quaternary
                    circle
                    size="small"
                    :disabled="sourceSettingsLoading || sourceSettingsSaving || !canMoveDraftSource(source.id, -1)"
                    :aria-label="t(`上移 ${source.name}`, `Move ${source.name} up`)"
                    @click="moveDraftSource(source.id, -1)"
                  >
                    <template #icon><NIcon><ChevronUp /></NIcon></template>
                  </NButton>
                </template>
                {{ t('上移', 'Move up') }}
              </NTooltip>
              <NTooltip trigger="hover">
                <template #trigger>
                  <NButton
                    quaternary
                    circle
                    size="small"
                    :disabled="sourceSettingsLoading || sourceSettingsSaving || !canMoveDraftSource(source.id, 1)"
                    :aria-label="t(`下移 ${source.name}`, `Move ${source.name} down`)"
                    @click="moveDraftSource(source.id, 1)"
                  >
                    <template #icon><NIcon><ChevronDown /></NIcon></template>
                  </NButton>
                </template>
                {{ t('下移', 'Move down') }}
              </NTooltip>
            </div>
          </article>
        </div>
      </NSpin>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="sourceSettingsSaving" @click="sourceSettingsModalOpen = false">
            {{ t('取消', 'Cancel') }}
          </NButton>
          <NButton
            type="primary"
            :loading="sourceSettingsSaving"
            :disabled="sourceSettingsLoading || !sourceSettingsLoadSucceeded || sourceSettingsSaving"
            @click="saveSourceSettings"
          >
            {{ t('保存', 'Save') }}
          </NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="proxyModalOpen"
      preset="card"
      :title="t('模型监控代理配置', 'Model monitoring proxy settings')"
      :style="proxyModalStyle"
      :content-style="proxyModalContentStyle"
      :footer-style="proxyModalFooterStyle"
      :mask-closable="!proxySaving"
      :close-on-esc="!proxySaving"
      :closable="!proxySaving"
      class="proxy-modal"
    >
      <NForm :model="proxyForm" label-placement="top">
        <div class="proxy-form">
          <div class="proxy-switch-row">
            <span class="proxy-switch-label">{{ t('使用代理', 'Use proxy') }}</span>
            <NSwitch
              v-model:value="proxyForm.enabled"
              :disabled="proxyLoading || proxySaving"
              :aria-label="t('使用代理', 'Use proxy')"
            />
          </div>
          <NFormItem :label="t('代理地址', 'Proxy URL')">
            <NInput
              v-model:value="proxyForm.proxy_url"
              :disabled="!proxyForm.enabled || proxyLoading || proxySaving"
              :placeholder="t('http://127.0.0.1:7890 或 socks5://127.0.0.1:1080', 'http://127.0.0.1:7890 or socks5://127.0.0.1:1080')"
            />
          </NFormItem>
        </div>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="proxySaving" @click="proxyModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton
            type="primary"
            :loading="proxySaving"
            :disabled="proxyLoading || !proxyLoadSucceeded || proxySaving"
            @click="saveProxySettings"
          >
            {{ t('保存', 'Save') }}
          </NButton>
        </NSpace>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.model-monitor-page { padding-bottom: 8px; }
.proxy-modal { width: min(520px, calc(100vw - 24px)); }
.source-settings-modal { width: min(640px, calc(100vw - 24px)); }
.proxy-form { display: grid; gap: 14px; }
.proxy-switch-row { display: flex; align-items: center; justify-content: space-between; min-height: 34px; padding: 8px 10px; border: 1px solid var(--cpa-border); border-radius: var(--cpa-radius); background: var(--cpa-surface-raised); }
.proxy-switch-label { color: var(--cpa-text); font-size: 14px; font-weight: 600; }
.proxy-form :deep(.n-form-item) { margin-bottom: 0; }
.source-settings-list { display: grid; gap: 10px; }
.source-settings-row { display: flex; align-items: center; gap: 8px; min-width: 0; padding: 10px 12px; border: 1px solid var(--cpa-border); border-radius: var(--cpa-radius); background: var(--cpa-surface-raised); }
.source-settings-row.is-disabled { opacity: 0.66; }
.source-settings-main { display: flex; flex: 1 1 auto; align-items: center; justify-content: space-between; min-width: 0; gap: 16px; }
.source-settings-copy { display: grid; min-width: 0; gap: 3px; }
.source-settings-copy strong { min-width: 0; overflow-wrap: anywhere; color: var(--cpa-text-strong); font-size: 14px; }
.source-settings-copy span { color: var(--cpa-text-muted); font-size: 12px; }
.source-order-actions { display: inline-flex; flex: 0 0 auto; gap: 2px; }
.monitor-metrics { grid-template-columns: repeat(4, minmax(150px, 1fr)); }
.monitor-metrics .metric-card { min-height: 92px; }
.metric-label { color: var(--cpa-text-muted); font-size: 13px; }
.metric-value { color: var(--cpa-text-strong); font-size: 28px; }
.aggregate-load-error-content { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.source-grid { display: grid; gap: 14px; }
.source-card { --source-health-color: var(--cpa-text-muted); border-left: 3px solid var(--source-health-color); }
.source-card.is-operational { --source-health-color: var(--cpa-success); }
.source-card.is-degraded_performance, .source-card.is-maintenance { --source-health-color: var(--cpa-warning); }
.source-card.is-partial_outage { --source-health-color: var(--cpa-accent-orange); }
.source-card.is-major_outage, .source-card.is-collection_error { --source-health-color: var(--cpa-danger); }
.source-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.source-heading > div:first-child { min-width: 0; }
.source-title-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.source-title-row h2 { min-width: 0; margin: 0; overflow-wrap: anywhere; color: var(--cpa-text-strong); font-size: 19px; }
.source-heading p { margin: 5px 0 0; color: var(--cpa-text-muted); font-size: 13px; }
.source-actions { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 4px; }
.refresh-all-icon.is-spinning { animation: refresh-all-spin 0.9s linear infinite; }
@keyframes refresh-all-spin { to { transform: rotate(360deg); } }
.source-health { display: flex; align-items: center; gap: 8px; margin: 12px 0 10px; padding: 9px 11px; color: var(--source-health-color); border: 1px solid color-mix(in srgb, var(--source-health-color) 25%, var(--cpa-border)); border-radius: 7px; background: color-mix(in srgb, var(--source-health-color) 8%, var(--cpa-surface)); }
.source-health strong { color: inherit; font-size: 14px; }
.source-loading-placeholder { min-height: 72px; display: flex; align-items: center; justify-content: center; color: var(--cpa-text-muted); font-size: 13px; }
.timestamp-line { display: flex; flex-wrap: wrap; gap: 5px 18px; margin: 10px 0 14px; color: var(--cpa-text-muted); font-size: 12px; }
.timestamp-line strong { color: var(--cpa-text); font-weight: 500; }
.group-list, .service-list { margin-top: 6px; }
.service-list + .group-list { margin-top: 18px; padding-top: 12px; border-top: 1px solid var(--cpa-border); }
.group-summary { display: grid; width: 100%; min-width: 0; gap: 8px; padding-right: 4px; }
.group-services { display: grid; gap: 8px; padding: 2px 0 8px 30px; }
.service-row { display: grid; gap: 8px; min-width: 0; padding: 10px 0; border-top: 1px solid var(--cpa-border); }
.service-row:first-child { border-top: 0; }
.service-header { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; }
.service-header > :first-child { min-width: 0; overflow-wrap: anywhere; }
.service-stat { color: var(--cpa-text-muted); font-size: 12px; }
.last-error { margin-bottom: 10px; }
.sample-strip { display: grid; grid-auto-flow: column; justify-content: start; width: max-content; max-width: 100%; gap: 2px; min-height: 22px; padding: 4px; overflow-x: auto; border-radius: 7px; background: var(--cpa-bg-soft); }
.sample-strip.is-day { grid-auto-columns: 7px; }
.sample-strip.is-minute { grid-auto-columns: 8px; }
.sample-tooltip-content { display: grid; gap: 4px; }
.sample-bar { display: block; width: 100%; height: 18px; border-radius: 2px; background: var(--cpa-text-muted); }
.sample-bar.is-operational { background: var(--cpa-success); }
.sample-bar.is-degraded_performance, .sample-bar.is-maintenance { background: var(--cpa-warning); }
.sample-bar.is-partial_outage { background: var(--cpa-accent-orange); }
.sample-bar.is-major_outage { background: var(--cpa-danger); }
.sample-bar.is-empty { background: var(--cpa-border-strong); }
.incidents { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--cpa-border); }
.incidents h3 { margin: 0 0 10px; color: var(--cpa-text-strong); font-size: 14px; }
.incident-row { display: flex; justify-content: space-between; gap: 12px; padding: 9px 0; color: inherit; text-decoration: none; border-top: 1px solid var(--cpa-border); }
.incident-row:first-of-type { border-top: 0; }
.incident-row span { display: grid; min-width: 0; gap: 2px; overflow-wrap: anywhere; }
.incident-row small, .incident-row time { color: var(--cpa-text-muted); font-size: 12px; }
@media (max-width: 860px) {
  .monitor-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 620px) {
  .page-header, .source-heading, .aggregate-load-error-content { align-items: stretch; flex-direction: column; }
  .monitor-metrics { grid-template-columns: 1fr 1fr; }
  .source-actions { justify-content: space-between; }
  .group-services { padding-left: 18px; }
  .incident-row { flex-direction: column; }
}
@media (prefers-reduced-motion: reduce) {
  .refresh-all-icon.is-spinning { animation: none; }
}
</style>
