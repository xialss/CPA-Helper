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
import type { AvailableModel, AvailableModelPrice, AvailableModelsResponse } from '@/shared/types/api'

type PriceField = keyof Pick<
  AvailableModelPrice,
  | 'input_usd_per_million'
  | 'output_usd_per_million'
  | 'cached_usd_per_million'
  | 'reasoning_usd_per_million'
>

const router = useRouter()
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
    return `部分失败 ${response.value.errors.length}`
  }
  if (response.value.queryable_api_key_count === 0) {
    return response.value.has_api_keys ? '不可查询' : '无 Key'
  }
  return response.value.has_api_keys ? '正常' : '无 Key'
})

function displayText(value: string | null | undefined): string {
  return value?.trim() || '-'
}

function formatUsdPerMtok(value: number): string {
  return value.toLocaleString('en-US', {
    maximumFractionDigits: 6,
  })
}

function renderPriceValue(row: AvailableModel, field: PriceField): string {
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
    errorMessage.value = error instanceof Error ? error.message : '加载可用模型失败'
  } finally {
    isLoading.value = false
  }
}

const columns: DataTableColumns<AvailableModel> = [
  {
    title: '模型 ID',
    key: 'id',
    width: 270,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'model-id' }, row.id),
  },
  {
    title: '名称',
    key: 'name',
    width: 220,
    ellipsis: { tooltip: true },
    render: (row) => displayText(row.name),
  },
  {
    title: 'Owner',
    key: 'owner',
    width: 150,
    render: (row) => displayText(row.owner),
  },
  {
    title: '来源 Key',
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
    title: '输入 ($/MTok)',
    key: 'input_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'input_usd_per_million'),
  },
  {
    title: '输出 ($/MTok)',
    key: 'output_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'output_usd_per_million'),
  },
  {
    title: '缓存 ($/MTok)',
    key: 'cached_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'cached_usd_per_million'),
  },
  {
    title: '思考 ($/MTok)',
    key: 'reasoning_usd_per_million',
    width: 145,
    render: (row) => renderPriceValue(row, 'reasoning_usd_per_million'),
  },
]

onMounted(refresh)
</script>

<template>
  <section class="page models-page" :aria-busy="isLoading">
    <div class="page-header">
      <div>
        <h1 class="page-title">可用模型</h1>
        <p class="page-subtitle">通过当前账号绑定的 CPA API Key 查询并聚合</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">
          <template #icon>
            <NIcon :component="RefreshCw" />
          </template>
          刷新
        </NButton>
      </NSpace>
    </div>

    <section class="panel model-table-panel">
      <div class="panel-inner model-panel">
        <NAlert v-if="errorMessage" type="error" :bordered="false">
          <div class="alert-row">
            <span>{{ errorMessage }}</span>
            <NButton size="small" secondary :loading="isLoading" @click="refresh">重试</NButton>
          </div>
        </NAlert>

        <template v-else>
          <NAlert
            v-if="response?.errors.length"
            type="warning"
            :bordered="false"
            title="部分 API Key 查询失败"
          >
            <div class="key-errors">
              <div v-for="error in response.errors" :key="error.api_key_hash">
                {{ error.description }}（{{ error.api_key_preview }}）：{{ error.message }}
              </div>
            </div>
          </NAlert>

          <div v-if="response" class="metric-grid model-metrics">
            <div class="metric-card">
              <div class="metric-icon" aria-hidden="true">
                <Cpu :size="20" :stroke-width="2.2" />
              </div>
              <div class="metric-label">可用模型</div>
              <div class="metric-value">{{ modelCount }}</div>
              <div class="metric-footnote">CPA 返回</div>
            </div>
            <div class="metric-card is-blue">
              <div class="metric-icon" aria-hidden="true">
                <KeyRound :size="20" :stroke-width="2.2" />
              </div>
              <div class="metric-label">可查询 Key</div>
              <div class="metric-value">{{ keySummary }}</div>
              <div class="metric-footnote">完整密钥</div>
            </div>
            <div class="metric-card is-green">
              <div class="metric-icon" aria-hidden="true">
                <ShieldCheck :size="20" :stroke-width="2.2" />
              </div>
              <div class="metric-label">查询状态</div>
              <div class="metric-value">{{ queryStatus }}</div>
              <div class="metric-footnote">当前账号</div>
            </div>
          </div>

          <div v-if="isLoading && !response" class="loading-state">
            <NSpin size="small" />
            <span>正在向 CPA 查询模型</span>
          </div>

          <div v-else-if="response && !response.has_api_keys" class="empty-state">
            <NEmpty description="还没有可用于查询模型的 API 密钥">
              <template #extra>
                <NButton type="primary" @click="goToApiKeys">去创建 API 密钥</NButton>
              </template>
            </NEmpty>
          </div>

          <div
            v-else-if="response && response.has_api_keys && response.queryable_api_key_count === 0"
            class="empty-state"
          >
            <NEmpty description="绑定的 API 密钥缺少完整密钥，无法查询模型">
              <template #extra>
                <NButton type="primary" @click="goToApiKeys">去 API 密钥页检查</NButton>
              </template>
            </NEmpty>
          </div>

          <div v-else-if="response && response.models.length === 0" class="empty-state">
            <NEmpty description="CPA 未返回可用模型">
              <template #extra>
                <NButton secondary :loading="isLoading" @click="refresh">重新查询</NButton>
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
            :scroll-x="1440"
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
