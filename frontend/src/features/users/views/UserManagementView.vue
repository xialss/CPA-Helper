<script setup lang="ts">
import type { Component } from 'vue'
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NDataTable,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NPopconfirm,
  NSpace,
  NSwitch,
  NTag,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { CircleDollarSign, KeyRound, ShieldCheck, UserRound } from 'lucide-vue-next'

import { getMe } from '@/features/auth/api/authApi'
import { setCurrentUser, useCurrentUser } from '@/features/auth/state/currentUser'
import {
  createUser,
  disableUser,
  enableUser,
  listUsersPage,
  updateUser,
  updateUserQuota,
} from '@/features/users/api/usersApi'
import { useI18n } from '@/shared/i18n'
import type { UserSummary } from '@/shared/types/api'
import { formatCompact, formatDateTime, formatInteger, formatUsd } from '@/shared/utils/format'

const message = useMessage()
const { errorText, t } = useI18n()
const { currentUser } = useCurrentUser()
const router = useRouter()
const isLoading = ref(false)
const isSavingUser = ref(false)
const users = ref<UserSummary[]>([])
const userPage = ref(1)
const userPageSize = 12
const userTotal = ref(0)
const editorVisible = ref(false)
const editingUserId = ref<number | null>(null)
const userAccount = ref('')
const userPassword = ref('')
const userPasswordConfirm = ref('')
const isUserAdmin = ref(false)
const isUserSuperAdmin = ref(false)
const initialUserAdmin = ref(false)
const initialUserSuperAdmin = ref(false)
const userNickname = ref('')
const quotaUnlimited = ref(true)
const quotaLifetimeUsd = ref(0)
const quotaMonthlyUsd = ref(0)
const initialQuotaUnlimited = ref(true)
const initialQuotaLifetimeUsd = ref<number | null>(null)
const initialQuotaMonthlyUsd = ref<number | null>(null)
const quotaLifetimeTouched = ref(false)
const quotaMonthlyTouched = ref(false)
const canEditQuota = computed(() => currentUser.value?.is_super_admin === true)
const canEditSuperAdminRole = computed(() => currentUser.value?.is_super_admin === true)
const isDemotingSelf = computed(
  () =>
    currentUser.value?.id === editingUserId.value &&
    currentUser.value?.is_super_admin === true &&
    !isUserSuperAdmin.value,
)
const canEditDraftQuota = computed(() => canEditQuota.value && !isDemotingSelf.value)

interface UserMetricCard {
  key: string
  label: string
  value: string
  footnote: string
  tone: 'teal' | 'blue' | 'purple' | 'green'
  icon: Component
}

const userMetrics = computed<UserMetricCard[]>(() => {
  const activeUsers = users.value.filter((user) => user.disabled_at === null).length
  const adminUsers = users.value.filter((user) => user.is_admin).length
  const boundKeys = users.value.reduce((total, user) => total + user.key_count, 0)
  const todayCost = users.value.reduce((total, user) => total + user.today_estimated_cost_usd, 0)
  return [
    {
      key: 'active',
      label: t('本页启用用户', 'Active users on page'),
      value: formatInteger(activeUsers),
      footnote: t(`共 ${formatInteger(userTotal.value)} 个账号`, `${formatInteger(userTotal.value)} accounts total`),
      tone: 'teal',
      icon: UserRound,
    },
    {
      key: 'admins',
      label: t('本页管理员', 'Admins on page'),
      value: formatInteger(adminUsers),
      footnote: t('拥有管理权限', 'Have admin access'),
      tone: 'purple',
      icon: ShieldCheck,
    },
    {
      key: 'keys',
      label: t('本页绑定 Key', 'Bound keys on page'),
      value: formatInteger(boundKeys),
      footnote: t('当前分页', 'Current page'),
      tone: 'blue',
      icon: KeyRound,
    },
    {
      key: 'cost',
      label: t('本页今日费用', 'Today cost on page'),
      value: formatUsd(todayCost),
      footnote: t('按现价估算', 'Estimated at current prices'),
      tone: 'green',
      icon: CircleDollarSign,
    },
  ]
})

function userLabel(row: UserSummary): string {
  return row.nickname.trim() || row.username.trim() || t('未知用户', 'Unknown user')
}

function quotaBalanceValue(row: UserSummary, bucket: 'monthly' | 'lifetime'): string {
  const value = bucket === 'monthly' ? row.quota.monthly_remaining_usd : row.quota.lifetime_remaining_usd
  if (value === null || value === undefined) {
    return t('未限制', 'Not limited')
  }
  return formatUsd(value)
}

function quotaBalanceClass(row: UserSummary): string {
  if (row.quota.paused || !row.quota.can_create_keys) {
    return 'is-error'
  }
  if (row.quota.unpriced_records > 0) {
    return 'is-warning'
  }
  return row.quota.unlimited ? 'is-unlimited' : 'is-normal'
}

function quotaDetail(row: UserSummary): string | null {
  if (row.quota.paused) {
    return t('Key 已因余额暂停', 'Keys paused due to balance')
  }
  if (row.quota.sync_error) {
    return t('同步异常', 'Sync error')
  }
  if (row.quota.unpriced_records > 0) {
    return t(`未定价 ${formatInteger(row.quota.unpriced_records)} 条`, `${formatInteger(row.quota.unpriced_records)} unpriced`)
  }
  return null
}

function todayRequestDetail(row: UserSummary): string {
  if (!row.today_records) {
    return t(`累计 ${formatInteger(row.records)}`, `${formatInteger(row.records)} total`)
  }
  const failed = row.today_failed_records
    ? t(`失败 ${formatInteger(row.today_failed_records)}`, `${formatInteger(row.today_failed_records)} failed`)
    : t('无失败', 'No failures')
  const rate = Math.round((row.today_success_records / row.today_records) * 100)
  return `${rate}% · ${failed}`
}

function todayCostDetail(row: UserSummary): string {
  if (!row.today_records) {
    return t('今日无请求', 'No requests today')
  }
  if (row.today_unpriced_records > 0) {
    return t(`未计价 ${formatInteger(row.today_unpriced_records)}`, `${formatInteger(row.today_unpriced_records)} unpriced`)
  }
  return t('已计价', 'Priced')
}

function lastModelLabel(row: UserSummary): string {
  return row.last_model ?? '-'
}

function lastProviderLabel(row: UserSummary): string {
  return row.last_provider ?? t('未知服务商', 'Unknown provider')
}

function setQuotaLifetimeUsd(value: number | null) {
  quotaLifetimeTouched.value = true
  quotaLifetimeUsd.value = value ?? 0
}

function setQuotaMonthlyUsd(value: number | null) {
  quotaMonthlyTouched.value = true
  quotaMonthlyUsd.value = value ?? 0
}

function setQuotaUnlimited(value: boolean) {
  quotaUnlimited.value = value
  if (!value && initialQuotaUnlimited.value) {
    quotaLifetimeTouched.value = true
    quotaMonthlyTouched.value = true
  }
}

function setSuperAdmin(value: boolean) {
  isUserSuperAdmin.value = value
  if (value) {
    isUserAdmin.value = true
  }
}

function resetEditor() {
  editingUserId.value = null
  userAccount.value = ''
  userPassword.value = ''
  isUserAdmin.value = false
  isUserSuperAdmin.value = false
  initialUserAdmin.value = false
  initialUserSuperAdmin.value = false
  userNickname.value = ''
  quotaUnlimited.value = true
  quotaLifetimeUsd.value = 0
  quotaMonthlyUsd.value = 0
  initialQuotaUnlimited.value = true
  initialQuotaLifetimeUsd.value = null
  initialQuotaMonthlyUsd.value = null
  quotaLifetimeTouched.value = false
  quotaMonthlyTouched.value = false
}

function openCreateUser() {
  resetEditor()
  userPassword.value = ''
  userPasswordConfirm.value = ''
  editorVisible.value = true
}

function editUser(row: UserSummary) {
  editingUserId.value = row.id
  userAccount.value = row.username
  userPassword.value = ''
  userPasswordConfirm.value = ''
  isUserAdmin.value = row.is_admin
  isUserSuperAdmin.value = row.is_super_admin
  initialUserAdmin.value = row.is_admin
  initialUserSuperAdmin.value = row.is_super_admin
  userNickname.value = row.nickname
  quotaUnlimited.value = row.quota.unlimited
  quotaLifetimeUsd.value = row.quota.lifetime_quota_usd ?? 0
  quotaMonthlyUsd.value = row.quota.monthly_quota_usd ?? 0
  initialQuotaUnlimited.value = row.quota.unlimited
  initialQuotaLifetimeUsd.value = row.quota.lifetime_quota_usd
  initialQuotaMonthlyUsd.value = row.quota.monthly_quota_usd
  quotaLifetimeTouched.value = false
  quotaMonthlyTouched.value = false
  editorVisible.value = true
}

async function refresh() {
  isLoading.value = true
  try {
    const result = await listUsersPage(userPage.value, userPageSize)
    users.value = result.items
    userTotal.value = result.total
  } catch (error) {
    message.error(errorText(error, '加载用户列表失败', 'Failed to load users'))
  } finally {
    isLoading.value = false
  }
}

function isUserDisabled(row: UserSummary): boolean {
  return row.disabled_at !== null
}

function onUserPageChange(page: number) {
  userPage.value = page
  void refresh()
}

function canManageUser(row: UserSummary): boolean {
  return canEditSuperAdminRole.value || (!row.is_admin && !row.is_super_admin)
}

const operatingUserIds = ref<Set<number>>(new Set())

async function disableUserRow(row: UserSummary) {
  if (operatingUserIds.value.has(row.id)) return
  operatingUserIds.value = new Set(operatingUserIds.value).add(row.id)
  try {
    await disableUser(row.id)
    message.success(t('用户已禁用', 'User disabled'))
    await refresh()
  } catch (error) {
    message.error(errorText(error, '禁用用户失败', 'Failed to disable user'))
  } finally {
    const next = new Set(operatingUserIds.value); next.delete(row.id); operatingUserIds.value = next
  }
}

async function enableUserRow(row: UserSummary) {
  if (operatingUserIds.value.has(row.id)) return
  operatingUserIds.value = new Set(operatingUserIds.value).add(row.id)
  try {
    await enableUser(row.id)
    message.success(t('用户已启用', 'User enabled'))
    await refresh()
  } catch (error) {
    message.error(errorText(error, '启用用户失败', 'Failed to enable user'))
  } finally {
    const next = new Set(operatingUserIds.value); next.delete(row.id); operatingUserIds.value = next
  }
}

async function saveUser() {
  if (isSavingUser.value) return
  const nickname = userNickname.value.trim()
  if (!nickname) {
    message.error(t('用户昵称不能为空', 'User nickname is required'))
    return
  }
  const username = userAccount.value.trim()
  if (!username) {
    message.error(t('账号不能为空', 'Account is required'))
    return
  }
  const isEditing = editingUserId.value !== null
  const wasEditing = isEditing
  const password = userPassword.value.trim()
  const passwordConfirm = userPasswordConfirm.value.trim()
  if (!isEditing && !password) {
    message.error(t('密码不能为空', 'Password is required'))
    return
  }
  if (password && password !== passwordConfirm) {
    message.error(t('两次输入的密码不一致', 'Passwords do not match'))
    return
  }
  isSavingUser.value = true
  try {
    const payload = {
      username,
      password: password || undefined,
      is_admin: canEditSuperAdminRole.value ? (isUserAdmin.value || isUserSuperAdmin.value) : initialUserAdmin.value,
      is_super_admin: canEditSuperAdminRole.value ? isUserSuperAdmin.value : initialUserSuperAdmin.value,
      nickname,
    }
    const saved =
      editingUserId.value !== null
        ? await updateUser(editingUserId.value, payload)
        : await createUser({
            ...payload,
            lifetime_quota_usd: canEditDraftQuota.value ? (quotaUnlimited.value ? null : quotaLifetimeUsd.value) : undefined,
            monthly_quota_usd: canEditDraftQuota.value ? (quotaUnlimited.value ? null : quotaMonthlyUsd.value) : undefined,
          })
    if (!isEditing) {
      editingUserId.value = saved.id
    }
    const quotaPayload = {
      lifetime_quota_usd:
        quotaUnlimited.value || (!quotaLifetimeTouched.value && initialQuotaLifetimeUsd.value === null)
          ? null
          : quotaLifetimeUsd.value,
      monthly_quota_usd:
        quotaUnlimited.value || (!quotaMonthlyTouched.value && initialQuotaMonthlyUsd.value === null)
          ? null
          : quotaMonthlyUsd.value,
    }
    const quotaChanged =
      quotaUnlimited.value !== initialQuotaUnlimited.value ||
      quotaPayload.lifetime_quota_usd !== initialQuotaLifetimeUsd.value ||
      quotaPayload.monthly_quota_usd !== initialQuotaMonthlyUsd.value
    const shouldUpdateSavedUserQuota = canEditDraftQuota.value && isEditing && quotaChanged
    if (shouldUpdateSavedUserQuota) {
      await updateUserQuota(saved.id, quotaPayload)
    }
    let redirectPath: string | null = null
    if (currentUser.value?.id === saved.id) {
      const refreshedUser = await getMe()
      setCurrentUser(refreshedUser)
      if (refreshedUser.must_change_password) {
        redirectPath = '/change-credentials'
      } else if (!refreshedUser.is_admin) {
        redirectPath = '/account/settings'
      }
    }
    message.success(wasEditing ? t('用户已保存', 'User saved') : t('用户已创建', 'User created'))
    editorVisible.value = false
    resetEditor()
    if (redirectPath) {
      await router.push(redirectPath)
    } else {
      await refresh()
    }
  } catch (error) {
    message.error(errorText(error, '保存用户失败', 'Failed to save user'))
  } finally {
    isSavingUser.value = false
  }
}

const columns = computed<DataTableColumns<UserSummary>>(() => [
  {
    title: t('用户昵称', 'User nickname'),
    key: 'nickname',
    width: 120,
    render: (row) => userLabel(row),
  },
  {
    title: t('账号', 'Account'),
    key: 'username',
    width: 130,
    render: (row) => row.username,
  },
  {
    title: t('角色', 'Role'),
    key: 'is_admin',
    width: 120,
    render: (row) =>
      row.is_super_admin
        ? t('超级管理员', 'Super admin')
        : row.is_admin
          ? t('管理员', 'Admin')
          : t('普通用户', 'Standard user'),
  },
  {
    title: t('状态', 'Status'),
    key: 'disabled_at',
    width: 90,
    render: (row) =>
      h(
        NTag,
        {
          size: 'small',
          type: isUserDisabled(row) ? 'warning' : 'success',
          bordered: false,
        },
        { default: () => (isUserDisabled(row) ? t('已禁用', 'Disabled') : t('启用中', 'Enabled')) },
      ),
  },
  {
    title: t('余额', 'Balance'),
    key: 'quota',
    width: 210,
    render: (row) => {
      const detail = quotaDetail(row)
      return h('div', { class: ['metric-stack', 'quota-balance-stack'] }, [
        h('div', { class: ['quota-balance-row', 'is-monthly', quotaBalanceClass(row)] }, [
          h('span', { class: 'quota-balance-label' }, t('每月余额：', 'Monthly: ')),
          h('strong', { class: 'quota-balance-value' }, quotaBalanceValue(row, 'monthly')),
        ]),
        h('div', { class: ['quota-balance-row', 'is-lifetime', quotaBalanceClass(row)] }, [
          h('span', { class: 'quota-balance-label' }, t('不限时余额：', 'Lifetime: ')),
          h('strong', { class: 'quota-balance-value' }, quotaBalanceValue(row, 'lifetime')),
        ]),
        ...(detail
          ? [
              h(
                'span',
                { class: ['metric-muted', 'quota-balance-detail', { 'is-error': row.quota.sync_error || row.quota.paused }] },
                detail,
              ),
            ]
          : []),
      ])
    },
  },
  {
    title: t('API KEY 数量', 'API keys'),
    key: 'key_count',
    width: 130,
    render: (row) => t(`${formatInteger(row.key_count)} 个`, `${formatInteger(row.key_count)} keys`),
  },
  {
    title: t('今日请求', 'Today requests'),
    key: 'today_records',
    width: 140,
    render: (row) =>
      h('div', { class: 'metric-stack' }, [
        h('span', { class: 'metric-primary' }, formatInteger(row.today_records)),
        h('span', { class: 'metric-muted' }, todayRequestDetail(row)),
      ]),
  },
  {
    title: t('今日输入', 'Today input'),
    key: 'today_input_tokens',
    width: 120,
    render: (row) => formatCompact(row.today_input_tokens),
  },
  {
    title: t('今日输出', 'Today output'),
    key: 'today_output_tokens',
    width: 120,
    render: (row) => formatCompact(row.today_output_tokens),
  },
  {
    title: t('今日缓存', 'Today cache'),
    key: 'today_cached_tokens',
    width: 120,
    render: (row) => formatCompact(row.today_cached_tokens),
  },
  {
    title: t('今日总 Token', 'Today total tokens'),
    key: 'today_total_tokens',
    width: 145,
    render: (row) => formatCompact(row.today_total_tokens),
  },
  {
    title: t('今日费用', 'Today cost'),
    key: 'today_estimated_cost_usd',
    width: 150,
    render: (row) =>
      h('div', { class: 'metric-stack' }, [
        h('span', { class: 'metric-primary' }, formatUsd(row.today_estimated_cost_usd)),
        h(
          'span',
          { class: ['metric-muted', { 'is-error': row.today_unpriced_records > 0 }] },
          todayCostDetail(row),
        ),
      ]),
  },
  {
    title: t('总费用', 'Total cost'),
    key: 'total_estimated_cost_usd',
    width: 150,
    render: (row) =>
      h('div', { class: 'metric-stack' }, [
        h('span', { class: 'metric-primary' }, formatUsd(row.total_estimated_cost_usd)),
        h(
          'span',
          { class: ['metric-muted', { 'is-error': row.total_unpriced_records > 0 }] },
          row.total_unpriced_records > 0
            ? t(`未计价 ${formatInteger(row.total_unpriced_records)}`, `${formatInteger(row.total_unpriced_records)} unpriced`)
            : t('账号建立以来', 'Since account creation'),
        ),
      ]),
  },
  {
    title: t('最近模型', 'Recent model'),
    key: 'last_model',
    width: 160,
    render: (row) =>
      h('div', { class: 'metric-stack' }, [
        h('span', { class: 'model-value' }, lastModelLabel(row)),
        h('span', { class: 'metric-muted' }, lastProviderLabel(row)),
      ]),
  },
  {
    title: t('最近使用', 'Last used'),
    key: 'last_seen_at',
    width: 180,
    render: (row) => h('span', { class: 'last-seen-value' }, formatDateTime(row.last_seen_at)),
  },
  {
    title: '',
    key: 'actions',
    width: 90,
    fixed: 'right',
    render: (row) => {
      if (!canManageUser(row)) {
        return null
      }
      return h(
        NSpace,
        { size: 4 },
        {
          default: () => [
            h(
              NButton,
              { size: 'small', quaternary: true, onClick: () => editUser(row) },
              { default: () => t('编辑', 'Edit') },
            ),
            row.id === currentUser.value?.id
              ? null
              : isUserDisabled(row)
                ? h(
                    NPopconfirm,
                    { onPositiveClick: () => enableUserRow(row) },
                    {
                      trigger: () =>
                        h(
                          NButton,
                          {
                            size: 'small',
                            quaternary: true,
                            type: 'primary',
                            loading: operatingUserIds.value.has(row.id),
                            disabled: operatingUserIds.value.has(row.id),
                          },
                          { default: () => t('启用', 'Enable') },
                        ),
                      default: () => t(`启用用户 ${userLabel(row)} 并恢复其 API KEY？`, `Enable user ${userLabel(row)} and restore their API keys?`),
                    },
                  )
                : h(
                    NPopconfirm,
                    { onPositiveClick: () => disableUserRow(row) },
                    {
                      trigger: () =>
                        h(
                          NButton,
                          {
                            size: 'small',
                            quaternary: true,
                            type: 'warning',
                            loading: operatingUserIds.value.has(row.id),
                            disabled: operatingUserIds.value.has(row.id),
                          },
                          { default: () => t('禁用', 'Disable') },
                        ),
                      default: () => t(`禁用用户 ${userLabel(row)} 并从 CPA 移除其 API KEY？`, `Disable user ${userLabel(row)} and remove their API keys from CPA?`),
                    },
                  ),
          ],
        },
      )
    },
  },
])

onMounted(refresh)
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">{{ t('用户管理', 'User Management') }}</h1>
        <p class="page-subtitle">{{ t('管理用户昵称、登录账号、密码和角色', 'Manage user nicknames, sign-in accounts, passwords, and roles') }}</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">{{ t('刷新', 'Refresh') }}</NButton>
        <NButton type="primary" @click="openCreateUser">{{ t('增加用户', 'Add user') }}</NButton>
      </NSpace>
    </div>

    <div class="metric-grid user-metrics">
      <div v-for="metric in userMetrics" :key="metric.key" class="metric-card" :class="`is-${metric.tone}`">
        <div class="metric-icon" aria-hidden="true">
          <component :is="metric.icon" :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">{{ metric.label }}</div>
        <div class="metric-value">{{ metric.value }}</div>
        <div class="metric-footnote">{{ metric.footnote }}</div>
      </div>
    </div>

    <section class="panel table-panel">
      <NDataTable
        size="small"
        :loading="isLoading"
        :columns="columns"
        :data="users"
        :pagination="{ page: userPage, pageSize: userPageSize, itemCount: userTotal, onUpdatePage: onUserPageChange }"
        table-layout="fixed"
        :scroll-x="2250"
      />
    </section>

    <NModal
      v-model:show="editorVisible"
      preset="card"
      :mask-closable="false"
      :closable="false"
      :title="editingUserId ? t('编辑用户', 'Edit user') : t('增加用户', 'Add user')"
      :style="{ width: 'min(520px, calc(100vw - 32px))' }"
    >
      <NAlert v-if="editingUserId === null" type="warning" :bordered="false" class="user-editor-warning">
        {{ t('账号一旦创建，不允许删除，只允许禁用，请谨慎操作。', 'Accounts cannot be deleted after creation. They can only be disabled, so proceed carefully.') }}
      </NAlert>

      <NForm label-placement="top">
        <NFormItem :label="t('用户昵称', 'User nickname')" required>
          <NInput
            v-model:value="userNickname"
            :placeholder="t('例如：研发用户', 'Example: Engineering user')"
            @keyup.enter="saveUser"
          />
        </NFormItem>
        <NFormItem :label="t('账号', 'Account')" required>
          <NInput
            v-model:value="userAccount"
            autocomplete="username"
            :disabled="editingUserId !== null"
            :placeholder="t('例如：user001', 'Example: user001')"
            @keyup.enter="saveUser"
          />
        </NFormItem>
        <NFormItem :label="t('密码', 'Password')" :required="editingUserId === null">
          <NInput
            v-model:value="userPassword"
            type="password"
            show-password-on="mousedown"
            autocomplete="new-password"
            :placeholder="editingUserId ? t('留空不修改密码', 'Leave blank to keep the current password') : t('请输入登录密码', 'Enter a sign-in password')"
            @keyup.enter="saveUser"
          />
        </NFormItem>
        <NFormItem :label="t('确认密码', 'Confirm password')" :required="editingUserId === null">
          <NInput
            v-model:value="userPasswordConfirm"
            type="password"
            show-password-on="mousedown"
            autocomplete="new-password"
            :placeholder="editingUserId ? t('仅在修改密码时填写', 'Fill only when changing the password') : t('请再次输入登录密码', 'Enter the sign-in password again')"
            @keyup.enter="saveUser"
          />
        </NFormItem>
        <NFormItem :label="t('管理员角色', 'Admin role')">
          <NSwitch v-model:value="isUserAdmin" :disabled="isUserSuperAdmin || !canEditSuperAdminRole" />
          <span class="role-switch-label">{{ isUserAdmin ? t('管理员', 'Admin') : t('普通用户', 'Standard user') }}</span>
        </NFormItem>
        <NFormItem v-if="canEditSuperAdminRole" :label="t('是否设为超级管理员', 'Set as super admin')">
          <NSwitch
            v-model:value="isUserSuperAdmin"
            :disabled="!canEditSuperAdminRole"
            @update:value="setSuperAdmin"
          />
          <span class="role-switch-label">{{ isUserSuperAdmin ? t('超级管理员', 'Super admin') : t('普通管理员或普通用户', 'Admin or standard user') }}</span>
        </NFormItem>
        <NAlert v-if="isDemotingSelf" type="warning" :bordered="false" class="quota-editor-hint">
          {{ t('取消自己的超级管理员身份后，余额设置将不可用，本次输入的余额修改不会保存。', 'After removing your own super-admin role, balance settings are unavailable and balance changes entered here will not be saved.') }}
        </NAlert>
        <NFormItem v-if="canEditQuota" :label="t('余额设置', 'Balance settings')">
          <div class="quota-unlimited-row">
            <div>
              <div class="quota-unlimited-title">{{ t('不限制余额', 'Unlimited balance') }}</div>
              <div class="quota-unlimited-desc">{{ t('开启后不扣余额，也不会因余额暂停 API Key。', 'When enabled, balances are not deducted and API keys are not paused due to balance.') }}</div>
            </div>
            <NSwitch
              :value="quotaUnlimited"
              :disabled="!canEditDraftQuota"
              @update:value="setQuotaUnlimited"
            />
          </div>
        </NFormItem>
        <div v-if="canEditQuota" class="form-grid quota-editor-grid">
          <NFormItem :label="t('不限时余额 USD', 'Lifetime balance USD')">
            <NInputNumber
              :value="quotaLifetimeUsd"
              :disabled="quotaUnlimited || !canEditDraftQuota"
              :min="0"
              :precision="8"
              placeholder="0"
              @update:value="setQuotaLifetimeUsd"
            />
          </NFormItem>
          <NFormItem :label="t('每月余额 USD', 'Monthly balance USD')">
            <NInputNumber
              :value="quotaMonthlyUsd"
              :disabled="quotaUnlimited || !canEditDraftQuota"
              :min="0"
              :precision="8"
              placeholder="0"
              @update:value="setQuotaMonthlyUsd"
            />
          </NFormItem>
        </div>
        <NAlert v-if="canEditDraftQuota" type="info" :bordered="false" class="quota-editor-hint">
          {{ t('关闭不限制后，扣费顺序：先扣每月余额，不足部分再扣不限时余额；两者都无剩余时暂停该用户的 API Key。', 'After unlimited balance is disabled, charges are deducted from monthly balance first, then lifetime balance. If neither has remaining balance, the user API keys are paused.') }}
        </NAlert>
        <div class="user-editor-actions">
          <NButton secondary @click="editorVisible = false">{{ t('取消', 'Cancel') }}</NButton>
          <NButton type="primary" :loading="isSavingUser" @click="saveUser">
            {{ editingUserId ? t('保存', 'Save') : t('创建', 'Create') }}
          </NButton>
        </div>
      </NForm>
    </NModal>
  </section>
</template>

<style scoped>
.user-metrics {
  grid-template-columns: repeat(4, minmax(150px, 1fr));
}

.user-editor-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.user-editor-warning {
  margin-bottom: 12px;
}

.quota-editor-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

.quota-editor-hint {
  margin: -2px 0 12px;
}

.quota-unlimited-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  gap: 16px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  background: var(--cpa-surface);
}

.quota-unlimited-title {
  color: var(--cpa-text-strong);
  font-size: 13px;
  font-weight: 760;
}

.quota-unlimited-desc {
  margin-top: 2px;
  color: var(--cpa-muted);
  font-size: 12px;
  line-height: 1.35;
}

.role-switch-label {
  margin-left: 8px;
  color: var(--cpa-muted);
  font-size: 12px;
}

:global(.metric-stack) {
  display: grid;
  gap: 2px;
  min-width: 0;
  line-height: 1.28;
}

:global(.quota-balance-stack) {
  gap: 3px;
}

:global(.quota-balance-row) {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  width: fit-content;
  max-width: 100%;
  padding: 2px 7px;
  overflow: hidden;
  border-radius: var(--cpa-radius-sm);
  font-size: 12px;
  line-height: 1.35;
  white-space: nowrap;
}

:global(.quota-balance-row.is-monthly.is-normal) {
  background: var(--cpa-success-weak);
  color: var(--cpa-success);
}

:global(.quota-balance-row.is-lifetime.is-normal) {
  background: var(--cpa-primary-weak);
  color: var(--cpa-primary);
}

:global(.quota-balance-row.is-unlimited) {
  background: var(--cpa-primary-wash);
  color: var(--cpa-primary);
}

:global(.quota-balance-row.is-warning) {
  background: var(--cpa-warning-weak);
  color: var(--cpa-warning);
}

:global(.quota-balance-row.is-error) {
  background: var(--cpa-danger-weak);
  color: var(--cpa-danger);
}

:global(.quota-balance-label),
:global(.quota-balance-value) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

:global(.quota-balance-label) {
  flex: 0 0 auto;
  font-weight: 600;
}

:global(.quota-balance-value) {
  font-weight: 760;
}

:global(.metric-primary) {
  font-weight: 600;
}

:global(.metric-muted) {
  min-width: 0;
  overflow: hidden;
  color: var(--cpa-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.metric-muted.is-error) {
  color: var(--cpa-danger);
}

:global(.model-value) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

:global(.last-seen-value) {
  display: inline-block;
  white-space: nowrap;
}

@media (max-width: 900px) {
  .user-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 560px) {
  .user-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .quota-editor-grid {
    grid-template-columns: 1fr;
  }
}
</style>
