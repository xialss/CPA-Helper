<script setup lang="ts">
import type { Component, CSSProperties } from 'vue'
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
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
  NTooltip,
  useDialog,
  useMessage,
  type DataTableColumns,
  type DataTableRowKey,
} from 'naive-ui'
import { Database, Layers3, RefreshCw, Search, Server, Settings2, Zap } from 'lucide-vue-next'

import {
  createModelPrice,
  deleteModelPrice,
  getLiteLLMProxySettings,
  listModelPriceCatalog,
  listModelPrices,
  syncLitellmModelPrices,
  updateLiteLLMProxySettings,
  updateModelPrice,
  updateModelPricePriorityMultiplier,
} from '@/features/pricing/api/pricingApi'
import type {
  LiteLLMProxySettingsPayload,
  ModelPrice,
  ModelPriceCatalogItem,
  ModelPriceCatalogResponse,
  ModelPriceLongContext,
  ModelPricePayload,
} from '@/shared/types/api'
import { formatDateTime, formatInteger, formatMultiplier } from '@/shared/utils/format'
import { useI18n } from '@/shared/i18n'

type PriceTableLayoutProps =
  | { flexHeight: true }
  | { flexHeight: false; maxHeight: string }

type PriceRowStatus = 'missing' | 'litellm' | 'manual'
type PriceStatusFilter = 'cpa' | 'missing' | 'litellm' | 'manual' | 'library'
type BillingUnit = 'token' | 'request'
type PriceGroupingMode = 'model' | 'provider'
type PriceFieldName = keyof Pick<
  ModelPrice,
  | 'input_usd_per_million'
  | 'output_usd_per_million'
  | 'cache_read_usd_per_million'
  | 'cache_creation_usd_per_million'
>

interface CatalogModelReference {
  id: string
  name: string
  owner: string | null
  suggestedProvider: string
}

interface PriceDisplayRow {
  rowType: 'detail'
  key: string
  in_cpa: boolean
  id: string
  name: string
  owner: string | null
  suggested_provider: string
  price: ModelPrice | null
  provider: string
  model: string
  comparisonModelKey: string
  catalogModels: CatalogModelReference[]
  billing_unit: BillingUnit
  status: PriceRowStatus
}

interface PriceGroupRow {
  rowType: 'group'
  key: string
  mode: PriceGroupingMode
  label: string
  children: PriceDisplayRow[]
  providerCount: number
  modelCount: number
  pricedCount: number
  unpricedCount: number
  billingUnits: BillingUnit[]
  longContextConfiguredCount: number
  longContextEligibleCount: number
  priorityConfiguredCount: number
  priorityEligibleCount: number
  latestUpdatedAt: string | null
}

type PriceTableRow = PriceDisplayRow | PriceGroupRow

const PRICE_TABLE_FALLBACK_MAX_HEIGHT = 'max(240px, calc(100dvh - 360px))'
const MODEL_PRICE_GROUPING_STORAGE_KEY = 'cpa-helper-model-price-grouping-mode'
const priceModalStyle: CSSProperties = { width: 'min(720px, calc(100vw - 32px))' }
const priorityModalStyle: CSSProperties = { width: 'min(420px, calc(100vw - 32px))' }
const proxyModalStyle: CSSProperties = { width: 'min(460px, calc(100vw - 32px))' }
const proxyModalContentStyle: CSSProperties = { padding: '16px 22px 4px' }
const proxyModalFooterStyle: CSSProperties = { padding: '12px 22px 18px' }
const desktopPriceLayoutQuery = window.matchMedia('(min-width: 861px)')
const message = useMessage()
const dialog = useDialog()
const { errorText, serverText, t } = useI18n()
const isLoading = ref(false)
const isSyncing = ref(false)
const modalOpen = ref(false)
const proxyModalOpen = ref(false)
const priorityModalOpen = ref(false)
const isProxyLoading = ref(false)
const isProxySaving = ref(false)
const isPrioritySaving = ref(false)
const isPriceSaving = ref(false)
const editingId = ref<number | null>(null)
const priorityEditingPrice = ref<ModelPrice | null>(null)
const priorityMultiplier = ref<number | null>(null)
const prices = ref<ModelPrice[]>([])
const catalog = ref<ModelPriceCatalogResponse | null>(null)
const selectedProvider = ref<string | null>(null)
const selectedStatus = ref<PriceStatusFilter | null>(null)
const searchQuery = ref('')
const groupingMode = ref<PriceGroupingMode>(readPriceGroupingMode())
const expandedRowKeys = ref<DataTableRowKey[]>([])
const longContextEnabled = ref(false)
const isDesktopPriceLayout = ref(desktopPriceLayoutQuery.matches)
const pagination = reactive({
  page: 1,
  pageSize: 20,
  onUpdatePage: updatePricePage,
})
const form = reactive<ModelPricePayload>({
  provider: '',
  model: '',
  input_usd_per_million: 0,
  output_usd_per_million: 0,
  cache_read_usd_per_million: 0,
  cache_creation_usd_per_million: 0,
  request_usd: null,
  long_context: null,
})
const longContextForm = reactive<ModelPriceLongContext>({
  threshold_input_tokens: 200000,
  input_usd_per_million: 0,
  output_usd_per_million: 0,
  cache_read_usd_per_million: 0,
  cache_creation_usd_per_million: 0,
})
const proxyForm = reactive<LiteLLMProxySettingsPayload>({
  enabled: false,
  proxy_url: '',
})

function catalogModelReference(model: ModelPriceCatalogItem): CatalogModelReference {
  return {
    id: model.id,
    name: model.name || model.id,
    owner: model.owner,
    suggestedProvider: model.suggested_provider,
  }
}

function pricedDisplayRow(
  price: ModelPrice,
  catalogModels: CatalogModelReference[],
): PriceDisplayRow {
  const catalogModel = catalogModels.length === 1 ? catalogModels[0] : null
  return {
    rowType: 'detail',
    key: `price:${price.id}`,
    in_cpa: catalogModels.length > 0,
    id: price.model,
    name: catalogModel?.name || price.model,
    owner: catalogModel?.owner ?? null,
    suggested_provider: catalogModel?.suggestedProvider ?? '',
    price,
    provider: price.provider,
    model: price.model,
    comparisonModelKey: price.model,
    catalogModels,
    billing_unit: billingUnitForPrice(price, price.model),
    status: priceStatus(price, price.model),
  }
}

function unpricedCatalogDisplayRow(model: ModelPriceCatalogItem): PriceDisplayRow {
  const provider = model.suggested_provider || model.owner || providerFromModelId(model.id)
  return {
    rowType: 'detail',
    key: `catalog:${model.id}`,
    in_cpa: true,
    id: model.id,
    name: model.name || model.id,
    owner: model.owner,
    suggested_provider: model.suggested_provider,
    price: null,
    provider,
    model: model.id,
    comparisonModelKey: model.id,
    catalogModels: [catalogModelReference(model)],
    billing_unit: billingUnitForPrice(null, model.id),
    status: 'missing',
  }
}

const priceRows = computed<PriceDisplayRow[]>(() => {
  const rows: PriceDisplayRow[] = []
  const catalogModels = catalog.value?.models ?? []
  const catalogModelsByPriceId = new Map<number, CatalogModelReference[]>()
  const renderedPriceIds = new Set<number>()

  for (const model of catalogModels) {
    if (!model.price) {
      continue
    }
    const references = catalogModelsByPriceId.get(model.price.id)
    if (references) {
      references.push(catalogModelReference(model))
    } else {
      catalogModelsByPriceId.set(model.price.id, [catalogModelReference(model)])
    }
  }

  for (const model of catalogModels) {
    if (!model.price) {
      rows.push(unpricedCatalogDisplayRow(model))
      continue
    }
    if (renderedPriceIds.has(model.price.id)) {
      continue
    }
    const references = catalogModelsByPriceId.get(model.price.id)
    if (!references) {
      throw new Error(`Catalog price ${model.price.id} is missing its model references`)
    }
    rows.push(pricedDisplayRow(model.price, references))
    renderedPriceIds.add(model.price.id)
  }

  for (const price of prices.value) {
    if (renderedPriceIds.has(price.id)) {
      continue
    }
    rows.push(pricedDisplayRow(price, []))
  }
  return rows
})

const providerOptions = computed(() =>
  [...new Set(priceRows.value.map((row) => row.provider).filter(Boolean))]
    .sort((a, b) => a.localeCompare(b))
    .map((provider) => ({ label: provider, value: provider })),
)

const liteLLMProxyHint = computed(() =>
  t(
    'LiteLLM 价格数据从 GitHub 下载；如果当前网络无法访问 GitHub，可以启用代理后再同步。',
    'LiteLLM price data is downloaded from GitHub. If GitHub is not reachable from this network, enable a proxy and sync again.',
  ),
)

const statusOptions = computed<Array<{ label: string; value: PriceStatusFilter }>>(() => [
  { label: t('CPA 可用模型', 'CPA available models'), value: 'cpa' },
  { label: t('未定价', 'Unpriced'), value: 'missing' },
  { label: 'LiteLLM', value: 'litellm' },
  { label: t('手动', 'Manual'), value: 'manual' },
  { label: t('仅有价格', 'Prices only'), value: 'library' },
])

const groupingOptions = computed<Array<{ label: string; value: PriceGroupingMode }>>(() => [
  { label: t('按模型', 'By model'), value: 'model' },
  { label: t('按渠道', 'By provider'), value: 'provider' },
])

const filteredPrices = computed(() => {
  return priceRows.value.filter((row) => {
    if (selectedProvider.value && row.provider !== selectedProvider.value) {
      return false
    }
    if (selectedStatus.value && !rowMatchesStatus(row, selectedStatus.value)) {
      return false
    }
    return priceMatchesSearch(row)
  })
})

const priceTableRows = computed<PriceGroupRow[]>(() =>
  groupPriceRows(filteredPrices.value, groupingMode.value),
)

watch([selectedProvider, selectedStatus, searchQuery], () => {
  pagination.page = 1
  expandedRowKeys.value = []
})

watch(groupingMode, (value) => {
  pagination.page = 1
  expandedRowKeys.value = []
  savePriceGroupingMode(value)
})

function renderSearchIcon() {
  return h(NIcon, { component: Search })
}

function updatePricePage(page: number) {
  pagination.page = page
}

function normalizePriceSearch(value: string) {
  return value.trim().toLowerCase()
}

function isPriceGroupingMode(value: string | null): value is PriceGroupingMode {
  return value === 'model' || value === 'provider'
}

function readPriceGroupingMode(): PriceGroupingMode {
  try {
    const storage = window.localStorage
    const value = storage.getItem(MODEL_PRICE_GROUPING_STORAGE_KEY)
    return isPriceGroupingMode(value) ? value : 'model'
  } catch {
    return 'model'
  }
}

function savePriceGroupingMode(value: PriceGroupingMode) {
  try {
    const storage = window.localStorage
    storage.setItem(MODEL_PRICE_GROUPING_STORAGE_KEY, value)
  } catch {
    // Keep the price page usable when local storage is unavailable.
  }
}

function uniqueNormalizedCount(values: string[]): number {
  return new Set(values.map((value) => normalizePriceSearch(value) || '__unknown__')).size
}

function latestPriceUpdate(children: PriceDisplayRow[]): string | null {
  let latest: string | null = null
  let latestTimestamp = Number.NEGATIVE_INFINITY
  for (const child of children) {
    const value = child.price?.updated_at
    if (!value) {
      continue
    }
    const timestamp = Date.parse(value)
    if (Number.isFinite(timestamp) && timestamp > latestTimestamp) {
      latest = value
      latestTimestamp = timestamp
    } else if (latest === null) {
      latest = value
    }
  }
  return latest
}

function createPriceGroupRow(
  mode: PriceGroupingMode,
  normalizedValue: string,
  label: string,
  children: PriceDisplayRow[],
): PriceGroupRow {
  const longContextEligibleRows = children.filter(
    (row) => row.billing_unit === 'token' && row.price !== null,
  )
  const priorityEligibleRows = children.filter((row) => supportsPriorityMultiplier(row.price))
  return {
    rowType: 'group',
    key: `group:${mode}:${normalizedValue}`,
    mode,
    label,
    children,
    providerCount: uniqueNormalizedCount(children.map((row) => row.provider)),
    modelCount: uniqueNormalizedCount(
      children.flatMap((row) =>
        row.catalogModels.length > 0
          ? row.catalogModels.map((model) => model.id)
          : [row.comparisonModelKey],
      ),
    ),
    pricedCount: children.filter((row) => row.status !== 'missing').length,
    unpricedCount: children.filter((row) => row.status === 'missing').length,
    billingUnits: [...new Set(children.map((row) => row.billing_unit))],
    longContextConfiguredCount: longContextEligibleRows.filter((row) => row.price?.long_context).length,
    longContextEligibleCount: longContextEligibleRows.length,
    priorityConfiguredCount: priorityEligibleRows.filter(
      (row) => typeof row.price?.priority_multiplier === 'number',
    ).length,
    priorityEligibleCount: priorityEligibleRows.length,
    latestUpdatedAt: latestPriceUpdate(children),
  }
}

function groupPriceRows(rows: PriceDisplayRow[], mode: PriceGroupingMode): PriceGroupRow[] {
  const groups = new Map<string, { label: string; children: PriceDisplayRow[] }>()
  for (const row of rows) {
    const rawValue = mode === 'model' ? row.comparisonModelKey : row.provider
    const normalizedValue = normalizePriceSearch(rawValue) || '__unknown__'
    const existing = groups.get(normalizedValue)
    if (existing) {
      existing.children.push(row)
      continue
    }
    groups.set(normalizedValue, {
      label:
        mode === 'provider' && !rawValue.trim()
          ? t('未识别服务商', 'Unknown provider')
          : rawValue,
      children: [row],
    })
  }
  return [...groups.entries()].map(([normalizedValue, group]) =>
    createPriceGroupRow(mode, normalizedValue, group.label, group.children),
  )
}

function isPriceGroupRow(row: PriceTableRow): row is PriceGroupRow {
  return row.rowType === 'group'
}

function pruneExpandedRowKeys() {
  const validKeys = new Set(priceTableRows.value.map((row) => row.key))
  expandedRowKeys.value = expandedRowKeys.value.filter((key) => validKeys.has(String(key)))
}

function providerFromModelId(modelId: string) {
  const separator = modelId.indexOf('/')
  return separator > 0 ? modelId.slice(0, separator) : ''
}

function billingUnitForModel(model: string): BillingUnit {
  return model.trim().toLowerCase().includes('image') ? 'request' : 'token'
}

function billingUnitForPrice(price: ModelPrice | null, fallbackModel: string): BillingUnit {
  if (price?.billing_unit === 'request') {
    return 'request'
  }
  if (price?.billing_unit === 'token') {
    return 'token'
  }
  return billingUnitForModel(price?.model || fallbackModel)
}

function priceReadyForBilling(price: ModelPrice, fallbackModel: string): boolean {
  return billingUnitForPrice(price, fallbackModel) === 'request' ? typeof price.request_usd === 'number' : true
}

function priceStatus(price: ModelPrice, fallbackModel: string): PriceRowStatus {
  if (!priceReadyForBilling(price, fallbackModel)) {
    return 'missing'
  }
  return price.auto_synced ? 'litellm' : 'manual'
}

function rowMatchesStatus(row: PriceDisplayRow, status: PriceStatusFilter) {
  switch (status) {
    case 'cpa':
      return row.in_cpa
    case 'library':
      return !row.in_cpa
    default:
      return row.status === status
  }
}

const normalizedSearchQuery = computed(() => normalizePriceSearch(searchQuery.value))

const filteredPriceCount = computed(() => filteredPrices.value.length)
const filteredGroupCount = computed(() => priceTableRows.value.length)

const totalPriceCount = computed(() => priceRows.value.length)
const cpaModelCount = computed(() => catalog.value?.models.length ?? 0)
const unpricedModelCount = computed(
  () => catalog.value?.unpriced_models ?? priceRows.value.filter((row) => row.in_cpa && row.status === 'missing').length,
)
const syncedPriceCount = computed(() => prices.value.filter((price) => price.auto_synced).length)
const manualPriceCount = computed(() => prices.value.filter((price) => !price.auto_synced).length)
const catalogNotice = computed(() => {
  const current = catalog.value
  if (!current) {
    return ''
  }
  if (!current.has_api_keys) {
    return t('还没有本地绑定的 API Key，当前只显示已有价格库条目。', 'No local API keys are bound yet. Only existing price library entries are shown.')
  }
  if (current.queryable_api_key_count === 0) {
    return t(
      '本地 API Key 没有保存明文 Key，暂时无法查询 CPA 当前模型，只显示已有价格库条目。',
      'Local API keys do not store plaintext keys, so CPA models cannot be queried for now. Only existing price library entries are shown.',
    )
  }
  if (current.errors.length > 0) {
    const details = current.errors
      .slice(0, 3)
      .map((item) =>
        t(
          `${item.description}：${serverText(item.message, '查询失败', 'Query failed')}`,
          `${item.description}: ${serverText(item.message, '查询失败', 'Query failed')}`,
        ),
      )
      .join(t('；', '; '))
    return t(`部分 Key 查询 CPA 模型失败：${details}`, `Some keys failed to query CPA models: ${details}`)
  }
  return ''
})
const priceTableLayoutProps = computed<PriceTableLayoutProps>(() =>
  isDesktopPriceLayout.value
    ? { flexHeight: true }
    : { flexHeight: false, maxHeight: PRICE_TABLE_FALLBACK_MAX_HEIGHT },
)
const isRequestPriceForm = computed(() => billingUnitForModel(form.model) === 'request')
const priceSaveHint = computed(() =>
  isRequestPriceForm.value
    ? t(
        'image 模型按每次成功调用固定金额计费，保存后会作为手动价格优先保留。',
        'Image models are charged a fixed amount per successful call. Saved values are kept as manual prices with priority.',
      )
    : t(
        '保存后会作为手动价格，后续 LiteLLM 同步会优先保留。',
        'Saved values are kept as manual prices and preserved by later LiteLLM syncs.',
      ),
)

interface PriceMetricCard {
  key: string
  label: string
  value: string
  footnote: string
  tone: 'teal' | 'blue' | 'purple' | 'orange'
  icon: Component
}

const priceMetrics = computed<PriceMetricCard[]>(() => [
  {
    key: 'models',
    label: t('CPA 模型', 'CPA models'),
    value: formatInteger(cpaModelCount.value),
    footnote: catalog.value
      ? t(
          `可查询 Key ${formatInteger(catalog.value.queryable_api_key_count)} / ${formatInteger(catalog.value.api_key_count)}`,
          `Queryable keys ${formatInteger(catalog.value.queryable_api_key_count)} / ${formatInteger(catalog.value.api_key_count)}`,
        )
      : t('等待刷新', 'Waiting for refresh'),
    tone: 'teal',
    icon: Layers3,
  },
  {
    key: 'unpriced',
    label: t('未定价', 'Unpriced'),
    value: formatInteger(unpricedModelCount.value),
    footnote: t(
      `筛选后 ${formatInteger(filteredPriceCount.value)} / ${formatInteger(totalPriceCount.value)}`,
      `Filtered ${formatInteger(filteredPriceCount.value)} / ${formatInteger(totalPriceCount.value)}`,
    ),
    tone: 'blue',
    icon: Server,
  },
  {
    key: 'synced',
    label: t('LiteLLM 同步', 'LiteLLM sync'),
    value: formatInteger(syncedPriceCount.value),
    footnote: t('自动维护', 'Auto maintained'),
    tone: 'purple',
    icon: RefreshCw,
  },
  {
    key: 'manual',
    label: t('手动价格', 'Manual prices'),
    value: formatInteger(manualPriceCount.value),
    footnote: t('优先保留', 'Preserved first'),
    tone: 'orange',
    icon: Database,
  },
])

function priceMatchesSearch(row: PriceDisplayRow) {
  if (!normalizedSearchQuery.value) {
    return true
  }
  return (
    row.provider.toLowerCase().includes(normalizedSearchQuery.value) ||
    row.model.toLowerCase().includes(normalizedSearchQuery.value) ||
    row.id.toLowerCase().includes(normalizedSearchQuery.value) ||
    row.name.toLowerCase().includes(normalizedSearchQuery.value) ||
    (row.owner ?? '').toLowerCase().includes(normalizedSearchQuery.value) ||
    row.suggested_provider.toLowerCase().includes(normalizedSearchQuery.value) ||
    row.catalogModels.some(
      (model) =>
        model.id.toLowerCase().includes(normalizedSearchQuery.value) ||
        model.name.toLowerCase().includes(normalizedSearchQuery.value) ||
        (model.owner ?? '').toLowerCase().includes(normalizedSearchQuery.value) ||
        model.suggestedProvider.toLowerCase().includes(normalizedSearchQuery.value),
    )
  )
}

function resetForm() {
  editingId.value = null
  form.provider = ''
  form.model = ''
  form.input_usd_per_million = 0
  form.output_usd_per_million = 0
  form.cache_read_usd_per_million = 0
  form.cache_creation_usd_per_million = 0
  form.request_usd = null
  form.long_context = null
  longContextEnabled.value = false
  longContextForm.threshold_input_tokens = 200000
  longContextForm.input_usd_per_million = 0
  longContextForm.output_usd_per_million = 0
  longContextForm.cache_read_usd_per_million = 0
  longContextForm.cache_creation_usd_per_million = 0
}

async function refresh() {
  isLoading.value = true
  try {
    const [nextPrices, nextCatalog] = await Promise.all([listModelPrices(), listModelPriceCatalog()])
    prices.value = nextPrices
    catalog.value = nextCatalog
    pruneExpandedRowKeys()
  } catch (error) {
    message.error(errorText(error, '加载模型价格失败', 'Failed to load model prices'))
  } finally {
    isLoading.value = false
  }
}

function openCreate(prefill: Partial<ModelPricePayload> = {}) {
  resetForm()
  form.provider = prefill.provider ?? ''
  form.model = prefill.model ?? ''
  form.input_usd_per_million = prefill.input_usd_per_million ?? 0
  form.output_usd_per_million = prefill.output_usd_per_million ?? 0
  form.cache_read_usd_per_million = prefill.cache_read_usd_per_million ?? 0
  form.cache_creation_usd_per_million = prefill.cache_creation_usd_per_million ?? 0
  form.request_usd = prefill.request_usd ?? null
  if (prefill.long_context) {
    setLongContextForm(prefill.long_context)
  }
  modalOpen.value = true
}

function openCreateForRow(row: PriceDisplayRow) {
  openCreate({
    provider: row.provider || row.suggested_provider || row.owner || '',
    model: row.id,
  })
}

function openEdit(row: ModelPrice) {
  resetForm()
  editingId.value = row.id
  form.provider = row.provider
  form.model = row.model
  form.input_usd_per_million = row.input_usd_per_million
  form.output_usd_per_million = row.output_usd_per_million
  form.cache_read_usd_per_million = row.cache_read_usd_per_million
  form.cache_creation_usd_per_million = row.cache_creation_usd_per_million
  form.request_usd = row.request_usd
  if (row.long_context) {
    setLongContextForm(row.long_context)
  }
  modalOpen.value = true
}

function setLongContextForm(value: ModelPriceLongContext) {
  longContextEnabled.value = true
  longContextForm.threshold_input_tokens = value.threshold_input_tokens
  longContextForm.input_usd_per_million = value.input_usd_per_million
  longContextForm.output_usd_per_million = value.output_usd_per_million
  longContextForm.cache_read_usd_per_million = value.cache_read_usd_per_million
  longContextForm.cache_creation_usd_per_million = value.cache_creation_usd_per_million
}

function validLongContextForm(): boolean {
  return (
    Number.isInteger(longContextForm.threshold_input_tokens) &&
    longContextForm.threshold_input_tokens > 0 &&
    [
      longContextForm.input_usd_per_million,
      longContextForm.output_usd_per_million,
      longContextForm.cache_read_usd_per_million,
      longContextForm.cache_creation_usd_per_million,
    ].every((value) => Number.isFinite(value) && value >= 0)
  )
}

async function savePrice() {
  const requestPriceMode = isRequestPriceForm.value
  const requestUSD = requestPriceMode && typeof form.request_usd === 'number' ? form.request_usd : null
  const longContext = !requestPriceMode && longContextEnabled.value
    ? { ...longContextForm }
    : null
  const payload: ModelPricePayload = {
    provider: form.provider.trim(),
    model: form.model.trim(),
    input_usd_per_million: form.input_usd_per_million,
    output_usd_per_million: form.output_usd_per_million,
    cache_read_usd_per_million: form.cache_read_usd_per_million,
    cache_creation_usd_per_million: form.cache_creation_usd_per_million,
    request_usd: requestUSD,
    long_context: longContext,
  }
  if (!payload.provider || !payload.model) {
    message.error(t('服务商和模型不能为空', 'Provider and model are required'))
    return
  }
  if (requestPriceMode && requestUSD === null) {
    message.error(t('image 模型需要填写每次调用价格', 'Image models require a per-call price'))
    return
  }
  if (longContext !== null && !validLongContextForm()) {
    message.error(t('长上下文阈值必须为正整数，价格必须是有限非负数', 'The long-context threshold must be a positive integer and all prices must be finite non-negative numbers'))
    return
  }
  isPriceSaving.value = true
  try {
    if (editingId.value === null) {
      await createModelPrice(payload)
      message.success(t('模型价格已创建', 'Model price created'))
    } else {
      await updateModelPrice(editingId.value, payload)
      message.success(t('模型价格已更新', 'Model price updated'))
    }
    modalOpen.value = false
    await refresh()
  } catch (error) {
    message.error(errorText(error, '保存模型价格失败', 'Failed to save model price'))
  } finally {
    isPriceSaving.value = false
  }
}

function supportsPriorityMultiplier(price: ModelPrice | null): boolean {
  if (!price) {
    return false
  }
  const provider = price.provider.trim().toLowerCase()
  return provider === 'openai' || provider === 'codex'
}

function openPriorityMultiplierEditor(price: ModelPrice) {
  priorityEditingPrice.value = price
  priorityMultiplier.value = price.priority_multiplier
  priorityModalOpen.value = true
}

async function savePriorityMultiplier() {
  const price = priorityEditingPrice.value
  const multiplier = priorityMultiplier.value
  if (!price || multiplier === null || !Number.isFinite(multiplier) || multiplier <= 0) {
    message.error(t('Fast 倍率必须大于 0', 'Fast multiplier must be greater than 0'))
    return
  }
  isPrioritySaving.value = true
  try {
    await updateModelPricePriorityMultiplier(price.id, { priority_multiplier: multiplier })
    priorityModalOpen.value = false
    message.success(t('Fast 倍率已更新', 'Fast multiplier updated'))
    await refresh()
  } catch (error) {
    message.error(errorText(error, '保存 Fast 倍率失败', 'Failed to save Fast multiplier'))
  } finally {
    isPrioritySaving.value = false
  }
}

async function syncPrices() {
  isSyncing.value = true
  try {
    const result = await syncLitellmModelPrices()
    message.success(
      t(
        `同步完成：LiteLLM 价格 ${result.imported} 条，手动价格保留 ${result.skipped_manual} 条`,
        `Sync complete: ${result.imported} LiteLLM prices imported, ${result.skipped_manual} manual prices preserved`,
      ),
    )
    await refresh()
  } catch (error) {
    const detail = errorText(error, '同步模型价格失败', 'Failed to sync model prices')
    message.error(t(`${detail}。${liteLLMProxyHint.value}`, `${detail}. ${liteLLMProxyHint.value}`))
  } finally {
    isSyncing.value = false
  }
}

async function openProxySettings() {
  proxyModalOpen.value = true
  isProxyLoading.value = true
  try {
    const settings = await getLiteLLMProxySettings()
    proxyForm.enabled = settings.enabled
    proxyForm.proxy_url = settings.proxy_url
  } catch (error) {
    message.error(errorText(error, '加载代理配置失败', 'Failed to load proxy settings'))
  } finally {
    isProxyLoading.value = false
  }
}

async function saveProxySettings() {
  const payload: LiteLLMProxySettingsPayload = {
    enabled: proxyForm.enabled,
    proxy_url: proxyForm.proxy_url.trim(),
  }
  if (payload.enabled && !payload.proxy_url) {
    message.error(t('启用代理时必须填写代理地址', 'Proxy URL is required when proxy is enabled'))
    return
  }
  isProxySaving.value = true
  try {
    const saved = await updateLiteLLMProxySettings(payload)
    proxyForm.enabled = saved.enabled
    proxyForm.proxy_url = saved.proxy_url
    proxyModalOpen.value = false
    message.success(t('代理配置已保存', 'Proxy settings saved'))
  } catch (error) {
    message.error(errorText(error, '保存代理配置失败', 'Failed to save proxy settings'))
  } finally {
    isProxySaving.value = false
  }
}

function confirmDelete(row: ModelPrice) {
  dialog.warning({
    title: t('删除价格', 'Delete price'),
    content: `${row.provider} / ${row.model}`,
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      await deleteModelPrice(row.id)
      message.success(t('模型价格已删除', 'Model price deleted'))
      await refresh()
    },
  })
}

function handleDesktopPriceLayoutChange(event: MediaQueryListEvent) {
  isDesktopPriceLayout.value = event.matches
}

function rowKey(row: PriceTableRow): DataTableRowKey {
  return row.key
}

function priceRowClassName(row: PriceTableRow) {
  return isPriceGroupRow(row) ? 'price-group-row' : 'price-detail-row'
}

function formatPriceValue(value: number | null | undefined) {
  return typeof value === 'number' ? String(value) : '-'
}

function summarizePriceValues(values: number[]): string {
  const finiteValues = values.filter((value) => Number.isFinite(value))
  if (finiteValues.length === 0) {
    return '-'
  }
  const minimum = Math.min(...finiteValues)
  const maximum = Math.max(...finiteValues)
  return minimum === maximum
    ? formatPriceValue(minimum)
    : `${formatPriceValue(minimum)} - ${formatPriceValue(maximum)}`
}

function renderBillingUnitBadge(unit: BillingUnit | 'mixed') {
  const isRequest = unit === 'request'
  const isMixed = unit === 'mixed'
  return h(
    'span',
    {
      style: {
        display: 'inline-flex',
        alignItems: 'center',
        minHeight: '22px',
        padding: '2px 8px',
        borderRadius: '6px',
        background: isMixed
          ? 'color-mix(in srgb, var(--cpa-text-muted) 12%, transparent)'
          : isRequest
            ? 'rgba(16, 185, 129, 0.13)'
            : 'rgba(124, 58, 237, 0.12)',
        color: isMixed ? 'var(--cpa-text-muted)' : isRequest ? '#047857' : '#6d28d9',
        fontSize: '12px',
        fontWeight: '600',
        lineHeight: '1.2',
      },
    },
    isMixed ? t('混合', 'Mixed') : isRequest ? t('按次', 'Per call') : t('按 Token', 'Per token'),
  )
}

function renderBillingUnitCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    return row.billingUnits.length === 1 && row.billingUnits[0]
      ? renderBillingUnitBadge(row.billingUnits[0])
      : renderBillingUnitBadge('mixed')
  }
  return renderBillingUnitBadge(row.billing_unit)
}

function renderTokenPriceValue(row: PriceTableRow, field: PriceFieldName) {
  if (isPriceGroupRow(row)) {
    if (row.mode === 'provider') {
      return h('span', { class: 'price-muted' }, '-')
    }
    const values = row.children
      .filter((child) => child.billing_unit === 'token' && child.price !== null)
      .map((child) => child.price?.[field])
      .filter((value): value is number => typeof value === 'number')
    return h('span', { class: 'price-group-range' }, summarizePriceValues(values))
  }
  if (row.billing_unit === 'request') {
    return h('span', { class: 'price-muted' }, '-')
  }
  return formatPriceValue(row.price?.[field])
}

function renderRequestPriceValue(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    if (row.mode === 'provider') {
      return h('span', { class: 'price-muted' }, '-')
    }
    const values = row.children
      .filter((child) => child.billing_unit === 'request')
      .map((child) => child.price?.request_usd)
      .filter((value): value is number => typeof value === 'number')
    return h('span', { class: 'price-group-range' }, summarizePriceValues(values))
  }
  if (row.billing_unit !== 'request') {
    return h('span', { class: 'price-muted' }, '-')
  }
  if (row.price?.request_usd === null || row.price?.request_usd === undefined) {
    return h('span', { class: 'price-muted' }, t('未定价', 'Unpriced'))
  }
  return formatPriceValue(row.price.request_usd)
}

function renderConfigurationSummary(
  configured: number,
  eligible: number,
  chineseLabel: string,
  englishLabel: string,
) {
  if (eligible === 0) {
    return h('span', { class: 'price-muted' }, '-')
  }
  const value = `${formatInteger(configured)}/${formatInteger(eligible)}`
  return h(
    NTooltip,
    null,
    {
      trigger: () =>
        h(
          NTag,
          { size: 'small', type: configured > 0 ? 'info' : 'default', bordered: false },
          { default: () => value },
        ),
      default: () => t(`${chineseLabel}：${value}`, `${englishLabel}: ${value}`),
    },
  )
}

function renderPriorityMultiplier(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    return renderConfigurationSummary(
      row.priorityConfiguredCount,
      row.priorityEligibleCount,
      '已配置 Fast 倍率',
      'Fast multipliers configured',
    )
  }
  if (!supportsPriorityMultiplier(row.price)) {
    return h('span', { class: 'price-muted' }, '-')
  }
  const multiplier = row.price?.priority_multiplier
  if (typeof multiplier !== 'number') {
    return h(
      NTag,
      { size: 'small', type: 'warning', bordered: false },
      { default: () => t('未配置', 'Unconfigured') },
    )
  }
  return h(
    NTag,
    { size: 'small', type: 'warning', bordered: false },
    { default: () => 'Fast x' + formatMultiplier(multiplier) },
  )
}

function formatThreshold(value: number): string {
  if (value >= 1000000 && value % 1000000 === 0) {
    return `${value / 1000000}M`
  }
  if (value >= 1000 && value % 1000 === 0) {
    return `${value / 1000}K`
  }
  return formatInteger(value)
}

function renderLongContextPrice(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    return renderConfigurationSummary(
      row.longContextConfiguredCount,
      row.longContextEligibleCount,
      '已配置长上下文价格',
      'Long-context prices configured',
    )
  }
  const longContext = row.price?.long_context
  if (row.billing_unit === 'request' || !longContext) {
    return h('span', { class: 'price-muted' }, '-')
  }
  const label = `>${formatThreshold(longContext.threshold_input_tokens)}`
  const details = [
    `${t('输入', 'Input')} ${formatPriceValue(longContext.input_usd_per_million)}`,
    `${t('输出', 'Output')} ${formatPriceValue(longContext.output_usd_per_million)}`,
    `${t('缓存读', 'Cache read')} ${formatPriceValue(longContext.cache_read_usd_per_million)}`,
    `${t('缓存写', 'Cache write')} ${formatPriceValue(longContext.cache_creation_usd_per_million)}`,
  ].join(' · ')
  return h(
    NTooltip,
    null,
    {
      trigger: () => h(NTag, { size: 'small', type: 'info', bordered: false }, { default: () => label }),
      default: () => `${label} ${t('输入 Token', 'input tokens')}: ${details}`,
    },
  )
}

function renderPriorityMultiplierAction(price: ModelPrice) {
  const label = t('设置 Fast 倍率', 'Set Fast multiplier')
  return h(
    NTooltip,
    null,
    {
      trigger: () =>
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            'aria-label': label,
            onClick: () => openPriorityMultiplierEditor(price),
          },
          { icon: () => h(NIcon, { component: Zap }) },
        ),
      default: () => label,
    },
  )
}

function modelDetailSubline(row: PriceDisplayRow): string {
  const catalogModelIds = row.catalogModels.map((model) => model.id)
  const hasDistinctCatalogIdentity =
    row.price !== null &&
    (catalogModelIds.length > 1 ||
      (catalogModelIds.length === 1 && catalogModelIds[0] !== row.id))
  if (hasDistinctCatalogIdentity) {
    return t(
      `CPA 模型：${catalogModelIds.join('、')}`,
      `CPA models: ${catalogModelIds.join(', ')}`,
    )
  }
  return row.name && row.name !== row.id ? row.name : ''
}

function renderModelCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    const title =
      row.mode === 'model'
        ? row.label
        : t(`${formatInteger(row.modelCount)} 个模型`, `${formatInteger(row.modelCount)} models`)
    const details =
      row.mode === 'model'
        ? t(
            `${formatInteger(row.providerCount)} 个服务商 · ${formatInteger(row.children.length)} 条明细`,
            `${formatInteger(row.providerCount)} providers · ${formatInteger(row.children.length)} details`,
          )
        : t(
            `${formatInteger(row.children.length)} 条价格明细`,
            `${formatInteger(row.children.length)} price details`,
          )
    return h('div', { class: 'price-group-cell' }, [
      h('div', { class: 'price-group-title' }, title),
      h('div', { class: 'price-group-sub' }, details),
    ])
  }
  const subline = modelDetailSubline(row)
  return h('div', { class: 'model-cell' }, [
    h('div', { class: 'model-title-row' }, [
      h('span', { class: 'model-name' }, row.id),
      row.in_cpa
        ? h(
            NTag,
            {
              class: 'model-availability-tag',
              size: 'small',
              type: 'success',
              bordered: false,
              style: { marginLeft: '16px' },
            },
            { default: () => t('CPA 可用模型', 'CPA available model') },
          )
        : null,
    ]),
    subline ? h('div', { class: 'model-sub' }, subline) : null,
  ])
}

function renderProviderCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    const title =
      row.mode === 'provider'
        ? row.label
        : t(`${formatInteger(row.providerCount)} 个服务商`, `${formatInteger(row.providerCount)} providers`)
    const details =
      row.mode === 'provider'
        ? t(
            `${formatInteger(row.modelCount)} 个模型`,
            `${formatInteger(row.modelCount)} models`,
          )
        : null
    return h('div', { class: 'price-group-cell' }, [
      h('div', { class: 'price-group-title' }, title),
      details ? h('div', { class: 'price-group-sub' }, details) : null,
    ])
  }
  return h('div', { class: 'provider-cell' }, [
    h('div', { class: 'provider-main' }, row.provider || '-'),
    row.owner && row.owner !== row.provider
      ? h('div', { class: 'model-sub' }, t('所有者', 'Owner') + `: ${row.owner}`)
      : null,
  ])
}

function renderStatusCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    return h('div', { class: 'price-group-status' }, [
      h(
        'strong',
        t(
          `${formatInteger(row.pricedCount)} 已定价`,
          `${formatInteger(row.pricedCount)} priced`,
        ),
      ),
      h(
        'span',
        row.unpricedCount > 0
          ? t(
              `${formatInteger(row.unpricedCount)} 未定价`,
              `${formatInteger(row.unpricedCount)} unpriced`,
            )
          : t('全部完成', 'Complete'),
      ),
    ])
  }
  const label = row.status === 'missing' ? t('未定价', 'Unpriced') : row.status === 'litellm' ? 'LiteLLM' : t('手动', 'Manual')
  const type = row.status === 'missing' ? 'warning' : row.status === 'litellm' ? 'info' : 'default'
  return h(
    NTag,
    { size: 'small', type, bordered: false },
    { default: () => label },
  )
}

function renderUpdatedCell(row: PriceTableRow) {
  const updatedAt = isPriceGroupRow(row) ? row.latestUpdatedAt : row.price?.updated_at
  return updatedAt ? formatDateTime(updatedAt) : '-'
}

function renderActionsCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    return null
  }
  return h(
    NSpace,
    { size: 4 },
    {
      default: () => [
        ...(row.price && supportsPriorityMultiplier(row.price)
          ? [renderPriorityMultiplierAction(row.price)]
          : []),
        row.price
          ? h(
              NButton,
              { size: 'small', quaternary: true, onClick: () => openEdit(row.price as ModelPrice) },
              { default: () => t('改价', 'Edit') },
            )
          : h(
              NButton,
              { size: 'small', type: 'primary', secondary: true, onClick: () => openCreateForRow(row) },
              { default: () => t('设价', 'Set price') },
            ),
        row.price
          ? h(
              NButton,
              { size: 'small', quaternary: true, type: 'error', onClick: () => confirmDelete(row.price as ModelPrice) },
              { default: () => t('删除', 'Delete') },
            )
          : null,
      ],
    },
  )
}

const columns = computed<DataTableColumns<PriceTableRow>>(() => [
  {
    title: t('模型', 'Model'),
    key: 'id',
    width: 380,
    ellipsis: { tooltip: true },
    render: renderModelCell,
  },
  {
    title: t('服务商', 'Provider'),
    key: 'provider',
    width: 160,
    ellipsis: { tooltip: true },
    render: renderProviderCell,
  },
  {
    title: t('定价', 'Pricing'),
    key: 'status',
    width: 120,
    render: renderStatusCell,
  },
  {
    title: t('计费方式', 'Billing'),
    key: 'billing_unit',
    width: 100,
    render: renderBillingUnitCell,
  },
  {
    title: t('每次 ($)', 'Per call ($)'),
    key: 'request_usd',
    width: 110,
    render: renderRequestPriceValue,
  },
  {
    title: t('输入 ($/MTok)', 'Input ($/MTok)'),
    key: 'input_usd_per_million',
    width: 125,
    render: (row) => renderTokenPriceValue(row, 'input_usd_per_million'),
  },
  {
    title: t('输出 ($/MTok)', 'Output ($/MTok)'),
    key: 'output_usd_per_million',
    width: 125,
    render: (row) => renderTokenPriceValue(row, 'output_usd_per_million'),
  },
  {
    title: t('缓存读 ($/MTok)', 'Cache read ($/MTok)'),
    key: 'cache_read_usd_per_million',
    width: 125,
    render: (row) => renderTokenPriceValue(row, 'cache_read_usd_per_million'),
  },
  {
    title: t('缓存写 ($/MTok)', 'Cache write ($/MTok)'),
    key: 'cache_creation_usd_per_million',
    width: 125,
    render: (row) => renderTokenPriceValue(row, 'cache_creation_usd_per_million'),
  },
  {
    title: t('长上下文', 'Long context'),
    key: 'long_context',
    width: 118,
    render: renderLongContextPrice,
  },
  {
    title: t('Fast 倍率', 'Fast multiplier'),
    key: 'priority_multiplier',
    width: 116,
    render: renderPriorityMultiplier,
  },
  {
    title: t('更新', 'Updated'),
    key: 'updated_at',
    width: 140,
    render: renderUpdatedCell,
  },
  {
    title: '',
    key: 'actions',
    width: 168,
    fixed: 'right',
    render: renderActionsCell,
  },
])

onMounted(() => {
  desktopPriceLayoutQuery.addEventListener('change', handleDesktopPriceLayoutChange)
  void refresh()
})

onBeforeUnmount(() => {
  desktopPriceLayoutQuery.removeEventListener('change', handleDesktopPriceLayoutChange)
})
</script>

<template>
  <section class="page price-page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('模型价格', 'Model prices') }}</h1>
        <p class="page-subtitle">
          {{ t('Token 模型按 USD / 百万 Token 计费，image 模型按每次成功调用计费', 'Token models are charged in USD per million tokens. Image models are charged per successful call.') }}
        </p>
      </div>
      <NSpace>
        <NButton secondary :loading="isSyncing" @click="syncPrices">
          <template #icon>
            <NIcon :component="RefreshCw" />
          </template>
          {{ t('同步 LiteLLM', 'Sync LiteLLM') }}
        </NButton>
        <NButton secondary :disabled="isSyncing" @click="openProxySettings">
          <template #icon>
            <NIcon :component="Settings2" />
          </template>
          {{ t('代理配置', 'Proxy settings') }}
        </NButton>
        <NButton type="primary" @click="() => openCreate()">{{ t('新增价格', 'Add price') }}</NButton>
      </NSpace>
    </div>

    <div class="metric-grid price-metrics">
      <div v-for="metric in priceMetrics" :key="metric.key" class="metric-card" :class="`is-${metric.tone}`">
        <div class="metric-icon" aria-hidden="true">
          <component :is="metric.icon" :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ metric.label }}</div>
        <div class="metric-value">{{ metric.value }}</div>
        <div class="metric-footnote">{{ metric.footnote }}</div>
      </div>
    </div>

    <section class="panel table-panel price-table-panel">
      <div class="price-table-top">
        <NAlert v-if="catalogNotice" class="price-alert" type="warning" :show-icon="false">
          {{ catalogNotice }}
        </NAlert>
        <div class="table-toolbar">
          <NSpace class="price-toolbar-layout" justify="space-between" align="center">
            <NSpace class="price-filters" align="center" :size="8">
              <div class="price-grouping-control">
                <span class="price-grouping-label">{{ t('视角', 'View') }}</span>
                <NRadioGroup v-model:value="groupingMode" size="small">
                  <NRadioButton
                    v-for="option in groupingOptions"
                    :key="option.value"
                    :value="option.value"
                  >
                    {{ option.label }}
                  </NRadioButton>
                </NRadioGroup>
              </div>
              <span class="filter-label">{{ t('服务商', 'Provider') }}</span>
              <NSelect
                v-model:value="selectedProvider"
                class="provider-filter"
                :options="providerOptions"
                clearable
                filterable
                :placeholder="t('全部服务商', 'All providers')"
              />
              <NSelect
                v-model:value="selectedStatus"
                class="status-filter"
                :options="statusOptions"
                clearable
                :placeholder="t('全部状态', 'All statuses')"
              />
              <NInput
                v-model:value="searchQuery"
                class="price-search"
                clearable
                :placeholder="t('搜索模型或服务商', 'Search models or providers')"
                :render-prefix="renderSearchIcon"
              />
            </NSpace>
            <span class="result-count">
              {{ t(
                `共 ${filteredGroupCount} 组 · ${filteredPriceCount} / ${totalPriceCount} 条明细`,
                `${filteredGroupCount} groups · ${filteredPriceCount} / ${totalPriceCount} details`,
              ) }}
            </span>
          </NSpace>
        </div>
      </div>
      <NDataTable
        v-model:expanded-row-keys="expandedRowKeys"
        class="price-table"
        v-bind="priceTableLayoutProps"
        size="small"
        :loading="isLoading"
        :columns="columns"
        :data="priceTableRows"
        :pagination="pagination"
        :row-key="rowKey"
        :row-class-name="priceRowClassName"
        :indent="18"
        :scroll-x="2040"
      />
    </section>

    <NModal
      v-model:show="modalOpen"
      preset="card"
      :title="editingId === null ? t('新增价格', 'Add price') : t('编辑价格', 'Edit price')"
      :style="priceModalStyle"
      class="price-modal"
    >
      <NForm :model="form" label-placement="top">
        <div class="form-grid">
          <NFormItem :label="t('服务商', 'Provider')">
            <NInput v-model:value="form.provider" />
          </NFormItem>
          <NFormItem :label="t('模型', 'Model')">
            <NInput v-model:value="form.model" />
          </NFormItem>
          <NFormItem v-if="isRequestPriceForm" :label="t('每次调用价格 USD', 'Per-call price USD')" class="wide-form-item">
            <NInputNumber v-model:value="form.request_usd" :min="0" :placeholder="t('例如：0.04', 'Example: 0.04')" />
          </NFormItem>
          <template v-else>
            <NFormItem :label="t('输入价格', 'Input price')">
              <NInputNumber v-model:value="form.input_usd_per_million" :min="0" />
            </NFormItem>
            <NFormItem :label="t('输出价格', 'Output price')">
              <NInputNumber v-model:value="form.output_usd_per_million" :min="0" />
            </NFormItem>
            <NFormItem :label="t('缓存读价格', 'Cache read price')">
              <NInputNumber v-model:value="form.cache_read_usd_per_million" :min="0" />
            </NFormItem>
            <NFormItem :label="t('缓存写价格', 'Cache write price')">
              <NInputNumber v-model:value="form.cache_creation_usd_per_million" :min="0" />
            </NFormItem>
            <div class="long-context-switch-row wide-form-item">
              <span>{{ t('长上下文阶梯', 'Long-context tier') }}</span>
              <NSwitch
                v-model:value="longContextEnabled"
                :disabled="isPriceSaving"
                :aria-label="t('启用长上下文阶梯', 'Enable long-context tier')"
              />
            </div>
            <template v-if="longContextEnabled">
              <NFormItem :label="t('输入 Token 阈值', 'Input token threshold')" class="wide-form-item">
                <NInputNumber
                  v-model:value="longContextForm.threshold_input_tokens"
                  :min="1"
                  :precision="0"
                  :step="1000"
                />
              </NFormItem>
              <NFormItem :label="t('长上下文输入价格', 'Long-context input price')">
                <NInputNumber v-model:value="longContextForm.input_usd_per_million" :min="0" />
              </NFormItem>
              <NFormItem :label="t('长上下文输出价格', 'Long-context output price')">
                <NInputNumber v-model:value="longContextForm.output_usd_per_million" :min="0" />
              </NFormItem>
              <NFormItem :label="t('长上下文缓存读价格', 'Long-context cache read price')">
                <NInputNumber v-model:value="longContextForm.cache_read_usd_per_million" :min="0" />
              </NFormItem>
              <NFormItem :label="t('长上下文缓存写价格', 'Long-context cache write price')">
                <NInputNumber v-model:value="longContextForm.cache_creation_usd_per_million" :min="0" />
              </NFormItem>
            </template>
          </template>
        </div>
      </NForm>
      <p class="price-save-hint">{{ priceSaveHint }}</p>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isPriceSaving" @click="modalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isPriceSaving" @click="savePrice">{{ t('保存', 'Save') }}</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="priorityModalOpen"
      preset="card"
      :title="t('编辑 Fast 倍率', 'Edit Fast multiplier')"
      :style="priorityModalStyle"
      class="priority-modal"
    >
      <NForm label-placement="top">
        <NFormItem :label="t('Fast 倍率', 'Fast multiplier')">
          <NInputNumber v-model:value="priorityMultiplier" :min="0" :disabled="isPrioritySaving" />
        </NFormItem>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isPrioritySaving" @click="priorityModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isPrioritySaving" @click="savePriorityMultiplier">{{ t('保存', 'Save') }}</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="proxyModalOpen"
      preset="card"
      :title="t('LiteLLM 代理配置', 'LiteLLM proxy settings')"
      :style="proxyModalStyle"
      :content-style="proxyModalContentStyle"
      :footer-style="proxyModalFooterStyle"
      class="proxy-modal"
    >
      <NForm :model="proxyForm" label-placement="top">
        <div class="proxy-form">
          <p class="proxy-hint">{{ liteLLMProxyHint }}</p>
          <div class="proxy-switch-row">
            <span class="proxy-switch-label">{{ t('使用代理', 'Use proxy') }}</span>
            <NSwitch
              v-model:value="proxyForm.enabled"
              :disabled="isProxyLoading || isProxySaving"
              :aria-label="t('使用代理', 'Use proxy')"
            />
          </div>
          <NFormItem :label="t('代理地址', 'Proxy URL')">
            <NInput
              v-model:value="proxyForm.proxy_url"
              :disabled="!proxyForm.enabled || isProxyLoading || isProxySaving"
              :placeholder="t('http://127.0.0.1:7890 或 socks5://127.0.0.1:1080', 'http://127.0.0.1:7890 or socks5://127.0.0.1:1080')"
            />
          </NFormItem>
        </div>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isProxySaving" @click="proxyModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isProxySaving" @click="saveProxySettings">{{ t('保存', 'Save') }}</NButton>
        </NSpace>
      </template>
    </NModal>
  </section>
</template>

<style scoped>
.price-modal {
  width: min(720px, calc(100vw - 24px));
  max-height: calc(100dvh - 32px);
}

.price-modal :deep(.n-card__content) {
  overflow-y: auto;
}

.proxy-modal {
  width: min(520px, calc(100vw - 24px));
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

.wide-form-item {
  grid-column: 1 / -1;
}

.long-context-switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 38px;
  margin-top: 4px;
  padding-top: 12px;
  border-top: 1px solid var(--cpa-border);
  color: var(--cpa-text);
  font-size: 14px;
  font-weight: 600;
}

.proxy-form {
  display: grid;
  gap: 14px;
}

.proxy-hint {
  margin: 0;
  padding: 10px 12px;
  border: 1px solid rgba(8, 145, 178, 0.22);
  border-radius: var(--cpa-radius);
  background: rgba(8, 145, 178, 0.08);
  color: var(--cpa-text-muted);
  font-size: 13px;
  line-height: 1.55;
}

.proxy-switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 34px;
  padding: 8px 10px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface-raised);
}

.proxy-switch-label {
  color: var(--cpa-text);
  font-size: 14px;
  font-weight: 600;
}

.proxy-form :deep(.n-form-item) {
  margin-bottom: 0;
}

.price-metrics {
  grid-template-columns: repeat(4, minmax(150px, 1fr));
}

.price-alert {
  border-radius: var(--cpa-radius);
}

.price-table-panel,
.price-table {
  min-width: 0;
  min-height: 0;
}

.price-table-panel {
  overflow: hidden;
}

.price-table-top {
  display: grid;
  gap: 8px;
}

.table-toolbar {
  padding: 14px 16px;
  border: 1px solid var(--cpa-border);
  border-bottom: 0;
  border-radius: var(--cpa-radius) var(--cpa-radius) 0 0;
  background: var(--cpa-surface-raised);
  box-shadow: var(--cpa-shadow-hairline);
}

.price-toolbar-layout {
  width: 100%;
  min-width: 0;
}

.price-table :deep(.n-data-table-wrapper) {
  border-radius: 0 0 var(--cpa-radius) var(--cpa-radius);
}

.filter-label,
.price-grouping-label,
.result-count {
  color: var(--cpa-text-muted);
  font-size: 13px;
  white-space: nowrap;
}

.price-grouping-control {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.provider-filter {
  width: 220px;
}

.status-filter {
  width: 150px;
}

.price-filters {
  min-width: 0;
  max-width: 100%;
}

.price-search {
  width: 280px;
}

.price-table :deep(.price-group-row td) {
  background: color-mix(in srgb, var(--cpa-surface-muted) 82%, var(--cpa-surface));
}

.price-table :deep(.price-group-row:hover td) {
  background: color-mix(in srgb, var(--cpa-primary) 7%, var(--cpa-surface-muted));
}

.price-table :deep(.price-group-cell) {
  min-width: 0;
}

.price-table :deep(.price-group-title) {
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text-strong);
  font-weight: 720;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.price-table :deep(.price-group-sub) {
  margin-top: 2px;
  overflow: hidden;
  color: var(--cpa-text-muted);
  font-size: 11px;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.price-table :deep(.price-group-status) {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.price-table :deep(.price-group-status strong),
.price-table :deep(.price-group-range) {
  color: var(--cpa-text-strong);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  font-weight: 700;
}

.price-table :deep(.price-group-status span) {
  color: var(--cpa-text-muted);
  font-size: 11px;
}

.price-table :deep(.model-cell),
.price-table :deep(.provider-cell) {
  min-width: 0;
}

.price-table :deep(.model-title-row) {
  display: flex;
  align-items: center;
  gap: 0;
  min-width: 0;
}

.price-table :deep(.model-availability-tag) {
  flex: 0 0 auto;
  margin-left: 2px;
}

.price-table :deep(.model-name),
.price-table :deep(.provider-main) {
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.price-table :deep(.model-sub) {
  margin-top: 2px;
  overflow: hidden;
  color: var(--cpa-text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.price-table :deep(.price-muted) {
  color: var(--cpa-text-muted);
}

.price-save-hint {
  margin: 4px 0 0;
  color: var(--cpa-text-muted);
  font-size: 13px;
}

@media (min-width: 861px) {
  .price-page {
    grid-template-rows: auto auto minmax(0, 1fr);
    height: calc(100dvh - 60px);
    min-height: 0;
    overflow: hidden;
  }

  .price-table-panel {
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    min-height: 0;
  }

  .price-table {
    height: 100%;
    min-height: 0;
  }

  .price-table :deep(.n-data-table-wrapper),
  .price-table :deep(.n-data-table-base-table),
  .price-table :deep(.n-data-table-base-table-body) {
    min-height: 0;
  }
}

@media (max-width: 980px) {
  .table-toolbar {
    padding: 12px;
  }

  .provider-filter {
    width: min(200px, calc(100vw - 32px));
  }

  .status-filter {
    width: min(160px, calc(100vw - 32px));
  }

  .price-search {
    width: min(240px, calc(100vw - 32px));
  }
}

@media (max-width: 620px) {
  .price-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .form-grid {
    grid-template-columns: 1fr;
  }

  .price-toolbar-layout {
    display: grid !important;
    gap: 8px !important;
  }

  .price-filters {
    display: grid !important;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px !important;
    width: 100%;
  }

  .filter-label {
    display: none;
  }

  .price-grouping-control,
  .price-search {
    grid-column: 1 / -1;
  }

  .price-grouping-control {
    justify-content: space-between;
  }

  .provider-filter,
  .status-filter,
  .price-search {
    width: 100%;
  }

  .result-count {
    justify-self: start;
    white-space: normal;
  }
}
</style>
