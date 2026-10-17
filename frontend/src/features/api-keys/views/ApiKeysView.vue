<script setup lang="ts">
import type { Component } from 'vue'
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  useDialog,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import {
  Activity,
  CircleDollarSign,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Layers3,
  Pencil,
  Power,
  Send,
  Trash2,
} from 'lucide-vue-next'

import {
  createApiKey,
  deleteApiKey,
  getModelRequestGuide,
  listApiKeys,
  testModelRequest,
  updateApiKey,
} from '@/features/api-keys/api/apiKeysApi'
import {
  getCPAHelperPluginKeyPolicy,
  getCPAHelperPluginDirectory,
  getCPAHelperPluginStatus,
  getCPAHelperPluginKeysSummary,
  normalizeCPAHelperPluginKey,
  updateCPAHelperPluginKeyPolicy,
} from '@/features/plugin/api/cpaHelperPluginApi'
import PluginRuleFields from '@/features/plugin/components/PluginRuleFields.vue'
import { ApiRequestError } from '@/shared/api/apiClient'
import { listAvailableModels } from '@/features/models/api/availableModelsApi'
import ModelListNotice from '@/features/models/components/ModelListNotice.vue'
import { getCurrentUserQuota } from '@/features/users/api/usersApi'
import { getUsageOverview } from '@/features/usage/api/usageApi'
import type {
  AvailableModel,
  AvailableModelsResponse,
  ModelRequestEndpoint,
  ModelRequestGuide,
  ModelRequestTestResponse,
  UsageSummary,
  UserApiKeySummary,
  UserQuotaStatus,
  CPAHelperPluginDirectory,
  CPAHelperPluginGroup,
  CPAHelperPluginKey,
  CPAHelperPluginKeySummary,
  CPAHelperPluginStatus,
} from '@/shared/types/api'
import { useI18n } from '@/shared/i18n'
import { copyToClipboard } from '@/shared/utils/clipboard'
import { formatCompact, formatDateTime, formatInteger, formatUsd } from '@/shared/utils/format'

const message = useMessage()
const dialog = useDialog()
const { copiedText, currentLanguage, errorText, serverText, t } = useI18n()
const isLoading = ref(false)
let refreshGeneration = 0
const isSaving = ref(false)
const apiKeys = ref<UserApiKeySummary[]>([])
const usageSummary = ref<UsageSummary | null>(null)
const quotaStatus = ref<UserQuotaStatus | null>(null)
const modelRequestGuide = ref<ModelRequestGuide | null>(null)
const availableModels = ref<AvailableModelsResponse | null>(null)
const editorVisible = ref(false)
const pluginKeyPolicyVisible = ref(false)
const pluginKeyPolicyHash = ref<string | null>(null)
const pluginKeyPolicyRevision = ref(0)
const pluginKeyPolicyGroups = ref<CPAHelperPluginKey['group_ids']>([])
const pluginKeyPolicyGroupOptions = ref<CPAHelperPluginGroup[]>([])
const pluginKeyPolicyDraft = ref<CPAHelperPluginKey | null>(null)
const pluginKeyPolicyError = ref<unknown>(null)
const editedApiKeyDescription = ref('')
const savedApiKeyDescription = ref('')
const apiKeyEditSaveError = ref<string | null>(null)
const pluginKeyToggleHash = ref<string | null>(null)
let pluginKeyPolicyLoadGeneration = 0
const pluginDirectory = ref<CPAHelperPluginDirectory | null>(null)
const pluginStatus = ref<CPAHelperPluginStatus | null>(null)
const pluginStatusError = ref<unknown>(null)
const pluginKeySummaries = ref<CPAHelperPluginKeySummary[] | null>(null)
const pluginKeySummaryError = ref<unknown>(null)
const pluginKeySummaryByHash = computed(() => new Map(
  (pluginKeySummaries.value ?? []).map((summary) => [summary.api_key_hash, summary]),
))
const pluginKeySummaryPlaceholder = computed(() =>
  pluginKeySummaries.value === null && !pluginKeySummaryError.value
    ? t('加载中', 'Loading')
    : t('不可用', 'Unavailable'),
)
let pluginKeySummaryRequest: Promise<void> | null = null
let pluginKeySummaryController: AbortController | null = null
let pluginKeySummaryTimer: ReturnType<typeof setTimeout> | null = null
let pluginKeySummaryRefreshQueued = false
let pluginKeySummaryStopped = false
const pluginKeyPolicySnapshot = ref('')
const pluginKeyPolicyDirty = computed(() => pluginKeyPolicyDraft.value !== null && JSON.stringify({
  ...pluginKeyPolicyDraft.value, group_ids: pluginKeyPolicyGroups.value,
}) !== pluginKeyPolicySnapshot.value)
const apiKeyEditDirty = computed(() => editedApiKeyDescription.value.trim() !== savedApiKeyDescription.value || pluginKeyPolicyDirty.value)
const pluginStatusText = computed(() => {
  if (!pluginStatusError.value) return ''
  if (pluginStatusError.value instanceof ApiRequestError && pluginStatusError.value.code === 'plugin_not_loaded') return t('CPA Helper Plugin 未加载或契约不兼容，原有 API Key 管理仍可使用。', 'CPA Helper Plugin is not loaded or its contract is incompatible; regular API key management remains available.')
  if (pluginStatusError.value instanceof ApiRequestError && pluginStatusError.value.code === 'plugin_unavailable') return t('CPA Helper Plugin 不可达，原有 API Key 管理仍可使用。', 'CPA Helper Plugin is unreachable; regular API key management remains available.')
  return errorText(pluginStatusError.value, 'CPA Helper Plugin 状态读取失败，原有 API Key 管理仍可使用。', 'CPA Helper Plugin status could not be read; regular API key management remains available.')
})
const isPluginPolicyLoading = ref(false)
const isPluginPolicySaving = ref(false)
const requestTestVisible = ref(false)
const requestTestApiKey = ref<UserApiKeySummary | null>(null)
const requestEndpoint = ref<ModelRequestEndpoint>('chat_completions')
const requestTestModel = ref<string | null>(null)
const requestTestMessageDefaults = {
  zh: '请用一句中文回复：连接测试成功。',
  en: 'Reply in one English sentence: connection test succeeded.',
} as const
const requestTestMessage = ref(requestTestMessageDefaults[currentLanguage.value])
const requestTestResult = ref<ModelRequestTestResponse | null>(null)
const requestTestError = ref<string | null>(null)
const isAvailableModelsLoading = ref(false)
let availableModelsRequestGeneration = 0
let availableModelsController: AbortController | null = null
const isRequestTesting = ref(false)
const apiKeyDescription = ref('Default')
const generatedApiKey = ref<string | null>(null)
const generatedApiKeyHash = ref<string | null>(null)
const visibleApiKeyHashes = ref<Set<string>>(new Set())

const requestLoadingText = computed(() => t('加载中', 'Loading'))

interface RequestEndpointOption {
  label: string
  value: ModelRequestEndpoint
  path: string
  urlLabel: string
}

const chatCompletionsEndpointOption = computed<RequestEndpointOption>(() => ({
  label: t('聊天补全', 'Chat Completions'),
  value: 'chat_completions',
  path: '/chat/completions',
  urlLabel: t('聊天补全 URL', 'Chat Completions URL'),
}))

const requestEndpointOptions = computed<RequestEndpointOption[]>(() => [
  chatCompletionsEndpointOption.value,
  {
    label: t('Responses 响应', 'Responses'),
    value: 'responses',
    path: '/responses',
    urlLabel: t('Responses 响应 URL', 'Responses URL'),
  },
  {
    label: t('Claude 消息', 'Claude Messages'),
    value: 'claude_messages',
    path: '/messages',
    urlLabel: t('Claude 消息 URL', 'Claude Messages URL'),
  },
])

interface ApiKeyMetricCard {
  key: string
  label: string
  value: string
  footnote: string
  tone: 'teal' | 'blue' | 'purple' | 'green'
  icon: Component
}

const apiKeyMetrics = computed<ApiKeyMetricCard[]>(() => {
  const summary = usageSummary.value
  const todayRequests = summary?.total_records ?? 0
  const failedToday = summary?.failed_records ?? 0
  const todayCost = summary?.estimated_cost_usd ?? 0
  const todayTokens = summary?.total_tokens ?? 0
  const quota = quotaStatus.value
  return [
    {
      key: 'keys',
      label: t('API 密钥', 'API keys'),
      value: formatInteger(apiKeys.value.length),
      footnote: t('当前账号', 'Current account'),
      tone: 'teal',
      icon: KeyRound,
    },
    {
      key: 'requests',
      label: t('今日请求', 'Requests today'),
      value: formatInteger(todayRequests),
      footnote: t(`失败 ${formatInteger(failedToday)}`, `${formatInteger(failedToday)} failed`),
      tone: 'blue',
      icon: Activity,
    },
    {
      key: 'tokens',
      label: t('今日 Token', 'Tokens today'),
      value: formatCompact(todayTokens),
      footnote: t('当前账号用量', 'Current account usage'),
      tone: 'purple',
      icon: Layers3,
    },
    {
      key: 'cost',
      label: t('今日费用', 'Cost today'),
      value: formatUsd(todayCost),
      footnote: t('按现价估算', 'Estimated at current prices'),
      tone: 'green',
      icon: CircleDollarSign,
    },
    {
      key: 'quota',
      label: t('可用余额', 'Available balance'),
      value: quotaValueText(quota),
      footnote: quotaFootnote(quota),
      tone: quota?.paused ? 'purple' : 'green',
      icon: CircleDollarSign,
    },
  ]
})

const canCreateApiKey = computed(() => quotaStatus.value?.can_create_keys ?? true)

const requestBaseURL = computed(() => modelRequestGuide.value?.openai_base_url ?? requestLoadingText.value)
const requestEndpointMeta = computed(
  () =>
    requestEndpointOptions.value.find((option) => option.value === requestEndpoint.value) ??
    chatCompletionsEndpointOption.value,
)
const requestEndpointURL = computed(() => {
  const baseURL = modelRequestGuide.value?.openai_base_url
  if (!baseURL) {
    return requestLoadingText.value
  }
  return `${baseURL.replace(/\/$/, '')}${requestEndpointMeta.value.path}`
})
const requestEndpointURLLabel = computed(() => requestEndpointMeta.value.urlLabel)
const requestTestApiKeyText = computed(() => requestTestApiKey.value?.api_key || t('<你的 API KEY>', '<your API key>'))
const requestHeaderLines = computed(() => {
  if (requestEndpoint.value === 'claude_messages') {
    return [`x-api-key: ${requestTestApiKeyText.value}`, 'anthropic-version: 2023-06-01']
  }
  return [`Authorization: Bearer ${requestTestApiKeyText.value}`]
})
const requestHeadersText = computed(() => requestHeaderLines.value.join('\n'))
const sampleRequest = computed(() => {
  const targetURL =
    requestEndpointURL.value === requestLoadingText.value
      ? `<${requestEndpointURLLabel.value}>`
      : requestEndpointURL.value
  const model = requestTestModel.value || t('<模型名>', '<model name>')
  const content = requestTestMessage.value.trim() || t('你好', 'Hello')
  const body = requestBodyForEndpoint(requestEndpoint.value, model, content)
  return [
    `curl ${targetURL} \\`,
    ...requestHeaderLines.value.map((header) => `  -H "${header}" \\`),
    '  -H "Content-Type: application/json" \\',
    `  -d ${quoteForCurl(JSON.stringify(body))}`,
  ].join('\n')
})
const requestTestModelOptions = computed(() => {
  const selectedHash = requestTestApiKey.value?.api_key_hash
  const models = availableModels.value?.models ?? []
  const filtered = selectedHash
    ? models.filter((model) => model.sources.some((source) => source.api_key_hash === selectedHash))
    : models
  return filtered.map((model) => ({
    label: modelOptionLabel(model),
    value: model.id,
  }))
})
const requestTestModelError = computed(() => availableModels.value?.errors.find(
  (error) => error.api_key_hash === requestTestApiKey.value?.api_key_hash,
))
const requestTestReplyText = computed(() => {
  const reply = requestTestResult.value?.reply?.trim()
  return reply || t('模型返回成功，但没有可展示文本。', 'The model returned successfully, but there is no displayable text.')
})
const requestTestUsageText = computed(() => {
  const usage = requestTestResult.value?.usage
  if (!usage) {
    return ''
  }
  const input = numberFromUsage(usage.prompt_tokens ?? usage.input_tokens)
  const output = numberFromUsage(usage.completion_tokens ?? usage.output_tokens)
  const total = numberFromUsage(usage.total_tokens)
  const parts: string[] = []
  if (input !== null) {
    parts.push(t(`输入 ${formatInteger(input)}`, `Input ${formatInteger(input)}`))
  }
  if (output !== null) {
    parts.push(t(`输出 ${formatInteger(output)}`, `Output ${formatInteger(output)}`))
  }
  if (total !== null) {
    parts.push(t(`总计 ${formatInteger(total)}`, `Total ${formatInteger(total)}`))
  }
  return parts.join(' / ')
})

watch(requestEndpoint, () => {
  requestTestResult.value = null
  requestTestError.value = null
})

watch(requestTestVisible, (visible) => {
  if (!visible) invalidateAvailableModelsForTest()
})

watch(currentLanguage, (language, previousLanguage) => {
  const previousDefault = requestTestMessageDefaults[previousLanguage]
  if (requestTestMessage.value === previousDefault) {
    requestTestMessage.value = requestTestMessageDefaults[language]
  }
})

function requestBodyForEndpoint(
  endpoint: ModelRequestEndpoint,
  model: string,
  content: string,
): Record<string, unknown> {
  if (endpoint === 'responses') {
    return {
      model,
      input: content,
      stream: false,
    }
  }
  if (endpoint === 'claude_messages') {
    return {
      model,
      max_tokens: 1024,
      messages: [{ role: 'user', content }],
    }
  }
  return {
    model,
    messages: [{ role: 'user', content }],
    stream: false,
  }
}

function quoteForCurl(value: string): string {
  return "'" + value.replace(/'/g, "'\"'\"'") + "'"
}

function quotaValueText(quota: UserQuotaStatus | null): string {
  if (!quota) {
    return t('加载中', 'Loading')
  }
  if (quota.unlimited) {
    return t('每月余额 无限制', 'Monthly balance unlimited')
  }
  return t(
    `每月余额 ${formatUsd(quota.monthly_remaining_usd ?? 0)}`,
    `Monthly balance ${formatUsd(quota.monthly_remaining_usd ?? 0)}`,
  )
}

function quotaFootnote(quota: UserQuotaStatus | null): string {
  if (!quota) {
    return t('额度加载中', 'Quota loading')
  }
  if (quota.unlimited) {
    return t('不限时余额 无限制', 'Lifetime balance unlimited')
  }
  const lifetimeText = t(
    `不限时余额 ${formatUsd(quota.lifetime_remaining_usd ?? 0)}`,
    `Lifetime balance ${formatUsd(quota.lifetime_remaining_usd ?? 0)}`,
  )
  const notes: string[] = []
  if (quota.sync_error) {
    notes.push(t('Key 同步异常', 'Key sync error'))
  }
  if (quota.unpriced_records > 0) {
    notes.push(t(`未定价 ${formatInteger(quota.unpriced_records)} 条`, `${formatInteger(quota.unpriced_records)} unpriced records`))
  }
  if (quota.paused) {
    notes.push(t('Key 已因余额暂停', 'Key paused due to balance'))
  }
  return notes.length > 0 ? `${lifetimeText} · ${notes.join(' · ')}` : lifetimeText
}

function modelOptionLabel(model: AvailableModel): string {
  return model.id
}

function numberFromUsage(value: unknown): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return null
  }
  return value
}

function ensureRequestTestModel() {
  const options = requestTestModelOptions.value
  if (options.length === 0) {
    requestTestModel.value = null
    return
  }
  if (!requestTestModel.value || !options.some((option) => option.value === requestTestModel.value)) {
    const firstOption = options[0]
    requestTestModel.value = firstOption ? firstOption.value : null
  }
}

function displayedApiKey(row: UserApiKeySummary): string {
  if (row.api_key && isApiKeyVisible(row)) {
    return row.api_key
  }
  return maskDisplayedApiKey(row.api_key)
}

function maskDisplayedApiKey(apiKey: string | null | undefined): string {
  if (!apiKey) {
    return t('未知', 'Unknown')
  }
  if (apiKey.length <= 10) return '••••'
  return `${apiKey.slice(0, 6)}...${apiKey.slice(-4)}`
}

function renderMaskedKeyTitle() {
  return t('密钥', 'Key')
}

function isApiKeyVisible(row: UserApiKeySummary): boolean {
  return visibleApiKeyHashes.value.has(row.api_key_hash)
}

function toggleApiKeyVisibility(row: UserApiKeySummary) {
  if (!row.api_key) {
    message.info(t('当前没有完整密钥可显示', 'No full key is available to show'))
    return
  }
  const nextVisible = new Set(visibleApiKeyHashes.value)
  if (nextVisible.has(row.api_key_hash)) {
    nextVisible.delete(row.api_key_hash)
  } else {
    nextVisible.add(row.api_key_hash)
  }
  visibleApiKeyHashes.value = nextVisible
}

async function copyApiKey(row: UserApiKeySummary) {
  try {
    if (!row.api_key) {
      message.info(t('当前没有完整密钥可复制', 'No full key is available to copy'))
      return
    }
    await copyToClipboard(row.api_key)
    message.success(t('API 密钥已复制', 'API key copied'))
  } catch (error) {
    message.error(errorText(error, '复制失败', 'Copy failed'))
  }
}

async function copyGeneratedApiKey() {
  if (!generatedApiKey.value) {
    return
  }
  try {
    await copyToClipboard(generatedApiKey.value)
    message.success(t('API 密钥已复制', 'API key copied'))
  } catch (error) {
    message.error(errorText(error, '复制失败', 'Copy failed'))
  }
}

async function loadModelRequestGuide() {
  try {
    modelRequestGuide.value = await getModelRequestGuide()
  } catch (error) {
    message.error(errorText(error, '加载请求地址失败', 'Failed to load request endpoints'))
  }
}

function invalidateAvailableModelsForTest() {
  availableModelsRequestGeneration++
  availableModelsController?.abort()
  availableModelsController = null
  isAvailableModelsLoading.value = false
  availableModels.value = null
  requestTestModel.value = null
}

async function loadAvailableModelsForTest() {
  const generation = ++availableModelsRequestGeneration
  availableModelsController?.abort()
  const controller = new AbortController()
  availableModelsController = controller
  isAvailableModelsLoading.value = true
  availableModels.value = null
  requestTestModel.value = null
  try {
    const models = await listAvailableModels(controller.signal)
    if (generation !== availableModelsRequestGeneration) return
    availableModels.value = models
    ensureRequestTestModel()
  } catch (error) {
    if (generation !== availableModelsRequestGeneration) return
    message.error(errorText(error, '加载可用模型失败', 'Failed to load available models'))
  } finally {
    if (generation === availableModelsRequestGeneration) {
      isAvailableModelsLoading.value = false
      availableModelsController = null
    }
  }
}

function openRequestTest(row: UserApiKeySummary) {
  requestTestApiKey.value = row
  requestTestModel.value = row.last_model ?? row.models[0] ?? null
  requestTestMessage.value = requestTestMessageDefaults[currentLanguage.value]
  requestTestResult.value = null
  requestTestError.value = null
  requestTestVisible.value = true
  if (!modelRequestGuide.value) {
    void loadModelRequestGuide()
  }
  void loadAvailableModelsForTest()
}

async function copyRequestValue(label: string, value: string) {
  if (!value || value === requestLoadingText.value) {
    return
  }
  try {
    await copyToClipboard(value)
    message.success(copiedText(label))
  } catch (error) {
    message.error(errorText(error, '复制失败', 'Copy failed'))
  }
}

async function runRequestTest() {
  if (isRequestTesting.value) {
    return
  }
  const currentKey = requestTestApiKey.value
  const model = requestTestModel.value?.trim() ?? ''
  if (!currentKey) {
    message.error(t('请选择要测试的 API KEY', 'Select an API key to test'))
    return
  }
  if (!model) {
    message.error(t('请选择测试模型', 'Select a test model'))
    return
  }
  isRequestTesting.value = true
  requestTestResult.value = null
  requestTestError.value = null
  try {
    requestTestResult.value = await testModelRequest({
      api_key_hash: currentKey.api_key_hash,
      endpoint: requestEndpoint.value,
      model,
      message: requestTestMessage.value,
    })
    message.success(t('请求测试完成', 'Request test completed'))
  } catch (error) {
    requestTestError.value = errorText(error, '请求测试失败', 'Request test failed')
  } finally {
    isRequestTesting.value = false
  }
}

function openCreateDialog() {
  if (!canCreateApiKey.value) {
    message.error(t('当前账号额度已用尽，API KEY 已暂停', 'This account has exhausted its quota, so API keys are paused'))
    return
  }
  apiKeyDescription.value = 'Default'
  generatedApiKey.value = null
  generatedApiKeyHash.value = null
  editorVisible.value = true
}

function closeGeneratedApiKey() {
  generatedApiKey.value = null
  generatedApiKeyHash.value = null
}

function refreshPluginKeySummaries(): Promise<void> {
  if (pluginKeySummaryStopped) return Promise.resolve()
  if (pluginKeySummaryTimer !== null) {
    clearTimeout(pluginKeySummaryTimer)
    pluginKeySummaryTimer = null
  }
  if (pluginKeySummaryRequest) {
    pluginKeySummaryRefreshQueued = true
    return pluginKeySummaryRequest
  }
  const request = async () => {
    do {
      pluginKeySummaryRefreshQueued = false
      pluginKeySummaryController = new AbortController()
      try {
        const summary = await getCPAHelperPluginKeysSummary(pluginKeySummaryController.signal)
        if (!pluginKeySummaryStopped && !pluginKeySummaryRefreshQueued) {
          pluginKeySummaries.value = summary.keys
          pluginKeySummaryError.value = null
        }
      } catch (error) {
        if (!pluginKeySummaryStopped && !pluginKeySummaryRefreshQueued) {
          pluginKeySummaries.value = null
          pluginKeySummaryError.value = error
        }
      }
    } while (pluginKeySummaryRefreshQueued && !pluginKeySummaryStopped)
  }
  pluginKeySummaryRequest = request().finally(() => {
    pluginKeySummaryRequest = null
    pluginKeySummaryController = null
    if (!pluginKeySummaryStopped) {
      pluginKeySummaryTimer = setTimeout(() => {
        pluginKeySummaryTimer = null
        void refreshPluginKeySummaries()
      }, 5000)
    }
  })
  return pluginKeySummaryRequest
}

async function refresh() {
  const generation = ++refreshGeneration
  void refreshPluginKeySummaries()
  isLoading.value = true
  try {
    const [nextApiKeys, overview, quota, guide] = await Promise.all([
      listApiKeys(),
      getUsageOverview({ scope: 'account' }, { includeOptions: false }),
      getCurrentUserQuota(),
      getModelRequestGuide(),
    ])
    if (generation !== refreshGeneration) return
    apiKeys.value = nextApiKeys
    usageSummary.value = overview.summary
    quotaStatus.value = quota
    modelRequestGuide.value = guide
    if (pluginKeyPolicyHash.value) {
      const current = apiKeys.value.find((item) => item.api_key_hash === pluginKeyPolicyHash.value)
      if (!current) {
        resetApiKeyEditor()
      }
    }
  } catch (error) {
    if (generation === refreshGeneration) message.error(errorText(error, '加载 API 密钥失败', 'Failed to load API keys'))
  } finally {
    if (generation === refreshGeneration) isLoading.value = false
  }
  if (generation !== refreshGeneration) return
  try {
    const status = await getCPAHelperPluginStatus()
    if (generation !== refreshGeneration) return
    pluginStatus.value = status
    pluginStatusError.value = null
  } catch (error) {
    if (generation === refreshGeneration) {
      pluginStatus.value = null
      pluginStatusError.value = error
    }
  }
}

async function editApiKey(row: UserApiKeySummary) {
  if (isPluginPolicySaving.value || pluginKeyToggleHash.value) return
  const generation = ++pluginKeyPolicyLoadGeneration
  pluginKeyPolicyHash.value = row.api_key_hash
  editedApiKeyDescription.value = row.description || ''
  savedApiKeyDescription.value = editedApiKeyDescription.value.trim()
  pluginKeyPolicyDraft.value = null
  pluginKeyPolicyGroups.value = []
  pluginKeyPolicyGroupOptions.value = []
  pluginKeyPolicySnapshot.value = ''
  pluginDirectory.value = null
  pluginKeyPolicyError.value = null
  apiKeyEditSaveError.value = null
  pluginKeyPolicyVisible.value = true
  isPluginPolicyLoading.value = true
  try {
    const [policy, choices] = await Promise.all([
      getCPAHelperPluginKeyPolicy(row.api_key_hash),
      getCPAHelperPluginDirectory(row.api_key_hash),
    ])
    if (generation !== pluginKeyPolicyLoadGeneration) return
    pluginKeyPolicyRevision.value = policy.policy_revision
    const normalizedKey = normalizeCPAHelperPluginKey(policy.key)
    pluginKeyPolicyGroups.value = [...normalizedKey.group_ids]
    pluginKeyPolicyGroupOptions.value = policy.groups
    pluginKeyPolicyDraft.value = JSON.parse(JSON.stringify(normalizedKey)) as CPAHelperPluginKey
    pluginDirectory.value = choices
    pluginKeyPolicySnapshot.value = JSON.stringify(pluginKeyPolicyDraft.value)
  } catch (error) {
    if (generation === pluginKeyPolicyLoadGeneration) pluginKeyPolicyError.value = error
  } finally {
    if (generation === pluginKeyPolicyLoadGeneration) isPluginPolicyLoading.value = false
  }
}

async function saveEditedApiKey() {
  if (isPluginPolicySaving.value || isPluginPolicyLoading.value || pluginKeyToggleHash.value || !pluginKeyPolicyHash.value || !apiKeyEditDirty.value) return
  const description = editedApiKeyDescription.value.trim()
  if (!description) {
    message.error(t('API KEY 描述不能为空', 'API key description is required'))
    return
  }
  const descriptionDirty = description !== savedApiKeyDescription.value
  const key = pluginKeyPolicyDraft.value && pluginKeyPolicyDirty.value
    ? JSON.parse(JSON.stringify({ ...pluginKeyPolicyDraft.value, group_ids: pluginKeyPolicyGroups.value })) as CPAHelperPluginKey
    : null
  if (key && (!Number.isSafeInteger(key.max_concurrency) || key.max_concurrency < 0)) {
    message.error(t('并发上限必须为非负整数', 'Concurrency limit must be a non-negative integer'))
    return
  }
  isPluginPolicySaving.value = true
  apiKeyEditSaveError.value = null
  let descriptionSaved = false
  try {
    if (descriptionDirty) {
      const saved = await updateApiKey(pluginKeyPolicyHash.value, { description })
      savedApiKeyDescription.value = saved.description
      editedApiKeyDescription.value = saved.description
      apiKeys.value = apiKeys.value.map((row) => row.api_key_hash === saved.api_key_hash ? saved : row)
      descriptionSaved = true
    }
    if (key) {
      const saved = await updateCPAHelperPluginKeyPolicy(pluginKeyPolicyHash.value, pluginKeyPolicyRevision.value, key)
      pluginKeyPolicyRevision.value = saved.policy_revision
      const normalizedKey = normalizeCPAHelperPluginKey(saved.key)
      pluginKeyPolicyDraft.value = JSON.parse(JSON.stringify(normalizedKey)) as CPAHelperPluginKey
      pluginKeyPolicyGroups.value = [...normalizedKey.group_ids]
      pluginKeyPolicySnapshot.value = JSON.stringify(pluginKeyPolicyDraft.value)
    }
    message.success(t('API Key 已更新', 'API key updated'))
    resetApiKeyEditor()
    invalidateAvailableModelsForTest()
    await refreshPluginKeySummaries()
  } catch (error) {
    const detail = errorText(error, '保存 API Key 失败', 'Failed to save API key')
    apiKeyEditSaveError.value = descriptionSaved
      ? t(`描述已保存，插件策略未保存：${detail}。未保存的修改已保留。`, `Description saved; plugin policy was not saved: ${detail}. Unsaved changes have been kept.`)
      : detail
    message.error(apiKeyEditSaveError.value)
    if (descriptionSaved) {
      invalidateAvailableModelsForTest()
      await refreshPluginKeySummaries()
    }
  } finally {
    isPluginPolicySaving.value = false
  }
}

function resetApiKeyEditor() {
  pluginKeyPolicyLoadGeneration++
  isPluginPolicyLoading.value = false
  pluginKeyPolicyVisible.value = false
  pluginKeyPolicyHash.value = null
}

function closePluginKeyPolicy() {
  if (isPluginPolicySaving.value) return
  if (!apiKeyEditDirty.value) {
    resetApiKeyEditor()
    return
  }
  dialog.warning({
    title: t('放弃修改', 'Discard changes'),
    content: t('尚未保存的 API Key 修改将被丢弃。', 'Unsaved API key changes will be discarded.'),
    positiveText: t('放弃', 'Discard'), negativeText: t('继续编辑', 'Keep editing'),
    onPositiveClick: resetApiKeyEditor,
  })
}

async function toggleApiKeyEnabled(row: UserApiKeySummary) {
  const summary = pluginKeySummaryByHash.value.get(row.api_key_hash)
  if (!summary || pluginKeyToggleHash.value || isPluginPolicySaving.value || pluginKeyPolicyVisible.value) return
  const enabled = !summary.enabled
  pluginKeyToggleHash.value = row.api_key_hash
  try {
    const policy = await getCPAHelperPluginKeyPolicy(row.api_key_hash)
    await updateCPAHelperPluginKeyPolicy(row.api_key_hash, policy.policy_revision, { ...policy.key, enabled })
    invalidateAvailableModelsForTest()
    await refreshPluginKeySummaries()
    message.success(enabled ? t('API Key 已启用', 'API key enabled') : t('API Key 已禁用', 'API key disabled'))
  } catch (error) {
    message.error(errorText(error, '修改 API Key 启用状态失败', 'Failed to change API key enabled state'))
  } finally {
    pluginKeyToggleHash.value = null
  }
}

async function saveApiKey() {
  if (isSaving.value) {
    return
  }
  const description = apiKeyDescription.value.trim()
  if (!description) {
    message.error(t('API KEY 描述不能为空', 'API key description is required'))
    return
  }
  isSaving.value = true
  try {
    if (!canCreateApiKey.value) {
      message.error(t('当前账号额度已用尽，API KEY 已暂停', 'This account has exhausted its quota, so API keys are paused'))
      return
    }
    const created = await createApiKey({ description })
    generatedApiKey.value = created.api_key ?? null
    generatedApiKeyHash.value = created.api_key_hash
    message.success(t('API 密钥已创建并同步到 CPA', 'API key created and synced to CPA'))
    editorVisible.value = false
    await refresh()
  } catch (error) {
    message.error(errorText(error, '保存 API 密钥失败', 'Failed to save API key'))
  } finally {
    isSaving.value = false
  }
}

function confirmDelete(row: UserApiKeySummary) {
  dialog.warning({
    title: t('删除 API 密钥', 'Delete API key'),
    content: t(
      `将删除 ${row.description || '未命名'} 对应的密钥，并从 CPA 中移除。`,
      `This deletes the key for ${row.description || 'Unnamed'} and removes it from CPA.`,
    ),
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      await deleteApiKey(row.api_key_hash)
      message.success(t('API 密钥已删除', 'API key deleted'))
      if (pluginKeyPolicyHash.value === row.api_key_hash) resetApiKeyEditor()
      if (generatedApiKeyHash.value === row.api_key_hash) {
        generatedApiKey.value = null
        generatedApiKeyHash.value = null
      }
      await refresh()
    },
  })
}

const columns = computed<DataTableColumns<UserApiKeySummary>>(() => [
  {
    title: t('Key 描述', 'Key description'),
    key: 'description',
    width: 180,
    ellipsis: { tooltip: true },
    render: (row) => row.description || '—',
  },
  {
    title: renderMaskedKeyTitle,
    key: 'api_key',
    width: 240,
    render: (row) =>
      h(
        'div',
        { class: 'api-key-cell' },
        [
          h(
            'button',
            {
              class: 'api-key-visibility-button',
              disabled: !row.api_key,
              title: isApiKeyVisible(row) ? t('隐藏完整密钥', 'Hide full key') : t('显示完整密钥', 'Show full key'),
              'aria-label': isApiKeyVisible(row) ? t('隐藏完整密钥', 'Hide full key') : t('显示完整密钥', 'Show full key'),
              type: 'button',
              onClick: () => toggleApiKeyVisibility(row),
            },
            [
              h(NIcon, {
                class: 'api-key-mask-icon',
                component: isApiKeyVisible(row) ? Eye : EyeOff,
              }),
            ],
          ),
          h(
            'button',
            {
              class: 'api-key-copy-button',
              type: 'button',
              title: row.api_key
                ? `${isApiKeyVisible(row) ? `${row.api_key}\n` : ''}${t('点击复制完整密钥', 'Click to copy full key')}`
                : t('无完整密钥可复制', 'No full key available to copy'),
              'aria-label': t('复制完整密钥', 'Copy full key'),
              onClick: () => copyApiKey(row),
            },
            h('span', { class: 'api-key-mask-text' }, displayedApiKey(row)),
          ),
        ],
      ),
  },
  {
    title: t('路由规则', 'Route rules'),
    key: 'group_names',
    width: 200,
    render: (row) => {
      const summary = pluginKeySummaryByHash.value.get(row.api_key_hash)
      if (!summary) return h('span', { class: 'api-key-unavailable' }, pluginKeySummaryPlaceholder.value)
      const names = summary.group_names
      return h('div', { class: 'api-key-rule-tags' }, [
        h(NTag, {
          size: 'small', bordered: false, type: summary.enabled ? 'success' : 'error',
          class: 'api-key-policy-state',
          title: summary.enabled ? t('此 Key 已启用', 'This key is enabled') : t('插件拒绝此 Key 的所有请求', 'The plugin rejects all requests from this key'),
        }, { default: () => summary.enabled ? t('已启用', 'Enabled') : t('已禁用', 'Disabled') }),
        ...(names.length === 0 ? [h('span', { class: 'api-key-unavailable' }, t('未关联', 'None'))] : []),
        ...names.slice(0, 2).map((name, index) => h(NTag, {
          key: `${index}:${name}`, size: 'small', bordered: false, class: 'api-key-rule-tag', title: name,
        }, { default: () => h('span', { class: 'api-key-rule-name' }, name) })),
        ...(names.length > 2 ? [h('span', {
          class: 'api-key-rule-more', title: names.slice(2).join('\n'), tabindex: 0,
          'aria-label': t(`另外 ${names.length - 2} 条规则：${names.slice(2).join('、')}`, `${names.length - 2} more rules: ${names.slice(2).join(', ')}`),
        }, `+${names.length - 2}`)] : []),
      ])
    },
  },
  {
    title: t('当前并发', 'Concurrency'),
    key: 'current_concurrency',
    width: 120,
    render: (row) => {
      const summary = pluginKeySummaryByHash.value.get(row.api_key_hash)
      if (!summary) return h('span', { class: 'api-key-unavailable' }, pluginKeySummaryPlaceholder.value)
      const limit = summary.max_concurrency === 0 ? t('不限', 'Unlimited') : formatInteger(summary.max_concurrency)
      return h('span', { class: 'api-key-concurrency' }, `${formatInteger(summary.current_concurrency)} / ${limit}`)
    },
  },
  {
    title: t('创建时间', 'Created at'),
    key: 'created_at',
    width: 170,
    render: (row) => formatDateTime(row.created_at),
  },
  {
    title: '',
    key: 'actions',
    width: 144,
    fixed: 'right',
    render: (row) =>
      h('div', { class: 'api-key-actions' }, [
          h(
            NButton,
            {
              size: 'small', quaternary: true, circle: true,
              title: t('请求测试', 'Request test'), 'aria-label': t('请求测试', 'Request test'),
              onClick: () => openRequestTest(row),
            },
            {
              icon: () => h(NIcon, { component: Send }),
            },
          ),
          h(
            NButton,
            {
              size: 'small', quaternary: true, circle: true,
              disabled: isPluginPolicySaving.value || !!pluginKeyToggleHash.value,
              title: t('编辑 API Key', 'Edit API key'), 'aria-label': t('编辑 API Key', 'Edit API key'),
              onClick: () => editApiKey(row),
            },
            { icon: () => h(NIcon, { component: Pencil }) },
          ),
          h(
            NButton,
            {
              size: 'small', quaternary: true, circle: true,
              disabled: !pluginKeySummaryByHash.value.has(row.api_key_hash) || !!pluginKeyToggleHash.value || isPluginPolicySaving.value || pluginKeyPolicyVisible.value,
              loading: pluginKeyToggleHash.value === row.api_key_hash,
              type: pluginKeySummaryByHash.value.get(row.api_key_hash)?.enabled ? 'warning' : 'success',
              title: pluginKeySummaryByHash.value.has(row.api_key_hash)
                ? pluginKeySummaryByHash.value.get(row.api_key_hash)?.enabled ? t('禁用 API Key', 'Disable API key') : t('启用 API Key', 'Enable API key')
                : t('启用状态暂不可用，请刷新后重试', 'Enabled state is unavailable; refresh and try again'),
              'aria-label': pluginKeySummaryByHash.value.has(row.api_key_hash)
                ? pluginKeySummaryByHash.value.get(row.api_key_hash)?.enabled ? t('禁用 API Key', 'Disable API key') : t('启用 API Key', 'Enable API key')
                : t('启用状态暂不可用，请刷新后重试', 'Enabled state is unavailable; refresh and try again'),
              onClick: () => toggleApiKeyEnabled(row),
            },
            { icon: () => h(NIcon, { component: Power }) },
          ),
          h(
            NButton,
            {
              size: 'small', quaternary: true, circle: true, type: 'error',
              disabled: isPluginPolicySaving.value || !!pluginKeyToggleHash.value,
              title: t('删除', 'Delete'), 'aria-label': t('删除', 'Delete'),
              onClick: () => confirmDelete(row),
            },
            { icon: () => h(NIcon, { component: Trash2 }) },
          ),
      ]),
  },
])

onMounted(refresh)
onBeforeUnmount(() => {
  refreshGeneration++
  pluginKeyPolicyLoadGeneration++
  invalidateAvailableModelsForTest()
  pluginKeySummaryStopped = true
  if (pluginKeySummaryTimer !== null) clearTimeout(pluginKeySummaryTimer)
  pluginKeySummaryController?.abort()
})
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('API 密钥', 'API keys') }}</h1>
        <p class="page-subtitle">{{ t('仅管理当前账号自己的密钥', 'Manage only keys for the current account') }}</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">{{ t('刷新', 'Refresh') }}</NButton>
        <NButton type="primary" :disabled="!canCreateApiKey" @click="openCreateDialog">
          {{ t('新建 API 密钥', 'New API key') }}
        </NButton>
      </NSpace>
    </div>

    <div class="metric-grid api-key-metrics">
      <div v-for="metric in apiKeyMetrics" :key="metric.key" class="metric-card" :class="`is-${metric.tone}`">
        <div class="metric-icon" aria-hidden="true">
          <component :is="metric.icon" :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ metric.label }}</div>
        <div class="metric-value">{{ metric.value }}</div>
        <div class="metric-footnote">{{ metric.footnote }}</div>
      </div>
    </div>

    <section class="panel api-key-panel-shell">
      <div class="panel-inner api-key-panel">
        <NAlert v-if="pluginStatusError" type="warning" :bordered="false" class="plugin-status-alert">
          {{ pluginStatusText }}
        </NAlert>
        <NAlert v-else-if="pluginKeySummaryError" type="warning" :bordered="false" class="plugin-status-alert">
          {{ errorText(pluginKeySummaryError, '路由规则与实时并发暂不可用', 'Route rules and live concurrency are unavailable') }}
        </NAlert>
        <NAlert v-else-if="pluginStatus" type="success" :bordered="false" class="plugin-status-alert">
          {{ t('CPA Helper Plugin 已连接，可编辑自己 API Key 的策略。', 'CPA Helper Plugin is connected; you can edit policies for your own API keys.') }}
        </NAlert>
        <NAlert type="warning" :bordered="false" :title="t('请求链路说明', 'Request path note')">
          {{ t('Agent 发起的模型请求仍需 Agent 直接发送到 CPA，CPA-Helper 不代理或中转这些请求；仅调用 CPA 的 usage 队列、API KEY 创建与删除、凭证管理等接口，用于用量查看、密钥创建和凭证维护。API 密钥拥有当前账号的完整权限，请妥善保管。', 'Model requests from agents must still be sent directly to CPA by the agent. CPA-Helper does not proxy or relay these requests. It only calls CPA usage queue, API key create/delete, and credential management APIs for usage views, key creation, and credential maintenance. API keys have full permissions for the current account; store them carefully.') }}
        </NAlert>

        <NAlert v-if="quotaStatus?.paused" type="error" :bordered="false" :title="t('额度已用尽', 'Quota exhausted')">
          {{ t('当前账号 API KEY 已从 CPA 暂停。补充额度或进入新月份恢复月额度后，系统会自动恢复可用 Key。', 'API keys for this account are paused in CPA. After quota is added or monthly quota resets, available keys are restored automatically.') }}
        </NAlert>
        <NAlert v-else-if="quotaStatus?.unpriced_records" type="warning" :bordered="false">
          {{ t(`当前账号存在 ${formatInteger(quotaStatus.unpriced_records)} 条未定价用量，未计入额度扣减。`, `This account has ${formatInteger(quotaStatus.unpriced_records)} unpriced usage records that are not deducted from quota.`) }}
        </NAlert>

        <div v-if="generatedApiKey" class="generated-key-box">
          <div class="generated-key-main">
            <div class="generated-key-title">{{ t('新创建的密钥', 'Newly created key') }}</div>
            <div class="generated-key-value">{{ generatedApiKey }}</div>
          </div>
          <NSpace>
            <NButton secondary @click="copyGeneratedApiKey">{{ t('复制', 'Copy') }}</NButton>
            <NButton tertiary @click="closeGeneratedApiKey">{{ t('关闭', 'Close') }}</NButton>
          </NSpace>
        </div>

        <NDataTable
          class="api-key-table"
          size="small"
          :loading="isLoading"
          :columns="columns"
          :data="apiKeys"
          :row-key="(row: UserApiKeySummary) => row.api_key_hash"
          :pagination="{ pageSize: 12 }"
          table-layout="fixed"
          :scroll-x="1054"
        />
      </div>
    </section>

    <NModal
      v-model:show="editorVisible"
      preset="card"
      :mask-closable="false"
      :closable="false"
      :title="t('新建 API 密钥', 'New API key')"
      :style="{ width: 'min(520px, calc(100vw - 32px))' }"
    >
      <NForm label-placement="top">
        <NFormItem :label="t('API KEY 描述', 'API key description')">
          <NInput
            v-model:value="apiKeyDescription"
            :disabled="isSaving"
            :placeholder="t('例如：VSCode', 'Example: VSCode')"
            @keyup.enter="saveApiKey"
          />
        </NFormItem>
        <div class="modal-actions">
          <NButton secondary :disabled="isSaving" @click="editorVisible = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton
            type="primary"
            :loading="isSaving"
            :disabled="isSaving || !canCreateApiKey"
            @click="saveApiKey"
          >
            {{ t('创建', 'Create') }}
          </NButton>
        </div>
      </NForm>
    </NModal>

    <NModal
      v-model:show="pluginKeyPolicyVisible"
      preset="card"
      :mask-closable="false"
      :close-on-esc="false"
      :closable="false"
      :title="t('编辑 API Key', 'Edit API key')"
      :style="{ width: 'min(900px, calc(100vw - 32px))' }"
      @esc="closePluginKeyPolicy"
    >
      <NForm label-placement="top" :show-feedback="false" class="api-key-edit-description">
        <NFormItem :label="t('API KEY 描述', 'API key description')">
          <NInput v-model:value="editedApiKeyDescription" :disabled="isPluginPolicySaving" :placeholder="t('例如：VSCode', 'Example: VSCode')" />
        </NFormItem>
      </NForm>
      <NAlert v-if="apiKeyEditSaveError" type="error" :bordered="false" class="api-key-edit-alert">{{ apiKeyEditSaveError }}</NAlert>
      <NAlert v-if="isPluginPolicyLoading" type="info" :bordered="false" class="api-key-edit-alert">{{ t('正在加载 Key 策略', 'Loading key policy') }}</NAlert>
      <NAlert v-else-if="pluginKeyPolicyError" type="warning" :bordered="false" class="api-key-edit-alert">
        {{ errorText(pluginKeyPolicyError, '加载 API Key 插件策略失败', 'Failed to load API key plugin policy') }}
        {{ t('描述仍可编辑和保存。', 'You can still edit and save the description.') }}
      </NAlert>
      <NAlert v-else-if="pluginKeyPolicyDraft" type="info" :bordered="false" class="api-key-edit-alert">
        {{ t('你只能修改自己持有的 API Key。拒绝列表始终优先于允许列表；并发上限为 0 表示不限制。', 'You can only edit API keys owned by your account. Deny lists always take precedence; 0 means unlimited concurrency.') }}
      </NAlert>
      <NForm v-if="pluginKeyPolicyDraft" label-placement="top">
        <div class="form-grid">
          <NFormItem :label="t('允许此 Key 发起请求', 'Allow requests from this key')">
            <div class="plugin-key-enabled-field">
              <NSpace align="center"><NSwitch v-model:value="pluginKeyPolicyDraft.enabled" :disabled="isPluginPolicySaving" /><NTag size="small" :bordered="false" :type="pluginKeyPolicyDraft.enabled ? 'success' : 'error'">{{ pluginKeyPolicyDraft.enabled ? t('已启用', 'Enabled') : t('已禁用', 'Disabled') }}</NTag></NSpace>
              <span class="plugin-key-enabled-hint">{{ t('禁用后，插件拒绝此 Key 的所有请求。', 'When disabled, the plugin rejects all requests from this key.') }}</span>
            </div>
          </NFormItem>
          <NFormItem :label="t('单 Key 并发上限', 'Per-key concurrency limit')"><NInputNumber v-model:value="pluginKeyPolicyDraft.max_concurrency" :min="0" :precision="0" :disabled="isPluginPolicySaving" /></NFormItem>
          <NFormItem :label="t('关联全局规则', 'Linked global rules')"><NSelect v-model:value="pluginKeyPolicyGroups" multiple :options="pluginKeyPolicyGroupOptions.map((group) => ({ label: group.name, value: group.id }))" :disabled="isPluginPolicySaving" /></NFormItem>
        </div>
        <PluginRuleFields :key="pluginKeyPolicyHash ?? ''" v-model="pluginKeyPolicyDraft.rule" :models="pluginDirectory?.models ?? []" :credentials="pluginDirectory?.credentials ?? []" :credential-providers="pluginDirectory?.credential_providers ?? []" :disabled="isPluginPolicySaving" />
      </NForm>
      <div class="modal-actions">
        <NButton secondary :disabled="isPluginPolicySaving" @click="closePluginKeyPolicy">{{ t('取消', 'Cancel') }}</NButton>
        <NButton type="primary" :loading="isPluginPolicySaving" :disabled="isPluginPolicyLoading || !apiKeyEditDirty" @click="saveEditedApiKey">{{ t('保存', 'Save') }}</NButton>
      </div>
    </NModal>

    <NModal
      v-model:show="requestTestVisible"
      preset="card"
      :title="t('请求测试', 'Request test')"
      :style="{ width: 'min(760px, calc(100vw - 32px))' }"
    >
      <div class="request-test">
        <NAlert type="info" :bordered="false">
          {{ t('这里提供当前 API KEY 的请求说明，也可以直接选择模型发起一次真实测试。', 'This shows request instructions for the current API key. You can also choose a model to run a real test request.') }}
        </NAlert>

        <div class="request-endpoint-switch">
          <span class="request-endpoint-label">{{ t('请求格式', 'Request format') }}</span>
          <NRadioGroup v-model:value="requestEndpoint" size="small">
            <NRadioButton
              v-for="option in requestEndpointOptions"
              :key="option.value"
              :value="option.value"
            >
              {{ option.label }}
            </NRadioButton>
          </NRadioGroup>
        </div>

        <div class="request-guide-list">
          <div class="request-guide-row">
            <div>
              <div class="request-guide-label">{{ t('基础 URL', 'Base URL') }}</div>
              <code class="request-guide-value">{{ requestBaseURL }}</code>
            </div>
            <NButton size="small" secondary @click="copyRequestValue(t('基础 URL', 'Base URL'), requestBaseURL)">
              <template #icon>
                <NIcon :component="Copy" />
              </template>
              {{ t('复制', 'Copy') }}
            </NButton>
          </div>
          <div class="request-guide-row">
            <div>
              <div class="request-guide-label">{{ requestEndpointURLLabel }}</div>
              <code class="request-guide-value">{{ requestEndpointURL }}</code>
            </div>
            <NButton size="small" secondary @click="copyRequestValue(t('请求 URL', 'Request URL'), requestEndpointURL)">
              <template #icon>
                <NIcon :component="Copy" />
              </template>
              {{ t('复制', 'Copy') }}
            </NButton>
          </div>
          <div class="request-guide-row">
            <div>
              <div class="request-guide-label">{{ t('请求 Header', 'Request headers') }}</div>
              <code class="request-guide-value request-guide-value-multiline">{{ requestHeadersText }}</code>
            </div>
            <NButton size="small" secondary @click="copyRequestValue(t('请求 Header', 'Request headers'), requestHeadersText)">
              <template #icon>
                <NIcon :component="Copy" />
              </template>
              {{ t('复制', 'Copy') }}
            </NButton>
          </div>
        </div>

        <div class="request-example">
          <div class="request-example-head">
            <span>{{ t('curl 示例', 'curl example') }}</span>
            <NButton size="small" secondary @click="copyRequestValue(t('curl 示例', 'curl example'), sampleRequest)">
              <template #icon>
                <NIcon :component="Copy" />
              </template>
              {{ t('复制示例', 'Copy example') }}
            </NButton>
          </div>
          <pre>{{ sampleRequest }}</pre>
        </div>

        <div class="request-test-section-title">{{ t('请求测试', 'Request test') }}</div>

        <ModelListNotice v-if="availableModels" :status="availableModels.model_list_filter_status" />
        <NAlert v-if="requestTestModelError" type="error" :bordered="false">
          {{ serverText(requestTestModelError.message, '当前 Key 模型查询失败', 'Model query failed for this key') }}
        </NAlert>

        <NForm label-placement="top" class="request-test-form">
          <NFormItem :label="t('测试模型', 'Test model')">
            <NSelect
              v-model:value="requestTestModel"
              filterable
              clearable
              :loading="isAvailableModelsLoading"
              :options="requestTestModelOptions"
              :placeholder="t('选择当前 Key 可用的模型', 'Select a model available to this key')"
            />
          </NFormItem>
          <NFormItem :label="t('测试消息', 'Test message')">
            <NInput
              v-model:value="requestTestMessage"
              type="textarea"
              :autosize="{ minRows: 3, maxRows: 5 }"
              :placeholder="t('输入要发送给模型的测试消息', 'Enter the test message to send to the model')"
            />
          </NFormItem>
        </NForm>

        <NAlert
          v-if="!isAvailableModelsLoading && requestTestModelOptions.length === 0"
          type="warning"
          :bordered="false"
        >
          {{ t('当前 Key 暂未查询到可选模型，可以先刷新模型列表，或到「可用模型」页面检查 Key 是否可用。', 'No selectable models were found for this key. Refresh the model list, or check whether the key is available on the Available models page.') }}
        </NAlert>

        <div class="modal-actions request-test-actions">
          <NButton secondary :loading="isAvailableModelsLoading" @click="loadAvailableModelsForTest">
            {{ t('刷新模型', 'Refresh models') }}
          </NButton>
          <NButton
            type="primary"
            :loading="isRequestTesting"
            :disabled="!requestTestModel || isAvailableModelsLoading"
            @click="runRequestTest"
          >
            <template #icon>
              <NIcon :component="Send" />
            </template>
            {{ t('发送测试', 'Send test') }}
          </NButton>
        </div>

        <NAlert v-if="requestTestError" type="error" :bordered="false">
          {{ requestTestError }}
        </NAlert>

        <div v-if="requestTestResult" class="request-test-result">
          <div class="request-test-result-head">
            <span>{{ t('模型回复', 'Model reply') }}</span>
            <span>
              HTTP {{ requestTestResult.status_code }} · {{ requestTestResult.duration_ms }}ms
              <template v-if="requestTestUsageText"> · {{ requestTestUsageText }}</template>
            </span>
          </div>
          <pre>{{ requestTestReplyText }}</pre>
        </div>
      </div>
    </NModal>
  </section>
</template>

<style scoped>
.api-key-panel {
  display: grid;
  gap: 14px;
  min-width: 0;
}

.api-key-metrics {
  grid-template-columns: repeat(5, minmax(0, 1fr));
}

.api-key-panel-shell,
.api-key-table {
  min-width: 0;
  min-height: 0;
}

.api-key-table :deep(.n-data-table-wrapper) {
  overflow: hidden;
}

.generated-key-box {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background:
    linear-gradient(135deg, rgb(0 154 168 / 10%), rgb(29 141 255 / 7%)),
    var(--cpa-primary-wash);
  box-shadow: var(--cpa-shadow-hairline);
}

.generated-key-main {
  min-width: 0;
}

.generated-key-title {
  margin-bottom: 4px;
  font-weight: 700;
}

.generated-key-value {
  overflow-wrap: anywhere;
  font-family: Consolas, 'SFMono-Regular', 'Microsoft YaHei UI', monospace;
  font-size: 13px;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.request-test {
  display: grid;
  gap: 14px;
}

.request-endpoint-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px 12px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface-muted);
}

.request-endpoint-label {
  color: var(--cpa-text-muted);
  font-size: 12px;
  font-weight: 700;
}

.request-guide-list {
  display: grid;
  overflow: hidden;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface);
}

.request-guide-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  min-width: 0;
  padding: 12px 14px;
  border-bottom: 1px solid var(--cpa-border);
}

.request-guide-row:last-child {
  border-bottom: 0;
}

.request-guide-label {
  margin-bottom: 4px;
  color: var(--cpa-text-muted);
  font-size: 12px;
  font-weight: 700;
}

.request-guide-value {
  display: block;
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--cpa-text);
  font-family: Consolas, 'SFMono-Regular', 'Microsoft YaHei UI', monospace;
  font-size: 13px;
  line-height: 1.45;
}

.request-guide-value-multiline {
  white-space: pre-wrap;
}

.request-example,
.request-test-form {
  display: grid;
}

.request-example {
  overflow: hidden;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface);
}

.request-example-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--cpa-border);
  font-weight: 700;
}

.request-example pre {
  overflow: auto;
  margin: 0;
  padding: 14px;
  background: var(--cpa-surface-muted);
  color: var(--cpa-text);
  font-family: Consolas, 'SFMono-Regular', 'Microsoft YaHei UI', monospace;
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
}

.request-test-section-title {
  color: var(--cpa-text);
  font-size: 14px;
  font-weight: 700;
}

.request-test-form {
  gap: 2px;
}

.request-test-actions {
  align-items: center;
}

.request-test-result {
  overflow: hidden;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface);
}

.request-test-result-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--cpa-border);
  font-weight: 700;
}

.request-test-result-head span:last-child {
  color: var(--cpa-text-muted);
  font-size: 12px;
  font-weight: 500;
}

.request-test-result pre {
  overflow: auto;
  margin: 0;
  padding: 14px;
  background: var(--cpa-surface-muted);
  color: var(--cpa-text);
  font-family: Consolas, 'SFMono-Regular', 'Microsoft YaHei UI', monospace;
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
}

:global(.api-key-cell) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  overflow: hidden;
}

:global(.api-key-visibility-button),
:global(.api-key-copy-button) {
  border: 0;
  background: transparent;
  color: var(--cpa-text);
  font: inherit;
  cursor: pointer;
}

:global(.api-key-visibility-button) {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  padding: 0;
  border-radius: 6px;
}

:global(.api-key-copy-button) {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  padding: 0;
  line-height: 1.35;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.api-key-rule-tags),
:global(.api-key-actions) {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}

:global(.api-key-rule-tags) {
  overflow: hidden;
  flex-wrap: wrap;
}

:global(.api-key-policy-state) { flex: 0 0 auto; }
.api-key-edit-description { margin-bottom: 12px; }
.api-key-edit-alert { margin-bottom: 12px; }
.plugin-key-enabled-field { display: grid; gap: 8px; }
.plugin-key-enabled-hint { color: var(--cpa-text-muted); font-size: 12px; }

:global(.api-key-rule-tag) {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 100%;
}

:global(.api-key-rule-tag .n-tag__content),
:global(.api-key-rule-name) {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.api-key-rule-more) {
  flex: 0 0 auto;
  color: var(--cpa-text-muted);
  font-size: 12px;
}

:global(.api-key-unavailable) {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

:global(.api-key-concurrency) {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

:global(.api-key-actions) {
  justify-content: flex-end;
  gap: 2px;
}

:global(.api-key-mask-icon) {
  flex: 0 0 auto;
  color: var(--cpa-text-muted);
}

:global(.api-key-mask-text) {
  display: block;
  min-width: 0;
  overflow: hidden;
  font-family: Consolas, 'SFMono-Regular', 'Microsoft YaHei UI', monospace;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.api-key-visibility-button:hover),
:global(.api-key-visibility-button:focus-visible),
:global(.api-key-copy-button:hover),
:global(.api-key-copy-button:focus-visible) {
  color: var(--cpa-primary);
}

:global(.api-key-visibility-button:disabled) {
  color: var(--cpa-text-muted);
  cursor: not-allowed;
  opacity: 0.56;
}

:global(.api-key-visibility-button:focus-visible),
:global(.api-key-copy-button:focus-visible) {
  outline: 2px solid color-mix(in srgb, var(--cpa-primary) 32%, transparent);
  outline-offset: 2px;
}

@media (max-width: 900px) {
  .api-key-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .generated-key-box {
    flex-direction: column;
  }

  .request-guide-row {
    grid-template-columns: 1fr;
  }

  .request-example-head,
  .request-test-result-head {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (max-width: 720px) {
  .api-key-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 430px) {
  .api-key-metrics {
    grid-template-columns: 1fr;
  }
}
</style>
