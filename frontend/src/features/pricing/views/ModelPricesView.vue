<script setup lang="ts">
import type { Component, CSSProperties } from 'vue'
import { computed, h, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NCheckbox,
  NDataTable,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NPagination,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NTooltip,
  useDialog,
  useMessage,
  type DataTableColumns,
  type DataTableRowKey,
} from 'naive-ui'
import { Database, Layers3, Pencil, RefreshCw, RotateCcw, Search, Server, Zap } from 'lucide-vue-next'

import {
  createModelPrice,
  deleteModelPriceLibraryConflict,
  deleteModelPrice,
  listModelPriceCatalog,
  listModelPriceLibraryConflicts,
  listModelPrices,
  listModelPriceChannelAliases,
  updateModelPriceChannelAlias,
  listLitellmModelOptions,
  promoteModelPriceLibraryConflict,
  replaceActiveModelPriceLibraryConflict,
  syncLitellmModelPrices,
  fetchModelPriceVersions,
  getDeepSeekTemplate,
  updateModelPrice,
  updateModelPricePriorityMultiplier,
} from '@/features/pricing/api/pricingApi'
import type {
  LiteLLMModelOption,
  LiteLLMModelOptionsResponse,
  ModelPrice,
  AIProviderBrand,
  ModelPriceCatalogItem,
  ModelPriceCatalogResponse,
  ModelPriceLibraryConflict,
  ModelPriceLibraryConflictLongContext,
  ModelPriceBillingUnit,
  ModelPriceLongContext,
  ModelPricePayload,
  ModelPriceChannelAlias,
  ModelPriceChannelAliasPayload,
  ModelPriceVersion,
  PriceTimeRule,
} from '@/shared/types/api'
import PriceTimeRuleEditor from '../components/PriceTimeRuleEditor.vue'
import PriceRatesMatrix from '../components/PriceRatesMatrix.vue'
import DeepSeekTemplateModal from '../components/DeepSeekTemplateModal.vue'
import { findModelGroupLibraryPrice, modelGroupLibraryPriceState } from '../utils/modelPriceGroupSummary'
import { multiplyModelPrice } from '../utils/modelPriceMultiplier'
import { formatDateTime, formatInteger, formatMultiplier, formatPreservedLongContextPrice, formatPriceValue } from '@/shared/utils/format'
import { useI18n } from '@/shared/i18n'
import { useViewportTooltip } from '@/shared/composables/useViewportTooltip'

type PriceTableLayoutProps =
  | { flexHeight: true }
  | { flexHeight: false; maxHeight: string }

type PriceRowStatus = 'missing' | 'litellm' | 'manual'
type PriceStatusFilter = 'cpa' | 'missing' | 'litellm' | 'manual' | 'library' | 'migration_conflict' | 'removed' | 'unavailable' | 'all'
type PriceGroupingMode = 'model' | 'provider'
type PriceScope = 'library' | 'channel'
type ChannelAuthType = 'apikey' | 'oauth'
type UnmatchedChannelStatus = 'conflict' | 'model_removed' | 'orphan' | 'unavailable'
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
  alias: string | null
  owner: string | null
  suggestedProvider: string
  channelBrand: string
  channelLabel: string
  channelAuthType: ChannelAuthType
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
  channelFilterKey: string
  channelAuthType: ChannelAuthType | null
  channelBrand: string | null
  channelKey: string | null
  channelAPIKeyMasked: string | null
  channelIdentityHash: string | null
  channelStatus: string
  channelDisabled: boolean
  channelLabelFallback: boolean
  channelAccountCount: number
  priceScope: PriceScope
  model: string
  comparisonModelKey: string
  catalogModels: CatalogModelReference[]
  templatePrice: ModelPrice | null
  billing_unit: ModelPriceBillingUnit
  status: PriceRowStatus
  migrationConflict: ModelPriceLibraryConflict | null
}

interface PriceGroupRow {
  rowType: 'group'
  key: string
  mode: PriceGroupingMode
  label: string
  children: PriceDisplayRow[]
  channelCount: number
  libraryPriceCount: number
  modelCount: number
  pricedCount: number
  unpricedCount: number
  billingUnits: ModelPriceBillingUnit[]
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
const conflictModalStyle: CSSProperties = { width: 'min(520px, calc(100vw - 32px))' }
const priorityModalStyle: CSSProperties = { width: 'min(420px, calc(100vw - 32px))' }
const desktopPriceLayoutQuery = window.matchMedia('(min-width: 861px)')
const message = useMessage()
const dialog = useDialog()
const { errorText, serverText, t } = useI18n()
const isLoading = ref(false)
const hoveredChannelLabelKey = ref<string | null>(null)
const focusedChannelLabelKey = ref<string | null>(null)
const channelLabelTooltips = useViewportTooltip(hoveredChannelLabelKey, focusedChannelLabelKey)
const isSyncing = ref(false)
const syncModalOpen = ref(false)
const syncOptionsLoading = ref(false)
const syncOptionsError = ref('')
const syncOptionsResponse = ref<LiteLLMModelOptionsResponse | null>(null)
const selectedSyncModels = ref<string[]>([])
const litellmModelOptions = ref<LiteLLMModelOption[]>([])
const syncSearch = ref('')
const syncPage = ref(1)
const syncPageSize = 40
const syncSelectionFilter = ref<'all' | 'matched' | 'selected'>('all')
const modalOpen = ref(false)
const priorityModalOpen = ref(false)
const conflictModalOpen = ref(false)
const correctLatestVersion = ref(false)
const timeTemplateOpen = ref(false)
const timePricingEnabled = ref(false)
const timeRule = ref<PriceTimeRule | null>(null)
const timeRuleLoading = ref(false)
let timeTemplateRequest = 0
function supportsDeepSeekTimePricingForm(
  priceScope: PriceScope,
  billingUnit: ModelPriceBillingUnit,
  model: string,
): boolean {
  return priceScope === 'channel' && billingUnit === 'token' && (model.split('/').pop()?.toLowerCase().startsWith('deepseek-') ?? false)
}
const deepSeekForm = computed(() => supportsDeepSeekTimePricingForm(form.price_scope, form.billing_unit, form.model))
const priceLabels = computed(() => Object.fromEntries(priceRows.value.filter(row => row.price).map(row => [row.price!.id, row.provider])))
const currentVersionId = computed(() => priceVersions.value.filter(version => Date.parse(version.effective_at) <= Date.now()).pop()?.id)
const latestVersionId = computed(() => priceVersions.value[priceVersions.value.length - 1]?.id)
// Newest first; long histories collapse to the most recent few until expanded.
const collapsedVersionCount = 3
const showAllVersions = ref(false)
const visiblePriceVersions = computed(() => {
  const newestFirst = [...priceVersions.value].reverse()
  return showAllVersions.value ? newestFirst : newestFirst.slice(0, collapsedVersionCount)
})
const editingSupportsFast = computed(() => supportsPriorityMultiplier(prices.value.find(price => price.id === editingId.value) ?? null))
function invalidateTimeTemplateRequest() {
  timeTemplateRequest += 1
  timeRuleLoading.value = false
}

async function toggleTimePricing(enabled: boolean) {
  invalidateTimeTemplateRequest()
  if (!deepSeekForm.value) {
    timePricingEnabled.value = false
    timeRule.value = null
    return
  }
  timePricingEnabled.value = enabled
  if (!enabled || timeRule.value) return
  timeRuleLoading.value = true
  const request = timeTemplateRequest
  const id = editingId.value
  const model = form.model
  const isCurrentRequest = () => timeTemplateRequest === request && modalOpen.value && editingId.value === id && form.model === model
  try {
    const template = await getDeepSeekTemplate()
    if (isCurrentRequest()) timeRule.value = template
  } catch (error) {
    if (isCurrentRequest()) {
      timePricingEnabled.value = false
      message.error(errorText(error, '加载峰谷模板失败', 'Failed to load time pricing template'))
    }
  } finally {
    if (isCurrentRequest()) timeRuleLoading.value = false
  }
}
const priceVersions = ref<ModelPriceVersion[]>([])
const priceVersionsLoading = ref(false)
const priceVersionsError = ref('')
let priceVersionRequest = 0
const isPrioritySaving = ref(false)
const isPriceSaving = ref(false)
const isConflictSaving = ref(false)
const editingId = ref<number | null>(null)
const editingChannelLabel = ref('')
const aliasModalOpen = ref(false)
const aliasSaving = ref(false)
const aliasLabel = ref('')
const aliasAPIKeyMasked = ref<string | null>(null)
const aliasIdentity = reactive<ModelPriceChannelAliasPayload>({ auth_type: 'apikey', channel_brand: 'gemini', channel_key: '', channel_identity_hash: '', label: '' })
watch(aliasModalOpen, (open) => {
  if (!open) aliasAPIKeyMasked.value = null
})
const channelAliases = ref<ModelPriceChannelAlias[]>([])
const priorityEditingPrice = ref<ModelPrice | null>(null)
const priorityMultiplier = ref<number | null>(null)
const priorityCorrectLatest = ref(false)
const priorityLatestVersion = ref<ModelPriceVersion | null>(null)
const priorityVersionsLoading = ref(false)
const priorityVersionsError = ref('')
let priorityVersionRequest = 0
const prices = ref<ModelPrice[]>([])
const libraryConflicts = ref<ModelPriceLibraryConflict[]>([])
const resolvingConflict = ref<ModelPriceLibraryConflict | null>(null)
const conflictProvider = ref('')
const conflictModel = ref('')
const catalog = ref<ModelPriceCatalogResponse | null>(null)
const selectedProvider = ref<string | null>(null)
const selectedStatus = ref<PriceStatusFilter | null>(null)
const searchQuery = ref('')
const groupingMode = ref<PriceGroupingMode>(readPriceGroupingMode())
const expandedRowKeys = ref<DataTableRowKey[]>([])
const longContextEnabled = ref(false)
const preservedLongContext = ref<ModelPriceLibraryConflictLongContext | null>(null)
const preserveInvalidLongContext = ref(false)
const channelTemplatePrice = ref<ModelPrice | null>(null)
const channelPriceMultiplier = ref<number | null>(null)
const isDesktopPriceLayout = ref(desktopPriceLayoutQuery.matches)
const pagination = reactive({
  page: 1,
  pageSize: 20,
  onUpdatePage: updatePricePage,
})
const form = reactive<ModelPricePayload>({
  provider: '',
  model: '',
  price_scope: 'library',
  channel_auth_type: null,
  channel_brand: null,
  channel_key: null,
  channel_identity_hash: null,
  billing_unit: 'token',
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
let beforeCorrection: {
  form: ModelPricePayload
  long: ModelPriceLongContext
  longEnabled: boolean
  preservedLong: ModelPriceLibraryConflictLongContext | null
  preserveInvalidLong: boolean
  rule: PriceTimeRule | null
  enabled: boolean
} | null = null
watch(modalOpen, (open) => {
  if (!open) invalidatePriceFormRequests()
})
watch(correctLatestVersion, (correct) => {
  if (!modalOpen.value) return
  if (correct) {
    const latest = priceVersions.value[priceVersions.value.length - 1]
    if (!latest) return
    invalidateTimeTemplateRequest()
    beforeCorrection = JSON.parse(JSON.stringify({
      form: { ...form },
      long: { ...longContextForm },
      longEnabled: longContextEnabled.value,
      preservedLong: preservedLongContext.value,
      preserveInvalidLong: preserveInvalidLongContext.value,
      rule: timeRule.value,
      enabled: timePricingEnabled.value,
    }))
    form.billing_unit = latest.billing_unit
    form.input_usd_per_million = latest.input_usd_per_million
    form.output_usd_per_million = latest.output_usd_per_million
    form.cache_read_usd_per_million = latest.cache_read_usd_per_million
    form.cache_creation_usd_per_million = latest.cache_creation_usd_per_million
    form.request_usd = latest.request_usd
    preservedLongContext.value = null
    preserveInvalidLongContext.value = false
    longContextEnabled.value = !!latest.long_context
    if (latest.long_context) setLongContextForm(latest.long_context)
    else if (latest.preserved_long_context) setPreservedLongContextForm(latest.preserved_long_context)
    timeRule.value = latest.time_pricing ? JSON.parse(JSON.stringify(latest.time_pricing)) : null
    timePricingEnabled.value = !!timeRule.value
  } else if (beforeCorrection) {
    const enabled = beforeCorrection.enabled
    Object.assign(form, beforeCorrection.form)
    Object.assign(longContextForm, beforeCorrection.long)
    longContextEnabled.value = beforeCorrection.longEnabled
    preservedLongContext.value = beforeCorrection.preservedLong
    preserveInvalidLongContext.value = beforeCorrection.preserveInvalidLong
    timeRule.value = beforeCorrection.rule
    beforeCorrection = null
    void toggleTimePricing(enabled)
  }
})
const preservedLongContextSummary = computed(() => {
  const value = preservedLongContext.value
  if (!value) {
    return ''
  }
  return [
    `${t('阈值', 'Threshold')} ${value.threshold_input_tokens === null ? t('未设置', 'Not set') : formatInteger(value.threshold_input_tokens)}`,
    `${t('输入', 'Input')} ${formatPreservedLongContextPrice(value, 'input_usd_per_million')}`,
    `${t('输出', 'Output')} ${formatPreservedLongContextPrice(value, 'output_usd_per_million')}`,
    `${t('缓存读', 'Cache read')} ${formatPreservedLongContextPrice(value, 'cache_read_usd_per_million')}`,
    `${t('缓存写', 'Cache write')} ${formatPreservedLongContextPrice(value, 'cache_creation_usd_per_million')}`,
  ].join(' · ')
})

function catalogModelReference(model: ModelPriceCatalogItem): CatalogModelReference {
  return {
    id: model.id,
    name: model.name,
    alias: model.alias,
    owner: model.owner,
    suggestedProvider: model.suggested_provider,
    channelBrand: model.channel_brand,
    channelLabel: model.channel_label,
    channelAuthType: model.channel_auth_type,
  }
}

function channelFilterKey(
  scope: PriceScope,
  authType: ChannelAuthType | null,
  brand: string | null,
  key: string | null,
  identityHash: string | null,
  provider: string,
) {
  if (scope === 'channel') {
    const selector = key?.trim() ?? ''
    if (selector) {
      const canonicalSelector = brand === 'openai_compatibility' ? selector.toLowerCase() : selector
      return `channel:${authType ?? 'apikey'}:${brand ?? 'unknown'}:selector:${canonicalSelector}`
    }
    return `channel:${authType ?? 'apikey'}:${brand ?? 'unknown'}:identity:${identityHash?.trim() || 'unknown'}`
  }
  return `library:${provider.trim().toLowerCase()}`
}

function channelPriceIdentityKey(
  authType: ChannelAuthType | null,
  brand: string | null,
  key: string | null,
  model: string,
): string | null {
  const channelIdentity = channelIdentityKey(authType, brand, key)
  const normalizedModel = model.trim().toLowerCase()
  if (!channelIdentity || !normalizedModel) {
    return null
  }
  return JSON.stringify([channelIdentity, normalizedModel])
}

function channelIdentityKey(
  authType: ChannelAuthType | null,
  brand: string | null,
  key: string | null,
): string | null {
  const normalizedBrand = brand?.trim().toLowerCase() ?? ''
  const selector = key?.trim() ?? ''
  if (!normalizedBrand || !selector) {
    return null
  }
  const canonicalSelector = normalizedBrand === 'openai_compatibility' ? selector.toLowerCase() : selector
  return JSON.stringify([authType ?? 'apikey', normalizedBrand, canonicalSelector])
}

function channelDisplayLabel(
  authType: ChannelAuthType | null,
  brand: string | null,
  fallback: string,
  accountCount = 0,
): string {
  if (authType !== 'oauth') {
    return fallback || channelBrandLabel(brand)
  }
  const base = t(
    `${channelBrandLabel(brand)} OAuth 账号池`,
    `${channelBrandLabel(brand)} OAuth account pool`,
  )
  return accountCount > 0
    ? t(`${base} (${accountCount} 个账号)`, `${base} (${accountCount} accounts)`)
    : base
}

function channelBrandLabel(brand: string | null): string {
  switch (brand) {
    case 'gemini':
      return 'Gemini'
    case 'codex':
      return 'Codex'
    case 'claude':
      return 'Claude'
    case 'vertex':
      return 'Vertex'
    case 'xai':
      return 'xAI'
    case 'openai_compatibility':
      return t('OpenAI 兼容', 'OpenAI-compatible')
    default:
      return t('未知渠道', 'Unknown channel')
  }
}

function maskedChannelReference(value: string | null): string {
  const normalized = value?.trim() ?? ''
  if (!normalized) {
    return t('已移除渠道', 'Removed channel')
  }
  if (normalized.length <= 8) {
    return `${normalized.slice(0, 2)}...`
  }
  return `${normalized.slice(0, 4)}...${normalized.slice(-3)}`
}

function orphanPriceChannelLabel(price: ModelPrice): string {
  if (price.price_scope === 'library') {
    return price.provider
  }
  const alias = channelAliasLabel(price.channel_auth_type, price.channel_brand, price.channel_key)
  if (alias) {
    return alias
  }
  if (price.channel_brand === 'openai_compatibility') {
    return price.channel_key || price.provider
  }
  if (price.channel_auth_type === 'oauth') {
    return channelDisplayLabel('oauth', price.channel_brand, '')
  }
  return `${channelBrandLabel(price.channel_brand)} · ${maskedChannelReference(price.channel_key)}`
}

function pricedDisplayRow(
  price: ModelPrice,
  catalogModel: ModelPriceCatalogItem | null,
  unmatchedChannelStatus: UnmatchedChannelStatus = 'orphan',
): PriceDisplayRow {
  const scope: PriceScope = price.price_scope === 'channel' ? 'channel' : 'library'
  const provider = catalogModel
    ? channelDisplayLabel(catalogModel.channel_auth_type, catalogModel.channel_brand, catalogModel.channel_label, catalogModel.channel_account_count)
    : orphanPriceChannelLabel(price)
  const catalogModels = catalogModel ? [catalogModelReference(catalogModel)] : []
  return {
    rowType: 'detail',
    key: `price:${price.id}`,
    in_cpa: catalogModel !== null,
    id: price.model,
    name: catalogModel?.alias || catalogModel?.name || price.model,
    owner: catalogModel?.owner ?? null,
    suggested_provider: catalogModel?.suggested_provider ?? '',
    price,
    provider,
    channelFilterKey: channelFilterKey(
      scope,
      price.channel_auth_type,
      price.channel_brand,
      price.channel_key,
      catalogModel?.channel_identity_hash ?? null,
      provider,
    ),
    channelAuthType: price.channel_auth_type,
    channelBrand: price.channel_brand,
    channelKey: price.channel_key,
    channelAPIKeyMasked: catalogModel?.channel_api_key_masked ?? null,
    channelIdentityHash: catalogModel?.channel_identity_hash ?? null,
    channelStatus: catalogModel?.channel_status ?? (scope === 'channel' ? unmatchedChannelStatus : 'ready'),
    channelDisabled: catalogModel?.channel_disabled ?? false,
    channelLabelFallback: catalogModel?.channel_label_fallback ?? scope === 'channel',
    channelAccountCount: catalogModel?.channel_account_count ?? 0,
    priceScope: scope,
    model: price.model,
    comparisonModelKey: normalizeModelComparisonKey(price.model),
    catalogModels,
    templatePrice: catalogModel?.template_price ?? null,
    billing_unit: billingUnitForPrice(price, price.model),
    status: priceStatus(price, price.model),
    migrationConflict: null,
  }
}

function migrationConflictDisplayRow(conflict: ModelPriceLibraryConflict): PriceDisplayRow {
  const row = pricedDisplayRow(conflict.price, null)
  return {
    ...row,
    key: `migration-conflict:${conflict.original_id}`,
    in_cpa: false,
    channelStatus: 'migration_conflict',
    migrationConflict: conflict,
  }
}

function unpricedCatalogDisplayRow(model: ModelPriceCatalogItem): PriceDisplayRow {
  const provider = channelDisplayLabel(model.channel_auth_type, model.channel_brand, model.channel_label, model.channel_account_count)
  return {
    rowType: 'detail',
    key: `catalog:${model.id}`,
    in_cpa: true,
    id: model.name,
    name: model.alias || model.name,
    owner: model.owner,
    suggested_provider: model.suggested_provider,
    price: null,
    provider,
    channelFilterKey: channelFilterKey(
      'channel',
      model.channel_auth_type,
      model.channel_brand,
      model.channel_key,
      model.channel_identity_hash,
      provider,
    ),
    channelAuthType: model.channel_auth_type,
    channelBrand: model.channel_brand,
    channelKey: model.channel_key,
    channelAPIKeyMasked: model.channel_api_key_masked ?? null,
    channelIdentityHash: model.channel_identity_hash,
    channelStatus: model.channel_status,
    channelDisabled: model.channel_disabled,
    channelLabelFallback: model.channel_label_fallback,
    channelAccountCount: model.channel_account_count,
    priceScope: 'channel',
    model: model.name,
    comparisonModelKey: normalizeModelComparisonKey(model.name),
    catalogModels: [catalogModelReference(model)],
    templatePrice: model.template_price,
    billing_unit: billingUnitForPrice(null, model.name),
    status: 'missing',
    migrationConflict: null,
  }
}

const priceRows = computed<PriceDisplayRow[]>(() => {
  const rows: PriceDisplayRow[] = []
  const catalogModels = catalog.value?.models ?? []
  const renderedPriceIds = new Set<number>()
  const catalogStatuses = new Map<string, Set<string>>()
  const catalogChannelIdentities = new Set<string>()

  for (const model of catalogModels) {
    const channelIdentity = channelIdentityKey(model.channel_auth_type, model.channel_brand, model.channel_key)
    if (channelIdentity) {
      catalogChannelIdentities.add(channelIdentity)
    }
    const identity = channelPriceIdentityKey(model.channel_auth_type, model.channel_brand, model.channel_key, model.name)
    if (!identity) {
      continue
    }
    const statuses = catalogStatuses.get(identity) ?? new Set<string>()
    statuses.add(model.channel_status)
    catalogStatuses.set(identity, statuses)
  }

  for (const model of catalogModels) {
    if (!model.price) {
      rows.push(unpricedCatalogDisplayRow(model))
      continue
    }
    rows.push({
      ...pricedDisplayRow(model.price, model),
      key: `catalog:${model.id}`,
    })
    renderedPriceIds.add(model.price.id)
  }

  for (const price of prices.value) {
    if (renderedPriceIds.has(price.id)) {
      continue
    }
    const identity = channelPriceIdentityKey(price.channel_auth_type, price.channel_brand, price.channel_key, price.model)
    const channelIdentity = channelIdentityKey(price.channel_auth_type, price.channel_brand, price.channel_key)
    const identityStatuses = identity ? catalogStatuses.get(identity) : undefined
    let unmatchedStatus: UnmatchedChannelStatus = 'orphan'
    if (price.channel_auth_type === 'oauth' && catalog.value?.oauth_channels_available === false) {
      unmatchedStatus = 'unavailable'
    } else if (price.channel_auth_type !== 'oauth' && catalog.value?.channels_available === false) {
      unmatchedStatus = 'unavailable'
    } else if (identityStatuses?.has('conflict')) {
      unmatchedStatus = 'conflict'
    } else if (!identityStatuses && channelIdentity && catalogChannelIdentities.has(channelIdentity)) {
      unmatchedStatus = 'model_removed'
    }
    rows.push(pricedDisplayRow(price, null, unmatchedStatus))
  }
  for (const conflict of libraryConflicts.value) {
    rows.push(migrationConflictDisplayRow(conflict))
  }
  return rows
})

const providerOptions = computed(() => {
  const options = new Map<string, string>()
  for (const row of priceRows.value) {
    options.set(row.channelFilterKey, row.provider)
  }
  return [...options.entries()]
    .sort((left, right) => left[1].localeCompare(right[1]))
    .map(([value, label]) => ({ label, value }))
})

const statusOptions = computed<Array<{ label: string; value: PriceStatusFilter }>>(() => [
  { label: t('渠道模型', 'Channel models'), value: 'cpa' },
  { label: t('未定价', 'Unpriced'), value: 'missing' },
  { label: 'LiteLLM', value: 'litellm' },
  { label: t('手动', 'Manual'), value: 'manual' },
  { label: t('通用价格', 'Library prices'), value: 'library' },
  { label: t('迁移冲突', 'Migration conflicts'), value: 'migration_conflict' },
  { label: t('已移除渠道或模型', 'Removed channels or models'), value: 'removed' },
  { label: t('渠道不可用', 'Unavailable channels'), value: 'unavailable' },
  { label: t('全部记录（含历史）', 'All records, including history'), value: 'all' },
])

const groupingOptions = computed<Array<{ label: string; value: PriceGroupingMode }>>(() => [
  { label: t('按模型', 'By model'), value: 'model' },
  { label: t('按渠道', 'By channel'), value: 'provider' },
])

const billingUnitOptions = computed<Array<{ label: string; value: ModelPriceBillingUnit }>>(() => [
  { label: t('按量（Token）', 'Per token'), value: 'token' },
  { label: t('按次（请求）', 'Per request'), value: 'request' },
])

const filteredPrices = computed(() => {
  return priceRows.value.filter((row) => {
    const includesHistory = selectedStatus.value === 'removed' || selectedStatus.value === 'unavailable' || selectedStatus.value === 'all'
    if (!includesHistory && row.priceScope === 'channel' && ['orphan', 'model_removed', 'unavailable'].includes(row.channelStatus)) {
      return false
    }
    if (selectedProvider.value && row.channelFilterKey !== selectedProvider.value) {
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

watch(longContextEnabled, (enabled) => {
  if (enabled) {
    preserveInvalidLongContext.value = false
  }
})

watch(preserveInvalidLongContext, (preserve) => {
  if (preserve) {
    longContextEnabled.value = false
  }
})

watch(
  () => form.billing_unit,
  (billingUnit) => {
    if (billingUnit === 'token') {
      form.request_usd = null
      return
    }
    longContextEnabled.value = false
    preserveInvalidLongContext.value = false
  },
)

function renderSearchIcon() {
  return h(NIcon, { component: Search })
}

function renderStatusOptionLabel(option: { label: string }) {
  return h('span', { class: 'status-option-label' }, option.label)
}

function updatePricePage(page: number) {
  pagination.page = page
}

function normalizePriceSearch(value: string) {
  return value.trim().toLowerCase()
}

function normalizeModelComparisonKey(value: string): string {
  const model = value.trim()
  if (model.startsWith('models/')) {
    return model.slice('models/'.length).trim()
  }
  if (model.startsWith('publishers/google/models/')) {
    return model.slice('publishers/google/models/'.length).trim()
  }
  return model
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

function uniqueStableCount(values: string[]): number {
  return new Set(values.map((value) => value.trim() || '__unknown__')).size
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

function rowIsOperationallyPriced(row: PriceDisplayRow): boolean {
  return row.migrationConflict === null && row.status !== 'missing' && (row.priceScope === 'library' || row.channelStatus === 'ready')
}

function createPriceGroupRow(
  mode: PriceGroupingMode,
  normalizedValue: string,
  label: string,
  children: PriceDisplayRow[],
): PriceGroupRow {
  const longContextEligibleRows = children.filter(
    (row) => rowIsOperationallyPriced(row) && row.billing_unit === 'token' && row.price !== null,
  )
  const priorityEligibleRows = children.filter(
    (row) => rowIsOperationallyPriced(row) && supportsPriorityMultiplier(row.price),
  )
  return {
    rowType: 'group',
    key: `group:${mode}:${normalizedValue}`,
    mode,
    label,
    children,
    channelCount: uniqueStableCount(
      children
        .filter((row) => row.priceScope === 'channel')
        .map((row) => row.channelFilterKey),
    ),
    libraryPriceCount: children.filter((row) => row.priceScope === 'library').length,
    modelCount: uniqueNormalizedCount(children.map((row) => row.comparisonModelKey)),
    pricedCount: children.filter(rowIsOperationallyPriced).length,
    unpricedCount: children.filter((row) => !rowIsOperationallyPriced(row)).length,
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
    const rawValue = mode === 'model' ? row.comparisonModelKey : row.channelFilterKey
    const label = mode === 'model' ? row.comparisonModelKey : row.provider
    const normalizedValue = mode === 'model'
      ? normalizePriceSearch(rawValue) || '__unknown__'
      : rawValue.trim() || '__unknown__'
    const existing = groups.get(normalizedValue)
    if (existing) {
      existing.children.push(row)
      continue
    }
    groups.set(normalizedValue, {
      label: mode === 'provider' && !label.trim() ? t('未识别渠道', 'Unknown channel') : label,
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

function billingUnitForModel(model: string): ModelPriceBillingUnit {
  return model.trim().toLowerCase().includes('image') ? 'request' : 'token'
}

function billingUnitForPrice(price: ModelPrice | null, fallbackModel: string): ModelPriceBillingUnit {
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
    case 'all':
      return true
    case 'removed':
      return row.priceScope === 'channel' && ['orphan', 'model_removed'].includes(row.channelStatus)
    case 'unavailable':
      return row.priceScope === 'channel' && row.channelStatus === 'unavailable'
    case 'cpa':
      return row.in_cpa
    case 'library':
      return row.priceScope === 'library'
    case 'missing':
      return !rowIsOperationallyPriced(row)
    case 'migration_conflict':
      return row.migrationConflict !== null
    default:
      return row.status === status
  }
}

const normalizedSearchQuery = computed(() => normalizePriceSearch(searchQuery.value))

const filteredPriceCount = computed(() => filteredPrices.value.length)
const filteredGroupCount = computed(() => priceTableRows.value.length)

const totalPriceCount = computed(() => priceRows.value.length)
const cpaModelCount = computed(() => catalog.value?.models.length ?? 0)
const channelCount = computed(
  () => new Set(
    (catalog.value?.models ?? []).map((model) => channelFilterKey(
      'channel',
      model.channel_auth_type,
      model.channel_brand,
      model.channel_key,
      model.channel_identity_hash,
      model.channel_label,
    )),
  ).size,
)
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
  if (!current.channels_available) {
    const detail = serverText(
      current.channel_error || '',
      '渠道配置暂时不可用',
      'Channel configuration is unavailable',
    )
    return t(
      `读取渠道配置失败：${detail}。当前仍可查看和维护通用价格。`,
      `Failed to load channel configuration: ${detail}. Library prices remain available.`,
    )
  }
  if (!current.oauth_channels_available && current.oauth_channel_error) {
    const detail = serverText(
      current.oauth_channel_error,
      'OAuth 账号池暂时不可用',
      'OAuth account pools are unavailable',
    )
    return t(
      `读取 OAuth 账号池失败：${detail}。API Key 渠道和通用价格仍可正常使用。`,
      `Failed to load OAuth account pools: ${detail}. API key channels and library prices remain available.`,
    )
  }
  return ''
})
const priceTableLayoutProps = computed<PriceTableLayoutProps>(() =>
  isDesktopPriceLayout.value
    ? { flexHeight: true }
    : { flexHeight: false, maxHeight: PRICE_TABLE_FALLBACK_MAX_HEIGHT },
)
const isRequestPriceForm = computed(() => form.billing_unit === 'request')
const isChannelPriceForm = computed(() => form.price_scope === 'channel')
const priceSaveHint = computed(() => {
  if (isChannelPriceForm.value) {
    if (editingId.value !== null && correctLatestVersion.value) {
      return t(
        '保存后将原地修正最新一条价格版本并保留其原生效时间；该时刻之后的请求在历史统计中按修正价重算，已结算的额度扣费不会改写。',
        'Saving overwrites the latest price version in place and keeps its original effective time. Requests since that moment are recalculated with the corrected price in history; settled quota charges are not rewritten.',
      )
    }
    return isRequestPriceForm.value
      ? t(
          '此价格只用于当前渠道的该模型；按次计费会按每次成功调用固定金额计算。',
          'This price applies only to this channel and model. Per-request billing charges a fixed amount for each successful call.',
        )
      : t(
          '此价格只用于当前渠道的该模型，通用价格仅作为本次表单的预填参考。',
          'This price applies only to this channel and model. The library price is used only as a form template.',
        )
  }
  return isRequestPriceForm.value
    ? t(
        '按次计费会按每次成功调用固定金额计算，保存后会作为手动价格优先保留。',
        'Per-request billing charges a fixed amount for each successful call. Saved values are kept as manual prices with priority.',
      )
    : t(
        '保存后会作为手动价格，后续 LiteLLM 同步会优先保留。',
        'Saved values are kept as manual prices and preserved by later LiteLLM syncs.',
      )
})

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
    label: t('渠道模型', 'Channel models'),
    value: formatInteger(cpaModelCount.value),
    footnote: catalog.value
      ? t(
          `${formatInteger(channelCount.value)} 个渠道`,
          `${formatInteger(channelCount.value)} channels`,
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
    (row.channelBrand ?? '').toLowerCase().includes(normalizedSearchQuery.value) ||
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

function invalidatePriceFormRequests() {
  invalidateTimeTemplateRequest()
  priceVersionRequest += 1
  priceVersionsLoading.value = false
}

function resetForm() {
  invalidatePriceFormRequests()
  editingId.value = null
  editingChannelLabel.value = ''
  form.provider = ''
  form.model = ''
  form.price_scope = 'library'
  form.channel_auth_type = null
  form.channel_brand = null
  form.channel_key = null
  form.channel_identity_hash = null
  form.billing_unit = 'token'
  form.input_usd_per_million = 0
  form.output_usd_per_million = 0
  form.cache_read_usd_per_million = 0
  form.cache_creation_usd_per_million = 0
  form.request_usd = null
  form.long_context = null
  longContextEnabled.value = false
  preservedLongContext.value = null
  preserveInvalidLongContext.value = false
  channelTemplatePrice.value = null
  channelPriceMultiplier.value = null
  timePricingEnabled.value = false
  timeRule.value = null
  beforeCorrection = null
  correctLatestVersion.value = false
  priceVersions.value = []
  priceVersionsError.value = ''
  showAllVersions.value = false
  longContextForm.threshold_input_tokens = 200000
  longContextForm.input_usd_per_million = 0
  longContextForm.output_usd_per_million = 0
  longContextForm.cache_read_usd_per_million = 0
  longContextForm.cache_creation_usd_per_million = 0
}

async function refresh() {
  isLoading.value = true
  try {
    const [nextPrices, nextCatalog, nextConflicts, nextAliases] = await Promise.all([
      listModelPrices(),
      listModelPriceCatalog(),
      listModelPriceLibraryConflicts(),
      listModelPriceChannelAliases(),
    ])
    prices.value = nextPrices
    catalog.value = nextCatalog
    libraryConflicts.value = nextConflicts
    channelAliases.value = nextAliases
    pruneExpandedRowKeys()
  } catch (error) {
    message.error(errorText(error, '加载模型价格失败', 'Failed to load model prices'))
  } finally {
    isLoading.value = false
  }
}

function openCreate(prefill: Partial<ModelPricePayload> = {}, channelLabel = '') {
  resetForm()
  editingChannelLabel.value = channelLabel
  form.provider = prefill.provider ?? ''
  form.model = prefill.model ?? ''
  form.price_scope = prefill.price_scope ?? 'library'
  form.channel_auth_type = prefill.channel_auth_type ?? null
  form.channel_brand = prefill.channel_brand ?? null
  form.channel_key = prefill.channel_key ?? null
  form.channel_identity_hash = prefill.channel_identity_hash ?? null
  form.billing_unit = prefill.billing_unit ?? billingUnitForModel(form.model)
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
  const template = row.templatePrice
  openCreate({
    provider: row.suggested_provider || row.channelBrand || row.provider,
    model: row.model,
    price_scope: 'channel',
    channel_auth_type: row.channelAuthType,
    channel_brand: row.channelBrand,
    channel_key: row.channelKey,
    channel_identity_hash: row.channelIdentityHash,
    billing_unit: billingUnitForPrice(template, row.model),
    input_usd_per_million: template?.input_usd_per_million ?? 0,
    output_usd_per_million: template?.output_usd_per_million ?? 0,
    cache_read_usd_per_million: template?.cache_read_usd_per_million ?? 0,
    cache_creation_usd_per_million: template?.cache_creation_usd_per_million ?? 0,
    request_usd: template?.request_usd ?? null,
    long_context: template?.long_context ?? null,
  }, row.provider)
  channelTemplatePrice.value = template
}

function openEdit(row: PriceDisplayRow) {
  if (!row.price) {
    return
  }
  const price = row.price
  resetForm()
  editingId.value = price.id
  editingChannelLabel.value = row.provider
  channelTemplatePrice.value = row.templatePrice
  form.provider = price.provider
  form.model = price.model
  form.price_scope = price.price_scope
  form.channel_auth_type = price.channel_auth_type
  form.channel_brand = price.channel_brand
  form.channel_key = price.channel_key
  form.channel_identity_hash = row.channelIdentityHash
  form.billing_unit = billingUnitForPrice(price, price.model)
  form.input_usd_per_million = price.input_usd_per_million
  form.output_usd_per_million = price.output_usd_per_million
  form.cache_read_usd_per_million = price.cache_read_usd_per_million
  form.cache_creation_usd_per_million = price.cache_creation_usd_per_million
  form.request_usd = price.request_usd
  const supportsTimePricing = supportsDeepSeekTimePricingForm(price.price_scope, form.billing_unit, price.model)
  timeRule.value = supportsTimePricing && price.time_pricing ? JSON.parse(JSON.stringify(price.time_pricing)) : null
  timePricingEnabled.value = supportsTimePricing && !!price.time_pricing
  if (price.long_context) {
    setLongContextForm(price.long_context)
  } else if (price.preserved_long_context) {
    setPreservedLongContextForm(price.preserved_long_context)
  }
  modalOpen.value = true
  if (price.price_scope === 'channel') {
    void loadPriceVersions(price.id)
  }
}

async function loadPriceVersions(priceId: number) {
  const request = ++priceVersionRequest
  const isCurrentRequest = () => priceVersionRequest === request && modalOpen.value && editingId.value === priceId
  priceVersionsLoading.value = true
  priceVersionsError.value = ''
  try {
    const versions = await fetchModelPriceVersions(priceId)
    if (!isCurrentRequest()) {
      return
    }
    priceVersions.value = versions
  } catch (error) {
    if (isCurrentRequest()) {
      priceVersionsError.value = errorText(error, '加载价格版本失败', 'Failed to load price versions')
    }
  } finally {
    if (isCurrentRequest()) {
      priceVersionsLoading.value = false
    }
  }
}

function setLongContextForm(value: ModelPriceLongContext) {
  longContextEnabled.value = true
  longContextForm.threshold_input_tokens = value.threshold_input_tokens
  longContextForm.input_usd_per_million = value.input_usd_per_million
  longContextForm.output_usd_per_million = value.output_usd_per_million
  longContextForm.cache_read_usd_per_million = value.cache_read_usd_per_million
  longContextForm.cache_creation_usd_per_million = value.cache_creation_usd_per_million
}

function setPreservedLongContextForm(value: ModelPriceLibraryConflictLongContext) {
  preservedLongContext.value = value
  preserveInvalidLongContext.value = true
  longContextEnabled.value = false
  longContextForm.threshold_input_tokens = value.threshold_input_tokens ?? 200000
  longContextForm.input_usd_per_million = value.input_usd_per_million ?? 0
  longContextForm.output_usd_per_million = value.output_usd_per_million ?? 0
  longContextForm.cache_read_usd_per_million = value.cache_read_usd_per_million ?? 0
  longContextForm.cache_creation_usd_per_million = value.cache_creation_usd_per_million ?? 0
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
  const billingUnit = form.billing_unit
  const requestPriceMode = billingUnit === 'request'
  const requestUSD = requestPriceMode &&
    typeof form.request_usd === 'number' &&
    Number.isFinite(form.request_usd) &&
    form.request_usd >= 0
    ? form.request_usd
    : null
  const preservePartialLongContext = !requestPriceMode &&
    preservedLongContext.value !== null &&
    preserveInvalidLongContext.value
  const longContext = !requestPriceMode && !preservePartialLongContext && longContextEnabled.value
    ? { ...longContextForm }
    : null
  const payload: ModelPricePayload = {
    provider: form.provider.trim(),
    model: form.model.trim(),
    price_scope: form.price_scope,
    channel_auth_type: form.channel_auth_type,
    channel_brand: form.channel_brand,
    channel_key: form.channel_key,
    channel_identity_hash: form.channel_identity_hash,
    billing_unit: billingUnit,
    input_usd_per_million: form.input_usd_per_million,
    output_usd_per_million: form.output_usd_per_million,
    cache_read_usd_per_million: form.cache_read_usd_per_million,
    cache_creation_usd_per_million: form.cache_creation_usd_per_million,
    request_usd: requestUSD,
    long_context: longContext,
    time_pricing: deepSeekForm.value && timePricingEnabled.value ? timeRule.value : null,
    time_pricing_set: true,
  }
  if (!requestPriceMode && preservedLongContext.value !== null) {
    payload.preserve_invalid_long_context = preservePartialLongContext
  }
  if (editingId.value !== null && payload.price_scope === 'channel' && correctLatestVersion.value) {
    payload.correct_latest_version = true
  }
  if (!payload.provider || !payload.model) {
    message.error(t('服务商和模型不能为空', 'Provider and model are required'))
    return
  }
  if (
    payload.price_scope === 'channel' &&
    (!payload.channel_auth_type || !payload.channel_brand || !payload.channel_key || (editingId.value === null && !payload.channel_identity_hash))
  ) {
    message.error(t('渠道标识不完整，请刷新页面后重试', 'Channel identity is incomplete. Refresh and try again.'))
    return
  }
  if (requestPriceMode && requestUSD === null) {
    message.error(t('按次计费需要填写有效的每次调用价格', 'Per-request billing requires a valid per-call price'))
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
  if (price.price_scope === 'channel') {
    return price.channel_brand === 'codex' || price.channel_brand === 'openai_compatibility'
  }
  return ['openai', 'codex'].includes(price.provider.trim().toLowerCase())
}

function openPriorityMultiplierEditor(price: ModelPrice) {
  if (isPrioritySaving.value) return
  priorityVersionRequest += 1
  priorityEditingPrice.value = price
  priorityMultiplier.value = price.priority_multiplier
  priorityCorrectLatest.value = false
  priorityLatestVersion.value = null
  priorityVersionsError.value = ''
  priorityVersionsLoading.value = price.price_scope === 'channel'
  priorityModalOpen.value = true
  if (price.price_scope === 'channel') {
    void loadPriorityPriceVersions(price.id, priorityVersionRequest)
  }
}

async function loadPriorityPriceVersions(priceId: number, request: number) {
  try {
    const versions = await fetchModelPriceVersions(priceId)
    if (priorityVersionRequest === request) {
      priorityLatestVersion.value = versions[versions.length - 1] ?? null
    }
  } catch (error) {
    if (priorityVersionRequest === request) {
      priorityVersionsError.value = errorText(error, '加载价格版本失败', 'Failed to load price versions')
    }
  } finally {
    if (priorityVersionRequest === request) priorityVersionsLoading.value = false
  }
}

async function savePriorityMultiplier() {
  if (isPrioritySaving.value || (priorityCorrectLatest.value && (priorityVersionsLoading.value || priorityVersionsError.value))) return
  const price = priorityEditingPrice.value
  const multiplier = priorityMultiplier.value
  if (!price || multiplier === null || !Number.isFinite(multiplier) || multiplier <= 0) {
    message.error(t('Fast 倍率必须大于 0', 'Fast multiplier must be greater than 0'))
    return
  }
  isPrioritySaving.value = true
  try {
    const correct = price.price_scope === 'channel' && priorityCorrectLatest.value
    await updateModelPricePriorityMultiplier(price.id, { priority_multiplier: multiplier, ...(correct ? { correct_latest_version: true } : {}) })
    priorityModalOpen.value = false
    message.success(t('Fast 倍率已更新', 'Fast multiplier updated'))
    await refresh()
  } catch (error) {
    message.error(errorText(error, '保存 Fast 倍率失败', 'Failed to save Fast multiplier'))
  } finally {
    isPrioritySaving.value = false
  }
}

const selectedSyncModelSet = computed(() => new Set(selectedSyncModels.value))
const syncFilterOptions = computed(() => [
  { label: t('全部候选', 'All candidates'), value: 'all' },
  { label: t('CPA 匹配', 'CPA matches'), value: 'matched' },
  { label: t('已选择', 'Selected'), value: 'selected' },
])
const filteredSyncModels = computed(() => {
  const query = syncSearch.value.trim().toLowerCase()
  return litellmModelOptions.value.filter((option) => {
    if (syncSelectionFilter.value === 'matched' && !option.matched_current) return false
    if (syncSelectionFilter.value === 'selected' && !selectedSyncModelSet.value.has(option.model)) return false
    return !query || `${option.model} ${option.price_model} ${option.provider}`.toLowerCase().includes(query)
  })
})
const pagedSyncModels = computed(() => filteredSyncModels.value.slice((syncPage.value - 1) * syncPageSize, syncPage.value * syncPageSize))
watch([syncSearch, syncSelectionFilter], () => { syncPage.value = 1 })
watch(() => filteredSyncModels.value.length, (length) => {
  syncPage.value = Math.min(syncPage.value, Math.max(1, Math.ceil(length / syncPageSize)))
})

function setSyncModelChecked(model: string, checked: boolean) {
  const next = new Set(selectedSyncModels.value)
  if (checked) next.add(model)
  else next.delete(model)
  selectedSyncModels.value = [...next]
}

function applyChannelPriceMultiplier() {
  const template = channelTemplatePrice.value
  const multiplier = channelPriceMultiplier.value
  if (!template) {
    message.error(t('当前模型没有可用的通用价', 'No library price is available for this model'))
    return
  }
  if (typeof multiplier !== 'number' || !Number.isFinite(multiplier) || multiplier < 0) {
    message.error(t('请输入不小于 0 的有效倍率', 'Enter a valid multiplier of 0 or greater'))
    return
  }
  if (template.billing_unit !== form.billing_unit) {
    message.error(t(
      '通用价与当前渠道价格的计费单位不一致，无法按倍率换算',
      'The library price and current channel price use different billing units and cannot be converted by multiplier',
    ))
    return
  }
  const converted = multiplyModelPrice(template, multiplier)
  if (!converted) {
    message.error(t('倍率换算结果必须是有限非负数，请检查通用价和倍率', 'Converted prices must be finite non-negative numbers. Check the library price and multiplier.'))
    return
  }
  if (form.billing_unit === 'request') {
    form.request_usd = converted.request_usd
  } else {
    form.input_usd_per_million = converted.input_usd_per_million
    form.output_usd_per_million = converted.output_usd_per_million
    form.cache_read_usd_per_million = converted.cache_read_usd_per_million
    form.cache_creation_usd_per_million = converted.cache_creation_usd_per_million
    if (converted.long_context) setLongContextForm(converted.long_context)
  }
  message.success(t('已按倍率换算并填入固定价格', 'Multiplier applied as fixed prices'))
}

function selectMatchedSyncModels() {
  selectedSyncModels.value = litellmModelOptions.value.filter((option) => option.matched_current).map((option) => option.model)
}

function selectFilteredSyncModels() {
  selectedSyncModels.value = [...new Set([...selectedSyncModels.value, ...filteredSyncModels.value.map((option) => option.model)])]
}

async function loadSyncOptions() {
  if (syncOptionsLoading.value || isSyncing.value) return
  syncOptionsLoading.value = true
  syncOptionsError.value = ''
  litellmModelOptions.value = []
  selectedSyncModels.value = []
  syncOptionsResponse.value = null
  try {
    syncOptionsResponse.value = await listLitellmModelOptions()
    litellmModelOptions.value = syncOptionsResponse.value.models
    selectMatchedSyncModels()
  } catch (error) {
    syncOptionsError.value = errorText(error, '加载 LiteLLM 模型列表失败', 'Failed to load LiteLLM model list')
  } finally {
    syncOptionsLoading.value = false
  }
}

async function openSyncModal() {
  if (isSyncing.value || syncOptionsLoading.value) return
  syncSearch.value = ''
  syncSelectionFilter.value = 'all'
  syncPage.value = 1
  syncModalOpen.value = true
  await loadSyncOptions()
}

const channelAliasMap = computed(() => {
  const aliases = new Map<string, string>()
  for (const alias of channelAliases.value) {
    const key = channelIdentityKey(alias.auth_type, alias.channel_brand, alias.channel_key)
    if (key !== null) {
      aliases.set(key, alias.label)
    }
  }
  return aliases
})

function channelAliasLabel(authType: ChannelAuthType | null, brand: string | null, key: string | null) {
  const identity = channelIdentityKey(authType, brand, key)
  return identity === null ? '' : channelAliasMap.value.get(identity) ?? ''
}

function isModelPriceChannelAliasBrand(brand: string | null): brand is AIProviderBrand {
  switch (brand) {
    case 'gemini':
    case 'codex':
    case 'claude':
    case 'vertex':
    case 'xai':
      return true
    default:
      return false
  }
}

function canEditChannelAlias(row: PriceDisplayRow) {
  return row.priceScope === 'channel' && row.channelAuthType === 'apikey' && isModelPriceChannelAliasBrand(row.channelBrand)
    && !!row.channelBrand && !!row.channelKey && !!row.channelIdentityHash && row.channelStatus === 'ready'
}

function openAliasEditor(row: PriceDisplayRow) {
  const brand = row.channelBrand
  if (!canEditChannelAlias(row) || !isModelPriceChannelAliasBrand(brand) || aliasSaving.value || isLoading.value || !row.channelKey || !row.channelIdentityHash) return
  aliasIdentity.auth_type = 'apikey'
  aliasIdentity.channel_brand = brand
  aliasIdentity.channel_key = row.channelKey
  aliasIdentity.channel_identity_hash = row.channelIdentityHash
  aliasAPIKeyMasked.value = row.channelAPIKeyMasked
  aliasLabel.value = channelAliasLabel(row.channelAuthType, row.channelBrand, row.channelKey)
  aliasModalOpen.value = true
}

async function saveAlias() {
  if (aliasSaving.value) return
  aliasSaving.value = true
  try {
    await updateModelPriceChannelAlias({ ...aliasIdentity, label: aliasLabel.value })
    aliasModalOpen.value = false
    await refresh()
    message.success(t('渠道名称已保存', 'Channel name saved'))
  } catch (error) {
    message.error(errorText(error, '保存渠道名称失败', 'Failed to save channel name'))
  } finally {
    aliasSaving.value = false
  }
}

async function syncPrices() {
  if (isSyncing.value || syncOptionsLoading.value || selectedSyncModels.value.length === 0) return
  isSyncing.value = true
  try {
    const result = await syncLitellmModelPrices({ models: selectedSyncModels.value })
    syncModalOpen.value = false
    message.success(
      t(
        `同步完成：新增 ${result.created} 条，更新 ${result.updated} 条，未变 ${result.unchanged} 条，保留手动 ${result.skipped_manual} 条，跳过无效 ${result.skipped_invalid} 条`,
        `Sync complete: ${result.created} created, ${result.updated} updated, ${result.unchanged} unchanged, ${result.skipped_manual} manual prices preserved, ${result.skipped_invalid} invalid skipped`,
      ),
    )
    await refresh()
  } catch (error) {
    message.error(errorText(error, '同步模型价格失败', 'Failed to sync model prices'))
  } finally {
    isSyncing.value = false
  }
}

function openConflictPromotion(conflict: ModelPriceLibraryConflict) {
  resolvingConflict.value = conflict
  conflictProvider.value = conflict.price.provider
  conflictModel.value = conflict.price.model
  conflictModalOpen.value = true
}

async function saveConflictPromotion() {
  const conflict = resolvingConflict.value
  const provider = conflictProvider.value.trim()
  const model = conflictModel.value.trim()
  if (!conflict || !provider || !model) {
    message.error(t('服务商和模型不能为空', 'Provider and model are required'))
    return
  }
  isConflictSaving.value = true
  try {
    await promoteModelPriceLibraryConflict(conflict.original_id, { provider, model })
    conflictModalOpen.value = false
    message.success(t('冲突价格已提升为通用价格', 'Conflict price promoted to a library price'))
    await refresh()
  } catch (error) {
    message.error(errorText(error, '提升冲突价格失败', 'Failed to promote conflict price'))
  } finally {
    isConflictSaving.value = false
  }
}

function confirmReplaceConflict(conflict: ModelPriceLibraryConflict) {
  dialog.warning({
    title: t('替换活动价格', 'Replace active price'),
    content: t(
      `将使用历史价格 ${conflict.price.provider} / ${conflict.price.model} 替换当前活动价格，当前活动价格会保留为迁移冲突。`,
      `Use historical price ${conflict.price.provider} / ${conflict.price.model} as the active price. The current active price will remain as a migration conflict.`,
    ),
    positiveText: t('替换', 'Replace'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      try {
        await replaceActiveModelPriceLibraryConflict(conflict.original_id)
        message.success(t('活动价格已替换', 'Active price replaced'))
        await refresh()
      } catch (error) {
        message.error(errorText(error, '替换活动价格失败', 'Failed to replace active price'))
      }
    },
  })
}

function confirmDeleteConflict(conflict: ModelPriceLibraryConflict) {
  dialog.warning({
    title: t('删除迁移冲突', 'Delete migration conflict'),
    content: `${conflict.price.provider} / ${conflict.price.model}`,
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      try {
        await deleteModelPriceLibraryConflict(conflict.original_id)
        message.success(t('迁移冲突已删除', 'Migration conflict deleted'))
        await refresh()
      } catch (error) {
        message.error(errorText(error, '删除迁移冲突失败', 'Failed to delete migration conflict'))
      }
    },
  })
}

function confirmDelete(row: PriceDisplayRow) {
  if (!row.price) {
    return
  }
  const price = row.price
  dialog.warning({
    title: t('删除价格', 'Delete price'),
    content: `${row.provider} / ${row.model}`,
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      try {
        await deleteModelPrice(price.id)
        message.success(t('模型价格已删除', 'Model price deleted'))
        await refresh()
      } catch (error) {
        message.error(errorText(error, '删除模型价格失败', 'Failed to delete model price'))
      }
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

function groupLibraryPrice(row: PriceGroupRow): ModelPrice | null {
  return findModelGroupLibraryPrice(row.children)
}

function groupHasConfiguredChannelPrice(row: PriceGroupRow): boolean {
  return row.children.some((child) => child.priceScope === 'channel' && child.price !== null && child.status !== 'missing')
}

function renderBillingUnitBadge(unit: ModelPriceBillingUnit | 'mixed') {
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
      // Prices of different models are not comparable; the by-channel group row leaves them blank.
      return h('span', { class: 'price-muted' }, '')
    }
    const library = groupLibraryPrice(row)
    if (modelGroupLibraryPriceState(library, groupHasConfiguredChannelPrice(row)) === 'empty') {
      return h('span', { class: 'price-muted' }, '')
    }
    return h('span', { title: t('通用价', 'Library price') }, !library ? t('未配置', 'Not configured') : library.billing_unit === 'token' ? formatPriceValue(library[field]) : '-')
  }
  if (row.billing_unit === 'request') {
    return h('span', { class: 'price-muted' }, '-')
  }
  return formatPriceValue(row.price?.[field])
}

function renderRequestPriceValue(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    if (row.mode === 'provider') {
      return h('span', { class: 'price-muted' }, '')
    }
    const library = groupLibraryPrice(row)
    if (modelGroupLibraryPriceState(library, groupHasConfiguredChannelPrice(row)) === 'empty') {
      return h('span', { class: 'price-muted' }, '')
    }
    return h('span', { title: t('通用价', 'Library price') }, !library ? t('未配置', 'Not configured') : library.billing_unit === 'request' ? formatPriceValue(library.request_usd) : '-')
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
  const archivedLongContext = row.migrationConflict?.archived_long_context ?? row.price?.preserved_long_context
  if (archivedLongContext) {
    const threshold = archivedLongContext.threshold_input_tokens
    const label = threshold === null ? t('部分配置', 'Partial') : `>${formatThreshold(threshold)}`
    const details = [
      `${t('阈值', 'Threshold')} ${threshold === null ? t('未设置', 'Not set') : formatInteger(threshold)}`,
      `${t('输入', 'Input')} ${formatPreservedLongContextPrice(archivedLongContext, 'input_usd_per_million')}`,
      `${t('输出', 'Output')} ${formatPreservedLongContextPrice(archivedLongContext, 'output_usd_per_million')}`,
      `${t('缓存读', 'Cache read')} ${formatPreservedLongContextPrice(archivedLongContext, 'cache_read_usd_per_million')}`,
      `${t('缓存写', 'Cache write')} ${formatPreservedLongContextPrice(archivedLongContext, 'cache_creation_usd_per_million')}`,
    ].join(' · ')
    return h(
      NTooltip,
      null,
      {
        trigger: () => h(NTag, { size: 'small', type: 'warning', bordered: false }, { default: () => label }),
        default: () => details,
      },
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
  if (row.name && row.name !== row.id) {
    return t(`别名：${row.name}`, `Alias: ${row.name}`)
  }
  return ''
}

function priceGroupSourceSummary(row: PriceGroupRow): string {
  const parts: string[] = []
  if (row.channelCount > 0) {
    parts.push(t(`${formatInteger(row.channelCount)} 个渠道`, `${formatInteger(row.channelCount)} channels`))
  }
  if (row.libraryPriceCount > 0) {
    parts.push(
      t(
        `${formatInteger(row.libraryPriceCount)} 个通用价`,
        `${formatInteger(row.libraryPriceCount)} library prices`,
      ),
    )
  }
  return parts.join(' · ')
}

function renderModelCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    const title =
      row.mode === 'model'
        ? row.label
        : t(`${formatInteger(row.modelCount)} 个模型`, `${formatInteger(row.modelCount)} models`)
    const details =
      row.mode === 'model'
        ? [
            priceGroupSourceSummary(row),
            t(
              `${formatInteger(row.children.length)} 条明细`,
              `${formatInteger(row.children.length)} details`,
            ),
          ].filter(Boolean).join(' · ')
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
            },
            { default: () => t('渠道模型', 'Channel model') },
          )
        : row.priceScope === 'library'
          ? h(
              NTag,
              {
                class: 'model-availability-tag',
                size: 'small',
                type: 'default',
                bordered: false,
              },
              { default: () => t('通用价', 'Library') },
            )
        : null,
    ]),
    subline ? h('div', { class: 'model-sub' }, subline) : null,
  ])
}

function providerGroupUnpricedCount(row: PriceGroupRow): number {
  // Keep the provider subtitle aligned with the operational status column.
  // A saved price can still be conflicted, removed, or otherwise unavailable.
  return row.unpricedCount
}

function channelLabelKeyMask(row: PriceTableRow): string | null {
  if (isPriceGroupRow(row)) {
    if (row.mode !== 'provider') return null
    const firstChild = row.children[0]
    const identityHash = firstChild?.channelIdentityHash
    const mask = firstChild ? channelLabelKeyMask(firstChild) : null
    return mask && identityHash && row.children.every((child) => child.channelIdentityHash === identityHash && channelLabelKeyMask(child) === mask)
      ? mask
      : null
  }
  return row.priceScope === 'channel' && row.channelAuthType === 'apikey' && row.channelBrand !== 'openai_compatibility'
    ? row.channelAPIKeyMasked
    : null
}

function renderChannelLabel(row: PriceTableRow, label: string) {
  const mask = channelLabelKeyMask(row)
  if (!mask?.trim()) {
    return h('span', { class: 'provider-label', title: label }, label)
  }
  const tooltipId = `price-channel-key-${encodeURIComponent(row.key)}`
  const tooltipText = label === mask ? mask : `${label}\n${mask}`
  return h('span', {
    ...channelLabelTooltips.triggerProps(row.key),
    class: ['provider-label', 'channel-key-trigger'],
    tabindex: 0,
    'aria-describedby': tooltipId,
  }, [
    label,
    h(NTooltip, channelLabelTooltips.tooltipProps(row.key), {
      default: () => h('span', { id: tooltipId, role: 'tooltip', class: 'price-channel-key-tooltip' }, tooltipText),
    }),
  ])
}

function renderProviderCell(row: PriceTableRow) {
  if (isPriceGroupRow(row)) {
    const isLibraryGroup = row.children.every((child) => child.priceScope === 'library')
    const title =
      row.mode === 'provider'
        ? isLibraryGroup
          ? t(`${row.label} · 通用价`, `${row.label} · Library`)
          : row.label
        : priceGroupSourceSummary(row)
    const details =
      row.mode === 'provider'
        ? t(
            `${formatInteger(row.modelCount)} 个模型 · ${formatInteger(row.children.length)} 条明细 · ${formatInteger(providerGroupUnpricedCount(row))} 未定价`,
            `${formatInteger(row.modelCount)} models · ${formatInteger(row.children.length)} details · ${formatInteger(providerGroupUnpricedCount(row))} unpriced`,
          )
        : null
    return h('div', { class: 'price-group-cell' }, [
      h('div', { class: 'price-group-title' }, [renderChannelLabel(row, title)]),
      details ? h('div', { class: 'price-group-sub' }, details) : null,
    ])
  }
  const detailParts = row.migrationConflict
    ? [t('待解决旧价格', 'Unresolved legacy price')]
    : row.priceScope === 'library'
      ? [t('通用价格库', 'Price library')]
    : [channelBrandLabel(row.channelBrand)]
  if (row.channelDisabled) {
    detailParts.push(t('已停用', 'Disabled'))
  }
  if (row.channelLabelFallback) {
    detailParts.push(t('标签回退', 'Fallback label'))
  }
  return h('div', { class: 'provider-cell' }, [
    h('div', { class: 'provider-main' }, [
      renderChannelLabel(row, row.provider || '-'),
      canEditChannelAlias(row)
        ? h(NTooltip, {}, {
            trigger: () => h(NButton, {
              size: 'tiny', quaternary: true, circle: true, disabled: aliasSaving.value || isLoading.value,
              'aria-label': t(`设置渠道名称：${row.provider}`, `Set channel name: ${row.provider}`),
              onClick: () => openAliasEditor(row),
            }, { icon: () => h(NIcon, { component: Pencil }) }),
            default: () => t('设置渠道名称', 'Set channel name'),
          })
        : null,
    ]),
    h('div', { class: 'model-sub' }, detailParts.join(' · ')),
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
  if (row.channelStatus === 'conflict') {
    return h(NTag, { size: 'small', type: 'error', bordered: false }, { default: () => t('渠道冲突', 'Channel conflict') })
  }
  if (row.channelStatus === 'migration_conflict') {
    return h(NTag, { size: 'small', type: 'error', bordered: false }, { default: () => t('迁移冲突', 'Migration conflict') })
  }
  if (row.channelStatus === 'missing_selector') {
    return h(NTag, { size: 'small', type: 'warning', bordered: false }, { default: () => t('缺少标识', 'Missing identity') })
  }
  if (row.channelStatus === 'orphan') {
    return h(NTag, { size: 'small', type: 'warning', bordered: false }, { default: () => t('渠道已移除', 'Channel removed') })
  }
  if (row.channelStatus === 'model_removed') {
    return h(NTag, { size: 'small', type: 'warning', bordered: false }, { default: () => t('模型已移除', 'Model removed') })
  }
  if (row.channelStatus === 'unavailable') {
    return h(NTag, { size: 'small', type: 'warning', bordered: false }, { default: () => t('渠道配置不可用', 'Channel unavailable') })
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
  if (row.migrationConflict) {
    const conflict = row.migrationConflict
    return h(
      NSpace,
      { size: 4 },
      {
        default: () => [
          h(
            NButton,
            { size: 'small', type: 'primary', secondary: true, onClick: () => openConflictPromotion(conflict) },
            { default: () => t('提升', 'Promote') },
          ),
          h(
            NButton,
            { size: 'small', quaternary: true, onClick: () => confirmReplaceConflict(conflict) },
            { default: () => t('替换', 'Replace') },
          ),
          h(
            NButton,
            { size: 'small', quaternary: true, type: 'error', onClick: () => confirmDeleteConflict(conflict) },
            { default: () => t('删除', 'Delete') },
          ),
        ],
      },
    )
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
              { size: 'small', quaternary: true, onClick: () => openEdit(row) },
              { default: () => t('改价', 'Edit') },
            )
          : h(
              NButton,
              {
                size: 'small',
                type: 'primary',
                secondary: true,
                disabled: row.channelStatus !== 'ready',
                onClick: () => openCreateForRow(row),
              },
              { default: () => t('设价', 'Set price') },
            ),
        row.price
          ? h(
              NButton,
              { size: 'small', quaternary: true, type: 'error', onClick: () => confirmDelete(row) },
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
    title: t('渠道', 'Channel'),
    key: 'provider',
    width: 160,
    ellipsis: { tooltip: false },
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
  invalidatePriceFormRequests()
  desktopPriceLayoutQuery.removeEventListener('change', handleDesktopPriceLayoutChange)
})
</script>

<template>
  <section class="page price-page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('模型价格', 'Model prices') }}</h1>
        <p class="page-subtitle">
          {{ t('每个真实渠道独立定价；通用价格仅用于预填，不参与渠道账单', 'Each actual channel has independent prices. Library prices are templates and never bill channel usage.') }}
        </p>
      </div>
      <NSpace>
        <NButton secondary :disabled="isLoading" @click="timeTemplateOpen = true">{{ t('DeepSeek 峰谷模板', 'DeepSeek time pricing template') }}</NButton>
        <NButton secondary :loading="isSyncing || syncOptionsLoading" :disabled="isLoading" @click="openSyncModal">
          <template #icon>
            <NIcon :component="RefreshCw" />
          </template>
          {{ t('同步 LiteLLM', 'Sync LiteLLM') }}
        </NButton>
        <NButton type="primary" @click="() => openCreate()">{{ t('新增通用价', 'Add library price') }}</NButton>
      </NSpace>
    </div>

    <DeepSeekTemplateModal v-model:show="timeTemplateOpen" :prices="prices" :labels="priceLabels" @applied="refresh" />
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
              <span class="filter-label">{{ t('渠道', 'Channel') }}</span>
              <NSelect
                v-model:value="selectedProvider"
                class="provider-filter"
                :options="providerOptions"
                clearable
                filterable
                :placeholder="t('全部渠道', 'All channels')"
              />
              <NSelect
                v-model:value="selectedStatus"
                class="status-filter"
                :options="statusOptions"
                :render-label="renderStatusOptionLabel"
                :consistent-menu-width="false"
                :menu-props="{ class: 'price-status-menu' }"
                clearable
                :placeholder="t('当前记录', 'Current records')"
              />
              <NInput
                v-model:value="searchQuery"
                class="price-search"
                clearable
                :placeholder="t('搜索模型、别名或渠道', 'Search models, aliases, or channels')"
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
      v-model:show="syncModalOpen"
      preset="card"
      :title="t('选择要同步的 LiteLLM 模型', 'Select LiteLLM models to sync')"
      :style="{ width: 'min(760px, calc(100vw - 32px))' }"
      :closable="!isSyncing"
      :mask-closable="!isSyncing"
      :close-on-esc="!isSyncing"
    >
      <NAlert v-if="syncOptionsError" type="error" class="sync-error">
        <div class="sync-error-content">
          <span>{{ syncOptionsError }}</span>
          <NButton size="small" @click="loadSyncOptions">{{ t('重试', 'Retry') }}</NButton>
        </div>
      </NAlert>
      <NAlert v-if="syncOptionsResponse?.channel_error || syncOptionsResponse?.oauth_channel_error" type="warning" class="sync-error">
        {{ t('部分 CPA 渠道不可用，默认选择可能不完整。', 'Some CPA channels are unavailable; the default selection may be incomplete.') }}
        {{ serverText(syncOptionsResponse.channel_error || syncOptionsResponse.oauth_channel_error || '') }}
      </NAlert>
      <NSpin :show="syncOptionsLoading">
        <div class="sync-toolbar">
          <NInput v-model:value="syncSearch" clearable :disabled="syncOptionsLoading || isSyncing" :placeholder="t('搜索模型或服务商', 'Search models or providers')">
            <template #prefix><NIcon :component="Search" /></template>
          </NInput>
          <NSelect v-model:value="syncSelectionFilter" :options="syncFilterOptions" :disabled="syncOptionsLoading || isSyncing" />
        </div>
        <NSpace :size="8" class="sync-actions">
          <NButton size="small" :disabled="syncOptionsLoading || isSyncing || !litellmModelOptions.length" @click="selectMatchedSyncModels">{{ t('选择 CPA 匹配', 'Select CPA matches') }}</NButton>
          <NButton size="small" :disabled="syncOptionsLoading || isSyncing || !filteredSyncModels.length" @click="selectFilteredSyncModels">{{ t('选择筛选结果', 'Select filtered') }}</NButton>
          <NButton size="small" :disabled="syncOptionsLoading || isSyncing || !selectedSyncModels.length" @click="selectedSyncModels = []">{{ t('清空选择', 'Clear selection') }}</NButton>
        </NSpace>
        <div class="sync-model-list">
          <NCheckbox
            v-for="option in pagedSyncModels"
            :key="option.model"
            :checked="selectedSyncModelSet.has(option.model)"
            :disabled="isSyncing"
            @update:checked="setSyncModelChecked(option.model, $event)"
          >
            <span class="sync-model-name" :title="option.model">{{ option.model }}</span>
            <span class="sync-model-provider">{{ option.provider }}</span>
            <NTag v-if="option.matched_current" size="small" :bordered="false" type="success">{{ t('CPA 匹配', 'CPA match') }}</NTag>
          </NCheckbox>
          <NEmpty v-if="!syncOptionsLoading && !syncOptionsError && !pagedSyncModels.length" :description="t('没有匹配的模型', 'No matching models')" />
        </div>
        <div class="sync-summary">
          <span>{{ t(`已选 ${formatInteger(selectedSyncModels.length)} / 候选 ${formatInteger(litellmModelOptions.length)}`, `${formatInteger(selectedSyncModels.length)} selected / ${formatInteger(litellmModelOptions.length)} candidates`) }}</span>
          <span v-if="syncOptionsResponse">{{ t('数据量', 'Payload') }} {{ (syncOptionsResponse.payload_bytes / 1024 / 1024).toFixed(2) }} MB</span>
        </div>
        <NPagination v-model:page="syncPage" :page-size="syncPageSize" :item-count="filteredSyncModels.length" :disabled="syncOptionsLoading || isSyncing" simple />
      </NSpin>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isSyncing" @click="syncModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isSyncing" :disabled="syncOptionsLoading || selectedSyncModels.length === 0" @click="syncPrices">{{ t('开始同步', 'Sync selected') }}</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="conflictModalOpen"
      preset="card"
      :title="t('提升迁移冲突价格', 'Promote migration conflict price')"
      :style="conflictModalStyle"
    >
      <NAlert type="warning" :show-icon="false">
        {{ t(
          '修改服务商或模型，使其不再与当前活动价格冲突。价格数值、Fast 倍率和长上下文字段会保留；同步来源会重置为手动，历史同步标记将被清除。',
          'Change the provider or model so it no longer conflicts with the active price. Price values, Fast multiplier, and long-context fields are preserved. Sync ownership is reset to manual and historical sync metadata is cleared.',
        ) }}
      </NAlert>
      <NForm label-placement="top" style="margin-top: 16px">
        <NFormItem :label="t('服务商', 'Provider')">
          <NInput v-model:value="conflictProvider" :disabled="isConflictSaving" />
        </NFormItem>
        <NFormItem :label="t('模型', 'Model')">
          <NInput v-model:value="conflictModel" :disabled="isConflictSaving" />
        </NFormItem>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isConflictSaving" @click="conflictModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isConflictSaving" @click="saveConflictPromotion">{{ t('提升', 'Promote') }}</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="modalOpen"
      preset="card"
      :title="editingId === null
        ? (isChannelPriceForm ? t('设置渠道价格', 'Set channel price') : t('新增通用价', 'Add library price'))
        : (isChannelPriceForm ? t('编辑渠道价格', 'Edit channel price') : t('编辑通用价', 'Edit library price'))"
      :style="priceModalStyle"
      class="price-modal"
    >
      <NForm :model="form" label-placement="top" :disabled="isPriceSaving" class="price-form">
        <!-- Section 1: identity -->
        <section class="price-section">
          <div class="form-grid">
            <NFormItem :label="isChannelPriceForm ? t('渠道', 'Channel') : t('服务商', 'Provider')">
              <NInput v-if="isChannelPriceForm" :value="editingChannelLabel" disabled />
              <NInput v-else v-model:value="form.provider" />
            </NFormItem>
            <NFormItem :label="t('模型', 'Model')">
              <NInput v-model:value="form.model" :disabled="isChannelPriceForm" />
            </NFormItem>
            <NFormItem :label="t('计费方式', 'Billing mode')" class="wide-form-item">
              <NRadioGroup v-model:value="form.billing_unit" class="billing-unit-options" :disabled="isPriceSaving">
                <NRadioButton
                  v-for="option in billingUnitOptions"
                  :key="option.value"
                  :value="option.value"
                >
                  {{ option.label }}
                </NRadioButton>
              </NRadioGroup>
            </NFormItem>
          </div>
        </section>

        <!-- Section 2: base rates -->
        <section class="price-section">
          <header class="price-section-header">
            <span class="price-section-title">{{ isRequestPriceForm ? t('价格', 'Price') : t('标准价格（USD / 1M）', 'Standard prices (USD / 1M)') }}</span>
            <div v-if="isChannelPriceForm && channelTemplatePrice" class="multiplier-control">
              <NInputNumber
                v-model:value="channelPriceMultiplier"
                size="small"
                :min="0"
                :precision="6"
                :step="0.01"
                clearable
                :disabled="isPriceSaving"
                :placeholder="t('通用价倍率', 'Library multiplier')"
              />
              <NButton size="small" secondary :disabled="isPriceSaving || channelPriceMultiplier === null" @click="applyChannelPriceMultiplier">
                {{ t('按通用价换算', 'Apply from library') }}
              </NButton>
            </div>
          </header>
          <NAlert
            v-if="preservedLongContext"
            type="warning"
            :show-icon="false"
          >
            <NSpace vertical size="small">
              <strong>{{ t('保留的历史部分长上下文配置', 'Preserved partial long-context configuration') }}</strong>
              <span>{{ preservedLongContextSummary }}</span>
              <NCheckbox v-model:checked="preserveInvalidLongContext" :disabled="isPriceSaving">
                {{ t('保存时保持这些原始字段不变', 'Keep these raw fields unchanged when saving') }}
              </NCheckbox>
              <span>
                {{ t(
                  '取消保留后，开启下方阶梯将用完整配置替换；保持关闭则会清除这些历史字段。',
                  'After clearing this option, enable the tier below to replace it with a complete configuration, or leave it disabled to clear the historical fields.',
                ) }}
              </span>
            </NSpace>
          </NAlert>
          <NFormItem v-if="isRequestPriceForm" :label="t('每次调用价格 USD', 'Per-call price USD')">
            <NInputNumber v-model:value="form.request_usd" :min="0" :placeholder="t('例如：0.04', 'Example: 0.04')" />
          </NFormItem>
          <div v-else class="rate-grid">
            <NFormItem :label="t('输入', 'Input')">
              <NInputNumber v-model:value="form.input_usd_per_million" :min="0" />
            </NFormItem>
            <NFormItem :label="t('输出', 'Output')">
              <NInputNumber v-model:value="form.output_usd_per_million" :min="0" />
            </NFormItem>
            <NFormItem :label="t('缓存读', 'Cache read')">
              <NInputNumber v-model:value="form.cache_read_usd_per_million" :min="0" />
            </NFormItem>
            <NFormItem :label="t('缓存写', 'Cache write')">
              <NInputNumber v-model:value="form.cache_creation_usd_per_million" :min="0" />
            </NFormItem>
          </div>
        </section>

        <!-- Section 3: long-context tier -->
        <section v-if="!isRequestPriceForm" class="price-section">
          <header class="price-section-header">
            <span class="price-section-title">{{ t('长上下文阶梯', 'Long-context tier') }}</span>
            <NSwitch
              v-model:value="longContextEnabled"
              :disabled="isPriceSaving"
              :aria-label="t('启用长上下文阶梯', 'Enable long-context tier')"
            />
          </header>
          <template v-if="longContextEnabled">
            <NFormItem :label="t('输入 Token 阈值', 'Input token threshold')">
              <NInputNumber
                v-model:value="longContextForm.threshold_input_tokens"
                :min="1"
                :precision="0"
                :step="1000"
              />
            </NFormItem>
            <div class="rate-grid">
              <NFormItem :label="t('输入', 'Input')">
                <NInputNumber v-model:value="longContextForm.input_usd_per_million" :min="0" />
              </NFormItem>
              <NFormItem :label="t('输出', 'Output')">
                <NInputNumber v-model:value="longContextForm.output_usd_per_million" :min="0" />
              </NFormItem>
              <NFormItem :label="t('缓存读', 'Cache read')">
                <NInputNumber v-model:value="longContextForm.cache_read_usd_per_million" :min="0" />
              </NFormItem>
              <NFormItem :label="t('缓存写', 'Cache write')">
                <NInputNumber v-model:value="longContextForm.cache_creation_usd_per_million" :min="0" />
              </NFormItem>
            </div>
          </template>
        </section>

        <!-- Section 4: time pricing (DeepSeek channel token prices only) -->
        <section v-if="deepSeekForm" class="price-section">
          <header class="price-section-header">
            <span class="price-section-title">{{ t('峰谷期', 'Time pricing') }}</span>
            <NSwitch :value="timePricingEnabled" :disabled="isPriceSaving || timeRuleLoading" :loading="timeRuleLoading" @update:value="toggleTimePricing" />
          </header>
          <template v-if="timePricingEnabled && timeRule">
            <details>
              <summary>{{ t('配置峰谷规则', 'Configure time pricing') }}</summary>
              <PriceTimeRuleEditor v-model="timeRule" :disabled="isPriceSaving" />
            </details>
            <PriceRatesMatrix :rule="timeRule" :rates="form" :long-context="longContextEnabled ? longContextForm : null" />
          </template>
        </section>

        <!-- Section 5: version history (existing channel prices) -->
        <section v-if="isChannelPriceForm && editingId !== null" class="price-section">
          <header class="price-section-header">
            <span class="price-section-title">
              {{ t('价格版本', 'Price versions') }}
              <span v-if="priceVersions.length" class="price-section-count">{{ priceVersions.length }}</span>
            </span>
            <label class="switch-row">
              <span>{{ t('修正上一次价格', 'Correct last price') }}</span>
              <NSwitch
                v-model:value="correctLatestVersion"
                size="small"
                :disabled="isPriceSaving || priceVersionsLoading || !!priceVersionsError"
                :aria-label="t('修正上一次价格而不新增版本', 'Correct the last price instead of appending a version')"
              />
            </label>
          </header>
          <NAlert v-if="correctLatestVersion && priceVersions.length" type="warning" :show-icon="false">{{ t('将覆盖最新版本（包括待生效版本），保留其生效时间：', 'Overwrites the latest version, including a scheduled version, retaining its effective time:') }} {{ formatDateTime(priceVersions[priceVersions.length - 1]!.effective_at) }}</NAlert>
          <NAlert v-if="priceVersionsError" type="error" :show-icon="false">{{ priceVersionsError }}</NAlert>
          <NSpin v-else :show="priceVersionsLoading" size="small">
            <ol v-if="priceVersions.length > 0" class="price-version-list">
              <li v-for="version in visiblePriceVersions" :key="version.id" class="price-version-item" :class="{ 'price-version-latest': version.id === latestVersionId }">
                <div class="price-version-head">
                  <span class="price-version-time">{{ formatDateTime(version.effective_at) }}</span>
                  <span v-if="Date.parse(version.effective_at) > Date.now()" class="price-version-tag price-version-tag-scheduled">{{ t('待生效', 'Scheduled') }}</span>
                  <span v-else-if="version.id === currentVersionId" class="price-version-tag price-version-tag-current">{{ t('当前生效', 'Currently effective') }}</span>
                  <span v-if="version.id === latestVersionId" class="price-version-tag">{{ t('最新', 'Latest') }}</span>
                  <span class="price-version-origin">{{ version.baseline ? t('迁移基线', 'Migration baseline') : t('保存', 'Saved') }}</span>
                </div>
                <PriceRatesMatrix
                  :rates="version"
                  :long-context="version.long_context"
                  :preserved-long-context="version.preserved_long_context"
                  :rule="version.time_pricing"
                  :billing-unit="version.billing_unit"
                  :request-usd="version.request_usd"
                  :priority-multiplier="version.priority_multiplier"
                  :show-fast="editingSupportsFast"
                  :show-time-disabled="deepSeekForm"
                />
              </li>
            </ol>
            <div v-else-if="!priceVersionsLoading" class="price-versions-empty">
              {{ t('暂无价格版本；保存后将创建第一条版本。', 'No price versions yet. Saving will create the first version.') }}
            </div>
            <NButton
              v-if="priceVersions.length > collapsedVersionCount"
              text
              size="small"
              class="price-versions-toggle"
              @click="showAllVersions = !showAllVersions"
            >
              {{ showAllVersions
                ? t('收起', 'Show fewer')
                : t(`显示全部 ${priceVersions.length} 个版本`, `Show all ${priceVersions.length} versions`) }}
            </NButton>
          </NSpin>
        </section>
      </NForm>
      <p class="price-save-hint">{{ priceSaveHint }}</p>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isPriceSaving" @click="modalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isPriceSaving" :disabled="timeRuleLoading" @click="savePrice">{{ t('保存', 'Save') }}</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal v-model:show="aliasModalOpen" preset="card" :title="t('设置渠道名称', 'Set channel name')" :style="{ width: 'min(460px, calc(100vw - 32px))' }" :closable="!aliasSaving" :mask-closable="!aliasSaving" :close-on-esc="!aliasSaving">
      <NForm label-placement="top">
        <NFormItem :label="t('渠道名称', 'Channel name')">
          <NInput v-model:value="aliasLabel" maxlength="200" show-count clearable :placeholder="t('默认名称', 'Default name')" :disabled="aliasSaving" />
        </NFormItem>
        <div class="model-sub">
          {{ channelBrandLabel(aliasIdentity.channel_brand) }} ·
          {{ aliasAPIKeyMasked || t('Key 不可用', 'Key unavailable') }}
        </div>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="aliasSaving || !aliasLabel" @click="aliasLabel = ''">
            <template #icon><NIcon :component="RotateCcw" /></template>
            {{ t('恢复默认名称', 'Reset name') }}
          </NButton>
          <NButton :disabled="aliasSaving" @click="aliasModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="aliasSaving" @click="saveAlias">{{ t('保存', 'Save') }}</NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal
      v-model:show="priorityModalOpen"
      preset="card"
      :title="t('编辑 Fast 倍率', 'Edit Fast multiplier')"
      :style="priorityModalStyle"
      class="priority-modal"
      :closable="!isPrioritySaving"
      :mask-closable="!isPrioritySaving"
      :close-on-esc="!isPrioritySaving"
    >
      <NForm label-placement="top">
        <NFormItem :label="t('Fast 倍率', 'Fast multiplier')">
          <NInputNumber v-model:value="priorityMultiplier" :min="0" :disabled="isPrioritySaving" />
        </NFormItem>
        <template v-if="priorityEditingPrice?.price_scope === 'channel'">
          <p v-if="priorityVersionsLoading" class="price-save-hint">{{ t('加载价格版本…', 'Loading price versions…') }}</p>
          <NAlert v-if="priorityVersionsError" type="error" :show-icon="false">{{ priorityVersionsError }}</NAlert>
          <div class="switch-row">
            <span>{{ t('修正上一次价格', 'Correct last price') }}</span>
            <NSwitch
              v-model:value="priorityCorrectLatest"
              :disabled="isPrioritySaving || priorityVersionsLoading || !!priorityVersionsError"
              :aria-label="t('修正上一次价格而不新增版本', 'Correct the last price instead of appending a version')"
            />
          </div>
          <NAlert v-if="priorityCorrectLatest" type="warning" :show-icon="false">
            <template v-if="priorityLatestVersion">
              {{ t('将修正最新版本，保留生效时间：', 'Corrects the latest version, retaining its effective time:') }}
              {{ formatDateTime(priorityLatestVersion.effective_at) }}
              <strong v-if="Date.parse(priorityLatestVersion.effective_at) > Date.now()">{{ t('（待生效）', ' (scheduled)') }}</strong>
              <div>{{ t('该版本当前 Fast 倍率：', 'Current Fast multiplier of this version:') }} {{ priorityLatestVersion.priority_multiplier === null ? t('默认', 'Default') : `${formatMultiplier(priorityLatestVersion.priority_multiplier)}x` }}</div>
            </template>
            <template v-else>{{ t('尚无历史版本，将从保存时刻新增版本。', 'No price history exists yet; a new version will start at save time.') }}</template>
          </NAlert>
          <p class="price-save-hint">
            {{ priorityCorrectLatest
              ? t('将原地修正最新一条价格版本的 Fast 倍率并保留其生效时间；该时刻之后的请求在历史统计中按修正价重算，已结算扣费不会改写。', 'Overwrites the Fast multiplier of the latest price version in place and keeps its effective time. Requests since that moment are recalculated in history; settled charges are not rewritten.')
              : t('保存后新增一条价格版本，从现在起生效。', 'Saving appends a new price version effective from now.') }}
          </p>
        </template>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton :disabled="isPrioritySaving" @click="priorityModalOpen = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isPrioritySaving" :disabled="priorityCorrectLatest && (priorityVersionsLoading || !!priorityVersionsError)" @click="savePriorityMultiplier">{{ t('保存', 'Save') }}</NButton>
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

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

.wide-form-item {
  grid-column: 1 / -1;
}

.billing-unit-options {
  display: flex;
  flex-wrap: wrap;
  max-width: 100%;
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
  width: 180px;
}

:global(.price-status-menu .n-base-select-menu__item) {
  min-height: 36px;
  white-space: normal;
}

:global(.price-status-menu) {
  min-width: 260px !important;
  max-width: calc(100vw - 24px);
}

:global(.price-status-menu .status-option-label) {
  display: block;
  max-width: min(320px, calc(100vw - 48px));
  overflow-wrap: anywhere;
  white-space: normal;
  line-height: 1.35;
}

.status-filter :deep(.n-base-selection-label) {
  min-height: 34px;
  white-space: normal;
}

.status-filter :deep(.n-base-selection) {
  min-height: 34px;
  height: auto;
}

.status-filter :deep(.n-base-selection-input__content) {
  white-space: normal;
  overflow-wrap: anywhere;
  line-height: 1.35;
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

.multiplier-control {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.multiplier-control :deep(.n-input-number) {
  width: 150px;
  max-width: 100%;
}

.price-form {
  display: grid;
  gap: 4px;
}

.price-section {
  display: grid;
  gap: 8px;
  padding: 12px 0;
  border-top: 1px solid var(--cpa-border);
}

.price-section:first-child {
  padding-top: 0;
  border-top: 0;
}

.price-section-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 28px;
}

.price-section-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--cpa-text);
  font-size: 14px;
  font-weight: 600;
}

.price-section-count {
  padding: 0 6px;
  border-radius: 999px;
  background: var(--cpa-surface-muted);
  color: var(--cpa-text-muted);
  font-size: 11px;
  font-weight: 600;
}

.price-section :deep(.n-form-item) {
  --n-feedback-height: 0;
}

.rate-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px 12px;
}

.switch-row {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--cpa-text);
  font-size: 13px;
}

.price-version-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.price-version-item {
  display: grid;
  gap: 6px;
  padding: 8px 10px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface-muted);
}

.price-version-item.price-version-latest {
  border-color: color-mix(in srgb, var(--cpa-primary) 40%, var(--cpa-border));
  background: var(--cpa-surface);
}

.price-version-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}

.price-version-time {
  color: var(--cpa-text);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.price-version-origin {
  margin-left: auto;
  color: var(--cpa-text-muted);
}

.price-version-tag {
  padding: 1px 6px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--cpa-primary) 14%, transparent);
  color: var(--cpa-primary);
  font-size: 11px;
  font-weight: 600;
}

.price-version-tag-current {
  background: var(--cpa-success-weak);
  color: var(--cpa-success);
}

.price-version-tag-scheduled {
  background: var(--cpa-warning-weak);
  color: var(--cpa-warning);
}

.price-versions-toggle {
  justify-self: start;
  margin-top: 6px;
}

.price-versions-empty {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.price-table :deep(.provider-main) {
  display: flex;
  align-items: center;
  gap: 4px;
}

.price-table :deep(.provider-label) {
  display: block;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.price-table :deep(.channel-key-trigger) {
  cursor: help;
}

.price-table :deep(.channel-key-trigger:focus-visible) {
  outline: 2px solid var(--cpa-primary);
  outline-offset: -2px;
}

:global(.price-channel-key-tooltip) {
  display: block;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.price-table :deep(.provider-main .n-button) {
  flex: 0 0 auto;
}

.sync-toolbar {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 150px;
  gap: 8px;
}

.sync-actions,
.sync-error {
  margin-bottom: 12px;
}

.sync-actions {
  margin-top: 8px;
}

.sync-model-list {
  height: min(380px, 42dvh);
  overflow: auto;
  overscroll-behavior: contain;
}

.sync-model-list > .n-checkbox {
  display: flex;
  min-height: 36px;
  align-items: center;
  border-bottom: 1px solid var(--cpa-border);
}

.sync-model-list :deep(.n-checkbox__label) {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  min-width: 0;
}

.sync-model-name {
  overflow-wrap: anywhere;
}

.sync-model-provider,
.sync-summary {
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.sync-summary {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 4px 12px;
  margin: 10px 0;
}

.sync-error-content {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

@media (max-width: 480px) {
  .sync-toolbar {
    grid-template-columns: minmax(0, 1fr);
  }

  .rate-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
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
    width: min(220px, calc(100vw - 32px));
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
    grid-template-columns: minmax(0, 1fr);
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
