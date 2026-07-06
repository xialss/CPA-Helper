<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCheckbox,
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
  NTooltip,
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
  AIProviderRecentRequestBucket,
  AIProviderSummary,
} from '@/shared/types/api'
import { formatInteger } from '@/shared/utils/format'

type ProviderEnabledFilter = 'all' | 'enabled' | 'disabled'

type DiscoveryModelStatus = 'existing' | 'new' | 'conflict'

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

interface DiscoveryModelItem {
  key: string
  name: string
  alias: string
  status: DiscoveryModelStatus
  statusLabel: string
  selectable: boolean
  selected: boolean
}

interface ProviderStatusBucket {
  key: string
  success: number
  failed: number
  time: string | null
  tone: 'idle' | 'success' | 'warning' | 'danger' | 'unavailable'
  label?: string
  note?: string
  aggregate?: boolean
}

interface ProviderStatusWindow {
  available: boolean
  success: number
  failed: number
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
  recent_status_available: boolean
  recent_requests: AIProviderRecentRequestBucket[]
}

const providerBrands: BrandConfig[] = [
  { brand: 'gemini', label: 'Gemini', keyLabel: 'Gemini API key' },
  { brand: 'codex', label: 'Codex', keyLabel: 'Codex API key' },
  { brand: 'claude', label: 'Claude', keyLabel: 'Claude API key' },
  { brand: 'vertex', label: 'Vertex', keyLabel: 'Vertex API key' },
  { brand: 'openai_compatibility', label: 'OpenAI-compatible', keyLabel: 'Provider API key' },
]

const providerStatusBucketCount = 20
const providerStatusCurrentWindowBucketCount = 3
const providerStatusBucketIntervalMs = 10 * 60 * 1000
const providerStatusTimestampPattern = /^\d{4}-\d{2}-\d{2}[T\s]/
const providerStatusTooltipThemeOverrides = {
  color: 'var(--cpa-surface-raised)',
  textColor: 'var(--cpa-text)',
  boxShadow: '0 12px 30px rgb(15 23 42 / 14%)',
  padding: '10px 12px',
  borderRadius: '8px',
}

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
const enabledFilter = ref<ProviderEnabledFilter>('all')
const search = ref('')

const drawerOpen = ref(false)
const editorMode = ref<'create' | 'edit'>('create')
const form = ref<ProviderDraft>(defaultDraft('gemini'))
const originalFormText = ref('')
const originalDisabled = ref(false)
const isSaving = ref(false)
const isDiscovering = ref(false)
const isTesting = ref(false)
const discoveredModels = ref<AIProviderModel[]>([])
const discoverySearch = ref('')
const selectedDiscoveryModelKeys = ref<string[]>([])
const actionResult = ref<AIProviderActionResponse | null>(null)
const testModel = ref('')
const testMessage = ref('请用一句中文回复：连接测试成功。')

const brandOptions = computed<SelectOption[]>(() =>
  providerBrands.map((item) => ({ label: item.label, value: item.brand })),
)
const enabledFilterOptions = computed<SelectOption[]>(() => [
  { label: t('全部', 'All'), value: 'all' },
  { label: t('启用', 'Enabled'), value: 'enabled' },
  { label: t('禁用', 'Disabled'), value: 'disabled' },
])
const currentBrandConfig = computed(() => brandConfig(form.value.brand))
const missingSettings = computed(() => isMissingSettingsError(loadError.value))
const enabledProviderCount = computed(() => providers.value.filter((provider) => provider.disabled !== true).length)
const tableRows = computed(() => {
  const keyword = search.value.trim().toLowerCase()
  return providers.value.filter((provider) => {
    if (provider.brand !== activeBrand.value) {
      return false
    }
    if (enabledFilter.value === 'enabled' && provider.disabled === true) {
      return false
    }
    if (enabledFilter.value === 'disabled' && provider.disabled !== true) {
      return false
    }
    if (!keyword) {
      return true
    }
    return [
      provider.brand_label,
      provider.name ?? '',
      provider.api_key_masked ?? '',
      provider.auth_index ?? '',
      provider.base_url ?? '',
      provider.prefix ?? '',
      provider.models.map((model) => `${model.name} ${model.alias ?? ''}`).join(' '),
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

function providerUsesExcludedModelsDisabled(brand: AIProviderBrand): boolean {
  return brand !== 'openai_compatibility'
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
    recent_status_available: true,
    recent_requests: [],
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
    excluded_models: provider.excluded_models.filter((item) => !providerUsesExcludedModelsDisabled(provider.brand) || item.trim() !== '*'),
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
    recent_status_available: provider.recent_status_available !== false,
    recent_requests: provider.recent_requests ?? [],
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
  originalDisabled.value = false
  discoveredModels.value = []
  discoverySearch.value = ''
  selectedDiscoveryModelKeys.value = []
  actionResult.value = null
  testModel.value = ''
  drawerOpen.value = true
}

function openEditDialog(provider: AIProviderItem) {
  editorMode.value = 'edit'
  form.value = providerToDraft(provider)
  originalFormText.value = JSON.stringify(form.value)
  originalDisabled.value = form.value.disabled
  discoveredModels.value = []
  discoverySearch.value = ''
  selectedDiscoveryModelKeys.value = []
  actionResult.value = null
  testModel.value = provider.models[0]?.name ?? ''
  drawerOpen.value = true
}

function handleBrandChange(value: string) {
  form.value = defaultDraft(value as AIProviderBrand)
  originalFormText.value = JSON.stringify(form.value)
  originalDisabled.value = false
  discoveredModels.value = []
  discoverySearch.value = ''
  selectedDiscoveryModelKeys.value = []
  actionResult.value = null
  testModel.value = ''
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

function draftToPayload(draft: ProviderDraft, mode: 'create' | 'edit' = editorMode.value): AIProviderItem {
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
    disabled: draft.disabled,
    prefix: draft.prefix.trim(),
    base_url: draft.base_url.trim(),
    original_base_url: mode === 'edit' ? draft.original_base_url : null,
    proxy_url: draft.proxy_url.trim(),
    models,
    headers: draft.headers
      .map((header) => ({ name: header.name.trim(), value: header.value }))
      .filter((header) => header.name !== ''),
    excluded_models: draft.excluded_models
      .map((item) => item.trim())
      .filter((item) => item !== '' && (!providerUsesExcludedModelsDisabled(draft.brand) || item !== '*')),
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
    recent_status_available: draft.recent_status_available,
    recent_requests: draft.recent_requests,
  }
  if (draft.brand === 'openai_compatibility' && !payload.name) {
    throw new Error(t('Provider 名称不能为空', 'Provider name is required'))
  }
  if (draft.brand !== 'openai_compatibility' && mode === 'create' && !payload.api_key) {
    throw new Error(t('新建 provider 必须填写 API key', 'A new provider requires an API key'))
  }
  return payload
}

async function saveProvider() {
  if (!canSave.value) {
    return
  }
  let payload: AIProviderItem
  try {
    payload = draftToPayload(form.value, editorMode.value)
  } catch (error) {
    message.error(errorText(error, '保存 AI provider 失败', 'Failed to save AI provider'))
    return
  }
  if (editorMode.value === 'edit' && !originalDisabled.value && payload.disabled === true) {
    confirmDisableProvider(payload, () => void persistProvider(payload))
    return
  }
  await persistProvider(payload)
}

async function persistProvider(payload: AIProviderItem) {
  isSaving.value = true
  try {
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

function providerIdentityLabel(provider: Pick<AIProviderItem, 'name' | 'api_key_masked' | 'auth_index' | 'identity_hash'>): string {
  return provider.name || provider.api_key_masked || provider.auth_index || provider.identity_hash.slice(0, 12)
}

function confirmDisableProvider(provider: AIProviderItem, onConfirm: () => void) {
  const identity = providerIdentityLabel(provider)
  dialog.warning({
    title: t('禁用 AI provider', 'Disable AI provider'),
    content: t(
      `将禁用 ${provider.brand_label} provider（${identity}）。禁用会写入 CLIProxyAPI 远端配置。`,
      `This disables the ${provider.brand_label} provider (${identity}) in the remote CLIProxyAPI config.`,
    ),
    positiveText: t('禁用', 'Disable'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: onConfirm,
  })
}

function confirmDelete(provider: AIProviderItem) {
  const identity = providerIdentityLabel(provider)
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
  const draft = providerToDraft(provider)
  draft.disabled = !draft.disabled
  let payload: AIProviderItem
  try {
    payload = draftToPayload(draft, 'edit')
  } catch (error) {
    message.error(errorText(error, '更新启用状态失败', 'Failed to update enabled state'))
    return
  }
  const execute = async () => {
    try {
      setSnapshot(await updateAIProvider(payload))
      message.success(draft.disabled ? t('Provider 已禁用', 'Provider disabled') : t('Provider 已启用', 'Provider enabled'))
    } catch (error) {
      message.error(errorText(error, '更新启用状态失败', 'Failed to update enabled state'))
    }
  }
  if (draft.disabled) {
    confirmDisableProvider(payload, () => void execute())
    return
  }
  await execute()
}

function normalizeDiscoveryModelName(value: string): string {
  return value.trim().replace(/^models\//, '').replace(/^publishers\/google\/models\//, '').toLowerCase()
}

function discoveryModelKey(model: Pick<AIProviderModel, 'name' | 'alias'>): string {
  return `${model.name.trim()}\u0000${model.alias ?? ''}`
}

function buildDiscoveryModelItems(): DiscoveryModelItem[] {
  const existingNames = new Set(form.value.models.map((model) => model.name.trim()).filter((name) => name !== ''))
  const existingAliases = new Set(form.value.models.map((model) => model.alias.trim()).filter((alias) => alias !== ''))
  const normalizedDisplays = new Map<string, Set<string>>()
  for (const model of discoveredModels.value) {
    const name = model.name.trim()
    if (!name) {
      continue
    }
    const normalized = normalizeDiscoveryModelName(name)
    const displays = normalizedDisplays.get(normalized) ?? new Set<string>()
    displays.add(name)
    normalizedDisplays.set(normalized, displays)
  }
  return discoveredModels.value
    .map((model) => {
      const name = model.name.trim()
      const alias = model.alias ?? ''
      const normalized = normalizeDiscoveryModelName(name)
      const internalConflict = (normalizedDisplays.get(normalized)?.size ?? 0) > 1
      const existing = existingNames.has(name)
      const conflict = !existing && (existingAliases.has(name) || internalConflict)
      const status: DiscoveryModelStatus = existing ? 'existing' : conflict ? 'conflict' : 'new'
      return {
        key: discoveryModelKey(model),
        name,
        alias,
        status,
        statusLabel:
          status === 'existing'
            ? t('已存在', 'Existing')
            : status === 'conflict'
              ? t('名称冲突', 'Name conflict')
              : t('新发现', 'New'),
        selectable: status === 'new',
        selected: status === 'new' && selectedDiscoveryModelKeys.value.includes(discoveryModelKey(model)),
      }
    })
    .filter((item) => item.name !== '')
}

const discoveredModelItems = computed(() => buildDiscoveryModelItems())
const visibleDiscoveredModelItems = computed(() => {
  const keyword = discoverySearch.value.trim().toLowerCase()
  if (!keyword) {
    return discoveredModelItems.value
  }
  return discoveredModelItems.value.filter((item) =>
    [item.name, item.alias, item.statusLabel].join(' ').toLowerCase().includes(keyword),
  )
})
const selectableDiscoveredModelItems = computed(() => discoveredModelItems.value.filter((item) => item.selectable))
const selectedDiscoveredModelCount = computed(() =>
  discoveredModelItems.value.filter((item) => item.selectable && selectedDiscoveryModelKeys.value.includes(item.key)).length,
)

function selectAllDiscoveredModels() {
  selectedDiscoveryModelKeys.value = selectableDiscoveredModelItems.value.map((item) => item.key)
}

function clearSelectedDiscoveredModels() {
  selectedDiscoveryModelKeys.value = []
}

function toggleDiscoveredModel(item: DiscoveryModelItem, checked: boolean) {
  const selected = new Set(selectedDiscoveryModelKeys.value)
  if (checked && item.selectable) {
    selected.add(item.key)
  } else {
    selected.delete(item.key)
  }
  selectedDiscoveryModelKeys.value = Array.from(selected)
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
      discoverySearch.value = ''
      selectedDiscoveryModelKeys.value = []
      selectedDiscoveryModelKeys.value = buildDiscoveryModelItems()
        .filter((item) => item.selectable)
        .map((item) => item.key)
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

function applySelectedDiscoveredModels() {
  const selected = new Set(selectedDiscoveryModelKeys.value)
  const existing = new Set(form.value.models.map((model) => model.name.trim()).filter((name) => name !== ''))
  let added = 0
  for (const item of discoveredModelItems.value) {
    if (!item.selectable || !selected.has(item.key) || existing.has(item.name)) {
      continue
    }
    addModel(item.name)
    existing.add(item.name)
    added++
  }
  selectedDiscoveryModelKeys.value = []
  if (added === 0) {
    message.warning(t('没有可加入的新模型', 'No new models to add'))
    return
  }
  message.success(t(`已加入 ${added} 个发现模型`, `${added} discovered models added`))
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
    (lowerText.includes('system settings') && lowerText.includes('cliproxyapi url and management key'))
  )
}

function providerEnabledText(provider: AIProviderItem): string {
  return provider.disabled ? t('禁用', 'Disabled') : t('启用', 'Enabled')
}

function providerRecentRequestTotal(provider: AIProviderItem): number {
  return provider.recent_success + provider.recent_failure
}

function recentRequestBucketTotal(bucket: Pick<AIProviderRecentRequestBucket, 'success' | 'failed'>): number {
  return Math.max(0, bucket.success ?? 0) + Math.max(0, bucket.failed ?? 0)
}

function parseProviderStatusBucketTime(value: string | null | undefined): number | null {
  const trimmed = value?.trim()
  if (!trimmed) {
    return null
  }
  if (!providerStatusTimestampPattern.test(trimmed)) {
    return null
  }
  const parsed = Date.parse(trimmed)
  if (!Number.isFinite(parsed)) {
    return null
  }
  return Math.floor(parsed / providerStatusBucketIntervalMs) * providerStatusBucketIntervalMs
}

function parseProviderClockBucketStart(value: string | null | undefined): number | null {
  const match = value?.trim().match(/(?:^|\D)([01]?\d|2[0-3]):([0-5]\d)(?:\D|$)/)
  if (!match) {
    return null
  }
  return Number(match[1]) * 60 + Number(match[2])
}

function formatProviderClockMinute(value: number): string {
  const normalized = ((value % 1440) + 1440) % 1440
  const hour = String(Math.floor(normalized / 60)).padStart(2, '0')
  const minute = String(normalized % 60).padStart(2, '0')
  return `${hour}:${minute}`
}

function providerClockBucketTimeRange(startMinute: number): string {
  return `${formatProviderClockMinute(startMinute)} - ${formatProviderClockMinute(startMinute + providerStatusBucketIntervalMs / 60_000)}`
}

function providerStatusAvailable(provider: AIProviderItem): boolean {
  return provider.recent_status_available !== false && provider.recent_status !== 'unavailable'
}

function providerStatusTextUnavailable(provider: AIProviderItem): boolean {
  return !providerStatusAvailable(provider)
}

function providerSuccessRateValue(provider: AIProviderItem): string {
  const total = providerRecentRequestTotal(provider)
  if (total <= 0) {
    return '--'
  }
  return `${Math.round((provider.recent_success / total) * 100)}%`
}

function providerCurrentWindowStatus(provider: AIProviderItem): ProviderStatusWindow {
  if (!providerStatusAvailable(provider)) {
    return { available: false, success: 0, failed: 0 }
  }
  const buckets = providerStatusBuckets(provider)
  if (buckets.length === 0 || buckets.some((bucket) => bucket.aggregate)) {
    return { available: false, success: 0, failed: 0 }
  }
  return buckets.slice(-providerStatusCurrentWindowBucketCount).reduce<ProviderStatusWindow>(
    (acc, bucket) => ({
      available: true,
      success: acc.success + Math.max(0, bucket.success),
      failed: acc.failed + Math.max(0, bucket.failed),
    }),
    { available: true, success: 0, failed: 0 },
  )
}

function providerCurrentWindowSuccessRate(status: ProviderStatusWindow): string {
  if (!status.available) {
    return '--'
  }
  const total = status.success + status.failed
  if (total <= 0) {
    return '--'
  }
  return `${Math.round((status.success / total) * 100)}%`
}

function providerStatusText(provider: AIProviderItem): string {
  if (!providerStatusAvailable(provider)) {
    return t('状态不可用', 'Status unavailable')
  }
  const status = providerCurrentWindowStatus(provider)
  if (!status.available) {
    return t('近30分钟内无分段数据', 'No 30-minute bucket data')
  }
  const rate = providerCurrentWindowSuccessRate(status)
  if (status.success + status.failed === 0) {
    return t('近30分钟内无请求', 'Last 30 min no requests')
  }
  if (providerStatusBucketTone(status.success, status.failed) === 'success') {
    return t(`近30分钟内成功 · ${rate}`, `Last 30 min healthy · ${rate}`)
  }
  return t(`近30分钟内失败 · ${rate}`, `Last 30 min failures · ${rate}`)
}

function providerSuccessRate(provider: AIProviderItem): string {
  return providerSuccessRateValue(provider)
}

function providerStatusRateTone(provider: AIProviderItem): ProviderStatusBucket['tone'] {
  if (!providerStatusAvailable(provider)) {
    return 'unavailable'
  }
  return providerStatusBucketTone(provider.recent_success, provider.recent_failure)
}

function providerStatusCountText(provider: AIProviderItem): string {
  if (!providerStatusAvailable(provider) && providerRecentRequestTotal(provider) === 0) {
    return t('成功 -- / 失败 --', 'S -- / F --')
  }
  return t(
    `成功 ${formatInteger(provider.recent_success)} / 失败 ${formatInteger(provider.recent_failure)}`,
    `S ${formatInteger(provider.recent_success)} / F ${formatInteger(provider.recent_failure)}`,
  )
}

function providerStatusBucketTone(success: number, failed: number): ProviderStatusBucket['tone'] {
  const total = success + failed
  if (total <= 0) {
    return 'idle'
  }
  const rate = success / total
  return rate >= 0.9 ? 'success' : rate >= 0.5 ? 'warning' : 'danger'
}

function emptyProviderStatusBuckets(tone: 'idle' | 'unavailable'): ProviderStatusBucket[] {
  return Array.from({ length: providerStatusBucketCount }, (_, index) => ({
    key: `${tone}-${index}`,
    success: 0,
    failed: 0,
    time: null,
    tone,
  }))
}

function providerAggregateStatusBuckets(provider: AIProviderItem): ProviderStatusBucket[] {
  const success = Math.max(0, provider.recent_success)
  const failed = Math.max(0, provider.recent_failure)
  const total = success + failed
  if (total <= 0) {
    return emptyProviderStatusBuckets(providerStatusAvailable(provider) ? 'idle' : 'unavailable')
  }
  const failedSegments =
    failed <= 0 ? 0 : success <= 0 ? providerStatusBucketCount : Math.min(providerStatusBucketCount - 1, Math.max(1, Math.round((failed / total) * providerStatusBucketCount)))
  const successSegments = providerStatusBucketCount - failedSegments
  const note = t(
    'CPA 未返回带时间的 20 段明细，当前色块按累计成功/失败比例展示概览。',
    'CPA did not return timed 20-bucket details, so these blocks summarize aggregate success/failure ratio.',
  )
  return Array.from({ length: providerStatusBucketCount }, (_, index) => {
    const isFailureSegment = index >= successSegments
    return {
      key: `aggregate-${index}`,
      success,
      failed,
      time: null,
      tone: isFailureSegment ? 'danger' : 'success',
      label: t('近期请求汇总', 'Recent request summary'),
      note,
      aggregate: true,
    }
  })
}

function providerTimeAlignedStatusBuckets(source: AIProviderRecentRequestBucket[]): ProviderStatusBucket[] | null {
  const bucketsByTime = new Map<number, { success: number; failed: number; time: string | null }>()
  for (const item of source) {
    const success = Math.max(0, item.success ?? 0)
    const failed = Math.max(0, item.failed ?? 0)
    const timestamp = parseProviderStatusBucketTime(item.time)
    if (timestamp === null) {
      if (success + failed > 0) {
        return null
      }
      continue
    }
    const existing = bucketsByTime.get(timestamp)
    if (existing) {
      existing.success += success
      existing.failed += failed
      existing.time = existing.time ?? item.time ?? null
    } else {
      bucketsByTime.set(timestamp, { success, failed, time: item.time ?? null })
    }
  }
  if (bucketsByTime.size === 0) {
    return null
  }
  const activeTimestamps = Array.from(bucketsByTime.entries())
    .filter(([, item]) => item.success + item.failed > 0)
    .map(([timestamp]) => timestamp)
  const latestTimestamp = activeTimestamps.length > 0 ? Math.max(...activeTimestamps) : Math.max(...Array.from(bucketsByTime.keys()))
  const firstTimestamp = latestTimestamp - (providerStatusBucketCount - 1) * providerStatusBucketIntervalMs
  const buckets: ProviderStatusBucket[] = []
  for (let index = 0; index < providerStatusBucketCount; index++) {
    const timestamp = firstTimestamp + index * providerStatusBucketIntervalMs
    const item = bucketsByTime.get(timestamp)
    if (!item) {
      buckets.push({ key: `idle-${timestamp}`, success: 0, failed: 0, time: new Date(timestamp).toISOString(), tone: 'idle' })
      continue
    }
    buckets.push({
      key: `bucket-${timestamp}`,
      success: item.success,
      failed: item.failed,
      time: item.time,
      tone: providerStatusBucketTone(item.success, item.failed),
    })
  }
  return buckets
}

function providerClockStatusBuckets(source: AIProviderRecentRequestBucket[]): ProviderStatusBucket[] | null {
  const bucketsByMinute = new Map<number, { success: number; failed: number; time: string | null }>()
  for (const item of source) {
    const success = Math.max(0, item.success ?? 0)
    const failed = Math.max(0, item.failed ?? 0)
    const minute = parseProviderClockBucketStart(item.time)
    if (minute === null) {
      if (success + failed > 0) {
        return null
      }
      continue
    }
    const existing = bucketsByMinute.get(minute)
    if (existing) {
      existing.success += success
      existing.failed += failed
      existing.time = existing.time ?? item.time ?? null
    } else {
      bucketsByMinute.set(minute, { success, failed, time: item.time ?? null })
    }
  }
  if (bucketsByMinute.size === 0) {
    return null
  }

  const sortedMinutes = Array.from(bucketsByMinute.keys()).sort((left, right) => left - right)
  let largestGapIndex = sortedMinutes.length - 1
  let largestGap = -1
  for (let index = 0; index < sortedMinutes.length; index++) {
    const current = sortedMinutes[index]!
    const next = sortedMinutes[(index + 1) % sortedMinutes.length]! + (index === sortedMinutes.length - 1 ? 1440 : 0)
    const gap = next - current
    if (gap > largestGap) {
      largestGap = gap
      largestGapIndex = index
    }
  }
  const intervalMinutes = providerStatusBucketIntervalMs / 60_000
  const orderedMinutes =
    largestGap <= intervalMinutes ? sortedMinutes : [...sortedMinutes.slice(largestGapIndex + 1), ...sortedMinutes.slice(0, largestGapIndex + 1)]
  const latestMinute = orderedMinutes[orderedMinutes.length - 1]!
  const firstMinute = latestMinute - (providerStatusBucketCount - 1) * intervalMinutes
  const buckets: ProviderStatusBucket[] = []
  for (let index = 0; index < providerStatusBucketCount; index++) {
    const minute = ((firstMinute + index * intervalMinutes) % 1440 + 1440) % 1440
    const item = bucketsByMinute.get(minute)
    if (!item) {
      buckets.push({
        key: `clock-idle-${minute}`,
        success: 0,
        failed: 0,
        time: null,
        tone: 'idle',
        label: providerClockBucketTimeRange(minute),
      })
      continue
    }
    buckets.push({
      key: `clock-${minute}`,
      success: item.success,
      failed: item.failed,
      time: item.time,
      tone: providerStatusBucketTone(item.success, item.failed),
      label: providerClockBucketTimeRange(minute),
    })
  }
  return buckets
}

function providerSequentialStatusBuckets(source: AIProviderRecentRequestBucket[]): ProviderStatusBucket[] {
  const recentSource = source.slice(-providerStatusBucketCount)
  const buckets: ProviderStatusBucket[] = []
  const idleCount = providerStatusBucketCount - recentSource.length
  for (let index = 0; index < idleCount; index++) {
    buckets.push({ key: `idle-${index}`, success: 0, failed: 0, time: null, tone: 'idle' })
  }
  recentSource.forEach((item, index) => {
    const success = Math.max(0, item.success ?? 0)
    const failed = Math.max(0, item.failed ?? 0)
    const bucket: ProviderStatusBucket = {
      key: `${item.time ?? 'bucket'}-${index}`,
      success,
      failed,
      time: item.time ?? null,
      tone: providerStatusBucketTone(success, failed),
    }
    if (!item.time) {
      bucket.label = t(`最近分段 ${index + 1}`, `Recent bucket ${index + 1}`)
    }
    buckets.push(bucket)
  })
  return buckets
}

function providerStatusBuckets(provider: AIProviderItem): ProviderStatusBucket[] {
  if (!providerStatusAvailable(provider)) {
    return emptyProviderStatusBuckets('unavailable')
  }
  const source = provider.recent_requests ?? []
  const sourceHasRequests = source.some((item) => recentRequestBucketTotal(item) > 0)
  const providerTotal = providerRecentRequestTotal(provider)
  const timeAlignedBuckets = providerTimeAlignedStatusBuckets(source)
  if (timeAlignedBuckets && (sourceHasRequests || providerTotal <= 0)) {
    return timeAlignedBuckets
  }
  const clockBuckets = providerClockStatusBuckets(source)
  if (clockBuckets && (sourceHasRequests || providerTotal <= 0)) {
    return clockBuckets
  }
  if (sourceHasRequests) {
    return providerSequentialStatusBuckets(source)
  }
  if (providerTotal > 0) {
    return providerAggregateStatusBuckets(provider)
  }
  return emptyProviderStatusBuckets('idle')
}

function formatProviderStatusBucketTime(value: number): string {
  const date = new Date(value)
  const hour = String(date.getHours()).padStart(2, '0')
  const minute = String(date.getMinutes()).padStart(2, '0')
  return `${hour}:${minute}`
}

function providerStatusBucketTimeRange(bucket: ProviderStatusBucket): string {
  if (bucket.label) {
    return bucket.label
  }
  const parsed = bucket.time ? Date.parse(bucket.time) : Number.NaN
  if (!Number.isFinite(parsed)) {
    return bucket.time || t('该 10 分钟段', 'This 10-minute bucket')
  }
  return `${formatProviderStatusBucketTime(parsed)} - ${formatProviderStatusBucketTime(parsed + providerStatusBucketIntervalMs)}`
}

function providerStatusBucketRate(bucket: ProviderStatusBucket): string {
  const total = bucket.success + bucket.failed
  if (total <= 0) {
    return '--'
  }
  return `${Math.round((bucket.success / total) * 100)}%`
}

function providerStatusBucketTitle(bucket: ProviderStatusBucket): string {
  if (bucket.tone === 'unavailable') {
    return t('状态不可用', 'Status unavailable')
  }
  const total = bucket.success + bucket.failed
  const range = providerStatusBucketTimeRange(bucket)
  if (total <= 0) {
    return t(`${range} 无请求`, `${range} no requests`)
  }
  return t(
    `${range} 成功 ${formatInteger(bucket.success)} / 失败 ${formatInteger(bucket.failed)} / 成功率 ${providerStatusBucketRate(bucket)}`,
    `${range} S ${formatInteger(bucket.success)} / F ${formatInteger(bucket.failed)} / Success ${providerStatusBucketRate(bucket)}`,
  )
}

function renderProviderStatusBucketTooltip(bucket: ProviderStatusBucket) {
  if (bucket.tone === 'unavailable') {
    return h('div', { class: 'provider-status-tooltip' }, [
      h('strong', { class: 'provider-status-tooltip-range' }, t('状态不可用', 'Status unavailable')),
      h('span', { class: 'provider-status-tooltip-note' }, t('近期请求状态条缺少可渲染分段数据', 'Recent status buckets are not available')),
    ])
  }
  return h('div', { class: 'provider-status-tooltip' }, [
    h('strong', { class: 'provider-status-tooltip-range' }, providerStatusBucketTimeRange(bucket)),
    bucket.note ? h('span', { class: 'provider-status-tooltip-note' }, bucket.note) : null,
    h('div', { class: 'provider-status-tooltip-counts' }, [
      h('span', { class: 'is-success' }, t(`成功 ${formatInteger(bucket.success)}`, `${formatInteger(bucket.success)} succeeded`)),
      h('span', { class: 'is-danger' }, t(`失败 ${formatInteger(bucket.failed)}`, `${formatInteger(bucket.failed)} failed`)),
      h('span', providerStatusBucketRate(bucket)),
    ]),
  ])
}

function renderProviderStatus(row: AIProviderItem) {
  const rateTone = providerStatusRateTone(row)
  return h('div', { class: 'provider-status-cell' }, [
    h('div', { class: 'provider-status-head' }, [
      h(NTag, { size: 'small', type: row.disabled ? 'warning' : 'success', round: false }, { default: () => providerEnabledText(row) }),
      h('span', { class: ['provider-status-text', providerStatusTextUnavailable(row) ? 'is-unavailable' : undefined] }, providerStatusText(row)),
    ]),
    h('div', { class: 'provider-status-progress' }, [
      h(
        'div',
        { class: 'provider-status-bars' },
        providerStatusBuckets(row).map((bucket) =>
          h(
            NTooltip,
            {
              key: bucket.key,
              trigger: 'hover',
              placement: 'top',
              themeOverrides: providerStatusTooltipThemeOverrides,
            },
            {
              trigger: () =>
                h('span', {
                  class: ['provider-status-block', `is-${bucket.tone}`, bucket.aggregate ? 'is-aggregate' : undefined],
                  'aria-label': providerStatusBucketTitle(bucket),
                }),
              default: () => renderProviderStatusBucketTooltip(bucket),
            },
          ),
        ),
      ),
      h('strong', { class: ['provider-status-rate-pill', `is-${rateTone}`] }, providerSuccessRate(row)),
    ]),
    h('div', { class: 'provider-status-counts' }, providerStatusCountText(row)),
  ])
}

function renderModelHeaderSummary(row: AIProviderItem) {
  return h('div', { class: 'provider-config-summary' }, [
    h('div', { class: 'provider-config-counts' }, [
      h(NTag, { size: 'small', round: false }, { default: () => t(`模型 ${row.models.length}`, `${row.models.length} models`) }),
      h(NTag, { size: 'small', round: false }, { default: () => t(`请求头 ${row.headers.length}`, `${row.headers.length} headers`) }),
      row.brand === 'openai_compatibility'
        ? h(NTag, { size: 'small', round: false }, { default: () => t(`Key ${row.api_key_entries.length}`, `${row.api_key_entries.length} keys`) })
        : null,
    ]),
  ])
}

function providerTableRowProps(row: AIProviderItem) {
  return {
    class: {
      'is-provider-disabled-row': row.disabled === true,
    },
  }
}

const columns = computed<DataTableColumns<AIProviderItem>>(() => [
  {
    title: t('密钥', 'Key'),
    key: 'key',
    width: 160,
    render: (row) =>
      h('div', { class: 'provider-identity' }, [
        h('strong', { title: providerIdentityLabel(row) }, providerIdentityLabel(row)),
      ]),
  },
  {
    title: t('服务地址', 'Service URL'),
    key: 'base_url',
    width: 260,
    render: (row) => h('span', { class: ['provider-url', row.base_url ? undefined : 'empty-cell'], title: row.base_url || '' }, row.base_url || '-'),
  },
  {
    title: t('优先级', 'Priority'),
    key: 'priority',
    width: 80,
    render: (row) => String(row.priority ?? '-'),
  },
  {
    title: t('前缀', 'Prefix'),
    key: 'prefix',
    width: 100,
    render: (row) => h('span', { class: row.prefix ? undefined : 'empty-cell', title: row.prefix || '' }, row.prefix || '-'),
  },
  {
    title: t('模型/请求头', 'Models / Headers'),
    key: 'models_headers',
    width: 190,
    render: (row) => renderModelHeaderSummary(row),
  },
  {
    title: t('状态', 'Status'),
    key: 'status',
    width: 340,
    render: (row) => renderProviderStatus(row),
  },
  {
    title: t('操作', 'Actions'),
    key: 'actions',
    width: 230,
    fixed: 'right',
    render: (row) =>
      h(NSpace, { size: 6 }, {
        default: () => [
          h(
            NButton,
            {
              size: 'small',
              secondary: true,
              type: row.disabled ? 'success' : 'warning',
              onClick: () => void toggleProviderDisabled(row),
            },
            {
              icon: () => h(NIcon, { component: row.disabled ? CheckCircle2 : XCircle }),
              default: () => (row.disabled ? t('启用', 'Enable') : t('禁用', 'Disable')),
            },
          ),
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
        <div class="metric-footnote">{{ t(`启用 ${formatInteger(enabledProviderCount)} 个 · 五类 provider 实时读取`, `${formatInteger(enabledProviderCount)} enabled · Loaded from five provider groups`) }}</div>
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
          <NTabs v-model:value="activeBrand" type="segment" class="provider-brand-tabs">
            <NTabPane v-for="item in providerBrands" :key="item.brand" :name="item.brand" :tab="item.label" />
          </NTabs>
          <NSelect v-model:value="enabledFilter" class="provider-enabled-filter" :options="enabledFilterOptions" />
          <NInput v-model:value="search" class="provider-search" clearable :placeholder="t('搜索名称、服务地址、模型', 'Search name, service URL, or model')">
            <template #prefix><NIcon :component="Search" /></template>
          </NInput>
        </div>

        <NDataTable
          class="provider-table"
          size="small"
          :loading="isLoading"
          :columns="columns"
          :data="tableRows"
          :row-props="providerTableRowProps"
          :pagination="{ pageSize: 10 }"
          table-layout="fixed"
          :scroll-x="1360"
        />
      </div>
    </section>

    <NDrawer v-model:show="drawerOpen" placement="right" width="min(920px, 100vw)" :mask-closable="false">
      <NDrawerContent :title="drawerTitle" body-content-style="padding: 0; height: 100%;">
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
              <NFormItem :label="t('优先级', 'Priority')">
                <NInputNumber v-model:value="form.priority" clearable />
              </NFormItem>
              <NFormItem :label="t('前缀 Prefix', 'Prefix')">
                <NInput v-model:value="form.prefix" clearable />
              </NFormItem>
              <NFormItem :label="t('Base URL', 'Base URL')">
                <NInput v-model:value="form.base_url" clearable />
              </NFormItem>
              <NFormItem v-if="form.brand !== 'openai_compatibility'" :label="t('代理 URL', 'Proxy URL')">
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
              <NFormItem :label="t('启用状态', 'Enabled state')">
                <div class="enable-switch-row">
                  <NSwitch v-model:value="form.disabled" :checked-value="false" :unchecked-value="true" />
                  <span :class="['enable-switch-label', form.disabled ? 'is-disabled' : 'is-enabled']">
                    {{ form.disabled ? t('禁用', 'Disabled') : t('启用', 'Enabled') }}
                  </span>
                </div>
              </NFormItem>
              <NFormItem v-if="form.brand !== 'vertex'" :label="t('禁用冷却调度', 'Disable cooling')">
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
              <NInput v-model:value="entry.proxy_url" clearable :placeholder="t('代理 URL', 'Proxy URL')" />
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
              <NInput v-model:value="header.name" placeholder="X-Custom-Header" />
              <NInput v-model:value="header.value" placeholder="value" />
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
            <div v-if="discoveredModels.length > 0" class="discovery-result-panel">
              <div class="discovery-toolbar">
                <NInput v-model:value="discoverySearch" clearable :placeholder="t('搜索发现模型', 'Search discovered models')">
                  <template #prefix><NIcon :component="Search" /></template>
                </NInput>
                <NSpace size="small">
                  <NButton size="small" secondary :disabled="selectableDiscoveredModelItems.length === 0" @click="selectAllDiscoveredModels">
                    {{ t('全选可选项', 'Select all') }}
                  </NButton>
                  <NButton size="small" secondary :disabled="selectedDiscoveredModelCount === 0" @click="clearSelectedDiscoveredModels">
                    {{ t('清空选择', 'Clear') }}
                  </NButton>
                  <NButton size="small" type="primary" secondary :disabled="selectedDiscoveredModelCount === 0" @click="applySelectedDiscoveredModels">
                    {{ t(`添加选中模型 (${selectedDiscoveredModelCount})`, `Add selected (${selectedDiscoveredModelCount})`) }}
                  </NButton>
                </NSpace>
              </div>
              <div class="discovery-list">
                <div v-for="item in visibleDiscoveredModelItems" :key="item.key" class="discovery-row" :class="`is-${item.status}`">
                  <NCheckbox :checked="item.selected" :disabled="!item.selectable" @update:checked="(checked) => toggleDiscoveredModel(item, checked)" />
                  <div class="discovery-model-main">
                    <strong>{{ item.name }}</strong>
                    <span v-if="item.alias">{{ item.alias }}</span>
                  </div>
                  <NTag size="small" :type="item.status === 'new' ? 'success' : item.status === 'conflict' ? 'warning' : 'default'" :round="false">
                    {{ item.statusLabel }}
                  </NTag>
                </div>
                <div v-if="visibleDiscoveredModelItems.length === 0" class="empty-state">{{ t('没有匹配的发现模型', 'No discovered models match') }}</div>
              </div>
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
.section-head,
.drawer-actions,
.discovery-toolbar {
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
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-start;
}

.provider-brand-tabs,
.provider-enabled-filter,
.provider-search {
  min-width: 0;
}

.provider-brand-tabs {
  flex: 1 1 640px;
  overflow: hidden;
}

.provider-enabled-filter {
  flex: 0 0 150px;
}

.provider-search {
  flex: 1 1 280px;
  max-width: 380px;
}

.provider-table {
  min-width: 0;
}

.provider-identity {
  display: grid;
  min-width: 0;
}

.provider-identity strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dirty-state,
.empty-cell {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.provider-url {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-config-summary {
  display: grid;
  gap: 7px;
  min-width: 0;
}

.provider-config-counts,
.enable-switch-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.provider-config-counts {
  flex-wrap: wrap;
  gap: 6px;
}

:global(.provider-status-cell) {
  display: grid;
  gap: 7px;
  min-width: 0;
}

:global(.provider-status-head),
:global(.provider-status-progress) {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

:global(.provider-status-head) {
  justify-content: flex-start;
}

:global(.provider-status-text) {
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.provider-status-text.is-unavailable) {
  color: var(--cpa-warning);
}

:global(.provider-status-bars) {
  display: grid;
  flex: 0 0 157px;
  grid-template-columns: repeat(20, 5px);
  gap: 3px;
  width: 157px;
  min-width: 157px;
  max-width: 157px;
}

:global(.provider-status-block) {
  display: block;
  width: 5px;
  height: 12px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--cpa-border) 68%, transparent);
}

:global(.provider-status-block.is-idle) {
  background: color-mix(in srgb, var(--cpa-border) 68%, transparent);
}

:global(.provider-status-block.is-success) {
  background: var(--cpa-success);
}

:global(.provider-status-block.is-warning) {
  background: var(--cpa-warning);
}

:global(.provider-status-block.is-danger) {
  background: var(--cpa-danger);
}

:global(.provider-status-block.is-unavailable) {
  background: repeating-linear-gradient(
    135deg,
    color-mix(in srgb, var(--cpa-warning) 42%, transparent),
    color-mix(in srgb, var(--cpa-warning) 42%, transparent) 3px,
    color-mix(in srgb, var(--cpa-border) 70%, transparent) 3px,
    color-mix(in srgb, var(--cpa-border) 70%, transparent) 6px
  );
}

:global(.provider-status-block.is-aggregate) {
  opacity: 0.82;
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--cpa-text-muted) 45%, transparent);
}

:global(.provider-status-rate-pill) {
  flex: 0 0 auto;
  min-width: 48px;
  padding: 2px 7px;
  border-radius: 6px;
  color: var(--cpa-text);
  font-size: 12px;
  line-height: 1.35;
  text-align: center;
  font-variant-numeric: tabular-nums;
  background: var(--cpa-surface-muted);
}

:global(.provider-status-rate-pill.is-success) {
  color: var(--cpa-success);
  background: var(--cpa-success-weak);
}

:global(.provider-status-rate-pill.is-idle) {
  color: var(--cpa-text-muted);
  background: var(--cpa-surface-muted);
}

:global(.provider-status-rate-pill.is-warning) {
  color: var(--cpa-warning);
  background: color-mix(in srgb, var(--cpa-warning) 12%, var(--cpa-surface-raised));
}

:global(.provider-status-rate-pill.is-danger) {
  color: var(--cpa-danger);
  background: var(--cpa-danger-weak);
}

:global(.provider-status-rate-pill.is-unavailable) {
  color: var(--cpa-warning);
  background: color-mix(in srgb, var(--cpa-warning) 10%, var(--cpa-surface-raised));
}

:global(.provider-status-counts) {
  overflow: hidden;
  color: var(--cpa-text-muted);
  font-size: 12px;
  line-height: 1.25;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.enable-switch-label {
  font-size: 13px;
  font-weight: 600;
}

.enable-switch-label.is-enabled {
  color: var(--cpa-success);
}

.enable-switch-label.is-disabled {
  color: var(--cpa-warning);
}

:global(.is-provider-disabled-row td) {
  background: color-mix(in srgb, var(--cpa-warning) 7%, transparent);
}

:global(.is-provider-disabled-row .provider-identity strong) {
  color: var(--cpa-text-muted);
}

:global(.provider-status-tooltip) {
  display: grid;
  gap: 8px;
  min-width: 150px;
  color: var(--cpa-text);
}

:global(.provider-status-tooltip-range) {
  color: var(--cpa-text-strong);
  font-size: 13px;
  font-weight: 700;
}

:global(.provider-status-tooltip-note) {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

:global(.provider-status-tooltip-counts) {
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--cpa-text-muted);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

:global(.provider-status-tooltip-counts .is-success) {
  color: var(--cpa-success);
}

:global(.provider-status-tooltip-counts .is-danger) {
  color: var(--cpa-danger);
}

.provider-form,
.field-section {
  display: grid;
  gap: 14px;
}

.provider-drawer {
  display: flex;
  min-height: 100%;
  box-sizing: border-box;
  flex-direction: column;
  gap: 14px;
  padding: 16px 20px 0;
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

.discovery-result-panel {
  display: grid;
  gap: 10px;
  min-width: 0;
}

.discovery-toolbar > :first-child {
  flex: 1 1 260px;
  min-width: 0;
}

.discovery-list {
  display: grid;
  max-height: 280px;
  overflow: auto;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
}

.discovery-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 10px;
  align-items: center;
  min-width: 0;
  padding: 9px 10px;
  border-bottom: 1px solid var(--cpa-border);
}

.discovery-row:last-child {
  border-bottom: 0;
}

.discovery-row.is-existing,
.discovery-row.is-conflict {
  background: color-mix(in srgb, var(--cpa-border) 24%, transparent);
}

.discovery-model-main {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.discovery-model-main strong,
.discovery-model-main span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.discovery-model-main span {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.empty-state {
  padding: 18px;
  color: var(--cpa-text-muted);
  text-align: center;
}

.drawer-actions {
  position: sticky;
  bottom: 0;
  z-index: 1;
  margin: auto -20px 0;
  padding: 12px 20px;
  border-top: 1px solid var(--cpa-border);
  background: var(--cpa-surface);
}

@media (max-width: 980px) {
  .provider-metrics,
  .form-grid,
  .action-grid,
  .model-row,
  .list-row,
  .two-col-list-row,
  .discovery-row {
    grid-template-columns: 1fr;
  }

  .settings-alert-content,
  .drawer-actions,
  .discovery-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .provider-brand-tabs,
  .provider-enabled-filter,
  .provider-search,
  .discovery-toolbar > :first-child {
    min-width: 0;
    max-width: none;
    width: 100%;
  }
}
</style>
