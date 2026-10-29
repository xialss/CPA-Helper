<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { NAlert, NButton, NSwitch, NTag } from 'naive-ui'

import { useCurrentUser } from '@/features/auth/state/currentUser'
import { isApiRequestError } from '@/shared/api/apiClient'
import { useI18n } from '@/shared/i18n'
import type { FeatureCenterSnapshot } from '@/shared/types/api'
import { getFeatureSettings, updateFeatureSettings } from '../api/featureCenterApi'
import { useFeatureState } from '../state/useFeatureState'

const { currentUser } = useCurrentUser()
const { t, errorText } = useI18n()
const { state, verify } = useFeatureState()
const saved = ref<FeatureCenterSnapshot | null>(null)
const draft = reactive({ image_studio_enabled: false, ai_radar_enabled: false })
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const feedback = ref('')
const conflict = ref(false)
let generation = 0
let controller: AbortController | null = null
const canManage = computed(() => currentUser.value?.is_super_admin === true)
const dirty = computed(() => saved.value !== null && (draft.image_studio_enabled !== saved.value.image_studio_enabled || draft.ai_radar_enabled !== saved.value.ai_radar_enabled))
const effective = computed(() => state.value.snapshot && (!saved.value || state.value.snapshot.revision >= saved.value.revision) ? state.value.snapshot : saved.value)

function latestSnapshot(snapshot: FeatureCenterSnapshot): FeatureCenterSnapshot {
  let latest = saved.value && saved.value.revision > snapshot.revision ? saved.value : snapshot
  if (state.value.snapshot && state.value.snapshot.revision > latest.revision) latest = state.value.snapshot
  return latest
}

watch([() => state.value.snapshot, loading, saving], () => {
  const snapshot = state.value.snapshot
  if (loading.value || saving.value || !saved.value || !snapshot || snapshot.revision <= saved.value.revision) return
  // Check against the previous saved baseline before replacing it. A stream
  // update must not turn an untouched form into a stale, apparently clean one.
  const preserveDraft = dirty.value || conflict.value
  saved.value = snapshot
  feedback.value = ''
  if (preserveDraft) conflict.value = true
  else Object.assign(draft, snapshot)
}, { flush: 'sync' })

async function load(resetDraft = true): Promise<void> {
  if (!canManage.value || saving.value) return
  const current = ++generation
  const owner = currentUser.value?.id
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  error.value = ''
  try {
    const snapshot = await getFeatureSettings(controller.signal)
    if (current !== generation || currentUser.value?.id !== owner) return
    saved.value = latestSnapshot(snapshot)
    if (resetDraft) { Object.assign(draft, saved.value); conflict.value = false; feedback.value = '' }
  } catch (cause) {
    if (current === generation && currentUser.value?.id === owner) error.value = errorText(cause, '读取功能设置失败', 'Failed to load feature settings')
  } finally { if (current === generation && currentUser.value?.id === owner) loading.value = false }
}

async function save(): Promise<void> {
  if (!canManage.value || !saved.value || saving.value || loading.value || conflict.value) return
  const current = ++generation
  const owner = currentUser.value?.id
  saving.value = true
  error.value = ''
  feedback.value = ''
  const payload = { revision: saved.value.revision, image_studio_enabled: draft.image_studio_enabled, ai_radar_enabled: draft.ai_radar_enabled }
  const owns = () => current === generation && currentUser.value?.id === owner
  try {
    const snapshot = await updateFeatureSettings(payload)
    if (!owns()) return
    saved.value = latestSnapshot(snapshot)
    Object.assign(draft, saved.value)
    feedback.value = saved.value.revision > snapshot.revision
      ? t('功能设置已保存，但之后又被其他会话更新。当前显示最新服务端状态，请重新审阅。', 'Feature settings were saved, then updated by another session. Review the latest server state shown here.')
      : t('功能设置已保存。在线会话收到状态后停止；已被第三方受理的生成或计费无法撤回。', 'Feature settings saved. Online sessions stop when they receive the state. Generation or billing already accepted by a third party cannot be revoked.')
    await verify().catch(() => { /* Shared control status visibly reports disconnection. */ })
  } catch (cause) {
    if (!owns()) return
    conflict.value = isApiRequestError(cause) && cause.code === 'conflict'
    error.value = errorText(cause, '保存功能设置未能确认', 'Could not confirm the feature settings save')
    // A transport error can follow a committed update. Always read the actual
    // state without replacing the administrator's draft or pretending rollback.
    try {
      const snapshot = await getFeatureSettings()
      if (!owns()) return
      const actual = latestSnapshot(snapshot)
      saved.value = actual
      if (!conflict.value && actual.image_studio_enabled === payload.image_studio_enabled && actual.ai_radar_enabled === payload.ai_radar_enabled) {
        feedback.value = t('重新读取确认：所选设置已保存。先前请求未正常完成，停止收尾结果未确认。', 'A fresh read confirms the selected settings were saved. The earlier request did not complete normally, so stop completion is unconfirmed.')
      } else if (actual.revision > payload.revision) {
        conflict.value = true
      }
    } catch (readError) {
      if (owns()) error.value += ` · ${errorText(readError, '实际生效状态也无法读取，请重新读取后确认', 'The effective state could not be read either. Reload to confirm')}`
    }
  } finally { if (owns()) saving.value = false }
}

onMounted(() => { void load() })
onBeforeUnmount(() => { generation++; controller?.abort() })
</script>

<template>
  <section class="panel feature-settings" :aria-label="t('功能配置', 'Feature configuration')">
    <div class="panel-header"><div><h2 class="panel-title">{{ t('功能配置', 'Feature configuration') }}</h2><p>{{ t('超级管理员的全局设置，对所有账号和在线会话生效。关闭会停止相关任务，并保留历史和配置。', 'Global settings managed by the super administrator apply to every account and online session. Disabling stops related work while retaining history and settings.') }}</p></div></div>
    <div class="feature-body">
      <NAlert v-if="!canManage" type="info">{{ t('仅超级管理员可修改功能配置', 'Only the super administrator can change feature settings') }}</NAlert>
      <NAlert v-if="error" type="error" :bordered="false">{{ error }}</NAlert>
      <NAlert v-if="feedback" type="info" :bordered="false">{{ feedback }}</NAlert>
      <NAlert v-if="conflict" type="warning" :bordered="false">{{ t('设置已被其他会话修改。您的草稿已保留；请重新读取并审阅后再保存。', 'Another session changed these settings. Your draft is retained. Reload and review before saving again.') }}</NAlert>
      <div v-for="entry in [{ key: 'image_studio_enabled' as const, zh: '生图广场', en: 'Image Studio' }, { key: 'ai_radar_enabled' as const, zh: 'AI 雷达', en: 'AI Radar' }]" :key="entry.key" class="feature-row">
        <div><strong>{{ t(entry.zh, entry.en) }}</strong><div class="effective">{{ t('服务端已保存', 'Saved on server') }}: {{ effective ? (effective[entry.key] ? t('开启', 'On') : t('关闭', 'Off')) : t('未知', 'Unknown') }}</div></div>
        <NSwitch v-model:value="draft[entry.key]" :disabled="!canManage || !saved || loading || saving" :aria-label="t(entry.zh, entry.en)" />
      </div>
      <div class="feature-actions"><NTag :type="state.verified ? 'success' : 'warning'" :bordered="false">{{ state.verified ? t('本会话已验证', 'Session verified') : t('本会话已暂停', 'Session paused') }}</NTag><span v-if="dirty">{{ t('有未保存的草稿', 'Unsaved changes') }}</span><NButton :loading="loading" :disabled="saving || loading" @click="load()">{{ t('重新读取并重置草稿', 'Reload and reset draft') }}</NButton><NButton type="primary" :loading="saving" :disabled="!canManage || !saved || loading || saving || conflict || !dirty" @click="save">{{ t('保存功能配置', 'Save feature settings') }}</NButton></div>
    </div>
  </section>
</template>

<style scoped>
.feature-body { display: grid; gap: 16px; padding: 20px; }
.panel-header p, .effective { margin: 6px 0 0; color: var(--cpa-text-muted); font-size: 12px; }
.feature-row, .feature-actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.feature-row { border-bottom: 1px solid var(--cpa-border); padding-bottom: 14px; }
.feature-actions { flex-wrap: wrap; justify-content: flex-end; }
</style>
