<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NDataTable,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NSelect,
  NSpace,
  NSwitch,
  NTabPane,
  NTabs,
  NTag,
  useDialog,
  useMessage,
  type DataTableColumns,
  type SelectOption,
} from 'naive-ui'
import {
  Bot,
  CheckCircle2,
  Edit3,
  FlaskConical,
  Plus,
  RefreshCw,
  Search,
  Settings,
  Trash2,
  Wifi,
  XCircle,
} from 'lucide-vue-next'

import {
  createAIProvider,
  deleteAIProvider,
  discoverAIProviderModels,
  listAIProviders,
  testAIProvider,
  updateAIProvider,
} from '@/features/ai-providers/api/aiProvidersApi'
import { useI18n } from '@/shared/i18n'
import type {
  AIProviderActionResponse,
  AIProviderBrand,
  AIProviderHeader,
  AIProviderItem,
  AIProviderModel,
  AIProviderSummary,
} from '@/shared/types/api'
import { formatInteger } from '@/shared/utils/format'

interface BrandConfig {
  brand: AIProviderBrand
  label: string
  keyLabel: string
}

interface ModelDraft {
  name: string
  alias: string
  force_mapping: boolean
  image: boolean
  thinking_text: string
}

interface KeyEntryDraft {
  api_key: string
  api_key_hash: string | null
  api_key_masked: string | null
  proxy_url: string
}

interface CloakDraft {
  mode: string
  strict_mode: boolean
  sensitive_words: string[]
  cache_user_id: boolean
}

interface ProviderDraft {
  brand: AIProviderBrand
  brand_label: string
  index: number
  identity_hash: string
  api_key: string
  api_key_hash: string | null
  api_key_masked: string | null
  auth_index: string | null
  name: string
  priority: number | null
  disabled: boolean
  prefix: string
  base_url: string
  original_base_url: string
  proxy_url: string
  models: ModelDraft[]
  headers: AIProviderHeader[]
  excluded_models: string[]
  disable_cooling: boolean
  websockets: boolean
  rebuild_mid_system_message: boolean
  experimental_cch_signing: boolean
  cloak: CloakDraft
  api_key_entries: KeyEntryDraft[]
  recent_success: number
  recent_failure: number
  recent_status: string
}

const providerBrands: BrandConfig[] = [
  { brand: 'gemini', label: 'Gemini', keyLabel: 'Gemini API key' },
  { brand: 'codex', label: 'Codex', keyLabel: 'Codex API key' },
  { brand: 'claude', label: 'Claude', keyLabel: 'Claude API key' },
  { brand: 'openai_compatibility', label: 'OpenAI-compatible', keyLabel: 'Provider API key' },
  { brand: 'vertex', label: 'Vertex', keyLabel: 'Vertex API key' },
]

const emptySummary: AIProviderSummary = {
  total: 0,
  gemini: 0,
  codex: 0,
  claude: 0,
  openai_compatibility: 0,
  vertex: 0,
  recent_success: 0,
  recent_failure: 0,
}

const router = useRouter()
const dialog = useDialog()
const message = useMessage()
const { errorText, t } = useI18n()

const providers = ref<AIProviderItem[]>([])
const summary = ref<AIProviderSummary>({ ...emptySummary })
const usageError = ref<string | null>(null)
const isLoading = ref(false)
const loadError = ref<string | null>(null)
const activeBrand = ref<AIProviderBrand>('gemini')
const search = ref('')

const drawerOpen = ref(false)
const editorMode = ref<'create' | 'edit'>('create')
const form = ref<ProviderDraft>(defaultDraft('gemini'))
const originalFormText = ref('')
const isSaving = ref(false)
const isDiscovering = ref(false)
const isTesting = ref(false)
const discoveredModels = ref<AIProviderModel[]>([])
const actionResult = ref<AIProviderActionResponse | null>(null)
const testModel = ref('')
const testMessage = ref('请用一句中文回复：连接测试成功。')

const brandOptions = computed<SelectOption[]>(() =>
  providerBrands.map((item) => ({ label: item.label, value: item.brand })),
)
const currentBrandConfig = computed(() => brandConfig(form.value.brand))
const missingSettings = computed(() => isMissingSettingsError(loadError.value))
const tableRows = computed(() => {
  const keyword = search.value.trim().toLowerCase()
  return providers.value.filter((provider) => {
    if (provider.brand !== activeBrand.value) {
      return false
    }
    if (!keyword) {
      return true
    }
    return [
      provider.brand_label,
      provider.name ?? '',
      provider.api_key_masked ?? '',
      provider.base_url ?? '',
      provider.prefix ?? '',
      provider.models.map((model) => model.name).join(' '),
    ]
      .join(' ')
      .toLowerCase()
      .includes(keyword)
  })
})
const drawerTitle = computed(() =>
  editorMode.value === 'edit' ? t('编辑 AI 提供商', 'Edit AI provider') : t('新建 AI 提供商', 'New AI provider'),
)
const formDirty = computed(() => JSON.stringify(form.value) !== originalFormText.value)
const canSave = computed(() => !isSaving.value && (editorMode.value === 'create' || formDirty.value))

function brandConfig(brand: AIProviderBrand): BrandConfig {
  const found = providerBrands.find((item) => item.brand === brand)
  if (found) {
    return found
  }
  return { brand: 'gemini', label: 'Gemini', keyLabel: 'Gemini API key' }
}

function defaultDraft(brand: AIProviderBrand): ProviderDraft {
  const config = brandConfig(brand)
  return {
    brand,
    brand_label: config.label,
    index: -1,
    identity_hash: '',
    api_key: '',
    api_key_hash: null,
    api_key_masked: null,
    auth_index: null,
    name: brand === 'openai_compatibility' ? '' : '',
    priority: null,
    disabled: false,
    prefix: '',
    base_url: '',
    original_base_url: '',
    proxy_url: '',
    models: [],
    headers: [],
    excluded_models: [],
    disable_cooling: false,
    websockets: false,
    rebuild_mid_system_message: false,
    experimental_cch_signing: false,
    cloak: {
      mode: '',
      strict_mode: false,
      sensitive_words: [],
      cache_user_id: false,
    },
    api_key_entries:
      brand === 'openai_compatibility'
        ? [{ api_key: '', api_key_hash: null, api_key_masked: null, proxy_url: '' }]
        : [],
    recent_success: 0,
    recent_failure: 0,
    recent_status: 'unknown',
  }
}

function modelToDraft(model: AIProviderModel): ModelDraft {
  return {
    name: model.name,
    alias: model.alias ?? '',
    force_mapping: model.force_mapping ?? false,
    image: model.image ?? false,
    thinking_text: model.thinking ? JSON.stringify(model.thinking, null, 2) : '',
  }
}

function providerToDraft(provider: AIProviderItem): ProviderDraft {
  const draft = defaultDraft(provider.brand)
  return {
    ...draft,
    brand_label: provider.brand_label,
    index: provider.index,
    identity_hash: provider.identity_hash,
    api_key_hash: provider.api_key_hash ?? null,
    api_key_masked: provider.api_key_masked ?? null,
    auth_index: provider.auth_index ?? null,
    name: provider.name ?? '',
    priority: provider.priority ?? null,
    disabled: provider.disabled ?? false,
    prefix: provider.prefix ?? '',
    base_url: provider.base_url ?? '',
    original_base_url: provider.base_url ?? '',
    proxy_url: provider.proxy_url ?? '',
    models: provider.models.map(modelToDraft),
    headers: provider.headers.map((header) => ({ ...header })),
    excluded_models: [...provider.excluded_models],
    disable_cooling: provider.disable_cooling ?? false,
    websockets: provider.websockets ?? false,
    rebuild_mid_system_message: provider.rebuild_mid_system_message ?? false,
    experimental_cch_signing: provider.experimental_cch_signing ?? false,
    cloak: {
      mode: provider.cloak?.mode ?? '',
      strict_mode: provider.cloak?.strict_mode ?? false,
      sensitive_words: provider.cloak?.sensitive_words ? [...provider.cloak.sensitive_words] : [],
      cache_user_id: provider.cloak?.cache_user_id ?? false,
    },
    api_key_entries:
      provider.brand === 'openai_compatibility'
        ? provider.api_key_entries.map((entry) => ({
            api_key: '',
            api_key_hash: entry.api_key_hash ?? null,
            api_key_masked: entry.api_key_masked ?? null,
            proxy_url: entry.proxy_url ?? '',
          }))
        : [],
    recent_success: provider.recent_success,
    recent_failure: provider.recent_failure,
    recent_status: provider.recent_status,
  }
}

function setSnapshot(next: { providers: AIProviderItem[]; summary: AIProviderSummary; usage_error?: string | null }) {
  providers.value = next.providers
  summary.value = next.summary
  usageError.value = next.usage_error ?? null
  loadError.value = null
}

async function refresh() {
  isLoading.value = true
  loadError.value = null
  try {
    setSnapshot(await listAIProviders())
  } catch (error) {
    providers.value = []
    summary.value = { ...emptySummary }
    loadError.value = errorText(error, '加载 AI 提供商失败', 'Failed to load AI providers')
  } finally {
    isLoading.value = false
  }
}

function openCreateDialog() {
  editorMode.value = 'create'
  form.value = defaultDraft(activeBrand.value)
  originalFormText.value = JSON.stringify(form.value)
  discoveredModels.value = []
  actionResult.value = null
  testModel.value = ''
  drawerOpen.value = true
}

function openEditDialog(provider: AIProviderItem) {
  editorMode.value = 'edit'
  form.value = providerToDraft(provider)
  originalFormText.value = JSON.stringify(form.value)
  discoveredModels.value = []
  actionResult.value = null
  testModel.value = provider.models[0]?.name ?? ''
  drawerOpen.value = true
}

function handleBrandChange(value: string) {
  form.value = defaultDraft(value as AIProviderBrand)
  originalFormText.value = JSON.stringify(form.value)
}

function addModel(name = '') {
  form.value.models.push({ name, alias: '', force_mapping: false, image: false, thinking_text: '' })
}

function removeModel(index: number) {
  form.value.models.splice(index, 1)
}

function addHeader() {
  form.value.headers.push({ name: '', value: '' })
}

function removeHeader(index: number) {
  form.value.headers.splice(index, 1)
}

function addExcludedModel() {
  form.value.excluded_models.push('')
}

function removeExcludedModel(index: number) {
  form.value.excluded_models.splice(index, 1)
}

function setExcludedModel(index: number, value: string) {
  form.value.excluded_models[index] = value
}

function addKeyEntry() {
  form.value.api_key_entries.push({ api_key: '', api_key_hash: null, api_key_masked: null, proxy_url: '' })
}

function removeKeyEntry(index: number) {
  form.value.api_key_entries.splice(index, 1)
}

function addSensitiveWord() {
  form.value.cloak.sensitive_words.push('')
}

function removeSensitiveWord(index: number) {
  form.value.cloak.sensitive_words.splice(index, 1)
}

function setSensitiveWord(index: number, value: string) {
  form.value.cloak.sensitive_words[index] = value
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function parseThinking(text: string): Record<string, unknown> | undefined {
  const trimmed = text.trim()
  if (!trimmed) {
    return undefined
  }
  const parsed: unknown = JSON.parse(trimmed)
  if (!isRecord(parsed)) {
    throw new Error(t('thinking 必须是 JSON object', 'thinking must be a JSON object'))
  }
  return parsed
}

function isBlankKeyEntry(entry: KeyEntryDraft) {
  return !entry.api_key.trim() && !entry.api_key_hash && !entry.api_key_masked && !entry.proxy_url.trim()
}

function draftToPayload(draft: ProviderDraft): AIProviderItem {
  const models = draft.models
    .map((model) => {
      const name = model.name.trim()
      if (!name) {
        return null
      }
      const payload: AIProviderModel = {
        name,
        force_mapping: model.force_mapping,
      }
      const alias = model.alias.trim()
      if (alias) {
        payload.alias = alias
      }
      if (draft.brand === 'openai_compatibility') {
        payload.image = model.image
        const thinking = parseThinking(model.thinking_text)
        if (thinking) {
          payload.thinking = thinking
        }
      }
      return payload
    })
    .filter((model): model is AIProviderModel => model !== null)
  const payload: AIProviderItem = {
    brand: draft.brand,
    brand_label: draft.brand_label,
    index: draft.index,
    identity_hash: draft.identity_hash,
    api_key: draft.api_key.trim(),
    api_key_hash: draft.api_key_hash,
    api_key_masked: draft.api_key_masked,
    auth_index: draft.auth_index,
    name: draft.brand === 'openai_compatibility' ? draft.name.trim() : null,
    priority: draft.priority,
    disabled: draft.brand === 'openai_compatibility' ? draft.disabled : null,
    prefix: draft.prefix.trim(),
    base_url: draft.base_url.trim(),
    original_base_url: editorMode.value === 'edit' ? draft.original_base_url : null,
    proxy_url: draft.proxy_url.trim(),
    models,
    headers: draft.headers
      .map((header) => ({ name: header.name.trim(), value: header.value }))
      .filter((header) => header.name !== ''),
    excluded_models: draft.excluded_models.map((item) => item.trim()).filter((item) => item !== ''),
    disable_cooling: draft.brand === 'vertex' ? null : draft.disable_cooling,
    websockets: draft.brand === 'codex' ? draft.websockets : null,
    rebuild_mid_system_message: draft.brand === 'claude' ? draft.rebuild_mid_system_message : null,
    experimental_cch_signing: draft.brand === 'claude' ? draft.experimental_cch_signing : null,
    cloak:
      draft.brand === 'claude'
        ? {
            mode: draft.cloak.mode.trim() || null,
            strict_mode: draft.cloak.strict_mode,
            sensitive_words: draft.cloak.sensitive_words.map((item) => item.trim()).filter((item) => item !== ''),
            cache_user_id: draft.cloak.cache_user_id,
          }
        : null,
    api_key_entries:
      draft.brand === 'openai_compatibility'
        ? draft.api_key_entries
            .filter((entry) => !isBlankKeyEntry(entry))
            .map((entry) => ({
              api_key: entry.api_key.trim(),
              api_key_hash: entry.api_key_hash,
              api_key_masked: entry.api_key_masked,
              proxy_url: entry.proxy_url.trim(),
            }))
        : [],
    recent_success: draft.recent_success,
    recent_failure: draft.recent_failure,
    recent_status: draft.recent_status,
  }
  if (draft.brand === 'openai_compatibility' && !payload.name) {
    throw new Error(t('Provider 名称不能为空', 'Provider name is required'))
  }
  if (draft.brand !== 'openai_compatibility' && editorMode.value === 'create' && !payload.api_key) {
    throw new Error(t('新建 provider 必须填写 API key', 'A new provider requires an API key'))
  }
  return payload
}

async function saveProvider() {
  if (!canSave.value) {
    return
  }
  isSaving.value = true
  try {
    const payload = draftToPayload(form.value)
    const response =
      editorMode.value === 'edit'
        ? await updateAIProvider(payload)
        : await createAIProvider(payload.brand, payload)
    setSnapshot(response)
    message.success(editorMode.value === 'edit' ? t('AI provider 已保存', 'AI provider saved') : t('AI provider 已创建', 'AI provider created'))
    drawerOpen.value = false
  } catch (error) {
    message.error(errorText(error, '保存 AI provider 失败', 'Failed to save AI provider'))
  } finally {
    isSaving.value = false
  }
}

function confirmDelete(provider: AIProviderItem) {
  const identity = provider.name || provider.api_key_masked || provider.auth_index || provider.identity_hash.slice(0, 12)
  dialog.warning({
    title: t('删除 AI provider', 'Delete AI provider'),
    content: t(
      `将删除 ${provider.brand_label} provider（${identity}）。此操作会写入 CLIProxyAPI 远端配置。`,
      `This deletes the ${provider.brand_label} provider (${identity}) from the remote CLIProxyAPI config.`,
    ),
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      try {
        setSnapshot(await deleteAIProvider(provider))
        message.success(t('AI provider 已删除', 'AI provider deleted'))
      } catch (error) {
        message.error(errorText(error, '删除 AI provider 失败', 'Failed to delete AI provider'))
      }
    },
  })
}

async function toggleProviderDisabled(provider: AIProviderItem) {
  if (provider.brand !== 'openai_compatibility') {
    return
  }
  const draft = providerToDraft(provider)
  draft.disabled = !draft.disabled
  try {
    setSnapshot(await updateAIProvider(draftToPayload(draft)))
    message.success(draft.disabled ? t('Provider 已停用', 'Provider disabled') : t('Provider 已启用', 'Provider enabled'))
  } catch (error) {
    message.error(errorText(error, '更新启用状态失败', 'Failed to update enabled state'))
  }
}

async function runDiscovery() {
  isDiscovering.value = true
  actionResult.value = null
  try {
    const payload = draftToPayload(form.value)
    const result = await discoverAIProviderModels({ brand: payload.brand, provider: payload })
    actionResult.value = result
    if (result.ok) {
      discoveredModels.value = result.models ?? []
      message.success(t('模型发现完成', 'Model discovery completed'))
    } else {
      message.error(result.error || t('模型发现失败', 'Model discovery failed'))
    }
  } catch (error) {
    message.error(errorText(error, '模型发现失败', 'Model discovery failed'))
  } finally {
    isDiscovering.value = false
  }
}

function applyDiscoveredModels() {
  const existing = new Set(form.value.models.map((model) => model.name.trim()).filter((name) => name !== ''))
  for (const model of discoveredModels.value) {
    if (!existing.has(model.name)) {
      addModel(model.name)
      existing.add(model.name)
    }
  }
  message.success(t('发现模型已加入表单', 'Discovered models added to the form'))
}

async function runConnectivityTest() {
  isTesting.value = true
  actionResult.value = null
  try {
    const payload = draftToPayload(form.value)
    const actionPayload = {
      brand: payload.brand,
      provider: payload,
    }
    const model = testModel.value.trim()
    const testText = testMessage.value.trim()
    const result = await testAIProvider({
      ...actionPayload,
      ...(model ? { model } : {}),
      ...(testText ? { message: testText } : {}),
    })
    actionResult.value = result
    if (result.ok) {
      message.success(t('连通性测试成功', 'Connectivity test succeeded'))
    } else {
      message.error(result.error || t('连通性测试失败', 'Connectivity test failed'))
    }
  } catch (error) {
    message.error(errorText(error, '连通性测试失败', 'Connectivity test failed'))
  } finally {
    isTesting.value = false
  }
}

function goSettings() {
  void router.push('/admin/settings')
}

function isMissingSettingsError(message: string | null) {
  const text = message ?? ''
  const lowerText = text.toLowerCase()
  return (
    text.includes('AI 提供商管理需要先到') ||
    (text.includes('系统设置') && text.includes('CLIProxyAPI 地址和管理密钥')) ||
    (lowerText.includes('system settings') && lowerText.includes('cliaproxyapi url and management key'))
  )
}

function statusTagType(provider: AIProviderItem): 'success' | 'warning' | 'default' {
  if (provider.recent_status === 'healthy') {
    return 'success'
  }
  if (provider.recent_status === 'failing') {
    return 'warning'
  }
  return 'default'
}

const columns = computed<DataTableColumns<AIProviderItem>>(() => [
  {
    title: t('标识', 'Identity'),
    key: 'identity',
    width: 260,
    render: (row) =>
      h('div', { class: 'provider-identity' }, [
        h('strong', row.name || row.api_key_masked || row.auth_index || row.identity_hash.slice(0, 12)),
        h('span', row.base_url || '-'),
      ]),
  },
  {
    title: t('Priority', 'Priority'),
    key: 'priority',
    width: 110,
    render: (row) => String(row.priority ?? '-'),
  },
  {
    title: t('模型', 'Models'),
    key: 'models',
    minWidth: 240,
    render: (row) =>
      h(
        'div',
        { class: 'model-chip-list' },
        row.models.slice(0, 4).map((model) => h(NTag, { size: 'small', round: false }, { default: () => model.alias || model.name })),
      ),
  },
  {
    title: t('近期状态', 'Recent status'),
    key: 'status',
    width: 190,
    render: (row) =>
      h(NSpace, { size: 4 }, {
        default: () => [
          h(NTag, { size: 'small', type: statusTagType(row), round: false }, { default: () => statusText(row) }),
          h('span', { class: 'usage-count' }, `S ${formatInteger(row.recent_success)} / F ${formatInteger(row.recent_failure)}`),
        ],
      }),
  },
  {
    title: '',
    key: 'actions',
    width: 300,
    fixed: 'right',
    render: (row) =>
      h(NSpace, { size: 4 }, {
        default: () => [
          row.brand === 'openai_compatibility'
            ? h(
                NButton,
                { size: 'small', quaternary: true, onClick: () => void toggleProviderDisabled(row) },
                { default: () => (row.disabled ? t('启用', 'Enable') : t('停用', 'Disable')) },
              )
            : null,
          h(
            NButton,
            { size: 'small', quaternary: true, onClick: () => openEditDialog(row) },
            { icon: () => h(NIcon, { component: Edit3 }), default: () => t('编辑', 'Edit') },
          ),
          h(
            NButton,
            { size: 'small', quaternary: true, type: 'error', onClick: () => confirmDelete(row) },
            { icon: () => h(NIcon, { component: Trash2 }), default: () => t('删除', 'Delete') },
          ),
        ],
      }),
  },
])

function statusText(provider: AIProviderItem): string {
  if (provider.recent_status === 'healthy') {
    return t('成功', 'Healthy')
  }
  if (provider.recent_status === 'failing') {
    return t('失败', 'Failing')
  }
  return t('未知', 'Unknown')
}

onMounted(refresh)
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('AI 提供商', 'AI Providers') }}</h1>
        <p class="page-subtitle">{{ t('实时管理 CLIProxyAPI 远端 provider 配置', 'Manage remote CLIProxyAPI provider configuration in real time') }}</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">
          <template #icon><NIcon :component="RefreshCw" /></template>
          {{ t('刷新', 'Refresh') }}
        </NButton>
        <NButton type="primary" :disabled="missingSettings" @click="openCreateDialog">
          <template #icon><NIcon :component="Plus" /></template>
          {{ t('新建 Provider', 'New provider') }}
        </NButton>
      </NSpace>
    </div>

    <NAlert v-if="missingSettings" type="warning" :bordered="false" class="settings-alert">
      <div class="settings-alert-content">
        <span>{{ loadError }}</span>
        <NButton type="primary" secondary @click="goSettings">
          <template #icon><NIcon :component="Settings" /></template>
          {{ t('前往系统设置', 'Open System Settings') }}
        </NButton>
      </div>
    </NAlert>
    <NAlert v-else-if="loadError" type="error" :bordered="false">{{ loadError }}</NAlert>
    <NAlert v-if="usageError && !missingSettings" type="warning" :bordered="false">{{ usageError }}</NAlert>

    <div class="metric-grid provider-metrics">
      <div class="metric-card is-primary">
        <div class="metric-icon"><Bot :size="20" /></div>
        <div class="metric-label">{{ t('Provider 总数', 'Providers') }}</div>
        <div class="metric-value">{{ formatInteger(summary.total) }}</div>
        <div class="metric-footnote">{{ t('五类 provider 实时读取', 'Loaded from five provider groups') }}</div>
      </div>
      <div class="metric-card is-success">
        <div class="metric-icon"><CheckCircle2 :size="20" /></div>
        <div class="metric-label">{{ t('近期成功', 'Recent success') }}</div>
        <div class="metric-value">{{ formatInteger(summary.recent_success) }}</div>
        <div class="metric-footnote">{{ t('来自 api-key-usage', 'From api-key-usage') }}</div>
      </div>
      <div class="metric-card is-danger">
        <div class="metric-icon"><XCircle :size="20" /></div>
        <div class="metric-label">{{ t('近期失败', 'Recent failures') }}</div>
        <div class="metric-value">{{ formatInteger(summary.recent_failure) }}</div>
        <div class="metric-footnote">{{ t('用于健康判断', 'Used for health hints') }}</div>
      </div>
    </div>

    <section class="panel provider-panel-shell">
      <div class="panel-inner provider-panel">
        <div class="provider-toolbar">
          <NTabs v-model:value="activeBrand" type="segment">
            <NTabPane v-for="item in providerBrands" :key="item.brand" :name="item.brand" :tab="item.label" />
          </NTabs>
          <NInput v-model:value="search" clearable :placeholder="t('搜索名称、Base URL、模型', 'Search name, base URL, or model')">
            <template #prefix><NIcon :component="Search" /></template>
          </NInput>
        </div>

        <NDataTable
          size="small"
          :loading="isLoading"
          :columns="columns"
          :data="tableRows"
          :pagination="{ pageSize: 10 }"
          table-layout="fixed"
          :scroll-x="1120"
        />
      </div>
    </section>

    <NDrawer v-model:show="drawerOpen" placement="right" width="min(920px, 100vw)" :mask-closable="false">
      <NDrawerContent :title="drawerTitle">
        <div class="provider-drawer">
          <NAlert v-if="editorMode === 'edit'" type="info" :bordered="false">
            {{ t('API key 输入框保持空白时会保留远端原值；页面不会回填完整密钥。', 'Leave API key fields blank to keep the remote value. Full keys are never filled back into the form.') }}
          </NAlert>

          <NForm label-placement="top" class="provider-form">
            <div class="form-grid">
              <NFormItem :label="t('Provider 类型', 'Provider type')">
                <NSelect v-model:value="form.brand" :options="brandOptions" :disabled="editorMode === 'edit'" @update:value="handleBrandChange" />
              </NFormItem>
              <NFormItem v-if="form.brand === 'openai_compatibility'" :label="t('Provider 名称', 'Provider name')" required>
                <NInput v-model:value="form.name" />
              </NFormItem>
              <NFormItem :label="t('Priority', 'Priority')">
                <NInputNumber v-model:value="form.priority" clearable />
              </NFormItem>
              <NFormItem :label="t('Prefix', 'Prefix')">
                <NInput v-model:value="form.prefix" clearable />
              </NFormItem>
              <NFormItem :label="t('Base URL', 'Base URL')">
                <NInput v-model:value="form.base_url" clearable />
              </NFormItem>
              <NFormItem v-if="form.brand !== 'openai_compatibility'" :label="t('Proxy URL', 'Proxy URL')">
                <NInput v-model:value="form.proxy_url" clearable />
              </NFormItem>
              <NFormItem v-if="form.brand !== 'openai_compatibility'" :label="currentBrandConfig.keyLabel">
                <NInput
                  v-model:value="form.api_key"
                  type="password"
                  show-password-on="click"
                  clearable
                  :placeholder="editorMode === 'edit' ? t('留空保留远端原值', 'Leave blank to keep remote value') : ''"
                />
              </NFormItem>
              <NFormItem v-if="form.brand === 'openai_compatibility'" :label="t('停用 Provider', 'Disable provider')">
                <NSwitch v-model:value="form.disabled" />
              </NFormItem>
              <NFormItem v-if="form.brand !== 'vertex'" :label="t('Disable cooling', 'Disable cooling')">
                <NSwitch v-model:value="form.disable_cooling" />
              </NFormItem>
              <NFormItem v-if="form.brand === 'codex'" :label="t('WebSockets', 'WebSockets')">
                <NSwitch v-model:value="form.websockets" />
              </NFormItem>
              <NFormItem v-if="form.brand === 'claude'" :label="t('重建 mid system message', 'Rebuild mid system message')">
                <NSwitch v-model:value="form.rebuild_mid_system_message" />
              </NFormItem>
              <NFormItem v-if="form.brand === 'claude'" :label="t('Experimental CCH signing', 'Experimental CCH signing')">
                <NSwitch v-model:value="form.experimental_cch_signing" />
              </NFormItem>
            </div>
          </NForm>

          <section v-if="form.brand === 'openai_compatibility'" class="field-section">
            <div class="section-head">
              <h3>{{ t('API key entries', 'API key entries') }}</h3>
              <NButton size="small" secondary @click="addKeyEntry">{{ t('新增 Key', 'Add key') }}</NButton>
            </div>
            <div v-for="(entry, index) in form.api_key_entries" :key="index" class="list-row two-col-list-row">
              <NInput
                v-model:value="entry.api_key"
                type="password"
                show-password-on="click"
                :placeholder="entry.api_key_masked || t('留空保留远端原值', 'Leave blank to keep remote value')"
              />
              <NInput v-model:value="entry.proxy_url" clearable :placeholder="t('Proxy URL', 'Proxy URL')" />
              <NButton tertiary type="error" @click="removeKeyEntry(index)">{{ t('删除', 'Delete') }}</NButton>
            </div>
          </section>

          <section class="field-section">
            <div class="section-head">
              <h3>{{ t('模型', 'Models') }}</h3>
              <NButton size="small" secondary @click="addModel()">{{ t('新增模型', 'Add model') }}</NButton>
            </div>
            <div v-for="(model, index) in form.models" :key="index" class="model-row">
              <NInput v-model:value="model.name" :placeholder="t('模型名称', 'Model name')" />
              <NInput v-model:value="model.alias" clearable :placeholder="t('Alias', 'Alias')" />
              <label class="inline-switch"><span>force-mapping</span><NSwitch v-model:value="model.force_mapping" /></label>
              <label v-if="form.brand === 'openai_compatibility'" class="inline-switch"><span>image</span><NSwitch v-model:value="model.image" /></label>
              <NInput
                v-if="form.brand === 'openai_compatibility'"
                v-model:value="model.thinking_text"
                type="textarea"
                :autosize="{ minRows: 2, maxRows: 5 }"
                :placeholder="t('thinking JSON object', 'thinking JSON object')"
              />
              <NButton tertiary type="error" @click="removeModel(index)">{{ t('删除', 'Delete') }}</NButton>
            </div>
          </section>

          <section class="field-section">
            <div class="section-head">
              <h3>{{ t('Headers', 'Headers') }}</h3>
              <NButton size="small" secondary @click="addHeader">{{ t('新增 Header', 'Add header') }}</NButton>
            </div>
            <div v-for="(header, index) in form.headers" :key="index" class="list-row">
              <NInput v-model:value="header.name" :placeholder="t('名称', 'Name')" />
              <NInput v-model:value="header.value" :placeholder="t('值', 'Value')" />
              <NButton tertiary type="error" @click="removeHeader(index)">{{ t('删除', 'Delete') }}</NButton>
            </div>
          </section>

          <section v-if="form.brand !== 'openai_compatibility'" class="field-section">
            <div class="section-head">
              <h3>{{ t('Excluded models', 'Excluded models') }}</h3>
              <NButton size="small" secondary @click="addExcludedModel">{{ t('新增排除项', 'Add excluded') }}</NButton>
            </div>
            <div v-for="(_, index) in form.excluded_models" :key="index" class="list-row single-list-row">
              <NInput
                :value="form.excluded_models[index] ?? ''"
                :placeholder="t('模型名称', 'Model name')"
                @update:value="(value) => setExcludedModel(index, value)"
              />
              <NButton tertiary type="error" @click="removeExcludedModel(index)">{{ t('删除', 'Delete') }}</NButton>
            </div>
          </section>

          <section v-if="form.brand === 'claude'" class="field-section">
            <div class="section-head">
              <h3>{{ t('Cloak', 'Cloak') }}</h3>
            </div>
            <div class="form-grid">
              <NFormItem label="mode"><NInput v-model:value="form.cloak.mode" clearable /></NFormItem>
              <NFormItem label="strict-mode"><NSwitch v-model:value="form.cloak.strict_mode" /></NFormItem>
              <NFormItem label="cache-user-id"><NSwitch v-model:value="form.cloak.cache_user_id" /></NFormItem>
            </div>
            <div class="section-head compact-head">
              <h3>{{ t('Sensitive words', 'Sensitive words') }}</h3>
              <NButton size="small" secondary @click="addSensitiveWord">{{ t('新增词', 'Add word') }}</NButton>
            </div>
            <div v-for="(_, index) in form.cloak.sensitive_words" :key="index" class="list-row single-list-row">
              <NInput
                :value="form.cloak.sensitive_words[index] ?? ''"
                @update:value="(value) => setSensitiveWord(index, value)"
              />
              <NButton tertiary type="error" @click="removeSensitiveWord(index)">{{ t('删除', 'Delete') }}</NButton>
            </div>
          </section>

          <section class="field-section">
            <div class="section-head">
              <h3>{{ t('模型发现与连通性', 'Discovery and connectivity') }}</h3>
            </div>
            <div class="action-grid">
              <NButton secondary :loading="isDiscovering" @click="runDiscovery">
                <template #icon><NIcon :component="FlaskConical" /></template>
                {{ t('发现模型', 'Discover models') }}
              </NButton>
              <NInput v-model:value="testModel" clearable :placeholder="t('测试模型，留空使用首个模型', 'Test model, blank uses the first model')" />
              <NInput v-model:value="testMessage" clearable :placeholder="t('测试消息', 'Test message')" />
              <NButton secondary :loading="isTesting" @click="runConnectivityTest">
                <template #icon><NIcon :component="Wifi" /></template>
                {{ t('连通性测试', 'Test connectivity') }}
              </NButton>
            </div>
            <div v-if="discoveredModels.length > 0" class="discovery-result">
              <div class="model-chip-list">
                <NTag v-for="model in discoveredModels" :key="model.name" size="small" :round="false">{{ model.name }}</NTag>
              </div>
              <NButton size="small" type="primary" secondary @click="applyDiscoveredModels">{{ t('加入模型列表', 'Add to models') }}</NButton>
            </div>
            <NAlert v-if="actionResult" :type="actionResult.ok ? 'success' : 'error'" :bordered="false">
              <template v-if="actionResult.ok">
                {{ t(`请求成功，HTTP ${actionResult.status_code}，耗时 ${actionResult.duration_ms}ms`, `Request succeeded, HTTP ${actionResult.status_code}, ${actionResult.duration_ms}ms`) }}
                <template v-if="actionResult.reply"> · {{ actionResult.reply }}</template>
              </template>
              <template v-else>{{ actionResult.error }}</template>
            </NAlert>
          </section>

          <div class="drawer-actions">
            <span class="dirty-state">{{ formDirty ? t('有未保存修改', 'Unsaved changes') : t('无未保存修改', 'No unsaved changes') }}</span>
            <NSpace>
              <NButton secondary :disabled="isSaving" @click="drawerOpen = false">{{ t('取消', 'Cancel') }}</NButton>
              <NButton type="primary" :loading="isSaving" :disabled="!canSave" @click="saveProvider">{{ t('保存', 'Save') }}</NButton>
            </NSpace>
          </div>
        </div>
      </NDrawerContent>
    </NDrawer>
  </section>
</template>

<style scoped>
.settings-alert-content,
.provider-toolbar,
.section-head,
.drawer-actions,
.discovery-result {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.provider-metrics {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.provider-panel {
  display: grid;
  gap: 14px;
  min-width: 0;
}

.provider-toolbar {
  align-items: flex-start;
}

.provider-toolbar > :first-child {
  flex: 1 1 auto;
  min-width: 280px;
}

.provider-toolbar > :last-child {
  flex: 0 1 360px;
}

.provider-identity {
  display: grid;
  gap: 3px;
  min-width: 0;
}

.provider-identity strong,
.provider-identity span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-identity span,
.usage-count,
.dirty-state {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.model-chip-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-width: 0;
}

.provider-drawer,
.provider-form,
.field-section {
  display: grid;
  gap: 14px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 14px;
}

.field-section {
  padding: 14px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface);
}

.section-head h3 {
  margin: 0;
  font-size: 14px;
}

.compact-head {
  margin-top: 4px;
}

.list-row,
.model-row {
  display: grid;
  gap: 10px;
  align-items: center;
  min-width: 0;
}

.list-row {
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
}

.two-col-list-row {
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;
}

.single-list-row {
  grid-template-columns: minmax(0, 1fr) auto;
}

.model-row {
  grid-template-columns: minmax(160px, 1.2fr) minmax(120px, 0.9fr) minmax(116px, auto) minmax(80px, auto) minmax(180px, 1fr) auto;
}

.inline-switch {
  display: inline-flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 34px;
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.action-grid {
  display: grid;
  grid-template-columns: auto minmax(160px, 1fr) minmax(220px, 1fr) auto;
  gap: 10px;
  align-items: center;
}

.drawer-actions {
  position: sticky;
  bottom: 0;
  z-index: 1;
  padding-top: 12px;
  border-top: 1px solid var(--cpa-border);
  background: var(--cpa-surface);
}

@media (max-width: 980px) {
  .provider-metrics,
  .form-grid,
  .provider-toolbar,
  .action-grid,
  .model-row,
  .list-row,
  .two-col-list-row {
    grid-template-columns: 1fr;
  }

  .provider-toolbar,
  .settings-alert-content,
  .drawer-actions,
  .discovery-result {
    align-items: stretch;
    flex-direction: column;
  }

  .provider-toolbar > :first-child,
  .provider-toolbar > :last-child {
    min-width: 0;
    width: 100%;
  }
}
</style>
