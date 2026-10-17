<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  NAlert, NButton, NDataTable, NForm, NFormItem, NInput, NModal, NSelect, NSpace,
  useDialog, useMessage, type DataTableColumns, type SelectOption,
} from 'naive-ui'

import PluginRuleFields from '@/features/plugin/components/PluginRuleFields.vue'
import {
  getCPAHelperPluginDirectory, getCPAHelperPluginPolicy, getCPAHelperPluginStatus,
  updateCPAHelperPluginPolicy,
} from '@/features/plugin/api/cpaHelperPluginApi'
import { ApiRequestError } from '@/shared/api/apiClient'
import { useI18n } from '@/shared/i18n'
import type {
  CPAHelperPluginDirectory, CPAHelperPluginGroup, CPAHelperPluginPolicy,
  CPAHelperPluginRule, CPAHelperPluginStatus,
} from '@/shared/types/api'

const message = useMessage()
const dialog = useDialog()
const { errorText, t } = useI18n()
const loading = ref(false)
const saving = ref(false)
let policySnapshotGeneration = 0
const policy = ref<CPAHelperPluginPolicy | null>(null)
const directory = ref<CPAHelperPluginDirectory | null>(null)
const status = ref<CPAHelperPluginStatus | null>(null)
const loadError = ref<unknown>(null)
const modalVisible = ref(false)
const dirty = ref(false)
const editingId = ref<string | null>(null)
const groupKeyIds = ref<string[]>([])
const groupDraft = reactive<CPAHelperPluginGroup>(Object.assign(Object.create(null), { id: '', name: '', note: '', rule: {} }))

const statusLabel = computed(() => {
  if (!loadError.value) return t('插件已连接', 'Plugin connected')
  if (loadError.value instanceof ApiRequestError && loadError.value.code === 'plugin_not_loaded') return t('插件未加载或契约不兼容', 'Plugin is not loaded or the contract is incompatible')
  if (loadError.value instanceof ApiRequestError && loadError.value.code === 'plugin_unavailable') return t('插件不可达', 'Plugin is unreachable')
  return t('插件状态读取失败', 'Plugin status could not be read')
})
const groups = computed(() => policy.value?.groups ?? [])
const credentialProviders = computed(() => directory.value?.credential_providers ?? [])
const normalizedGroupIds = (value: unknown): string[] => Array.isArray(value)
  ? value.filter((item): item is string => typeof item === 'string')
  : []
const boundKeyCount = (id: string) => policy.value?.keys.filter((key) => normalizedGroupIds(key.group_ids).includes(id)).length ?? 0
const keyOptions = computed(() => {
  return (policy.value?.keys ?? []).map((key) => {
    const owner = policy.value?.helper_key_metadata?.[key.id]
    const ownerLabel = owner
      ? [owner.nickname, owner.username].filter(Boolean).join(' / ')
      : t('非 Helper 管理 · 归属未知', 'Not managed by Helper · Owner unknown')
    const label = [ownerLabel, owner?.description || key.label, owner?.key_preview || `${key.id.slice(0, 8)}…`].filter(Boolean).join(' · ')
    return { label, value: key.id }
  })
})
const filterKeyOption = (pattern: string, option: SelectOption) =>
  `${option.label ?? ''} ${option.value ?? ''}`.toLocaleLowerCase().includes(pattern.trim().toLocaleLowerCase())
const ruleTextEllipsis = {
  tooltip: {
    width: 'trigger' as const,
    style: { maxWidth: 'calc(100vw - 32px)', overflowWrap: 'anywhere' },
  },
}
const columns = computed<DataTableColumns<CPAHelperPluginGroup>>(() => [
  { title: t('名称', 'Name'), key: 'name', width: 260, ellipsis: ruleTextEllipsis },
  { title: t('备注', 'Note'), key: 'note', width: 360, ellipsis: ruleTextEllipsis, render: (row) => row.note || '-' },
  { title: t('关联 Key', 'Bound keys'), key: 'keys', width: 110, render: (row) => `${boundKeyCount(row.id)}` },
  { title: '', key: 'actions', width: 150, fixed: 'right', render: (row) => h(NSpace, { size: 4, wrap: false }, { default: () => [
    h(NButton, { size: 'small', quaternary: true, disabled: loading.value || saving.value, onClick: () => openEditor(row) }, { default: () => t('编辑', 'Edit') }),
    h(NButton, { size: 'small', quaternary: true, type: 'error', disabled: loading.value || saving.value, onClick: () => removeGroup(row) }, { default: () => t('删除', 'Delete') }),
  ] }) },
])

async function refresh() {
  if (saving.value || modalVisible.value) return
  const generation = ++policySnapshotGeneration
  loading.value = true
  loadError.value = null
  try {
    const [pluginStatus, nextPolicy, nextDirectory] = await Promise.all([
      getCPAHelperPluginStatus(), getCPAHelperPluginPolicy(), getCPAHelperPluginDirectory(),
    ])
    if (generation !== policySnapshotGeneration) return
    status.value = pluginStatus
    policy.value = {
      ...nextPolicy,
      groups: Array.isArray(nextPolicy.groups) ? nextPolicy.groups : [],
      keys: Array.isArray(nextPolicy.keys)
        ? nextPolicy.keys.map((key) => ({ ...key, group_ids: normalizedGroupIds(key.group_ids) }))
        : [],
    }
    directory.value = nextDirectory
  } catch (error) {
    if (generation !== policySnapshotGeneration) return
    loadError.value = error
    status.value = null
    policy.value = null
    directory.value = null
    message.error(errorText(error, '加载路由规则失败', 'Failed to load route rules'))
  } finally { if (generation === policySnapshotGeneration) loading.value = false }
}
function openEditor(group?: CPAHelperPluginGroup) {
  if (loading.value || saving.value || modalVisible.value || !policy.value || loadError.value) return
  const value = group ?? {
    id: Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, '0')).join(''),
    name: '', note: '', rule: {},
  }
  for (const key of Object.keys(groupDraft)) Reflect.deleteProperty(groupDraft, key)
  Object.assign(groupDraft, JSON.parse(JSON.stringify(value)))
  editingId.value = group?.id ?? null
  groupKeyIds.value = group ? (policy.value?.keys.filter((key) => normalizedGroupIds(key.group_ids).includes(group.id)).map((key) => key.id) ?? []) : []
  dirty.value = false
  modalVisible.value = true
}
function updateRule(value: CPAHelperPluginRule) { groupDraft.rule = value; dirty.value = true }
function closeEditor() {
  if (saving.value) return
  if (dirty.value) {
    dialog.warning({
      title: t('放弃修改', 'Discard changes'), content: t('尚未保存的规则修改将被丢弃。', 'Unsaved rule changes will be discarded.'),
      positiveText: t('放弃', 'Discard'), negativeText: t('继续编辑', 'Keep editing'),
      onPositiveClick: () => {
        if (saving.value || !modalVisible.value) return false
        modalVisible.value = false
        dirty.value = false
      },
    })
    return
  }
  modalVisible.value = false
}
async function saveGroup() {
  if (!policy.value || loading.value || saving.value || !modalVisible.value) return
  if (!groupDraft.name.trim()) { message.error(t('请输入规则名称', 'Enter a rule name')); return }
  const next = JSON.parse(JSON.stringify(policy.value)) as CPAHelperPluginPolicy
  const value = JSON.parse(JSON.stringify({ ...groupDraft, name: groupDraft.name.trim(), note: groupDraft.note?.trim() ?? '' })) as CPAHelperPluginGroup
  const index = next.groups.findIndex((group) => group.id === value.id)
  if (index >= 0) next.groups[index] = value
  else next.groups.push(value)
  for (const key of next.keys) {
    const groupIds = normalizedGroupIds(key.group_ids)
    const includes = groupIds.includes(value.id)
    const shouldInclude = groupKeyIds.value.includes(key.id)
    if (shouldInclude && !includes) key.group_ids = [...groupIds, value.id]
    if (!shouldInclude && includes) key.group_ids = groupIds.filter((id) => id !== value.id)
  }
  saving.value = true
  const generation = ++policySnapshotGeneration
  try {
    const saved = await updateCPAHelperPluginPolicy(next)
    if (generation !== policySnapshotGeneration) return
    policy.value = saved
    dirty.value = false
    modalVisible.value = false
    message.success(t('路由规则已保存', 'Route rule saved'))
  } catch (error) {
    if (generation === policySnapshotGeneration) message.error(errorText(error, '保存路由规则失败，编辑内容仍保留', 'Failed to save route rule; your edits are preserved'))
  } finally { if (generation === policySnapshotGeneration) saving.value = false }
}
async function removeGroup(group: CPAHelperPluginGroup) {
  if (loading.value || saving.value || modalVisible.value) return
  if (!policy.value || boundKeyCount(group.id) > 0) { message.warning(t('请先解除所有 API Key 的关联', 'Unbind all API keys first')); return }
  const confirmation = dialog.warning({
    title: t('删除路由规则', 'Delete route rule'), content: t(`确定删除「${group.name}」？`, `Delete “${group.name}”?`),
    positiveText: t('删除', 'Delete'), negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      if (!policy.value || loading.value || saving.value || modalVisible.value || !policy.value.groups.some((item) => item.id === group.id)) return false
      if (boundKeyCount(group.id) > 0) { message.warning(t('请先解除所有 API Key 的关联', 'Unbind all API keys first')); return false }
      saving.value = true
      const generation = ++policySnapshotGeneration
      confirmation.loading = true
      try {
        const next = JSON.parse(JSON.stringify(policy.value)) as CPAHelperPluginPolicy
        next.groups = next.groups.filter((item) => item.id !== group.id)
        const saved = await updateCPAHelperPluginPolicy(next)
        if (generation !== policySnapshotGeneration) return false
        policy.value = saved
        message.success(t('路由规则已删除', 'Route rule deleted'))
      } catch (error) {
        if (generation === policySnapshotGeneration) message.error(errorText(error, '删除路由规则失败', 'Failed to delete route rule'))
        return false
      } finally {
        confirmation.loading = false
        if (generation === policySnapshotGeneration) saving.value = false
      }
    },
  })
}
onMounted(refresh)
onBeforeUnmount(() => { policySnapshotGeneration++ })
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div><h1 class="page-title">{{ t('路由规则', 'Route rules') }}</h1><p class="page-subtitle">{{ t('维护全局路由规则与 API Key 关联。仅超级管理员可访问。', 'Manage global route rules and API key bindings. Super admins only.') }}</p></div>
      <NSpace><NButton secondary :loading="loading" :disabled="saving || modalVisible" @click="refresh">{{ t('刷新', 'Refresh') }}</NButton><NButton type="primary" :disabled="loading || saving || !policy || !!loadError" @click="openEditor()">{{ t('新增规则', 'New rule') }}</NButton></NSpace>
    </div>
    <NAlert :type="loadError ? 'error' : 'success'" :bordered="false" class="status-alert">
      <template #header>{{ statusLabel }}</template>
      <span v-if="loadError">{{ t('插件状态不可用时，CPA-Helper 的其他功能仍可继续使用。', 'Other CPA-Helper features remain available when the plugin is unavailable.') }}</span>
    </NAlert>
    <section class="panel"><div class="panel-inner"><NDataTable :loading="loading" :columns="columns" :data="groups" :pagination="false" table-layout="fixed" :scroll-x="880" /><div v-if="!loading && policy && groups.length === 0" class="empty-state">{{ t('暂无全局路由规则', 'No global route rules') }}</div></div></section>
    <NModal v-model:show="modalVisible" preset="card" :mask-closable="false" :closable="false" :close-on-esc="false" :style="{ width: 'min(860px, calc(100vw - 32px))' }" :title="editingId ? t('编辑路由规则', 'Edit route rule') : t('新增路由规则', 'New route rule')" @esc="closeEditor">
      <NForm label-placement="top">
        <NFormItem :label="t('规则名称', 'Rule name')" required><NInput v-model:value="groupDraft.name" :disabled="saving" @update:value="dirty = true" /></NFormItem>
        <NFormItem :label="t('备注', 'Note')"><NInput :value="groupDraft.note ?? ''" :disabled="saving" @update:value="(value) => { groupDraft.note = value; dirty = true }" /></NFormItem>
        <NFormItem :label="t('关联 API Key', 'Bound API keys')">
          <NSelect v-model:value="groupKeyIds" multiple filterable :options="keyOptions" :filter="filterKeyOption" :disabled="saving" :placeholder="t('搜索用户名、昵称、描述或脱敏 Key', 'Search username, nickname, description or masked key')" @update:value="dirty = true" />
        </NFormItem>
        <PluginRuleFields :key="editingId ?? groupDraft.id" v-model="groupDraft.rule" :models="directory?.models ?? []" :credentials="directory?.credentials ?? []" :credential-providers="credentialProviders" :disabled="saving" @update:model-value="updateRule" />
      </NForm>
      <div class="modal-actions"><NButton secondary :disabled="saving" @click="closeEditor">{{ t('取消', 'Cancel') }}</NButton><NButton type="primary" :loading="saving" @click="saveGroup">{{ t('保存规则', 'Save rule') }}</NButton></div>
    </NModal>
  </section>
</template>

<style scoped>
.status-alert { margin-bottom: 18px; }
.empty-state { padding: 32px 0; text-align: center; color: var(--cpa-muted); }
</style>
