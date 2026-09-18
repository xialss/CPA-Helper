<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { NAlert, NButton, NEmpty, NIcon, NSpin, NTooltip, useMessage } from 'naive-ui'
import { ExternalLink, RefreshCw } from 'lucide-vue-next'

import { getAIRadar, getAIRadarHistory, refreshAIRadar } from '@/features/ai-radar/api/aiRadarApi'
import EfficiencyPkChart from '@/features/ai-radar/components/EfficiencyPkChart.vue'
import IqHistoryChart from '@/features/ai-radar/components/IqHistoryChart.vue'
import IqTierCards from '@/features/ai-radar/components/IqTierCards.vue'
import {
  formatRadarCount,
  formatRadarIQ,
  isRadarModelVisible,
  type RadarHistoryWindow,
} from '@/features/ai-radar/utils/radarMetrics'
import {
  msUntilNextBoundary,
  radarAutoRefreshIntervalMs,
} from '@/features/ai-radar/utils/radarSchedule'
import { useCurrentUser } from '@/features/auth/state/currentUser'
import { isApiRequestError } from '@/shared/api/apiClient'
import { useI18n } from '@/shared/i18n'
import type { AIRadarHistoryResponse, AIRadarResponse } from '@/shared/types/api'
import { formatDateTime } from '@/shared/utils/format'

const message = useMessage()
const { serverText, t } = useI18n()
const { currentUser } = useCurrentUser()

const live = ref<AIRadarResponse | null>(null)
const liveLoading = ref(false)
const liveError = ref<string | null>(null)
const history = ref<AIRadarHistoryResponse | null>(null)
const historyLoading = ref(false)
const historyWindow = ref<RadarHistoryWindow>('30d')
const refreshing = ref(false)

/**
 * Auto-refresh lands on wall-clock five-minute boundaries (:00, :05, :10).
 * Five minutes divides an hour exactly, so an epoch-aligned remainder is also
 * aligned to the local clock in every whole-minute timezone.
 */
const autoRefreshIntervalMs = radarAutoRefreshIntervalMs

let liveGeneration = 0
let historyGeneration = 0
let autoRefreshTimer: number | undefined
let lastSourceRevision: string | null = null
let isMounted = false
let forceRefreshInFlight = false
let autoRefreshQueued = false

const isAdmin = computed(() => currentUser.value?.is_admin === true)

const points = computed(() => (live.value?.points ?? []).filter((point) => isRadarModelVisible(point.model)))

const historySeries = computed(() => (history.value?.series ?? []).filter((series) => isRadarModelVisible(series.model)))

const modelCount = computed(() => new Set(points.value.map((point) => point.model)).size)

const bestPoint = computed(() => {
  let best: AIRadarResponse['points'][number] | null = null
  for (const point of points.value) {
    if (typeof point.iq !== 'number') continue
    if (!best || typeof best.iq !== 'number' || point.iq > best.iq) best = point
  }
  return best
})

function describeError(error: unknown, fallback: string): string {
  if (isApiRequestError(error)) return error.message
  if (error instanceof Error) return error.message
  return fallback
}

/**
 * Sole writer of `live`. Both the plain read and the administrator force
 * refresh go through here so one monotonic generation owns the snapshot and a
 * slower earlier response can never overwrite a newer one.
 */
async function requestLive(force: boolean): Promise<AIRadarResponse | null> {
  const generation = ++liveGeneration
  liveLoading.value = true
  try {
    const response = force ? await refreshAIRadar() : await getAIRadar()
    if (generation !== liveGeneration) return null
    live.value = response
    liveError.value = null
    // An explicit force refresh reloads history unconditionally; a timed read
    // only does so when upstream actually published a new revision.
    syncHistoryWithRevision(response.source_updated_at, !force)
    return response
  } catch (error) {
    if (generation !== liveGeneration) return null
    liveError.value = describeError(error, t('加载 AI 雷达失败', 'Failed to load the AI radar'))
    return null
  } finally {
    // The newest request is always the one that clears the spinner.
    if (generation === liveGeneration) liveLoading.value = false
  }
}

function loadLive() {
  void requestLive(false)
}

/**
 * History gains rows only when upstream publishes a revision (observations are
 * keyed by it), so the series are re-read on that signal rather than on every
 * tick — the `all` window alone is several hundred kilobytes.
 */
function syncHistoryWithRevision(sourceUpdatedAt: string | null, reload: boolean) {
  if (sourceUpdatedAt === null) return
  const changed = lastSourceRevision !== sourceUpdatedAt
  lastSourceRevision = sourceUpdatedAt
  if (reload && changed) void loadHistory()
}

/** Re-arms from the clock each time, so a slept or throttled tab self-corrects. */
function scheduleAutoRefresh() {
  window.clearTimeout(autoRefreshTimer)
  if (!isMounted) return
  autoRefreshTimer = window.setTimeout(() => {
    autoRefreshTimer = undefined
    if (!isMounted) return
    void runAutoRefresh()
  }, msUntilNextBoundary(Date.now(), autoRefreshIntervalMs))
}

async function runAutoRefresh() {
  if (forceRefreshInFlight || refreshing.value) {
    autoRefreshQueued = true
    return
  }
  await requestLive(false)
  if (!isMounted) return
  scheduleAutoRefresh()
}

async function loadHistory() {
  const generation = ++historyGeneration
  historyLoading.value = true
  try {
    const response = await getAIRadarHistory(historyWindow.value)
    if (generation !== historyGeneration) return
    history.value = response
  } catch (error) {
    if (generation !== historyGeneration) return
    history.value = null
    message.error(describeError(error, t('加载历史数据失败', 'Failed to load history')))
  } finally {
    if (generation === historyGeneration) historyLoading.value = false
  }
}

function handleWindowChange(value: RadarHistoryWindow) {
  if (value === historyWindow.value) return
  historyWindow.value = value
  void loadHistory()
}

/**
 * Administrator-only escalation: bypass the shared cache and pull upstream now.
 * History is always re-read, because completing a pending backfill also lands
 * rows without upstream changing its revision.
 */
async function forceRefresh() {
  if (refreshing.value) return
  refreshing.value = true
  forceRefreshInFlight = true
  try {
    const response = await requestLive(true)
    if (!response) return
    if (response.available && !response.stale) {
      message.success(t('已强制刷新上游数据', 'Upstream data refreshed'))
    }
    await loadHistory()
  } finally {
    forceRefreshInFlight = false
    refreshing.value = false
    if (isMounted && autoRefreshQueued) {
      autoRefreshQueued = false
      void runAutoRefresh()
    }
  }
}

onMounted(() => {
  isMounted = true
  void loadLive()
  void loadHistory()
  scheduleAutoRefresh()
})

onBeforeUnmount(() => {
  isMounted = false
  autoRefreshQueued = false
  forceRefreshInFlight = false
  liveGeneration += 1
  historyGeneration += 1
  window.clearTimeout(autoRefreshTimer)
  autoRefreshTimer = undefined
})
</script>

<template>
  <div class="page radar-page">
    <header class="page-header">
      <div>
        <h1 class="page-title">{{ t('AI 雷达', 'AI Radar') }}</h1>
        <p class="page-subtitle">
          {{
            t(
              'codexradar.com 的 deep-swe 基准：各 GPT 模型与推理档位的通过率、综合成本与历史走势。IQ 为基准通过率的 1.5 倍缩放，并非通用智力评分。',
              'The deep-swe benchmark from codexradar.com: pass rate, combined cost and history per GPT model and reasoning tier. IQ is the benchmark pass rate scaled by 1.5, not a general intelligence score.',
            )
          }}
        </p>
      </div>
      <div class="radar-actions">
        <span class="radar-cadence">
          {{ t('每 5 分钟自动更新', 'Auto-updates every 5 minutes') }}
        </span>
        <NTooltip v-if="isAdmin" trigger="hover">
          <template #trigger>
            <NButton
              size="small"
              secondary
              :loading="refreshing"
              :disabled="refreshing"
              @click="forceRefresh()"
            >
              <template #icon>
                <NIcon><RefreshCw /></NIcon>
              </template>
              {{ t('强制刷新', 'Force refresh') }}
            </NButton>
          </template>
          {{
            t(
              '立刻向 codexradar.com 抓取一次，跳过 60 秒共享缓存，并刷新所有用户看到的缓存。仅管理员可用，以免放大对第三方站点的请求量。',
              'Fetch codexradar.com right now, bypassing the 60s shared cache and refreshing it for every user. Administrator only, to avoid amplifying requests to a third-party site.',
            )
          }}
        </NTooltip>
      </div>
    </header>

    <NAlert v-if="live?.stale" type="warning" :bordered="false" :show-icon="true">
      {{
        t(
          '上游暂时不可用，以下为最近一次成功抓取的数据。',
          'The upstream is unavailable; showing the most recent successful fetch.',
        )
      }}
    </NAlert>

    <NAlert v-if="liveError" type="error" :bordered="false" :show-icon="true">
      <div class="radar-error">
        <span>{{ liveError }}</span>
        <NButton size="tiny" secondary :loading="liveLoading" @click="loadLive()">
          {{ t('重试', 'Retry') }}
        </NButton>
      </div>
    </NAlert>

    <div v-if="!live && liveLoading" class="panel">
      <div class="panel-inner radar-loading">
        <NSpin size="large" />
      </div>
    </div>

    <template v-if="live">
      <template v-if="live.available">
        <div class="metric-grid radar-metrics">
          <div class="metric-card">
            <span class="metric-label">{{ t('覆盖模型', 'Models') }}</span>
            <span class="metric-value">{{ formatRadarCount(modelCount) }}</span>
            <span class="metric-footnote">{{ t('仅 GPT 系', 'GPT family only') }}</span>
          </div>
          <div class="metric-card">
            <span class="metric-label">{{ t('观测点', 'Observations') }}</span>
            <span class="metric-value">{{ formatRadarCount(points.length) }}</span>
            <span class="metric-footnote">{{ t('模型 × 推理档位', 'Model × reasoning tier') }}</span>
          </div>
          <div class="metric-card">
            <span class="metric-label">{{ t('最高 IQ', 'Highest IQ') }}</span>
            <span class="metric-value">{{ formatRadarIQ(bestPoint?.iq ?? null) }}</span>
            <span class="metric-footnote">
              {{ bestPoint ? `${bestPoint.model} · ${bestPoint.effort}` : '—' }}
            </span>
          </div>
          <div class="metric-card">
            <span class="metric-label">{{ t('上游更新', 'Upstream update') }}</span>
            <span class="metric-value radar-metric-time">
              {{ formatDateTime(live.source_updated_at) }}
            </span>
            <span class="metric-footnote">{{ t('源站数据时间', 'Source data time') }}</span>
          </div>
        </div>

        <IqTierCards :points="points" :low-sample-runs="live.low_sample_runs" />

        <EfficiencyPkChart :points="points" :loading="liveLoading" />
      </template>
    </template>

    <!-- History is stored locally, so it stays readable while the live feed is down. -->
    <NAlert v-if="history?.message" type="warning" :bordered="false" :show-icon="true">
      {{ serverText(history.message) }}
    </NAlert>

    <IqHistoryChart
      :series="historySeries"
      :window="historyWindow"
      :loading="historyLoading"
      @update:window="handleWindowChange"
    />

    <template v-if="live">
      <template v-if="live.available">
        <div class="panel">
          <div class="panel-inner radar-note">
            <p>
              {{
                t(
                  '效能图按对数轴绘制，X 轴为成本指标。综合成本按源站规则「图中最高值归一为 100」显示；耗时与费用直接使用原值（分钟 / 美元）。越靠左上越高效。',
                  'The efficiency chart uses a logarithmic axis. Combined cost follows the source rule "highest plotted value reads 100"; duration and cost are plotted at their real minutes and dollars. Upper-left is more efficient.',
                )
              }}
            </p>
            <p>
              {{
                t(
                  '综合成本 = 平均费用 × (平均耗时 / 10) ^ 3.05323795 × 100，对应「2.5 倍价格可换 1.35 倍速度」的折算，入库值与源站一致、未归一化。',
                  'Combined cost = average cost × (average duration / 10) ^ 3.05323795 × 100, matching the source rule "2.5x price buys 1.35x speed". Stored values stay unnormalized.',
                )
              }}
            </p>
            <p>
              {{
                t(
                  `历史数据每 ${history?.interval_hours ?? 4} 小时记录一个观察点，并回填了源站公开的历史快照。`,
                  `History records one observation every ${history?.interval_hours ?? 4} hours, backfilled from the source site's published snapshots.`,
                )
              }}
            </p>
            <p class="radar-source">
              <span>{{ t('数据来源', 'Source') }}: {{ live.source_label }}</span>
              <a :href="live.source_url" target="_blank" rel="noopener noreferrer">
                {{ live.source_url }}
                <NIcon size="14"><ExternalLink /></NIcon>
              </a>
            </p>
          </div>
        </div>
      </template>

      <div v-else class="panel">
        <div class="panel-inner radar-unavailable">
          <NEmpty :description="t('数据暂不可用', 'Data is currently unavailable')" />
          <p class="radar-unavailable-detail">
            {{ live.message ? serverText(live.message) : t('请稍后重试。', 'Try again later.') }}
          </p>
          <NButton size="small" secondary :loading="liveLoading" @click="loadLive()">
            {{ t('重试', 'Retry') }}
          </NButton>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.radar-page {
  min-width: 0;
}

/* Four cards, so the shared six-column grid would leave a gap. */
.radar-metrics {
  grid-template-columns: repeat(4, minmax(138px, 1fr));
}

@media (max-width: 1180px) {
  .radar-metrics {
    grid-template-columns: repeat(2, minmax(128px, 1fr));
  }
}

@media (max-width: 720px) {
  .radar-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.radar-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
}

.radar-cadence {
  color: var(--cpa-text-muted);
  font-size: 12px;
  white-space: nowrap;
}

@media (max-width: 720px) {
  .radar-cadence {
    display: none;
  }
}

.radar-error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.radar-loading {
  display: grid;
  min-height: 180px;
  place-items: center;
}

.radar-metric-time {
  font-size: 16px;
}

.radar-note {
  display: grid;
  gap: 8px;
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.radar-note p {
  margin: 0;
  text-wrap: pretty;
}

.radar-source {
  display: flex;
  align-items: center;
  gap: 8px;
}

.radar-source a {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--cpa-primary);
  text-decoration: none;
}

.radar-source a:hover {
  text-decoration: underline;
}

.radar-unavailable {
  display: grid;
  justify-items: center;
  gap: 12px;
  padding: 34px 18px;
  text-align: center;
}

.radar-unavailable-detail {
  margin: 0;
  max-width: 520px;
  color: var(--cpa-text-muted);
  font-size: 13px;
}

@media (max-width: 860px) {
  .page-header {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
