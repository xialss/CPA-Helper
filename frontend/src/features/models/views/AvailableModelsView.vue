<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NSpace,
  NSpin,
  NTag,
  type DataTableColumns,
} from 'naive-ui'
import { Cpu, KeyRound, RefreshCw, ShieldCheck } from 'lucide-vue-next'

import { listAvailableModels } from '@/features/models/api/availableModelsApi'
import { useI18n } from '@/shared/i18n'
import type { AvailableModel, AvailableModelPrice, AvailableModelsResponse } from '@/shared/types/api'

type PriceField = keyof Pick<
  AvailableModelPrice,
  | 'input_usd_per_million'
  | 'output_usd_per_million'
  | 'cache_read_usd_per_million'
  | 'cache_creation_usd_per_million'
>
type BillingUnit = 'token' | 'request'

const router = useRouter()
const { currentLanguage, errorText, serverText, t } = useI18n()
const isLoading = ref(false)
const errorMessage = ref<string | null>(null)
const response = ref<AvailableModelsResponse | null>(null)

const modelCount = computed(() => response.value?.models.length ?? 0)
const keySummary = computed(() => {
  if (!response.value) {
    return '-'
  }
  return `${response.value.queryable_api_key_count} / ${response.value.api_key_count}`
})
const queryStatus = computed(() => {
  if (!response.value) {
    return '-'
  }
  if (response.value.errors.length > 0) {
    return t(`部分失败 ${response.value.errors.length}`, `${response.value.errors.length} failed`)
  }
  if (response.value.queryable_api_key_count === 0) {
    return response.value.has_api_keys ? t('不可查询', 'Unavailable') : t('无 Key', 'No keys')
  }
  return response.value.has_api_keys ? t('正常', 'Normal') : t('无 Key', 'No keys')
})

function displayText(value: string | null | undefined): string {
  return value?.trim() || '-'
}

function formatUsdPerMtok(value: number): string {
  return value.toLocaleString(currentLanguage.value === 'zh' ? 'zh-CN' : 'en-US', {
    maximumFractionDigits: 6,
  })
}

function billingUnitForModel(model: string): BillingUnit {
  return model.trim().toLowerCase().includes('image') ? 'request' : 'token'
}

function modelBillingUnit(row: AvailableModel): BillingUnit {
  if (row.price?.billing_unit === 'request') {
    return 'request'
  }
  if (row.price?.billing_unit === 'token') {
    return 'token'
  }
  return billingUnitForModel(row.price?.model || row.id)
}

function renderBillingUnit(row: AvailableModel) {
  const unit = modelBillingUnit(row)
  const isRequest = unit === 'request'
  return h(
    'span',
    {
      style: {
        display: 'inline-flex',
        alignItems: 'center',
        minHeight: '22px',
        padding: '2px 8px',
        borderRadius: '6px',
        background: isRequest ? 'rgba(16, 185, 129, 0.13)' : 'rgba(124, 58, 237, 0.12)',
        color: isRequest ? '#047857' : '#6d28d9',
        fontSize: '12px',
        fontWeight: '600',
        lineHeight: '1.2',
      },
    },
    isRequest ? t('按次', 'Per request') : t('按 Token', 'Per token'),
  )
}

function renderRequestPrice(row: AvailableModel) {
  if (modelBillingUnit(row) !== 'request') {
    return h('span', { class: 'model-price-muted' }, '-')
  }
  if (row.price?.request_usd === null || row.price?.request_usd === undefined) {
    return h('span', { class: 'model-price-muted' }, t('未定价', 'Unpriced'))
  }
  return formatUsdPerMtok(row.price.request_usd)
}

function renderPriceValue(row: AvailableModel, field: PriceField) {
  if (modelBillingUnit(row) === 'request') {
    return h('span', { class: 'model-price-muted' }, '-')
  }
  if (!row.price) {
    return '-'
  }
  return formatUsdPerMtok(row.price[field])
}

function goToApiKeys() {
  void router.push('/account/keys')
}

async function refresh() {
  isLoading.value = true
  errorMessage.value = null
  try {
    response.value = await listAvailableModels()
  } catch (error) {
    response.value = null
    errorMessage.value = errorText(error, '加载可用模型失败', 'Failed to load available models')
  } finally {
    isLoading.value = false
  }
}

const columns = computed<DataTableColumns<AvailableModel>>(() => [
  {
    title: t('模型 ID', 'Model ID'),
    key: 'id',
    width: 270,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'model-id' }, row.id),
  },
  {
    title: t('名称', 'Name'),
    key: 'name',
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => displayText(row.name),
  },
  {
    title: t('所有者', 'Owner'),
    key: 'owner',
    width: 150,
    render: (row) => displayText(row.owner),
  },
  {
    title: t('来源 Key', 'Source Key'),
    key: 'sources',
    width: 220,
    render: (row) =>
      h(
        NSpace,
        { size: 4, wrap: true },
        {
          default: () =>
            row.sources.map((source) =>
              h(
                NTag,
                { key: source.api_key_hash, size: 'small', bordered: false, type: 'info' },
                { default: () => `${source.description} · ${source.api_key_preview}` },
              ),
            ),
        },
      ),
  },
  {
    title: t('计费方式', 'Billing'),
    key: 'billing_unit',
    width: 110,
    render: renderBillingUnit,
  },
  {
    title: t('每次 ($)', 'Per request ($)'),
    key: 'request_usd',
    width: 110,
    render: renderRequestPrice,
  },
  {
    title: t('输入 ($/MTok)', 'Input ($/MTok)'),
    key: 'input_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'input_usd_per_million'),
  },
  {
    title: t('输出 ($/MTok)', 'Output ($/MTok)'),
    key: 'output_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'output_usd_per_million'),
  },
  {
    title: t('缓存读 ($/MTok)', 'Cache read ($/MTok)'),
    key: 'cache_read_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'cache_read_usd_per_million'),
  },
  {
    title: t('缓存写 ($/MTok)', 'Cache write ($/MTok)'),
    key: 'cache_creation_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'cache_creation_usd_per_million'),
  },
])

onMounted(refresh)
</script>

<template>
  <section class="page models-page" :aria-busy="isLoading">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('可用模型', 'Available Models') }}</h1>
        <p class="page-subtitle">{{ t('通过当前账号绑定的 CPA API Key 查询并聚合', 'Query and aggregate models from CPA API keys bound to the current account') }}</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">
          <template #icon>
            <NIcon :component="RefreshCw" />
          </template>
          {{ t('刷新', 'Refresh') }}
        </NButton>
      </NSpace>
    </div>

    <section class="panel model-table-panel">
      <div class="panel-inner model-panel">
        <NAlert v-if="errorMessage" type="error" :bordered="false">
          <div class="alert-row">
            <span>{{ errorMessage }}</span>
            <NButton size="small" secondary :loading="isLoading" @click="refresh">{{ t('重试', 'Retry') }}</NButton>
          </div>
        </NAlert>

        <template v-else>
          <NAlert
            v-if="response?.errors.length"
            type="warning"
            :bordered="false"
            :title="t('部分 API Key 查询失败', 'Some API key queries failed')"
          >
            <div class="key-errors">
              <div v-for="error in response.errors" :key="error.api_key_hash">
                {{
                  t(
                    `${error.description}（${error.api_key_preview}）：${serverText(error.message, '查询失败', 'Query failed')}`,
                    `${error.description} (${error.api_key_preview}): ${serverText(error.message, '查询失败', 'Query failed')}`,
                  )
                }}
              </div>
            </div>
          </NAlert>

          <div v-if="response" class="metric-grid model-metrics">
            <div class="metric-card">
              <div class="metric-icon" aria-hidden="true">
                <Cpu :size="20" :stroke-width="2.2" />
              </div>
              <div class="metric-label">{{ t('可用模型', 'Available models') }}</div>
              <div class="metric-value">{{ modelCount }}</div>
              <div class="metric-footnote">{{ t('CPA 返回', 'Returned by CPA') }}</div>
            </div>
            <div class="metric-card is-blue">
              <div class="metric-icon" aria-hidden="true">
                <KeyRound :size="20" :stroke-width="2.2" />
              </div>
              <div class="metric-label">{{ t('可查询 Key', 'Queryable keys') }}</div>
              <div class="metric-value">{{ keySummary }}</div>
              <div class="metric-footnote">{{ t('完整密钥', 'Complete keys') }}</div>
            </div>
            <div class="metric-card is-green">
              <div class="metric-icon" aria-hidden="true">
                <ShieldCheck :size="20" :stroke-width="2.2" />
              </div>
              <div class="metric-label">{{ t('查询状态', 'Query status') }}</div>
              <div class="metric-value">{{ queryStatus }}</div>
              <div class="metric-footnote">{{ t('当前账号', 'Current account') }}</div>
            </div>
          </div>

          <div v-if="isLoading && !response" class="loading-state">
            <NSpin size="small" />
            <span>{{ t('正在向 CPA 查询模型', 'Querying CPA for models') }}</span>
          </div>

          <div v-else-if="response && !response.has_api_keys" class="empty-state">
            <NEmpty :description="t('还没有可用于查询模型的 API 密钥', 'No API keys are available for model queries yet')">
              <template #extra>
                <NButton type="primary" @click="goToApiKeys">{{ t('去创建 API 密钥', 'Create API key') }}</NButton>
              </template>
            </NEmpty>
          </div>

          <div
            v-else-if="response && response.has_api_keys && response.queryable_api_key_count === 0"
            class="empty-state"
          >
            <NEmpty :description="t('绑定的 API 密钥缺少完整密钥，无法查询模型', 'Bound API keys are missing complete keys and cannot query models')">
              <template #extra>
                <NButton type="primary" @click="goToApiKeys">{{ t('去 API 密钥页检查', 'Check API keys') }}</NButton>
              </template>
            </NEmpty>
          </div>

          <div v-else-if="response && response.models.length === 0" class="empty-state">
            <NEmpty :description="t('CPA 未返回可用模型', 'CPA returned no available models')">
              <template #extra>
                <NButton secondary :loading="isLoading" @click="refresh">{{ t('重新查询', 'Query again') }}</NButton>
              </template>
            </NEmpty>
          </div>

          <NDataTable
            v-else-if="response"
            class="available-models-table"
            size="small"
            :loading="isLoading"
            :columns="columns"
            :data="response.models"
            :pagination="{ pageSize: 20 }"
            max-height="max(240px, calc(100dvh - 360px))"
            :scroll-x="1660"
            table-layout="fixed"
          />
        </template>
      </div>
    </section>
  </section>
</template>

<style scoped>
.model-panel {
  display: grid;
  gap: 14px;
  min-width: 0;
  min-height: 0;
}

.model-metrics {
  grid-template-columns: repeat(3, minmax(128px, 1fr));
}

.models-page,
.model-table-panel,
.available-models-table {
  min-width: 0;
}

.model-table-panel {
  overflow: hidden;
}

.model-id {
  font-family: "Cascadia Mono", "SFMono-Regular", Consolas, monospace;
  font-size: 13px;
}

.model-price-muted {
  color: var(--cpa-text-muted);
}

.alert-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.key-errors {
  display: grid;
  gap: 4px;
}

.loading-state,
.empty-state {
  display: grid;
  min-height: 220px;
  place-items: center;
  color: var(--cpa-text-muted);
}

.loading-state {
  grid-auto-flow: column;
  justify-content: center;
  gap: 8px;
}

@media (min-width: 861px) {
  .models-page {
    grid-template-rows: auto minmax(0, 1fr);
    min-height: 0;
  }
}

@media (max-width: 720px) {
  .model-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .model-metrics .metric-card:last-child {
    grid-column: 1 / -1;
  }

  .alert-row {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
