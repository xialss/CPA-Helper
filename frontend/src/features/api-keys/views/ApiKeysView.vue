<script setup lang="ts">
import type { Component } from 'vue'
import { computed, h, onMounted, ref } from 'vue'
import {
  NAlert,
  NButton,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NModal,
  NSpace,
  useDialog,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { Activity, CircleDollarSign, Eye, EyeOff, KeyRound, Layers3 } from 'lucide-vue-next'

import {
  createApiKey,
  deleteApiKey,
  listApiKeys,
  updateApiKey,
} from '@/features/api-keys/api/apiKeysApi'
import { getUsageOverview } from '@/features/usage/api/usageApi'
import type { UsageSummary, UserApiKeySummary } from '@/shared/types/api'
import { copyToClipboard } from '@/shared/utils/clipboard'
import { formatCompact, formatDateTime, formatInteger, formatUsd } from '@/shared/utils/format'

const message = useMessage()
const dialog = useDialog()
const isLoading = ref(false)
const isSaving = ref(false)
const apiKeys = ref<UserApiKeySummary[]>([])
const usageSummary = ref<UsageSummary | null>(null)
const editorVisible = ref(false)
const editingApiKeyHash = ref<string | null>(null)
const apiKeyDescription = ref('VSCode')
const generatedApiKey = ref<string | null>(null)
const generatedApiKeyHash = ref<string | null>(null)
const visibleApiKeyHashes = ref<Set<string>>(new Set())

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
  return [
    {
      key: 'keys',
      label: 'API 密钥',
      value: formatInteger(apiKeys.value.length),
      footnote: '当前账号',
      tone: 'teal',
      icon: KeyRound,
    },
    {
      key: 'requests',
      label: '今日请求',
      value: formatInteger(todayRequests),
      footnote: `失败 ${formatInteger(failedToday)}`,
      tone: 'blue',
      icon: Activity,
    },
    {
      key: 'tokens',
      label: '今日 Token',
      value: formatCompact(todayTokens),
      footnote: '当前账号用量',
      tone: 'purple',
      icon: Layers3,
    },
    {
      key: 'cost',
      label: '今日费用',
      value: formatUsd(todayCost),
      footnote: '按现价估算',
      tone: 'green',
      icon: CircleDollarSign,
    },
  ]
})

function displayedApiKey(row: UserApiKeySummary): string {
  if (row.api_key && isApiKeyVisible(row)) {
    return row.api_key
  }
  return maskDisplayedApiKey(row.api_key)
}

function maskDisplayedApiKey(apiKey: string | null | undefined): string {
  if (!apiKey) {
    return '未知'
  }
  if (apiKey.length <= 12) {
    return `${apiKey.slice(0, 3)}${'*'.repeat(Math.max(apiKey.length - 3, 0))}`
  }
  const visiblePrefix = apiKey.startsWith('sk-') ? 4 : 6
  const visibleSuffix = 4
  const maskedLength = Math.max(apiKey.length - visiblePrefix - visibleSuffix, 8)
  return `${apiKey.slice(0, visiblePrefix)}${'*'.repeat(maskedLength)}${apiKey.slice(-visibleSuffix)}`
}

function renderMaskedKeyTitle() {
  return h('span', { class: 'api-key-title' }, [
    h(NIcon, { class: 'api-key-mask-icon', component: EyeOff }),
    h('span', '密钥（点击复制）'),
  ])
}

function isApiKeyVisible(row: UserApiKeySummary): boolean {
  return visibleApiKeyHashes.value.has(row.api_key_hash)
}

function toggleApiKeyVisibility(row: UserApiKeySummary) {
  if (!row.api_key) {
    message.info('当前没有完整密钥可显示')
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
      message.info('当前没有完整密钥可复制')
      return
    }
    await copyToClipboard(row.api_key)
    message.success('API 密钥已复制')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '复制失败')
  }
}

async function copyGeneratedApiKey() {
  if (!generatedApiKey.value) {
    return
  }
  try {
    await copyToClipboard(generatedApiKey.value)
    message.success('API 密钥已复制')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '复制失败')
  }
}

function openCreateDialog() {
  editingApiKeyHash.value = null
  apiKeyDescription.value = 'VSCode'
  generatedApiKey.value = null
  generatedApiKeyHash.value = null
  editorVisible.value = true
}

function closeGeneratedApiKey() {
  generatedApiKey.value = null
  generatedApiKeyHash.value = null
}

function editApiKey(row: UserApiKeySummary) {
  editingApiKeyHash.value = row.api_key_hash
  apiKeyDescription.value = row.description || 'VSCode'
  generatedApiKey.value = null
  generatedApiKeyHash.value = null
  editorVisible.value = true
}

async function refresh() {
  isLoading.value = true
  try {
    const [nextApiKeys, overview] = await Promise.all([
      listApiKeys(),
      getUsageOverview({ scope: 'account' }),
    ])
    apiKeys.value = nextApiKeys
    usageSummary.value = overview.summary
    if (editingApiKeyHash.value) {
      const current = apiKeys.value.find((item) => item.api_key_hash === editingApiKeyHash.value)
      if (!current) {
        editorVisible.value = false
        editingApiKeyHash.value = null
      }
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载 API 密钥失败')
  } finally {
    isLoading.value = false
  }
}

async function saveApiKey() {
  if (isSaving.value) {
    return
  }
  const description = apiKeyDescription.value.trim()
  if (!description) {
    message.error('API KEY 描述不能为空')
    return
  }
  isSaving.value = true
  try {
    if (editingApiKeyHash.value) {
      await updateApiKey(editingApiKeyHash.value, { description })
      message.success('API 密钥已更新')
    } else {
      const created = await createApiKey({ description })
      generatedApiKey.value = created.api_key ?? null
      generatedApiKeyHash.value = created.api_key_hash
      message.success('API 密钥已创建并同步到 CPA')
    }
    editorVisible.value = false
    editingApiKeyHash.value = null
    await refresh()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '保存 API 密钥失败')
  } finally {
    isSaving.value = false
  }
}

function confirmDelete(row: UserApiKeySummary) {
  dialog.warning({
    title: '删除 API 密钥',
    content: `将删除 ${row.description || '未命名'} 对应的密钥，并从 CPA 中移除。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: async () => {
      await deleteApiKey(row.api_key_hash)
      message.success('API 密钥已删除')
      if (editingApiKeyHash.value === row.api_key_hash) {
        editorVisible.value = false
        editingApiKeyHash.value = null
      }
      if (generatedApiKeyHash.value === row.api_key_hash) {
        generatedApiKey.value = null
        generatedApiKeyHash.value = null
      }
      await refresh()
    },
  })
}

const columns: DataTableColumns<UserApiKeySummary> = [
  {
    title: renderMaskedKeyTitle,
    key: 'api_key',
    width: 430,
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
              title: isApiKeyVisible(row) ? '隐藏完整密钥' : '显示完整密钥',
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
              title: row.api_key ? '点击复制完整密钥' : '无完整密钥可复制',
              onClick: () => copyApiKey(row),
            },
            h('span', { class: 'api-key-mask-text' }, displayedApiKey(row)),
          ),
        ],
      ),
  },
  {
    title: '描述',
    key: 'description',
    width: 240,
    render: (row) => row.description || '-',
  },
  {
    title: '创建时间',
    key: 'created_at',
    width: 180,
    render: (row) => formatDateTime(row.created_at),
  },
  {
    title: '',
    key: 'actions',
    width: 130,
    fixed: 'right',
    render: (row) =>
      h(NSpace, { size: 4 }, {
        default: () => [
          h(
            NButton,
            { size: 'small', quaternary: true, onClick: () => editApiKey(row) },
            { default: () => '编辑' },
          ),
          h(
            NButton,
            { size: 'small', quaternary: true, type: 'error', onClick: () => confirmDelete(row) },
            { default: () => '删除' },
          ),
        ],
      }),
  },
]

onMounted(refresh)
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">API 密钥</h1>
        <p class="page-subtitle">仅管理当前账号自己的密钥</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">刷新</NButton>
        <NButton type="primary" @click="openCreateDialog">新建 API 密钥</NButton>
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
        <NAlert type="warning" :bordered="false" title="请求链路说明">
          Agent 发起的模型请求仍需 Agent 直接发送到 CPA，CPA-Helper 不代理或中转这些请求；仅调用 CPA
          的 usage 队列、API KEY 创建与删除、凭证管理等接口，用于用量查看、密钥创建和凭证维护。API
          密钥拥有当前账号的完整权限，请妥善保管。
        </NAlert>

        <div v-if="generatedApiKey" class="generated-key-box">
          <div class="generated-key-main">
            <div class="generated-key-title">新创建的密钥</div>
            <div class="generated-key-value">{{ generatedApiKey }}</div>
          </div>
          <NSpace>
            <NButton secondary @click="copyGeneratedApiKey">复制</NButton>
            <NButton tertiary @click="closeGeneratedApiKey">关闭</NButton>
          </NSpace>
        </div>

        <NDataTable
          class="api-key-table"
          size="small"
          :loading="isLoading"
          :columns="columns"
          :data="apiKeys"
          :pagination="{ pageSize: 12 }"
          table-layout="fixed"
          :scroll-x="980"
        />
      </div>
    </section>

    <NModal
      v-model:show="editorVisible"
      preset="card"
      :mask-closable="false"
      :closable="false"
      :title="editingApiKeyHash ? '编辑 API 密钥' : '新建 API 密钥'"
      :style="{ width: 'min(520px, calc(100vw - 32px))' }"
    >
      <NForm label-placement="top">
        <NFormItem label="API KEY 描述">
          <NInput
            v-model:value="apiKeyDescription"
            :disabled="isSaving"
            placeholder="例如：VSCode"
            @keyup.enter="saveApiKey"
          />
        </NFormItem>
        <div class="modal-actions">
          <NButton secondary :disabled="isSaving" @click="editorVisible = false">取消</NButton>
          <NButton type="primary" :loading="isSaving" :disabled="isSaving" @click="saveApiKey">
            {{ editingApiKeyHash ? '保存' : '创建' }}
          </NButton>
        </div>
      </NForm>
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
  grid-template-columns: repeat(4, minmax(0, 1fr));
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

:global(.api-key-cell) {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  min-width: 0;
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

:global(.api-key-title) {
  display: inline-flex;
  align-items: center;
  gap: 12px;
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
