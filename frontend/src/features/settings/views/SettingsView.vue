<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
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
  useDialog,
  useMessage,
} from 'naive-ui'
import { Activity, Database, Power, Server } from 'lucide-vue-next'

import {
  getCollectorStatus,
  getSettings,
  updateSettings,
} from '@/features/settings/api/settingsApi'
import {
  getCPAHelperPluginStatus,
  getCPAHelperResponseModelConfig,
  updateCPAHelperResponseModelConfig,
} from '@/features/plugin/api/cpaHelperPluginApi'
import { useCurrentUser } from '@/features/auth/state/currentUser'
import FeatureSettingsPanel from '@/features/feature-center/components/FeatureSettingsPanel.vue'
import { ApiRequestError } from '@/shared/api/apiClient'
import { useI18n } from '@/shared/i18n'
import type { CPAHelperResponseModelConfig, CollectorStatus, SettingsUpdatePayload } from '@/shared/types/api'
import { formatDateTime, formatInteger } from '@/shared/utils/format'

const message = useMessage()
const dialog = useDialog()
const { errorText, serverText, t } = useI18n()
const { currentUser } = useCurrentUser()
const responseVerificationSupported = ref(false)
const canManageResponseVerification = computed(() => currentUser.value?.is_super_admin === true && responseVerificationSupported.value)
const isLoading = ref(false)
const isSaving = ref(false)
const collectorStatus = ref<CollectorStatus | null>(null)

const settingsForm = reactive({
  cliaproxy_url: 'http://127.0.0.1:8317',
  model_request_url: 'http://127.0.0.1:8317',
  management_key: '',
  collector_enabled: false,
  batch_size: 100,
  poll_interval_seconds: 2,
  retry_interval_seconds: 10,
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
    return t('开启', 'On')
  }
  if (collectorStatus.value?.remote_enabled === false) {
    return t('关闭', 'Off')
  }
  return t('未知', 'Unknown')
})

const collectorEnabledText = computed(() => (collectorStatus.value?.enabled ? t('开启', 'On') : t('关闭', 'Off')))
const collectorRunningText = computed(() => (collectorStatus.value?.running ? t('运行中', 'Running') : t('空闲', 'Idle')))
const isManagementKeySet = ref(false)
const responseModelConfigLoading = ref(false)
const responseModelConfigSaving = ref(false)
const responseModelConfigError = ref<unknown>(null)
const responseModelLoaded = ref(false)
const savedResponseModelSnapshot = ref('')
let settingsLoadGeneration = 0
let responseModelConfigGeneration = 0
let savedCPAURL: string | null = null
const responseConfig = reactive<CPAHelperResponseModelConfig>({
  enabled: false, stream_enabled: true,
  max_buffer_bytes: 2097152, max_total_buffer_bytes: 8388608,
  action: 'reject', unknown_action: 'pass', case_sensitive: false,
  ignore_thinking_suffix: true, ignored_models: [], accepted_models: {},
})
const ignoredModelsText = ref('')
const acceptedModelRows = ref<Array<{ request: string; responses: string }>>([])
const responseModelSnapshot = () => JSON.stringify({
  config: responseConfig,
  ignoredModelsText: ignoredModelsText.value,
  acceptedModelRows: acceptedModelRows.value,
})
const responseModelDirty = computed(() => responseModelLoaded.value && responseModelSnapshot() !== savedResponseModelSnapshot.value)
const responseModelFormDisabled = computed(() => isSaving.value || responseModelConfigLoading.value || responseModelConfigSaving.value || !responseModelLoaded.value || !!responseModelConfigError.value)
const responseModelConfigErrorText = computed(() => {
  if (!responseModelConfigError.value) return ''
  if (responseModelConfigError.value instanceof ApiRequestError && responseModelConfigError.value.code === 'plugin_not_loaded') return t('CPA Helper Plugin 未加载或契约不兼容；系统其他设置仍可使用。', 'CPA Helper Plugin is not loaded or its contract is incompatible; other system settings remain available.')
  if (responseModelConfigError.value instanceof ApiRequestError && responseModelConfigError.value.code === 'plugin_unavailable') return t('CPA Helper Plugin 不可达；系统其他设置仍可使用。', 'CPA Helper Plugin is unreachable; other system settings remain available.')
  return errorText(responseModelConfigError.value, '插件响应核验配置不可用；系统其他设置仍可使用。', 'Plugin response verification is unavailable; other system settings remain usable.')
})

function refresh() {
  if (isLoading.value || isSaving.value || responseModelConfigSaving.value) return
  if (responseModelDirty.value) {
    dialog.warning({ title: t('放弃修改', 'Discard changes'), content: t('尚未保存的响应核验修改将被丢弃。', 'Unsaved response verification changes will be discarded.'), positiveText: t('放弃并刷新', 'Discard and refresh'), negativeText: t('继续编辑', 'Keep editing'), onPositiveClick: () => {
      if (isLoading.value || isSaving.value || responseModelConfigSaving.value) return false
      return loadSettings()
    } })
    return
  }
  return loadSettings()
}

function applyResponseModelConfig(config: CPAHelperResponseModelConfig) {
  for (const key of Object.keys(responseConfig)) {
    if (!Object.prototype.hasOwnProperty.call(config, key)) delete responseConfig[key]
  }
  Object.assign(responseConfig, config)
  ignoredModelsText.value = config.ignored_models.join('\n')
  acceptedModelRows.value = Object.entries(config.accepted_models).map(([request, responses]) => ({ request, responses: responses.join(', ') }))
  responseModelConfigError.value = null
  responseModelLoaded.value = true
  savedResponseModelSnapshot.value = responseModelSnapshot()
}

function invalidateResponseModelConfig() {
  responseModelConfigGeneration++
  responseVerificationSupported.value = false
  responseModelLoaded.value = false
  responseModelConfigLoading.value = false
  responseModelConfigError.value = null
  savedResponseModelSnapshot.value = ''
  ignoredModelsText.value = ''
  acceptedModelRows.value = []
}

async function loadResponseModelConfig() {
  if (currentUser.value?.is_super_admin !== true || responseModelConfigSaving.value) return
  const generation = ++responseModelConfigGeneration
  responseModelConfigLoading.value = true
  try {
    const status = await getCPAHelperPluginStatus()
    if (generation !== responseModelConfigGeneration) return
    responseVerificationSupported.value = status.response_model_verification_supported
    if (!responseVerificationSupported.value) {
      responseModelLoaded.value = false
      responseModelConfigError.value = null
      savedResponseModelSnapshot.value = ''
      return
    }
    const loaded = await getCPAHelperResponseModelConfig()
    if (generation !== responseModelConfigGeneration) return
    applyResponseModelConfig(loaded)
  } catch (error) {
    if (generation === responseModelConfigGeneration) responseModelConfigError.value = error
  } finally {
    if (generation === responseModelConfigGeneration) responseModelConfigLoading.value = false
  }
}

async function loadSettings(includeResponseConfig = true) {
  const generation = ++settingsLoadGeneration
  isLoading.value = true
  try {
    const [settings, status] = await Promise.all([
      getSettings(),
      getCollectorStatus(),
      includeResponseConfig ? loadResponseModelConfig() : Promise.resolve(),
    ])
    if (generation !== settingsLoadGeneration) return
    savedCPAURL = settings.cliaproxy_url
    settingsForm.cliaproxy_url = settings.cliaproxy_url
    settingsForm.model_request_url = settings.model_request_url
    settingsForm.management_key = ''
    isManagementKeySet.value = settings.management_key_set
    settingsForm.collector_enabled = settings.collector_enabled
    settingsForm.batch_size = settings.batch_size
    settingsForm.poll_interval_seconds = settings.poll_interval_seconds
    settingsForm.retry_interval_seconds = settings.retry_interval_seconds
    collectorStatus.value = status
  } catch (error) {
    if (generation === settingsLoadGeneration) message.error(errorText(error, '加载设置失败', 'Failed to load settings'))
  } finally {
    if (generation === settingsLoadGeneration) isLoading.value = false
  }
}

async function saveResponseModelConfig() {
  if (isSaving.value || !canManageResponseVerification.value || !responseModelLoaded.value || responseModelConfigSaving.value) return
  const acceptedModels: Record<string, string[]> = Object.create(null)
  for (const row of acceptedModelRows.value) {
    const request = row.request.trim()
    const responses = row.responses.split(',').map((value) => value.trim()).filter(Boolean)
    if (!request || responses.length === 0) {
      message.error(t('允许响应模型映射不能为空', 'Accepted model mappings cannot be empty'))
      return
    }
    if (Object.prototype.hasOwnProperty.call(acceptedModels, request)) {
      message.error(t('请求模型映射不能重复', 'Requested model mappings must be unique'))
      return
    }
    acceptedModels[request] = [...new Set(responses)]
  }
  const config: CPAHelperResponseModelConfig = {
    ...responseConfig,
    ignored_models: ignoredModelsText.value.split('\n').map((value) => value.trim()).filter(Boolean),
    accepted_models: acceptedModels,
  }
  if (config.max_buffer_bytes < 1048576 || config.max_buffer_bytes > 268435456 || config.max_total_buffer_bytes < config.max_buffer_bytes || config.max_total_buffer_bytes > 1073741824) {
    message.error(t('暂存大小范围无效：单请求 1–256 MiB，总量不能小于单请求且不超过 1 GiB。', 'Invalid buffer sizes: request 1–256 MiB; total must be at least request and at most 1 GiB.'))
    return
  }
  responseModelConfigSaving.value = true
  const generation = ++responseModelConfigGeneration
  responseModelConfigLoading.value = false
  try {
    const saved = await updateCPAHelperResponseModelConfig(config)
    if (generation !== responseModelConfigGeneration) return
    applyResponseModelConfig(saved)
    message.success(t('响应核验配置已保存并生效', 'Response verification config saved and active'))
  } catch (error) {
    if (generation === responseModelConfigGeneration) message.error(errorText(error, '保存响应核验配置失败', 'Failed to save response verification config'))
  } finally {
    if (generation === responseModelConfigGeneration) responseModelConfigSaving.value = false
  }
}

async function saveSettings() {
  if (isLoading.value || isSaving.value || responseModelConfigSaving.value) return
  settingsLoadGeneration++
  isSaving.value = true
  try {
    const payload: SettingsUpdatePayload = {
      cliaproxy_url: settingsForm.cliaproxy_url,
      model_request_url: settingsForm.model_request_url,
      collector_enabled: settingsForm.collector_enabled,
      batch_size: settingsForm.batch_size,
      poll_interval_seconds: settingsForm.poll_interval_seconds,
      retry_interval_seconds: settingsForm.retry_interval_seconds,
    }
    const trimmedKey = settingsForm.management_key.trim()
    if (trimmedKey) {
      payload.management_key = trimmedKey
    }
    const saved = await updateSettings(payload)
    const connectionChanged = saved.cliaproxy_url !== savedCPAURL || !!trimmedKey
    savedCPAURL = saved.cliaproxy_url
    if (connectionChanged) invalidateResponseModelConfig()
    settingsForm.management_key = ''
    isManagementKeySet.value = saved.management_key_set
    message.success(t('设置已保存', 'Settings saved'))
    await loadSettings(connectionChanged)
  } catch (error) {
    message.error(errorText(error, '保存设置失败', 'Failed to save settings'))
  } finally {
    isSaving.value = false
  }
}

onMounted(refresh)
onBeforeUnmount(() => {
  settingsLoadGeneration++
  responseModelConfigGeneration++
})
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('系统设置', 'System Settings') }}</h1>
        <p class="page-subtitle">{{ t('集中管理采集配置', 'Manage collection settings in one place') }}</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" :disabled="isSaving || responseModelConfigSaving" @click="refresh">{{ t('刷新', 'Refresh') }}</NButton>
        <NButton type="primary" :loading="isSaving" :disabled="isLoading || responseModelConfigSaving" @click="saveSettings">{{ t('保存设置', 'Save settings') }}</NButton>
      </NSpace>
    </div>

    <div class="metric-grid settings-metrics">
      <div class="metric-card" :class="collectorStatus?.enabled ? 'is-green' : 'is-orange'">
        <div class="metric-icon" aria-hidden="true">
          <Power :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ t('本地采集', 'Local collection') }}</div>
        <div class="metric-value">{{ collectorEnabledText }}</div>
        <div class="metric-footnote">{{ t('系统开关', 'System switch') }}</div>
      </div>
      <div class="metric-card" :class="collectorStatus?.running ? 'is-teal' : 'is-blue'">
        <div class="metric-icon" aria-hidden="true">
          <Activity :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ t('运行状态', 'Run status') }}</div>
        <div class="metric-value">{{ collectorRunningText }}</div>
        <div class="metric-footnote">{{ t('采集进程', 'Collector process') }}</div>
      </div>
      <div class="metric-card" :class="remoteStatusType === 'success' ? 'is-green' : 'is-purple'">
        <div class="metric-icon" aria-hidden="true">
          <Server :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ t('远端开关', 'Remote switch') }}</div>
        <div class="metric-value">{{ remoteStatusText }}</div>
        <div class="metric-footnote">CLIProxyAPI</div>
      </div>
      <div class="metric-card is-blue">
        <div class="metric-icon" aria-hidden="true">
          <Database :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ t('累计写入', 'Records written') }}</div>
        <div class="metric-value">{{ formatInteger(collectorStatus?.records_collected ?? 0) }}</div>
        <div class="metric-footnote">{{ t('本地记录', 'Local records') }}</div>
      </div>
    </div>

    <FeatureSettingsPanel />
    <div class="grid-two">
      <section class="panel">
        <div class="panel-inner">
          <h2 class="section-title">{{ t('采集配置', 'Collection Settings') }}</h2>
          <NForm :model="settingsForm" label-placement="top" :disabled="isLoading || isSaving">
            <div class="form-grid">
              <div class="field-stack">
                <div class="field-label">{{ t('CLIProxyAPI 地址', 'CLIProxyAPI URL') }}</div>
                <NInput v-model:value="settingsForm.cliaproxy_url" />
                <div class="form-help">{{ t('用于采集队列、API Key 同步和管理接口。', 'Used for collection queues, API key sync, and management APIs.') }}</div>
              </div>
              <div class="field-stack">
                <div class="field-label">{{ t('模型请求地址（例如：填写CPA外网地址）', 'Model request URL (for example, CPA public URL)') }}</div>
                <NInput
                  v-model:value="settingsForm.model_request_url"
                  :placeholder="t('例如：http://192.168.26.50:8317', 'Example: http://192.168.26.50:8317')"
                />
                <div class="form-help">{{ t('仅用于 API 密钥页「请求测试」生成 URL 和示例。', 'Only used to generate URLs and examples for request tests on the API keys page.') }}</div>
              </div>
              <NFormItem :label="t('管理密钥', 'Management key')">
                <NInput
                  v-model:value="settingsForm.management_key"
                  type="password"
                  show-password-on="mousedown"
                  :placeholder="isManagementKeySet ? t('已配置（留空保持不变）', 'Configured (leave empty to keep current)') : t('请输入 CLIProxyAPI 管理密钥', 'Enter the CLIProxyAPI management key')"
                />
              </NFormItem>
              <NFormItem :label="t('开启本地采集', 'Enable local collection')">
                <NSwitch v-model:value="settingsForm.collector_enabled" />
              </NFormItem>
              <NFormItem :label="t('批量读取数', 'Batch size')">
                <NInputNumber v-model:value="settingsForm.batch_size" :min="1" :max="1000" />
              </NFormItem>
              <NFormItem :label="t('轮询间隔（秒）', 'Poll interval (seconds)')">
                <NInputNumber v-model:value="settingsForm.poll_interval_seconds" :min="0.2" />
              </NFormItem>
              <NFormItem :label="t('重试间隔（秒）', 'Retry interval (seconds)')">
                <NInputNumber v-model:value="settingsForm.retry_interval_seconds" :min="1" />
              </NFormItem>
            </div>
          </NForm>
        </div>
      </section>

      <section class="panel">
        <div class="panel-inner">
          <h2 class="section-title">{{ t('采集状态', 'Collection Status') }}</h2>
          <NDescriptions label-placement="left" :column="1" size="small" bordered>
            <NDescriptionsItem :label="t('本地采集', 'Local collection')">
              <NTag :type="collectorStatus?.enabled ? 'success' : 'default'" size="small">
                {{ collectorStatus?.enabled ? t('开启', 'On') : t('关闭', 'Off') }}
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('运行状态', 'Run status')">
              <NTag :type="collectorStatus?.running ? 'success' : 'default'" size="small">
                {{ collectorStatus?.running ? t('运行中', 'Running') : t('空闲', 'Idle') }}
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('远端开关', 'Remote switch')">
              <NTag :type="remoteStatusType" size="small">
                {{ remoteStatusText }}
              </NTag>
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('累计写入', 'Records written')">
              {{ formatInteger(collectorStatus?.records_collected ?? 0) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('最后轮询', 'Last poll')">
              {{ formatDateTime(collectorStatus?.last_poll_at ?? null) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('最后成功', 'Last success')">
              {{ formatDateTime(collectorStatus?.last_success_at ?? null) }}
            </NDescriptionsItem>
          </NDescriptions>
          <NAlert
            v-if="collectorStatus?.last_error"
            type="warning"
            :bordered="false"
            class="status-alert"
          >
            {{ serverText(collectorStatus.last_error, '采集异常', 'Collector error') }}
          </NAlert>
        </div>
      </section>

      <NAlert v-if="currentUser?.is_super_admin && responseModelConfigError && !canManageResponseVerification" type="warning" :bordered="false" class="status-alert">
        {{ responseModelConfigErrorText }}
      </NAlert>
      <section v-if="canManageResponseVerification" class="panel response-verification-panel">
        <div class="panel-inner">
          <div class="section-heading-row">
            <div>
              <h2 class="section-title">{{ t('响应核验', 'Response verification') }}</h2>
              <p class="form-help">{{ t('仅超级管理员可修改，保存前会由插件校验并确认热更新。', 'Super admins only. The plugin validates and confirms hot reload before success.') }}</p>
            </div>
            <NButton type="primary" :loading="responseModelConfigSaving" :disabled="isSaving || responseModelConfigLoading || !responseModelLoaded || !!responseModelConfigError" @click="saveResponseModelConfig">
              {{ t('保存核验配置', 'Save verification config') }}
            </NButton>
          </div>
          <NAlert v-if="responseModelConfigError" type="warning" :bordered="false" class="status-alert">
            {{ responseModelConfigErrorText }}
          </NAlert>
          <NForm class="response-verification-form" :model="responseConfig" label-placement="top" :show-feedback="false" :disabled="responseModelFormDisabled">
            <div class="response-verification-grid">
              <NFormItem :show-feedback="false" :label="t('启用核验', 'Enable verification')"><NSwitch v-model:value="responseConfig.enabled" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('核验流式响应', 'Verify streaming responses')"><NSwitch v-model:value="responseConfig.stream_enabled" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('拦截动作', 'Mismatch action')"><NSelect v-model:value="responseConfig.action" :options="[{ label: t('拒绝', 'Reject'), value: 'reject' }, { label: t('仅审计', 'Audit only'), value: 'audit' }]" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('无法核验时', 'Unknown response action')"><NSelect v-model:value="responseConfig.unknown_action" :options="[{ label: t('放行', 'Pass'), value: 'pass' }, { label: t('拒绝', 'Reject'), value: 'reject' }]" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('单请求暂存上限（字节）', 'Per-request buffer limit (bytes)')"><NInputNumber v-model:value="responseConfig.max_buffer_bytes" :min="1048576" :max="268435456" :precision="0" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('并发暂存总上限（字节）', 'Total buffer limit (bytes)')"><NInputNumber v-model:value="responseConfig.max_total_buffer_bytes" :min="1048576" :max="1073741824" :precision="0" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('区分大小写', 'Case sensitive')"><NSwitch v-model:value="responseConfig.case_sensitive" /></NFormItem>
              <NFormItem :show-feedback="false" :label="t('忽略思考后缀', 'Ignore thinking suffix')"><NSwitch v-model:value="responseConfig.ignore_thinking_suffix" /></NFormItem>
            </div>
            <NFormItem :show-feedback="false" :label="t('跳过核验的模型（每行一个）', 'Ignored models (one per line)')"><NInput v-model:value="ignoredModelsText" type="textarea" :rows="4" /></NFormItem>
            <NFormItem :show-feedback="false" :label="t('允许响应模型映射', 'Accepted response model mappings')">
              <div class="mapping-list">
                <div v-for="(row, index) in acceptedModelRows" :key="index" class="mapping-row">
                  <NInput v-model:value="row.request" :placeholder="t('请求模型', 'Requested model')" />
                  <NInput v-model:value="row.responses" :placeholder="t('允许响应模型，逗号分隔', 'Allowed response models, comma separated')" />
                  <NButton quaternary type="error" :disabled="responseModelFormDisabled" @click="acceptedModelRows.splice(index, 1)">{{ t('删除', 'Remove') }}</NButton>
                </div>
                <NButton dashed :disabled="responseModelFormDisabled" @click="acceptedModelRows.push({ request: '', responses: '' })">{{ t('添加映射', 'Add mapping') }}</NButton>
              </div>
            </NFormItem>
          </NForm>
        </div>
      </section>
    </div>
  </section>
</template>

<style scoped>
.section-title {
  margin: 0 0 8px;
}

.settings-metrics {
  grid-template-columns: repeat(4, minmax(150px, 1fr));
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

.response-verification-panel .section-heading-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.response-verification-panel .section-heading-row > .n-button {
  flex-shrink: 0;
}

.response-verification-form {
  display: grid;
  gap: 12px;
  margin-top: 12px;
}

.response-verification-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  align-items: start;
  gap: 12px 16px;
}

.response-verification-form :deep(.n-form-item-label) {
  padding-bottom: 6px;
}

.response-verification-form :deep(.n-form-item-blank) {
  min-height: 34px;
}

.field-stack {
  display: grid;
  gap: 6px;
  width: 100%;
  min-width: 0;
  align-content: start;
}

.field-label {
  color: var(--cpa-text);
  font-size: 14px;
  line-height: 1.35;
}

.form-help {
  margin: 0;
  padding-left: 8px;
  border-left: 2px solid color-mix(in srgb, var(--cpa-primary) 42%, transparent);
  color: var(--cpa-text);
  font-size: 13px;
  font-weight: 600;
  line-height: 1.45;
}

.mapping-list {
  display: grid;
  gap: 8px;
  width: 100%;
}

.mapping-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr) auto;
  gap: 8px;
  align-items: center;
}

@media (max-width: 720px) {
  .mapping-row { grid-template-columns: 1fr; }
  .response-verification-grid { grid-template-columns: 1fr; }
  .response-verification-panel .section-heading-row { flex-wrap: wrap; }
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
