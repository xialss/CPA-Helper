<script setup lang="ts">
import { computed, h, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  NButton,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import {
  Activity,
  AlertTriangle,
  Copy,
  Gauge,
  Network,
  ShieldCheck,
  Trash2,
  Users,
} from 'lucide-vue-next'

import {
  clearCodexKeeperLogs,
  getCodexKeeperSettings,
  getCodexKeeperStatus,
  previewCodexKeeperSchedule,
  runCodexKeeperOnce,
  startCodexKeeper,
  stopCodexKeeper,
  updateCodexKeeperSettings,
} from '@/features/codex-keeper/api/codexKeeperApi'
import type {
  CodexKeeperPriorityRule,
  CodexKeeperSettingsUpdatePayload,
  CodexKeeperStatus,
} from '@/shared/types/api'
import { copyToClipboard } from '@/shared/utils/clipboard'
import { formatDateTime, formatInteger } from '@/shared/utils/format'

type LogTone = 'danger' | 'debug' | 'default' | 'info' | 'warning'

interface ParsedLogLine {
  component: string
  key: string
  level: string
  message: string
  raw: string
  time: string
  tone: LogTone
}

const message = useMessage()
const isLoading = ref(false)
const isSaving = ref(false)
const isActing = ref(false)
const status = ref<CodexKeeperStatus | null>(null)
const priorityRules = ref<CodexKeeperPriorityRule[]>([])
const nextRunTimes = ref<string[]>([])
const schedulePreviewError = ref('')
const logBodyRef = ref<HTMLElement | null>(null)
const shouldFollowLatestLog = ref(true)
let statusTimer: number | undefined
let schedulePreviewTimer: number | undefined

const conditionalRefreshIntervalOptions = [
  { label: '关闭', value: 0 },
  { label: '5 秒', value: 5 },
  { label: '10 秒', value: 10 },
  { label: '30 秒', value: 30 },
  { label: '60 秒', value: 60 },
]

const form = reactive({
  schedule_cron: '*/30 * * * *',
  quota_threshold: 100,
  usage_timeout_seconds: 15,
  cpa_timeout_seconds: 30,
  max_retries: 2,
  worker_threads: 8,
  conditional_refresh_interval_seconds: 30,
  account_refresh_cache_minutes: 10,
  dry_run: true,
  auto_start_daemon: false,
})

const stats = computed(() => status.value?.stats)
const isRunning = computed(() => status.value?.running === true)
const runningModes = computed(() => new Set(status.value?.running_modes ?? []))
const isDaemonRunning = computed(() => status.value?.daemon_running === true)
const isRunOnceBlocked = computed(
  () => runningModes.value.has('once') || runningModes.value.has('daemon'),
)
const statusLogs = computed(() =>
  Array.isArray(status.value?.logs) ? status.value.logs : [],
)
const parsedLogLines = computed(() =>
  statusLogs.value
    .map((line, index) => parseLogLine(line, index))
    .filter((line): line is ParsedLogLine => line !== null),
)
const logText = computed(() => parsedLogLines.value.map((line) => line.raw).join('\n'))
const displayedLogLines = computed(() =>
  [...parsedLogLines.value].reverse(),
)
const displayedPriorityRules = computed(() =>
  [...priorityRules.value].sort((left, right) => {
    const priorityDiff = Number(right.priority) - Number(left.priority)
    if (priorityDiff !== 0) {
      return priorityDiff
    }
    return left.account_type.localeCompare(right.account_type)
  }),
)
const stateType = computed(() => {
  if (isRunning.value || isDaemonRunning.value) {
    return 'success'
  }
  if (status.value?.state === 'error' || status.value?.state === 'failed') {
    return 'error'
  }
  if (status.value?.state === 'stopping') {
    return 'warning'
  }
  return 'default'
})
const statusDetailText = computed(() => {
  const detail = status.value?.detail
  if (isDaemonRunning.value && !isRunning.value) {
    return '自动巡检已开启'
  }
  if (!detail) {
    return '未运行'
  }
  return detail
    .replace(/守护运行中/g, '自动巡检运行中')
    .replace(/守护进程/g, '后台自动巡检')
    .replace(/守护任务/g, '自动巡检任务')
    .replace(/守护已启动/g, '已开始自动巡检')
})
const statusFootnoteText = computed(() => (isDaemonRunning.value ? '等待 Cron 调度' : '后台自动巡检'))

watch(logText, () => {
  if (shouldFollowLatestLog.value) {
    scrollLogToTop()
  }
})

function applySettings(nextSettings: Awaited<ReturnType<typeof getCodexKeeperSettings>>) {
  form.schedule_cron = nextSettings.schedule_cron
  form.quota_threshold = nextSettings.quota_threshold
  form.usage_timeout_seconds = nextSettings.usage_timeout_seconds
  form.cpa_timeout_seconds = nextSettings.cpa_timeout_seconds
  form.max_retries = nextSettings.max_retries
  form.worker_threads = nextSettings.worker_threads
  form.conditional_refresh_interval_seconds = nextSettings.conditional_refresh_interval_seconds
  form.account_refresh_cache_minutes = nextSettings.account_refresh_cache_minutes
  form.dry_run = nextSettings.dry_run
  form.auto_start_daemon = nextSettings.auto_start_daemon
  nextRunTimes.value = nextSettings.next_run_times
  schedulePreviewError.value = ''
  priorityRules.value = nextSettings.priority_rules.map((rule) => ({ ...rule }))
}

async function loadAll() {
  isLoading.value = true
  try {
    const [settings, nextStatus] = await Promise.all([
      getCodexKeeperSettings(),
      getCodexKeeperStatus(),
    ])
    applySettings(settings)
    status.value = nextStatus
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载账号巡检失败')
  } finally {
    isLoading.value = false
  }
}

async function loadStatus() {
  try {
    status.value = await getCodexKeeperStatus()
  } catch {
    return
  }
}

function normalizedRules(): CodexKeeperPriorityRule[] {
  const seen = new Set<string>()
  return priorityRules.value
    .map((rule) => ({
      account_type: rule.account_type.trim().toLowerCase(),
      priority: Number(rule.priority),
    }))
    .filter((rule) => {
      if (!rule.account_type || seen.has(rule.account_type)) {
        return false
      }
      seen.add(rule.account_type)
      return rule.priority >= 0 && rule.priority <= 20
    })
}

async function saveSettings() {
  const rules = normalizedRules()
  if (rules.length !== priorityRules.value.length) {
    message.error('账号类型不可为空或重复，优先级必须在 0 ~ 20')
    return
  }
  isSaving.value = true
  try {
    const payload: CodexKeeperSettingsUpdatePayload = {
      ...form,
      priority_rules: rules,
    }
    const saved = await updateCodexKeeperSettings(payload)
    applySettings(saved)
    message.success('巡检配置已保存')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '保存巡检配置失败')
  } finally {
    isSaving.value = false
  }
}

async function loadSchedulePreview() {
  const scheduleCron = form.schedule_cron.trim()
  if (!scheduleCron) {
    nextRunTimes.value = []
    schedulePreviewError.value = '请填写 Cron 表达式'
    return
  }
  try {
    const preview = await previewCodexKeeperSchedule({ schedule_cron: scheduleCron })
    if (form.schedule_cron.trim() !== scheduleCron) {
      return
    }
    nextRunTimes.value = preview.next_run_times
    schedulePreviewError.value = ''
  } catch (error) {
    if (form.schedule_cron.trim() !== scheduleCron) {
      return
    }
    nextRunTimes.value = []
    schedulePreviewError.value =
      error instanceof Error ? error.message : 'Cron 表达式无效，请使用 5 段格式'
  }
}

function queueSchedulePreview() {
  if (schedulePreviewTimer !== undefined) {
    window.clearTimeout(schedulePreviewTimer)
  }
  schedulePreviewTimer = window.setTimeout(() => {
    void loadSchedulePreview()
  }, 350)
}

async function runAction(action: () => Promise<void>, successText: string) {
  isActing.value = true
  try {
    await action()
    message.success(successText)
    await loadStatus()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '操作失败')
  } finally {
    isActing.value = false
  }
}

function addRule() {
  priorityRules.value.push({ account_type: '', priority: 0 })
}

function removeRule(rule: CodexKeeperPriorityRule) {
  const index = priorityRules.value.indexOf(rule)
  if (index >= 0) {
    priorityRules.value.splice(index, 1)
  }
}

function logTone(level: string): LogTone {
  const normalizedLevel = level.trim().toUpperCase()
  if (normalizedLevel === 'INFO') {
    return 'info'
  }
  if (normalizedLevel === 'WARNING' || normalizedLevel === 'WARN') {
    return 'warning'
  }
  if (normalizedLevel === 'ERROR' || normalizedLevel === 'CRITICAL' || normalizedLevel === 'FATAL') {
    return 'danger'
  }
  if (normalizedLevel === 'DEBUG') {
    return 'debug'
  }
  return 'default'
}

function parseSlogFields(line: string): Record<string, string> | null {
  const fields: Record<string, string> = {}
  let cursor = 0
  while (cursor < line.length) {
    while (line[cursor] === ' ') {
      cursor += 1
    }
    if (cursor >= line.length) {
      break
    }
    const keyStart = cursor
    while (cursor < line.length && line[cursor] !== '=' && line[cursor] !== ' ') {
      cursor += 1
    }
    if (cursor >= line.length || line[cursor] !== '=') {
      return null
    }
    const key = line.slice(keyStart, cursor)
    cursor += 1

    let value = ''
    if (line[cursor] === '"') {
      cursor += 1
      let escaped = false
      while (cursor < line.length) {
        const char = line[cursor]
        cursor += 1
        if (escaped) {
          value += char
          escaped = false
          continue
        }
        if (char === '\\') {
          escaped = true
          continue
        }
        if (char === '"') {
          break
        }
        value += char
      }
    } else {
      const valueStart = cursor
      while (cursor < line.length && line[cursor] !== ' ') {
        cursor += 1
      }
      value = line.slice(valueStart, cursor)
    }
    fields[key] = value
  }
  return fields.time && fields.level && fields.msg ? fields : null
}

function parseLogLine(line: string, index: number): ParsedLogLine | null {
  const fields = parseSlogFields(line)
  if (!fields) {
    return null
  }
  const time = fields.time
  const level = fields.level
  const messageText = fields.msg
  if (!time || !level || !messageText) {
    return null
  }
  const component = fields.component ?? '-'
  const extraFields = Object.entries(fields)
    .filter(([key]) => !['time', 'level', 'component', 'msg'].includes(key))
    .map(([key, value]) => `${key}=${value}`)
  const logMessage = [messageText, ...extraFields].filter(Boolean).join(' ')
  return {
    component,
    key: `${index}-${line}`,
    level,
    message: logMessage,
    raw: line,
    time,
    tone: logTone(level),
  }
}

function isNearLogTop(element: HTMLElement) {
  return element.scrollTop <= 48
}

function handleLogScroll(event: Event) {
  const target = event.currentTarget
  if (target instanceof HTMLElement) {
    shouldFollowLatestLog.value = isNearLogTop(target)
  }
}

function scrollLogToTop() {
  void nextTick(() => {
    const logBody = logBodyRef.value
    if (logBody && shouldFollowLatestLog.value) {
      logBody.scrollTop = 0
    }
  })
}

async function copyLogText() {
  if (!logText.value) {
    message.info('暂无日志可复制')
    return
  }
  try {
    await copyToClipboard(logText.value)
    message.success('维护日志已复制')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '复制失败')
  }
}

const priorityColumns: DataTableColumns<CodexKeeperPriorityRule> = [
  {
    title: '账号类型',
    key: 'account_type',
    minWidth: 132,
    render: (row) =>
      h(NInput, {
        size: 'small',
        value: row.account_type,
        placeholder: '例如 pro_20x',
        onUpdateValue: (value: string) => {
          row.account_type = value
        },
      }),
  },
  {
    title: '优先级',
    key: 'priority',
    width: 112,
    render: (row) =>
      h(NInputNumber, {
        size: 'small',
        value: row.priority,
        min: 0,
        max: 20,
        onUpdateValue: (value: number | null) => {
          row.priority = value ?? 0
        },
      }),
  },
  {
    title: '',
    key: 'actions',
    width: 58,
    render: (row) =>
      h(
        NButton,
        { size: 'tiny', quaternary: true, type: 'error', onClick: () => removeRule(row) },
        { default: () => '移除' },
      ),
  },
]

onMounted(() => {
  void loadAll()
  statusTimer = window.setInterval(() => {
    void loadStatus()
  }, 3000)
})

watch(() => form.schedule_cron, queueSchedulePreview)

onBeforeUnmount(() => {
  if (statusTimer !== undefined) {
    window.clearInterval(statusTimer)
  }
  if (schedulePreviewTimer !== undefined) {
    window.clearTimeout(schedulePreviewTimer)
  }
})
</script>

<template>
  <section class="page inspection-page">
    <div class="page-header">
      <div>
        <h1 class="page-title">巡检设置</h1>
        <p class="page-subtitle">维护 Codex auth file 的健康状态和调度优先级</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="loadAll">重新加载</NButton>
        <NButton type="primary" :loading="isSaving" :disabled="isRunning" @click="saveSettings">
          保存配置
        </NButton>
      </NSpace>
    </div>

    <div class="metric-grid">
      <div class="metric-card inspection-status-card">
        <div class="metric-icon" aria-hidden="true">
          <Activity :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">运行状态</div>
        <div class="metric-value inspection-status-value" :title="statusDetailText">
          <NTag class="inspection-status-tag" :type="stateType" size="small" :bordered="false">
            {{ statusDetailText }}
          </NTag>
        </div>
        <div class="metric-footnote">{{ statusFootnoteText }}</div>
      </div>
      <div class="metric-card">
        <div class="metric-icon" aria-hidden="true">
          <Users :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">账号总数</div>
        <div class="metric-value">{{ formatInteger(stats?.total ?? 0) }}</div>
        <div class="metric-footnote">纳入巡检</div>
      </div>
      <div class="metric-card is-green">
        <div class="metric-icon" aria-hidden="true">
          <ShieldCheck :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">健康账号</div>
        <div class="metric-value">{{ formatInteger(stats?.healthy ?? 0) }}</div>
        <div class="metric-footnote">最近通过</div>
      </div>
      <div class="metric-card is-orange">
        <div class="metric-icon" aria-hidden="true">
          <AlertTriangle :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">坏凭证禁用</div>
        <div class="metric-value">{{ formatInteger(stats?.status_disabled ?? 0) }}</div>
        <div class="metric-footnote">自动处理</div>
      </div>
      <div class="metric-card is-purple">
        <div class="metric-icon" aria-hidden="true">
          <Gauge :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">优先级降级</div>
        <div class="metric-value">{{ formatInteger(stats?.priority_degraded ?? 0) }}</div>
        <div class="metric-footnote">额度保护</div>
      </div>
      <div class="metric-card is-red">
        <div class="metric-icon" aria-hidden="true">
          <Network :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">网络错误</div>
        <div class="metric-value">{{ formatInteger(stats?.network_error ?? 0) }}</div>
        <div class="metric-footnote">本轮异常</div>
      </div>
    </div>

    <div class="grid-two inspection-settings-grid">
      <section class="panel inspection-config-panel">
        <div class="panel-inner config-panel-inner">
          <div class="section-heading">
            <h2 class="section-title">巡检配置</h2>
            <NSpace class="config-actions" size="small">
              <NButton
                size="small"
                secondary
                :loading="isActing"
                :disabled="isRunOnceBlocked"
                @click="runAction(runCodexKeeperOnce, '已开始执行一轮')"
              >
                执行一轮
              </NButton>
              <NButton
                size="small"
                type="primary"
                :loading="isActing"
                :disabled="isDaemonRunning"
                @click="runAction(startCodexKeeper, '已开始自动巡检')"
              >
                开始自动巡检
              </NButton>
              <NButton
                size="small"
                secondary
                type="warning"
                :loading="isActing"
                :disabled="!isDaemonRunning"
                @click="runAction(stopCodexKeeper, '已请求停止')"
              >
                停止
              </NButton>
            </NSpace>
          </div>
          <NForm class="config-form" :model="form" label-placement="top" size="small">
            <div class="config-sections">
              <section class="config-block">
                <h3 class="config-block-title">调度</h3>
                <div class="schedule-grid">
                  <NFormItem label="Cron 表达式">
                    <NInput v-model:value="form.schedule_cron" placeholder="例如 */30 * * * *" />
                  </NFormItem>
                  <div class="schedule-preview">
                    <div class="preview-title">后续 5 次调用</div>
                    <div v-if="schedulePreviewError" class="preview-error">
                      {{ schedulePreviewError }}
                    </div>
                    <div v-else-if="nextRunTimes.length" class="preview-grid">
                      <span v-for="time in nextRunTimes" :key="time" class="preview-time">
                        {{ formatDateTime(time) }}
                      </span>
                    </div>
                    <div v-else class="preview-muted">填写 Cron 表达式后显示</div>
                  </div>
                </div>
                <div class="conditional-refresh-grid">
                  <NFormItem label="按条件扫描间隔">
                    <NSelect
                      v-model:value="form.conditional_refresh_interval_seconds"
                      :options="conditionalRefreshIntervalOptions"
                    />
                  </NFormItem>
                  <NFormItem label="账号刷新缓存（分钟）">
                    <NInputNumber
                      v-model:value="form.account_refresh_cache_minutes"
                      :min="1"
                      :precision="0"
                    />
                  </NFormItem>
                </div>
                <div class="conditional-refresh-help">
                  <p>
                    <strong>按条件扫描间隔：</strong>后台自动巡检开启后，每隔多久检查一次是否有账号需要刷新；会查找缓存时间内有实际请求的账号，以及额度刷新时间已到的账号。
                  </p>
                  <p>
                    <strong>账号刷新缓存：</strong>控制自动任务的防重复时间；同一账号在缓存时间内不会被自动巡检或按条件扫描重复刷新，手动刷新会绕过缓存但会更新缓存时间。
                  </p>
                </div>
              </section>

              <section class="config-block">
                <h3 class="config-block-title">执行参数</h3>
                <div class="params-grid">
                  <NFormItem label="额度阈值（%）">
                    <NInputNumber v-model:value="form.quota_threshold" :min="0" :max="100" />
                  </NFormItem>
                  <NFormItem label="Usage 超时（秒）">
                    <NInputNumber v-model:value="form.usage_timeout_seconds" :min="1" />
                  </NFormItem>
                  <NFormItem label="CPA 超时（秒）">
                    <NInputNumber v-model:value="form.cpa_timeout_seconds" :min="1" />
                  </NFormItem>
                  <NFormItem label="重试次数">
                    <NInputNumber v-model:value="form.max_retries" :min="0" :max="5" />
                  </NFormItem>
                  <NFormItem label="Worker 数">
                    <NInputNumber v-model:value="form.worker_threads" :min="1" :max="64" />
                  </NFormItem>
                </div>
                <div class="switch-row">
                  <NFormItem class="switch-form-item">
                    <div class="switch-setting">
                      <div class="switch-copy">
                        <span class="switch-title">只检查不修改</span>
                        <p class="switch-help">开启后只模拟处理，不会禁用账号或调整优先级。</p>
                      </div>
                      <NSwitch v-model:value="form.dry_run" class="switch-control" />
                    </div>
                  </NFormItem>
                  <NFormItem class="switch-form-item">
                    <div class="switch-setting">
                      <div class="switch-copy">
                        <span class="switch-title">启动后自动巡检</span>
                        <p class="switch-help">每次 CPA-Helper 启动后，自动按上面的计划检查账号。</p>
                      </div>
                      <NSwitch v-model:value="form.auto_start_daemon" class="switch-control" />
                    </div>
                  </NFormItem>
                </div>
              </section>
            </div>
          </NForm>
          <section class="config-block runtime-block">
            <h3 class="config-block-title">运行信息</h3>
            <div class="runtime-info-grid">
              <div class="runtime-stat">
                <span class="runtime-label">CLIProxyAPI</span>
                <strong class="runtime-value">
                  {{ status ? '使用系统设置中的地址和管理密钥' : '等待加载' }}
                </strong>
              </div>
              <div class="runtime-stat">
                <span class="runtime-label">最近开始</span>
                <strong class="runtime-value">
                  {{ formatDateTime(status?.last_started_at ?? null) }}
                </strong>
              </div>
              <div class="runtime-stat">
                <span class="runtime-label">最近完成</span>
                <strong class="runtime-value">
                  {{ formatDateTime(status?.last_finished_at ?? null) }}
                </strong>
              </div>
            </div>
          </section>
        </div>
      </section>

      <section class="panel priority-rules-panel">
        <div class="panel-inner">
          <div class="section-heading">
            <h2 class="section-title">账号类型优先级</h2>
            <NButton size="small" secondary @click="addRule">新增规则</NButton>
          </div>
          <p class="section-hint">
            账号当前优先级超过 20 时视为手动优先，巡检不会覆盖；0 ~ 20 会按这里的账号类型规则维护。
          </p>
          <NDataTable
            class="priority-table"
            size="small"
            :columns="priorityColumns"
            :data="displayedPriorityRules"
            :pagination="false"
            :scroll-x="320"
          />
        </div>
      </section>
    </div>

    <section class="panel log-panel">
      <div class="panel-inner log-panel-inner">
        <div class="section-heading">
          <h2 class="section-title">维护日志</h2>
          <NSpace class="log-actions" size="small">
            <NButton secondary :disabled="!logText" @click="copyLogText">
              <template #icon>
                <NIcon :component="Copy" />
              </template>
              复制日志
            </NButton>
            <NButton secondary :loading="isActing" @click="runAction(clearCodexKeeperLogs, '日志已清空')">
              <template #icon>
                <NIcon :component="Trash2" />
              </template>
              清空日志
            </NButton>
          </NSpace>
        </div>
        <div
          ref="logBodyRef"
          class="log-view"
          role="log"
          aria-label="维护日志"
          @scroll="handleLogScroll"
        >
          <div v-if="displayedLogLines.length === 0" class="log-empty">暂无日志</div>
          <div v-else class="log-lines">
            <div
              v-for="line in displayedLogLines"
              :key="line.key"
              class="log-line"
              :class="`is-${line.tone}`"
              :title="line.raw"
            >
              <time class="log-time">{{ line.time }}</time>
              <span class="log-level">{{ line.level }}</span>
              <span class="log-component">{{ line.component }}</span>
              <span class="log-message">{{ line.message }}</span>
            </div>
          </div>
        </div>
      </div>
    </section>
  </section>
</template>

<style scoped>
.section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.section-title {
  margin: 0;
  color: var(--cpa-text);
  font-size: 15px;
}

.section-hint {
  margin: -2px 0 10px;
  color: var(--cpa-text-muted);
  font-size: 12px;
  line-height: 1.5;
}

.inspection-status-card {
  min-width: 0;
}

.inspection-status-value {
  display: flex;
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
}

.inspection-status-tag {
  max-width: 100%;
  min-width: 0;
}

.inspection-status-value :deep(.n-tag) {
  max-width: 100%;
  min-width: 0;
}

.inspection-status-value :deep(.n-tag__content) {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.inspection-config-panel .panel-inner {
  padding: 12px 14px 14px;
}

.inspection-settings-grid {
  align-items: start;
}

.config-panel-inner {
  display: grid;
  gap: 10px;
}

.config-actions {
  flex-shrink: 0;
}

.config-form {
  min-width: 0;
}

.config-form :deep(.n-form-item) {
  margin: 0;
}

.config-form :deep(.n-form-item-label) {
  padding: 0 0 4px;
  color: var(--cpa-text);
  font-size: 12px;
}

.config-form :deep(.n-form-item-feedback-wrapper) {
  min-height: 0;
}

.config-form :deep(.n-form-item-blank) {
  min-height: 30px;
}

.switch-form-item :deep(.n-form-item-blank) {
  min-height: 0;
}

.config-form :deep(.n-input-number),
.config-form :deep(.n-input) {
  width: 100%;
}

.config-sections {
  display: grid;
  gap: 10px;
}

.config-block {
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--cpa-border);
  border-radius: 6px;
  background: var(--cpa-surface-muted);
}

.config-block-title {
  margin: 0 0 8px;
  color: var(--cpa-text);
  font-size: 12px;
  font-weight: 700;
  line-height: 1.2;
}

.schedule-grid {
  display: grid;
  grid-template-columns: minmax(220px, 0.82fr) minmax(0, 1fr);
  gap: 10px;
  align-items: end;
}

.conditional-refresh-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(180px, 1fr));
  gap: 8px 10px;
  margin-top: 10px;
}

.conditional-refresh-help {
  display: grid;
  gap: 2px;
  margin: 2px 0 0;
  color: var(--cpa-text-muted);
  font-size: 12px;
  line-height: 1.45;
}

.conditional-refresh-help p {
  margin: 0;
}

.params-grid {
  display: grid;
  grid-template-columns: repeat(5, minmax(92px, 1fr));
  gap: 8px 10px;
}

.switch-row {
  display: grid;
  grid-template-columns: repeat(2, minmax(220px, 1fr));
  gap: 10px;
  margin-top: 10px;
}

.switch-form-item {
  min-width: 0;
}

.switch-setting {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  min-height: 72px;
  padding: 10px 12px;
  border: 1px solid var(--cpa-border);
  border-radius: 6px;
  background: var(--cpa-surface);
}

.switch-copy {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.switch-title {
  color: var(--cpa-text);
  font-size: 13px;
  font-weight: 650;
  line-height: 1.25;
}

.switch-control {
  flex: 0 0 auto;
}

.switch-help {
  margin: 0;
  color: var(--cpa-text-muted);
  font-size: 12px;
  line-height: 1.35;
  text-wrap: pretty;
}

.schedule-preview {
  min-width: 0;
  min-height: 54px;
  padding: 7px 10px;
  border: 1px solid var(--cpa-border);
  border-radius: 6px;
  background: var(--cpa-surface);
}

.preview-title {
  margin-bottom: 5px;
  color: var(--cpa-text);
  font-size: 12px;
  font-weight: 600;
}

.preview-grid {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 4px;
}

.preview-time {
  min-width: 0;
  padding: 3px 5px;
  border-radius: 4px;
  background: var(--cpa-surface-muted);
  color: var(--cpa-text);
  font-size: 12px;
  text-align: center;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.preview-muted,
.preview-error {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.preview-error {
  color: var(--cpa-danger);
}

.runtime-info-grid {
  display: grid;
  grid-template-columns: minmax(180px, 1.25fr) repeat(2, minmax(120px, 1fr));
  gap: 8px;
}

.runtime-stat {
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--cpa-border);
  border-radius: 6px;
  background: var(--cpa-surface);
}

.runtime-label {
  display: block;
  margin-bottom: 3px;
  color: var(--cpa-text-muted);
  font-size: 12px;
  line-height: 1.2;
}

.runtime-value {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.priority-table {
  min-width: 0;
}

.priority-rules-panel {
  align-self: start;
}

.priority-rules-panel .panel-inner {
  padding: 16px 20px 18px;
}

.priority-rules-panel .section-heading {
  margin-bottom: 6px;
}

.priority-rules-panel .section-hint {
  margin-bottom: 12px;
}

.priority-table :deep(.n-data-table-wrapper),
.priority-table :deep(.n-data-table-base-table),
.priority-table :deep(.n-data-table-base-table-body) {
  min-width: 0;
}

.priority-table :deep(.n-data-table-th),
.priority-table :deep(.n-data-table-td) {
  padding: 6px 10px;
}

.priority-table :deep(.n-data-table-th) {
  font-size: 13px;
}

.priority-table :deep(.n-input),
.priority-table :deep(.n-input-number) {
  width: 100%;
}

.log-panel,
.log-panel-inner,
.log-view {
  min-height: 0;
}

.log-panel-inner {
  display: grid;
  gap: 10px;
}

.log-panel-inner .section-heading {
  margin-bottom: 0;
}

.log-actions {
  flex-shrink: 0;
}

.log-view {
  height: 520px;
  overflow: auto;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius-sm);
  background:
    linear-gradient(180deg, rgb(255 255 255 / 54%), rgb(255 255 255 / 18%)),
    var(--cpa-surface-muted);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 78%);
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: color-mix(in srgb, var(--cpa-text-muted) 44%, transparent) transparent;
}

.log-view::-webkit-scrollbar {
  width: 14px;
  height: 14px;
}

.log-view::-webkit-scrollbar-track,
.log-view::-webkit-scrollbar-corner {
  background: transparent;
}

.log-view::-webkit-scrollbar-thumb {
  min-height: 48px;
  border: 5px solid transparent;
  border-radius: 999px;
  background: color-mix(in srgb, var(--cpa-text-muted) 44%, transparent);
  background-clip: content-box;
}

.log-view::-webkit-scrollbar-thumb:hover {
  background: color-mix(in srgb, var(--cpa-primary) 58%, var(--cpa-text-muted));
  background-clip: content-box;
}

:root.dark .log-view {
  background:
    linear-gradient(180deg, rgb(255 255 255 / 5%), rgb(255 255 255 / 1%)),
    var(--cpa-surface-muted);
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 8%);
}

.log-lines {
  display: grid;
  min-width: 860px;
  padding: 8px;
}

.log-line {
  display: grid;
  grid-template-columns: 216px 68px minmax(112px, 148px) minmax(0, 1fr);
  gap: 10px;
  align-items: start;
  min-width: 0;
  padding: 7px 9px;
  border-bottom: 1px solid color-mix(in srgb, var(--cpa-border) 74%, transparent);
  color: var(--cpa-text);
  font-family: "Cascadia Mono", "SFMono-Regular", Consolas, monospace;
  font-size: 12px;
  line-height: 1.45;
}

.log-line:last-child {
  border-bottom: 0;
}

.log-line:hover {
  border-radius: var(--cpa-radius-sm);
  background: var(--cpa-primary-wash);
}

:root.dark .log-line:hover {
  background: color-mix(in srgb, var(--cpa-primary-wash) 70%, transparent);
}

.log-time,
.log-component,
.log-message {
  min-width: 0;
}

.log-time,
.log-component {
  overflow: hidden;
  color: var(--cpa-text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.log-level {
  display: inline-flex;
  width: fit-content;
  min-width: 48px;
  justify-content: center;
  padding: 1px 6px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius-sm);
  background: var(--cpa-surface);
  color: var(--cpa-text-muted);
  font-size: 11px;
  font-weight: 760;
  line-height: 1.35;
}

.log-message {
  color: var(--cpa-text);
  overflow-wrap: anywhere;
}

.log-line.is-info .log-level {
  border-color: color-mix(in srgb, var(--cpa-primary) 24%, transparent);
  background: var(--cpa-primary-wash);
  color: var(--cpa-primary);
}

.log-line.is-warning .log-level {
  border-color: color-mix(in srgb, var(--cpa-warning) 28%, transparent);
  background: var(--cpa-warning-weak);
  color: var(--cpa-warning);
}

.log-line.is-danger .log-level {
  border-color: color-mix(in srgb, var(--cpa-danger) 28%, transparent);
  background: var(--cpa-danger-weak);
  color: var(--cpa-danger);
}

.log-line.is-debug .log-level {
  border-color: color-mix(in srgb, var(--cpa-accent-blue) 24%, transparent);
  background: var(--cpa-accent-blue-weak);
  color: var(--cpa-accent-blue);
}

.log-empty {
  display: grid;
  height: 100%;
  min-height: 180px;
  place-items: center;
  color: var(--cpa-text-muted);
  font-size: 13px;
}

@media (min-width: 861px) {
  .inspection-page {
    grid-template-rows: auto auto auto auto;
    min-height: 0;
  }
}

@media (max-width: 760px) {
  .section-heading {
    align-items: flex-start;
    flex-direction: column;
  }

  .schedule-grid,
  .conditional-refresh-grid,
  .runtime-info-grid {
    grid-template-columns: 1fr;
  }

  .params-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .preview-grid {
    grid-template-columns: 1fr;
  }

  .switch-row {
    grid-template-columns: 1fr;
  }

  .log-view {
    height: 420px;
  }

  .log-lines {
    min-width: 0;
  }

  .log-line {
    grid-template-columns: 142px 58px minmax(0, 1fr);
  }

  .log-message {
    grid-column: 1 / -1;
  }
}

@media (max-width: 520px) {
  .params-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
