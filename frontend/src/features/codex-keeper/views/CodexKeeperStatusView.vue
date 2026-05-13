<script setup lang="ts">
import { computed, h, onMounted, reactive, ref, watch } from 'vue'
import {
  NButton,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NPopconfirm,
  NSelect,
  NSpace,
  NTag,
  useMessage,
  type DataTableColumns,
  type DataTableRowKey,
} from 'naive-ui'
import { Activity, AlertTriangle, Gauge, PauseCircle, RefreshCw, Users, Zap } from 'lucide-vue-next'

import {
  bulkDeleteCodexKeeperAccounts,
  deleteCodexKeeperAccount,
  disableCodexKeeperAccount,
  enableCodexKeeperAccount,
  getCodexKeeperSettings,
  listCodexKeeperAccounts,
  updateCodexKeeperPriority,
} from '@/features/codex-keeper/api/codexKeeperApi'
import type { CodexKeeperAccount, CodexKeeperPriorityRule } from '@/shared/types/api'
import { formatDateTime, formatInteger } from '@/shared/utils/format'

type FixedPriorityFilter = 'all' | 'high' | 'minusOne' | 'low'
type PriorityTypeFilter = `type:${string}`
type PriorityFilter = FixedPriorityFilter | PriorityTypeFilter
type PriorityMode = 'low' | 'high' | 'default'
type AccountAction = 'toggle' | 'priority' | 'delete'
type QuotaWindowItem = { label: string; remainingPercent: number; resetAt: string | null }

const accountTablePagination = { pageSize: 18, pageSlot: 7 }
const disabledTableScrollX = 1810
const normalTableScrollX = 1770
const message = useMessage()
const isLoading = ref(false)
const isBulkDeleting = ref(false)
const actingActions = ref<Set<string>>(new Set())
const accounts = ref<CodexKeeperAccount[]>([])
const priorityRules = ref<CodexKeeperPriorityRule[]>([])
const selectedAccount = ref<CodexKeeperAccount | null>(null)
const selectedDisabledAccountKeys = ref<DataTableRowKey[]>([])
const detailOpen = ref(false)
const filters = reactive({
  keyword: '',
  accountType: null as string | null,
  priority: 'all' as PriorityFilter,
})
const bulkDeleteDialog = reactive({
  show: false,
})
const priorityDialog = reactive({
  show: false,
  mode: 'low' as PriorityMode,
  account: null as CodexKeeperAccount | null,
  value: null as number | null,
})

const priorityRuleMap = computed(() =>
  Object.fromEntries(priorityRules.value.map((rule) => [rule.account_type, rule.priority])),
)
const priorityFilterOptions = computed<Array<{ label: string; value: PriorityFilter }>>(() => [
  { label: '全部优先级', value: 'all' },
  { label: '手动优先 >20', value: 'high' },
  ...[...priorityRules.value]
    .filter((rule) => rule.priority >= 0 && rule.priority <= 20)
    .sort((left, right) => {
      const priorityDiff = right.priority - left.priority
      return priorityDiff === 0
        ? left.account_type.localeCompare(right.account_type)
        : priorityDiff
    })
    .map((rule) => ({
      label: `${formatInteger(rule.priority)} (${rule.account_type})`,
      value: priorityTypeFilter(rule.account_type),
    })),
  { label: '临时降级 -1', value: 'minusOne' },
  { label: '手动低优先 <-1', value: 'low' },
])

const accountTypeOptions = computed(() =>
  [...new Set(accounts.value.map((item) => item.account_type).filter(Boolean))]
    .sort((a, b) => String(a).localeCompare(String(b)))
    .map((value) => ({ label: String(value), value: String(value) })),
)

const filteredAccounts = computed(() =>
  accounts.value.filter((account) => {
    const keyword = filters.keyword.trim().toLowerCase()
    if (
      keyword &&
      ![account.name, account.email ?? ''].some((value) => value.toLowerCase().includes(keyword))
    ) {
      return false
    }
    if (filters.accountType && account.account_type !== filters.accountType) {
      return false
    }
    return matchesPriorityFilter(account, filters.priority)
  }),
)
const filteredDisabledAccounts = computed(() =>
  filteredAccounts.value.filter((account) => account.disabled),
)
const filteredNormalAccounts = computed(() =>
  filteredAccounts.value.filter((account) => !account.disabled).sort(compareNormalAccounts),
)
const tableLoading = computed(() => isLoading.value)
const enabledAccountCount = computed(() => accounts.value.filter((account) => !account.disabled).length)
const disabledAccountCount = computed(() => accounts.value.filter((account) => account.disabled).length)
const hasDisabledAccounts = computed(() => disabledAccountCount.value > 0)
const errorAccountCount = computed(() => accounts.value.filter((account) => account.last_error).length)
const systemPriorityCount = computed(
  () =>
    accounts.value.filter(
      (account) => account.priority !== null && account.priority >= -1 && account.priority <= 20,
    ).length,
)
const highPriorityCount = computed(
  () => accounts.value.filter((account) => account.priority !== null && account.priority > 20).length,
)
const activeFilterCount = computed(
  () =>
    Number(filters.keyword.trim() !== '') +
    Number(filters.accountType !== null) +
    Number(filters.priority !== 'all'),
)
const disabledAccountPagination = computed(() =>
  filteredDisabledAccounts.value.length > 8 ? { pageSize: 8, pageSlot: 5 } : false,
)
const selectedDisabledAccountNames = computed(() =>
  selectedDisabledAccountKeys.value.map((key) => String(key)),
)
const selectedDisabledCount = computed(() => selectedDisabledAccountNames.value.length)
const canBulkDelete = computed(() => selectedDisabledCount.value > 0 && !isBulkDeleting.value)
const bulkDeletePreviewNames = computed(() => selectedDisabledAccountNames.value.slice(0, 5))
const bulkDeletePreviewOverflow = computed(() =>
  Math.max(0, selectedDisabledCount.value - bulkDeletePreviewNames.value.length),
)
const canSubmitPriority = computed(() => {
  if (priorityDialog.mode === 'default') {
    return priorityDialog.account !== null && defaultPriority(priorityDialog.account) !== null
  }
  const value = priorityDialog.value
  if (value === null || !Number.isInteger(value)) {
    return false
  }
  return priorityDialog.mode === 'low' ? value < -1 : value > 20
})
const priorityDialogTitle = computed(() => '修改优先级')
const priorityDialogHint = computed(() => {
  if (priorityDialog.mode === 'low') {
    return '手动低优先级必须小于 -1，巡检永远不会自动调整。'
  }
  if (priorityDialog.mode === 'high') {
    return '手动优先必须大于 20，额度耗尽时会临时降为 -1，恢复后回到该值。'
  }
  const account = priorityDialog.account
  const value = account ? defaultPriority(account) : null
  return value === null
    ? '该账号类型没有配置默认优先级，不能使用类型默认值。'
    : `将优先级设置为当前账号类型默认值 ${value}。`
})
const priorityDialogBounds = computed(() => {
  if (priorityDialog.mode === 'low') {
    return { max: -2 }
  }
  if (priorityDialog.mode === 'high') {
    return { min: 21 }
  }
  return {}
})
const priorityModeOptions = computed(() => {
  const defaultValue = priorityDialog.account ? defaultPriority(priorityDialog.account) : null
  return [
    { label: '手动低优先 (< -1)', value: 'low' },
    { label: '手动优先 (> 20)', value: 'high' },
    {
      label: defaultValue === null ? '类型默认优先级（不可用）' : `类型默认优先级 (${defaultValue})`,
      value: 'default',
      disabled: defaultValue === null,
    },
  ]
})

function matchesPriorityFilter(account: CodexKeeperAccount, value: PriorityFilter): boolean {
  if (value === 'high') {
    return account.priority !== null && account.priority > 20
  }
  if (value === 'minusOne') {
    return account.priority === -1
  }
  if (value === 'low') {
    return account.priority !== null && account.priority < -1
  }
  const accountType = priorityTypeFromFilter(value)
  if (accountType !== null) {
    return (
      account.account_type === accountType &&
      account.priority !== null &&
      account.priority >= 0 &&
      account.priority <= 20
    )
  }
  return true
}

function priorityTypeFilter(accountType: string): PriorityTypeFilter {
  return `type:${accountType}`
}

function priorityTypeFromFilter(value: PriorityFilter): string | null {
  return value.startsWith('type:') ? value.slice('type:'.length) : null
}

function normalAccountTypePriority(account: CodexKeeperAccount): number {
  if (!account.account_type) {
    return Number.NEGATIVE_INFINITY
  }
  return priorityRuleMap.value[account.account_type] ?? Number.NEGATIVE_INFINITY
}

function compareNormalAccounts(left: CodexKeeperAccount, right: CodexKeeperAccount): number {
  const priorityDiff = normalAccountTypePriority(right) - normalAccountTypePriority(left)
  if (priorityDiff !== 0) {
    return priorityDiff
  }
  return (left.email ?? left.name).localeCompare(right.email ?? right.name)
}

function defaultPriority(account: CodexKeeperAccount): number | null {
  if (!account.account_type) {
    return null
  }
  return priorityRuleMap.value[account.account_type] ?? null
}

function isPaidQuotaWindowAccount(accountType: string | null): boolean {
  const normalized = accountType?.trim().toLowerCase()
  return normalized === 'plus' || normalized === 'team' || normalized?.startsWith('pro') === true
}

function quotaWindowLabels(accountType: string | null): { primary: string; secondary: string } {
  const normalized = accountType?.trim().toLowerCase()
  if (normalized === 'free') {
    return { primary: '周限额', secondary: '次限额' }
  }
  if (isPaidQuotaWindowAccount(accountType)) {
    return { primary: '5小时限额', secondary: '周限额' }
  }
  return { primary: '主', secondary: '次' }
}

function quotaWindowItems(account: CodexKeeperAccount): QuotaWindowItem[] {
  if (account.primary_used_percent === null) {
    return []
  }
  const labels = quotaWindowLabels(account.account_type)
  const items = [
    {
      label: labels.primary,
      remainingPercent: remainingQuotaPercent(account.primary_used_percent),
      resetAt: account.primary_reset_at,
    },
  ]
  if (account.secondary_used_percent !== null) {
    items.push({
      label: labels.secondary,
      remainingPercent: remainingQuotaPercent(account.secondary_used_percent),
      resetAt: account.secondary_reset_at,
    })
  }
  return items
}

function remainingQuotaPercent(usedPercent: number): number {
  return Math.max(0, Math.min(100, 100 - usedPercent))
}

function quotaBarTone(percent: number): string {
  if (percent < 30) {
    return 'is-danger'
  }
  if (percent < 70) {
    return 'is-warning'
  }
  return 'is-healthy'
}

function formatQuotaResetTime(value: string | null): string | null {
  if (!value) {
    return null
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return null
  }
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function quotaText(account: CodexKeeperAccount): string {
  const items = quotaWindowItems(account)
  if (items.length === 0) {
    return '-'
  }
  return items
    .map((item) => {
      const resetTime = formatQuotaResetTime(item.resetAt)
      return resetTime
        ? `${item.label}剩余 ${item.remainingPercent}%，刷新 ${resetTime}`
        : `${item.label}剩余 ${item.remainingPercent}%`
    })
    .join(' / ')
}

function latestActionText(account: CodexKeeperAccount): string {
  return account.last_error?.trim() || account.latest_action?.trim() || '-'
}

function renderQuotaCell(account: CodexKeeperAccount) {
  const items = quotaWindowItems(account)
  if (items.length === 0) {
    return '-'
  }
  return h(
    'div',
    { class: 'quota-window-cell' },
    items.map((item) => {
      const resetTime = formatQuotaResetTime(item.resetAt)
      return h(
        'div',
        {
          class: 'quota-window-item',
          title: resetTime
            ? `${item.label}剩余 ${item.remainingPercent}%，刷新 ${resetTime}`
            : `${item.label}剩余 ${item.remainingPercent}%`,
        },
        [
          h('div', { class: 'quota-window-head' }, [
            h('span', { class: 'quota-window-label' }, item.label),
            h('span', { class: 'quota-window-meta' }, [
              h('span', { class: 'quota-window-percent' }, `${item.remainingPercent}%`),
              resetTime ? h('span', { class: 'quota-window-reset' }, resetTime) : null,
            ]),
          ]),
          h('div', { class: 'quota-window-track' }, [
            h('div', {
              class: ['quota-window-fill', quotaBarTone(item.remainingPercent)],
              style: { width: `${item.remainingPercent}%` },
            }),
          ]),
        ],
      )
    }),
  )
}

async function loadAccounts() {
  isLoading.value = true
  try {
    const [accountsResponse, settings] = await Promise.all([
      listCodexKeeperAccounts(),
      getCodexKeeperSettings(),
    ])
    accounts.value = accountsResponse.items
    priorityRules.value = settings.priority_rules
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载账号状态失败')
  } finally {
    isLoading.value = false
  }
}

function accountRowKey(account: CodexKeeperAccount): string {
  return account.name
}

function handleDisabledSelectionUpdate(keys: DataTableRowKey[]) {
  selectedDisabledAccountKeys.value = keys
}

function pruneSelectedDisabledAccountKeys() {
  const availableNames = new Set(filteredDisabledAccounts.value.map((account) => account.name))
  selectedDisabledAccountKeys.value = selectedDisabledAccountKeys.value.filter((key) =>
    availableNames.has(String(key)),
  )
}

function openBulkDeleteDialog() {
  if (!canBulkDelete.value) {
    return
  }
  bulkDeleteDialog.show = true
}

async function submitBulkDelete() {
  const authNames = selectedDisabledAccountNames.value
  if (authNames.length === 0) {
    return
  }
  isBulkDeleting.value = true
  try {
    const result = await bulkDeleteCodexKeeperAccounts({ auth_names: authNames })
    const deletedNames = new Set(result.deleted)
    selectedDisabledAccountKeys.value = selectedDisabledAccountKeys.value.filter(
      (key) => !deletedNames.has(String(key)),
    )
    if (result.failed.length > 0 && result.deleted.length > 0) {
      message.warning(`批量删除完成：成功 ${result.deleted.length} 个，失败 ${result.failed.length} 个`)
    } else if (result.failed.length > 0) {
      message.error(`批量删除失败：失败 ${result.failed.length} 个`)
    } else {
      message.success(`已删除 ${result.deleted.length} 个已禁用账号`)
    }
    bulkDeleteDialog.show = false
    await loadAccounts()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '批量删除失败')
  } finally {
    isBulkDeleting.value = false
  }
}

function openDetail(account: CodexKeeperAccount) {
  selectedAccount.value = account
  detailOpen.value = true
}

function openPriorityDialog(account: CodexKeeperAccount) {
  priorityDialog.account = account
  const mode =
    account.priority !== null && account.priority < -1
      ? 'low'
      : account.priority !== null && account.priority > 20
        ? 'high'
        : 'default'
  setPriorityDialogMode(defaultPriority(account) === null && mode === 'default' ? 'low' : mode)
  priorityDialog.show = true
}

function setPriorityDialogMode(mode: PriorityMode) {
  priorityDialog.mode = mode
  const account = priorityDialog.account
  if (!account) {
    priorityDialog.value = null
    return
  }
  if (mode === 'low') {
    priorityDialog.value = account.priority !== null && account.priority < -1 ? account.priority : -2
    return
  }
  if (mode === 'high') {
    priorityDialog.value = account.priority !== null && account.priority > 20 ? account.priority : 21
    return
  }
  priorityDialog.value = defaultPriority(account)
}

async function submitPriorityDialog() {
  if (!priorityDialog.account || !canSubmitPriority.value) {
    return
  }
  const value =
    priorityDialog.mode === 'default'
      ? defaultPriority(priorityDialog.account)
      : priorityDialog.value
  if (value === null) {
    return
  }
  await runAccountAction(
    priorityDialog.account,
    'priority',
    () => updateCodexKeeperPriority(priorityDialog.account!.name, value),
    '优先级已更新',
  )
  priorityDialog.show = false
}

function accountActionKey(account: CodexKeeperAccount, action: AccountAction): string {
  return `${action}\u0000${account.name}`
}

function isActionLoading(account: CodexKeeperAccount, action: AccountAction): boolean {
  return actingActions.value.has(accountActionKey(account, action))
}

function isRowActing(account: CodexKeeperAccount): boolean {
  return (['toggle', 'priority', 'delete'] as const).some((action) =>
    isActionLoading(account, action),
  )
}

async function runAccountAction(
  account: CodexKeeperAccount,
  actionType: AccountAction,
  action: () => Promise<void>,
  successText: string,
) {
  const key = accountActionKey(account, actionType)
  if (actingActions.value.has(key)) {
    return
  }
  actingActions.value = new Set(actingActions.value).add(key)
  try {
    await action()
    message.success(successText)
    await loadAccounts()
  } catch (error) {
    message.error(error instanceof Error ? error.message : '账号操作失败')
  } finally {
    const nextActions = new Set(actingActions.value)
    nextActions.delete(key)
    actingActions.value = nextActions
  }
}

const baseColumns: DataTableColumns<CodexKeeperAccount> = [
  { title: '账号', key: 'name', width: 300, ellipsis: { tooltip: true } },
  { title: '邮箱', key: 'email', width: 260, ellipsis: { tooltip: true } },
  {
    title: '类型',
    key: 'account_type',
    width: 90,
    render: (row) => row.account_type ?? '未知',
  },
  {
    title: '状态',
    key: 'disabled',
    width: 90,
    render: (row) =>
      h(
        NTag,
        { size: 'small', bordered: false, type: row.disabled ? 'warning' : 'success' },
        { default: () => (row.disabled ? '已禁用' : '启用中') },
      ),
  },
  {
    title: '优先级',
    key: 'priority',
    width: 88,
    render: (row) => (row.priority === null ? '-' : formatInteger(row.priority)),
  },
  {
    title: '额度窗口',
    key: 'quota',
    width: 260,
    render: (row) => renderQuotaCell(row),
  },
  {
    title: '最近巡检',
    key: 'last_checked_at',
    width: 150,
    render: (row) => formatDateTime(row.last_checked_at),
  },
  {
    title: '最近操作',
    key: 'latest_action',
    width: 340,
    ellipsis: { tooltip: true },
    render: (row) => {
      const text = latestActionText(row)
      return text === '-' ? '-' : h('span', { class: 'latest-action-text', title: text }, text)
    },
  },
]

const disabledActionColumn: DataTableColumns<CodexKeeperAccount>[number] = {
  title: '',
  key: 'actions',
  width: 180,
  fixed: 'right',
  render: (row: CodexKeeperAccount) => {
    return h(
      NSpace,
      { class: 'account-actions', size: 4, wrap: false },
      {
        default: () => [
          h(
            NButton,
            { size: 'small', quaternary: true, onClick: () => openDetail(row) },
            { default: () => '详情' },
          ),
          h(
            NPopconfirm,
            {
              onPositiveClick: () =>
                runAccountAction(
                  row,
                  'toggle',
                  () => enableCodexKeeperAccount(row.name),
                  '账号已启用',
                ),
            },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                    type: 'primary',
                    disabled: isRowActing(row) || isBulkDeleting.value,
                    loading: isActionLoading(row, 'toggle'),
                  },
                  { default: () => '启用' },
                ),
              default: () => `启用 ${row.name}？`,
            },
          ),
          h(
            NPopconfirm,
            {
              disabled: isBulkDeleting.value,
              onPositiveClick: () =>
                runAccountAction(
                  row,
                  'delete',
                  () => deleteCodexKeeperAccount(row.name),
                  '账号已删除',
                ),
            },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                    type: 'error',
                    disabled: isRowActing(row) || isBulkDeleting.value,
                    loading: isActionLoading(row, 'delete'),
                  },
                  { default: () => '删除' },
                ),
              default: () => `删除 ${row.name}？此操作会从 CPA 删除 auth file。`,
            },
          ),
        ],
      },
    )
  },
}

const normalActionColumn: DataTableColumns<CodexKeeperAccount>[number] = {
  title: '',
  key: 'actions',
  width: 188,
  fixed: 'right',
  render: (row: CodexKeeperAccount) => {
    return h(
      NSpace,
      { class: 'account-actions', size: 4, wrap: false },
      {
        default: () => [
          h(
            NButton,
            { size: 'small', quaternary: true, onClick: () => openDetail(row) },
            { default: () => '详情' },
          ),
          h(
            NPopconfirm,
            {
              onPositiveClick: () =>
                runAccountAction(
                  row,
                  'toggle',
                  () => disableCodexKeeperAccount(row.name),
                  '账号已禁用',
                ),
            },
            {
              trigger: () =>
                h(
                  NButton,
                  {
                    size: 'small',
                    quaternary: true,
                    type: 'warning',
                    disabled: isRowActing(row) || isBulkDeleting.value,
                    loading: isActionLoading(row, 'toggle'),
                  },
                  { default: () => '禁用' },
                ),
              default: () => `禁用 ${row.name}？`,
            },
          ),
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              disabled: isRowActing(row) || isBulkDeleting.value,
              onClick: () => openPriorityDialog(row),
            },
            { default: () => '优先级' },
          ),
        ],
      },
    )
  },
}

const disabledColumns = computed<DataTableColumns<CodexKeeperAccount>>(() => [
  {
    type: 'selection',
    width: 44,
    disabled: (row: CodexKeeperAccount) => isRowActing(row) || isBulkDeleting.value,
  },
  ...baseColumns,
  disabledActionColumn,
])

const normalColumns = computed<DataTableColumns<CodexKeeperAccount>>(() => [
  ...baseColumns,
  normalActionColumn,
])

watch(filteredDisabledAccounts, pruneSelectedDisabledAccountKeys)

onMounted(loadAccounts)
</script>

<template>
  <section class="page account-status-page">
    <div class="page-header">
      <div>
        <h1 class="page-title">账号状态</h1>
        <p class="page-subtitle">查看 Codex auth file 的健康、额度和优先级维护结果</p>
      </div>
      <div class="header-actions">
        <NButton secondary :loading="isLoading" @click="loadAccounts">
          <template #icon>
            <NIcon :component="RefreshCw" />
          </template>
          重新加载
        </NButton>
      </div>
    </div>

    <div class="metric-grid account-metrics">
      <div class="metric-card">
        <div class="metric-icon" aria-hidden="true">
          <Users :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">账号总数</div>
        <div class="metric-value">{{ formatInteger(accounts.length) }}</div>
        <div class="metric-footnote">全部 auth file</div>
      </div>
      <div class="metric-card is-success">
        <div class="metric-icon" aria-hidden="true">
          <Activity :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">启用中</div>
        <div class="metric-value">{{ formatInteger(enabledAccountCount) }}</div>
        <div class="metric-footnote">可参与调度</div>
      </div>
      <div class="metric-card is-warning">
        <div class="metric-icon" aria-hidden="true">
          <PauseCircle :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">已禁用</div>
        <div class="metric-value">{{ formatInteger(disabledAccountCount) }}</div>
        <div class="metric-footnote">停用账号</div>
      </div>
      <div class="metric-card is-danger">
        <div class="metric-icon" aria-hidden="true">
          <AlertTriangle :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">检测异常</div>
        <div class="metric-value">{{ formatInteger(errorAccountCount) }}</div>
        <div class="metric-footnote">最近错误</div>
      </div>
      <div class="metric-card">
        <div class="metric-icon" aria-hidden="true">
          <Gauge :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">巡检托管</div>
        <div class="metric-value">{{ formatInteger(systemPriorityCount) }}</div>
        <div class="metric-footnote">类型默认</div>
      </div>
      <div class="metric-card">
        <div class="metric-icon" aria-hidden="true">
          <Zap :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">手动优先</div>
        <div class="metric-value">{{ formatInteger(highPriorityCount) }}</div>
        <div class="metric-footnote">高优先级</div>
      </div>
    </div>

    <section class="panel account-list-panel">
      <div class="status-toolbar">
        <div class="toolbar-heading">
          <div>
            <h2 class="toolbar-title">账号列表</h2>
            <p class="toolbar-subtitle">
              正常 {{ filteredNormalAccounts.length }} / {{ enabledAccountCount }} 个账号
              <template v-if="hasDisabledAccounts">
                ，已禁用 {{ filteredDisabledAccounts.length }} / {{ disabledAccountCount }} 个账号
              </template>
            </p>
          </div>
          <NTag v-if="activeFilterCount > 0" size="small" type="info" :bordered="false">
            已筛选 {{ activeFilterCount }} 项
          </NTag>
        </div>
        <div class="filter-grid">
          <NInput v-model:value="filters.keyword" clearable placeholder="搜索账号或邮箱" />
          <NSelect
            v-model:value="filters.accountType"
            :options="accountTypeOptions"
            clearable
            filterable
            placeholder="账号类型"
          />
          <NSelect
            v-model:value="filters.priority"
            :options="priorityFilterOptions"
          />
        </div>
      </div>

      <div class="account-sections">
        <section v-if="hasDisabledAccounts" class="account-section">
          <div class="account-section-header">
            <div class="account-section-title-group">
              <h3 class="account-section-title">已禁用账号</h3>
              <p class="account-section-subtitle">
                显示 {{ filteredDisabledAccounts.length }} / {{ disabledAccountCount }} 个账号
              </p>
            </div>
            <div class="account-section-actions">
              <NButton
                secondary
                type="error"
                :disabled="!canBulkDelete"
                :loading="isBulkDeleting"
                @click="openBulkDeleteDialog"
              >
                批量删除（{{ selectedDisabledCount }}）
              </NButton>
            </div>
          </div>
          <NDataTable
            class="account-table"
            size="small"
            :loading="tableLoading"
            :columns="disabledColumns"
            :data="filteredDisabledAccounts"
            :row-key="accountRowKey"
            :checked-row-keys="selectedDisabledAccountKeys"
            :pagination="disabledAccountPagination"
            table-layout="fixed"
            :scroll-x="disabledTableScrollX"
            @update:checked-row-keys="handleDisabledSelectionUpdate"
          >
            <template #empty>
              <div class="empty-state">当前筛选下暂无已禁用账号</div>
            </template>
          </NDataTable>
        </section>

        <section class="account-section">
          <div class="account-section-header">
            <div class="account-section-title-group">
              <h3 class="account-section-title">正常账号</h3>
              <p class="account-section-subtitle">
                显示 {{ filteredNormalAccounts.length }} / {{ enabledAccountCount }} 个账号
              </p>
            </div>
          </div>
          <NDataTable
            class="account-table"
            size="small"
            :loading="tableLoading"
            :columns="normalColumns"
            :data="filteredNormalAccounts"
            :pagination="accountTablePagination"
            table-layout="fixed"
            :scroll-x="normalTableScrollX"
          >
            <template #empty>
              <div class="empty-state">暂无正常账号</div>
            </template>
          </NDataTable>
        </section>
      </div>
    </section>

    <NDrawer v-model:show="detailOpen" placement="right" :width="420">
      <NDrawerContent title="账号详情">
        <NDescriptions v-if="selectedAccount" label-placement="left" :column="1" size="small" bordered>
          <NDescriptionsItem label="账号">{{ selectedAccount.name }}</NDescriptionsItem>
          <NDescriptionsItem label="邮箱">{{ selectedAccount.email ?? '-' }}</NDescriptionsItem>
          <NDescriptionsItem label="账号类型">
            {{ selectedAccount.account_type ?? '未知' }}
          </NDescriptionsItem>
          <NDescriptionsItem label="启用状态">
            {{ selectedAccount.disabled ? '已禁用' : '启用中' }}
          </NDescriptionsItem>
          <NDescriptionsItem label="当前优先级">
            {{ selectedAccount.priority ?? '-' }}
          </NDescriptionsItem>
          <NDescriptionsItem label="类型默认优先级">
            {{ defaultPriority(selectedAccount) ?? '-' }}
          </NDescriptionsItem>
          <NDescriptionsItem label="额度窗口">{{ quotaText(selectedAccount) }}</NDescriptionsItem>
          <NDescriptionsItem label="状态码">
            {{ selectedAccount.last_status_code ?? '-' }}
          </NDescriptionsItem>
          <NDescriptionsItem label="最近健康">
            {{ formatDateTime(selectedAccount.last_healthy_at) }}
          </NDescriptionsItem>
          <NDescriptionsItem label="最近巡检">
            {{ formatDateTime(selectedAccount.last_checked_at) }}
          </NDescriptionsItem>
          <NDescriptionsItem label="最近操作">
            {{ latestActionText(selectedAccount) }}
          </NDescriptionsItem>
        </NDescriptions>
      </NDrawerContent>
    </NDrawer>

    <NModal v-model:show="bulkDeleteDialog.show" preset="dialog" title="批量删除已禁用账号">
      <div class="bulk-delete-dialog">
        <p class="bulk-delete-warning">
          将删除已选 {{ selectedDisabledCount }} 个已禁用账号，并从 CPA 删除 auth file。此操作不可恢复。
        </p>
        <div v-if="bulkDeletePreviewNames.length > 0" class="bulk-delete-preview">
          <span v-for="name in bulkDeletePreviewNames" :key="name">{{ name }}</span>
          <span v-if="bulkDeletePreviewOverflow > 0">另 {{ bulkDeletePreviewOverflow }} 个...</span>
        </div>
      </div>
      <template #action>
        <NSpace justify="end">
          <NButton :disabled="isBulkDeleting" @click="bulkDeleteDialog.show = false">取消</NButton>
          <NButton
            type="error"
            :disabled="selectedDisabledCount === 0"
            :loading="isBulkDeleting"
            @click="submitBulkDelete"
          >
            确认删除
          </NButton>
        </NSpace>
      </template>
    </NModal>

    <NModal v-model:show="priorityDialog.show" preset="dialog" :title="priorityDialogTitle">
      <div class="priority-dialog">
        <NSelect
          :value="priorityDialog.mode"
          :options="priorityModeOptions"
          @update:value="(value) => setPriorityDialogMode(value as PriorityMode)"
        />
        <NInputNumber
          v-if="priorityDialog.mode !== 'default'"
          v-model:value="priorityDialog.value"
          :precision="0"
          v-bind="priorityDialogBounds"
        />
        <p class="priority-hint">{{ priorityDialogHint }}</p>
      </div>
      <template #action>
        <NSpace justify="end">
          <NButton @click="priorityDialog.show = false">取消</NButton>
          <NButton
            type="primary"
            :disabled="!canSubmitPriority"
            :loading="
              priorityDialog.account
                ? isActionLoading(priorityDialog.account, 'priority')
                : false
            "
            @click="submitPriorityDialog"
          >
            确认
          </NButton>
        </NSpace>
      </template>
    </NModal>
  </section>
</template>

<style scoped>
.account-status-page,
.account-list-panel,
.account-section,
.account-table {
  min-width: 0;
}

.header-actions {
  display: flex;
  justify-content: flex-end;
}

.account-metrics {
  grid-template-columns: repeat(6, minmax(112px, 1fr));
}

.account-metrics .metric-card {
  min-height: 104px;
  padding: 14px 12px;
}

.account-metrics .metric-value {
  font-size: 20px;
}

.status-toolbar {
  display: grid;
  gap: 12px;
  padding: 14px;
  border-bottom: 1px solid var(--cpa-border);
  background: var(--cpa-glass);
  backdrop-filter: blur(14px);
}

.toolbar-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
}

.toolbar-title {
  margin: 0;
  color: var(--cpa-text);
  font-size: 15px;
  font-weight: 700;
  line-height: 1.25;
}

.toolbar-subtitle {
  margin: 3px 0 0;
  color: var(--cpa-text-muted);
  font-size: 13px;
}

.filter-grid {
  display: grid;
  grid-template-columns: minmax(220px, 1.35fr) minmax(150px, 0.8fr) minmax(170px, 0.9fr);
  gap: 8px;
  min-width: 0;
}

.account-sections {
  display: grid;
  gap: 14px;
  padding: 14px;
}

.account-section {
  display: grid;
  gap: 10px;
}

.account-section + .account-section {
  padding-top: 12px;
  border-top: 1px solid var(--cpa-border);
}

.account-section-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  min-width: 0;
}

.account-section-title-group {
  min-width: 0;
}

.account-section-title {
  margin: 0;
  color: var(--cpa-text);
  font-size: 14px;
  font-weight: 700;
  line-height: 1.25;
}

.account-section-subtitle {
  margin: 3px 0 0;
  color: var(--cpa-text-muted);
  font-size: 12px;
}

.account-section-actions {
  display: flex;
  flex-shrink: 0;
  align-items: center;
  justify-content: flex-end;
}

.account-table :deep(.n-data-table-th) {
  white-space: nowrap;
}

.account-table :deep(.n-data-table-td) {
  vertical-align: middle;
}

:global(.quota-window-cell) {
  display: grid;
  gap: 8px;
  min-width: 0;
  padding: 4px 0;
}

:global(.quota-window-item) {
  display: grid;
  gap: 4px;
  min-width: 0;
}

:global(.quota-window-head) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
  line-height: 1.2;
}

:global(.quota-window-label) {
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text);
  font-size: 12px;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.quota-window-meta) {
  display: inline-flex;
  flex-shrink: 0;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

:global(.quota-window-percent) {
  color: var(--cpa-text);
  font-size: 12px;
  font-weight: 700;
}

:global(.quota-window-reset) {
  color: var(--cpa-text-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

:global(.quota-window-track) {
  height: 5px;
  overflow: hidden;
  background: var(--cpa-surface-muted);
  border-radius: 999px;
}

:global(.quota-window-fill) {
  height: 100%;
  min-width: 3px;
  border-radius: inherit;
}

:global(.quota-window-fill.is-healthy) {
  background: var(--cpa-success);
}

:global(.quota-window-fill.is-warning) {
  background: var(--cpa-warning);
}

:global(.quota-window-fill.is-danger) {
  background: var(--cpa-danger);
}

:global(.latest-action-text) {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.account-actions) {
  justify-content: flex-end;
}

.empty-state {
  padding: 42px 0;
  color: var(--cpa-text-muted);
  font-size: 13px;
  text-align: center;
}

.bulk-delete-dialog,
.priority-dialog {
  display: grid;
  gap: 8px;
}

.bulk-delete-warning,
.priority-hint {
  margin: 0;
  color: var(--cpa-text-muted);
  font-size: 13px;
}

.bulk-delete-preview {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 116px;
  padding: 8px;
  overflow: auto;
  color: var(--cpa-text);
  font-size: 12px;
  background: var(--cpa-surface-muted);
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius-sm);
}

.bulk-delete-preview span {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 1280px) {
  .account-metrics {
    grid-template-columns: repeat(3, minmax(112px, 1fr));
  }
}

@media (max-width: 980px) {
  .filter-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 560px) {
  .account-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .toolbar-heading,
  .account-section-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .account-section-actions {
    width: 100%;
    justify-content: flex-start;
  }

  .filter-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .filter-grid .n-input {
    grid-column: 1 / -1;
  }
}
</style>
