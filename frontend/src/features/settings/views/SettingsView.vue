<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDescriptions,
  NDescriptionsItem,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  useMessage,
} from 'naive-ui'
import { Activity, Database, Power, Server } from 'lucide-vue-next'

import {
  getCollectorStatus,
  getSettings,
  updateSettings,
} from '@/features/settings/api/settingsApi'
import { useThemePreference } from '@/shared/composables/useThemePreference'
import type { CollectorStatus, SettingsUpdatePayload, ThemePreference } from '@/shared/types/api'
import { formatDateTime, formatInteger } from '@/shared/utils/format'

const message = useMessage()
const isLoading = ref(false)
const isSaving = ref(false)
const collectorStatus = ref<CollectorStatus | null>(null)
const { preference, setThemePreference } = useThemePreference()

const settingsForm = reactive({
  cliaproxy_url: 'http://127.0.0.1:8317',
  management_key: '',
  collector_enabled: false,
  batch_size: 100,
  poll_interval_seconds: 2,
  retry_interval_seconds: 10,
  theme_preference: 'system' as ThemePreference,
})

const remoteStatusType = computed(() => {
  if (collectorStatus.value?.remote_enabled === true) {
    return 'success'
  }
  if (collectorStatus.value?.remote_enabled === false) {
    return 'error'
  }
  return 'warning'
})

const remoteStatusText = computed(() => {
  if (collectorStatus.value?.remote_enabled === true) {
    return '开启'
  }
  if (collectorStatus.value?.remote_enabled === false) {
    return '关闭'
  }
  return '未知'
})

const collectorEnabledText = computed(() => (collectorStatus.value?.enabled ? '开启' : '关闭'))
const collectorRunningText = computed(() => (collectorStatus.value?.running ? '运行中' : '空闲'))

async function refresh() {
  isLoading.value = true
  try {
    const [settings, status] = await Promise.all([
      getSettings(),
      getCollectorStatus(),
    ])
    settingsForm.cliaproxy_url = settings.cliaproxy_url
    settingsForm.management_key = settings.management_key
    settingsForm.collector_enabled = settings.collector_enabled
    settingsForm.batch_size = settings.batch_size
    settingsForm.poll_interval_seconds = settings.poll_interval_seconds
    settingsForm.retry_interval_seconds = settings.retry_interval_seconds
    settingsForm.theme_preference = preference.value
    collectorStatus.value = status
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载设置失败')
  } finally {
    isLoading.value = false
  }
}

async function saveSettings() {
  isSaving.value = true
  try {
    const payload: SettingsUpdatePayload = {
      cliaproxy_url: settingsForm.cliaproxy_url,
      management_key: settingsForm.management_key,
      collector_enabled: settingsForm.collector_enabled,
      batch_size: settingsForm.batch_size,
      poll_interval_seconds: settingsForm.poll_interval_seconds,
      retry_interval_seconds: settingsForm.retry_interval_seconds,
      theme_preference: settingsForm.theme_preference,
    }
    const saved = await updateSettings(payload)
    settingsForm.management_key = saved.management_key
    setThemePreference(saved.theme_preference)
    message.success('设置已保存')
    await refresh()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '保存设置失败')
  } finally {
    isSaving.value = false
  }
}

watch(
  preference,
  (value) => {
    settingsForm.theme_preference = value
  },
  { immediate: true },
)

onMounted(refresh)
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">系统设置</h1>
        <p class="page-subtitle">集中管理采集和主题</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">刷新</NButton>
        <NButton type="primary" :loading="isSaving" @click="saveSettings">保存设置</NButton>
      </NSpace>
    </div>

    <div class="metric-grid settings-metrics">
      <div class="metric-card" :class="collectorStatus?.enabled ? 'is-green' : 'is-orange'">
        <div class="metric-icon" aria-hidden="true">
          <Power :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">本地采集</div>
        <div class="metric-value">{{ collectorEnabledText }}</div>
        <div class="metric-footnote">系统开关</div>
      </div>
      <div class="metric-card" :class="collectorStatus?.running ? 'is-teal' : 'is-blue'">
        <div class="metric-icon" aria-hidden="true">
          <Activity :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">运行状态</div>
        <div class="metric-value">{{ collectorRunningText }}</div>
        <div class="metric-footnote">采集进程</div>
      </div>
      <div class="metric-card" :class="remoteStatusType === 'success' ? 'is-green' : 'is-purple'">
        <div class="metric-icon" aria-hidden="true">
          <Server :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">远端开关</div>
        <div class="metric-value">{{ remoteStatusText }}</div>
        <div class="metric-footnote">CLIProxyAPI</div>
      </div>
      <div class="metric-card is-blue">
        <div class="metric-icon" aria-hidden="true">
          <Database :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">累计写入</div>
        <div class="metric-value">{{ formatInteger(collectorStatus?.records_collected ?? 0) }}</div>
        <div class="metric-footnote">本地记录</div>
      </div>
    </div>

    <div class="grid-two">
      <section class="panel">
        <div class="panel-inner">
          <h2 class="section-title">采集配置</h2>
          <NForm :model="settingsForm" label-placement="top">
            <div class="form-grid">
              <NFormItem label="CLIProxyAPI / CPAMC 地址">
                <NInput v-model:value="settingsForm.cliaproxy_url" />
              </NFormItem>
              <NFormItem label="管理密钥">
                <NInput
                  v-model:value="settingsForm.management_key"
                  type="password"
                  show-password-on="mousedown"
                  placeholder="请输入 CLIProxyAPI 管理密钥"
                />
              </NFormItem>
              <NFormItem label="开启本地采集">
                <NSwitch v-model:value="settingsForm.collector_enabled" />
              </NFormItem>
              <NFormItem label="批量读取数">
                <NInputNumber v-model:value="settingsForm.batch_size" :min="1" :max="1000" />
              </NFormItem>
              <NFormItem label="轮询间隔（秒）">
                <NInputNumber v-model:value="settingsForm.poll_interval_seconds" :min="0.2" />
              </NFormItem>
              <NFormItem label="重试间隔（秒）">
                <NInputNumber v-model:value="settingsForm.retry_interval_seconds" :min="1" />
              </NFormItem>
              <NFormItem label="主题偏好">
                <NSelect
                  v-model:value="settingsForm.theme_preference"
                  :options="[
                    { label: '跟随系统', value: 'system' },
                    { label: '浅色', value: 'light' },
                    { label: '暗色', value: 'dark' },
                  ]"
                  @update:value="(value: ThemePreference) => setThemePreference(value)"
                />
              </NFormItem>
            </div>
          </NForm>
        </div>
      </section>

      <section class="panel">
        <div class="panel-inner">
          <h2 class="section-title">采集状态</h2>
          <NDescriptions label-placement="left" :column="1" size="small" bordered>
            <NDescriptionsItem label="本地采集">
              <NTag :type="collectorStatus?.enabled ? 'success' : 'default'" size="small">
                {{ collectorStatus?.enabled ? '开启' : '关闭' }}
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem label="运行状态">
              <NTag :type="collectorStatus?.running ? 'success' : 'default'" size="small">
                {{ collectorStatus?.running ? '运行中' : '空闲' }}
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem label="远端开关">
              <NTag :type="remoteStatusType" size="small">
                {{ remoteStatusText }}
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem label="累计写入">
              {{ formatInteger(collectorStatus?.records_collected ?? 0) }}
            </NDescriptionsItem>
            <NDescriptionsItem label="最后轮询">
              {{ formatDateTime(collectorStatus?.last_poll_at ?? null) }}
            </NDescriptionsItem>
            <NDescriptionsItem label="最后成功">
              {{ formatDateTime(collectorStatus?.last_success_at ?? null) }}
            </NDescriptionsItem>
          </NDescriptions>
          <NAlert
            v-if="collectorStatus?.last_error"
            type="warning"
            :bordered="false"
            class="status-alert"
          >
            {{ collectorStatus.last_error }}
          </NAlert>
        </div>
      </section>
    </div>
  </section>
</template>

<style scoped>
.section-title {
  margin: 0 0 12px;
}

.settings-metrics {
  grid-template-columns: repeat(4, minmax(150px, 1fr));
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

.status-alert {
  margin-top: 10px;
}

@media (max-width: 900px) {
  .settings-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .form-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 560px) {
  .settings-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
