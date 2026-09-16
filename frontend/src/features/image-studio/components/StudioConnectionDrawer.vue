<script setup lang="ts">
import { computed } from 'vue'
import { NAlert, NButton, NDrawer, NDrawerContent, NForm, NFormItem, NInput, NTag, useDialog } from 'naive-ui'

import { useI18n } from '@/shared/i18n'

import { upstreamApiUrl } from '../api/imageStudioApi'
import { useImageStudio } from '../state/useImageStudio'
import { useStudioFeedback } from '../state/useStudioFeedback'
import { problemMessage } from '../utils/studioErrors'
import { saveStatusText } from '../utils/studioPresentation'

defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [show: boolean] }>()
const { t } = useI18n()
const dialog = useDialog()
const beginAction = useStudioFeedback()
const { connection, connectionSave, localLoading, saveConnection, clearConnection } = useImageStudio()
const saving = computed(() => connectionSave.value.status === 'saving')
const endpoint = computed(() => {
  try { return upstreamApiUrl(connection.value.baseUrl, 'chat/completions') } catch { return '' }
})

async function save(): Promise<void> {
  const action = beginAction()
  try {
    if (await saveConnection()) action.success('连接已保存在当前账号的浏览器中', 'Connection saved in this browser for this account')
  } catch (error) { action.error(error) }
}

function confirmClear(): void {
  const action = beginAction()
  dialog.warning({
    title: t('清除连接设置', 'Clear connection settings'),
    content: t('清除当前账号在此浏览器保存的连接和表单中的 Key。进行中的请求仍沿用提交时的设置。', 'Remove this account’s saved connection and the key in the form. Running requests retain the settings used when submitted.'),
    positiveText: t('清除', 'Clear'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      if (!action.current()) return
      try {
        if (!await clearConnection()) return false
        action.success('已清除保存的连接', 'Saved connection cleared')
      } catch (error) { action.error(error); return false }
    },
  })
}
</script>

<template>
  <NDrawer :show="show" width="min(480px, 100vw)" @update:show="emit('update:show', $event)">
    <NDrawerContent :title="t('上游连接', 'Upstream connection')" closable>
      <div class="connection-content">
        <p class="connection-note">{{ t('直接使用你填写的上游生成图片。保存后，设置仅在当前浏览器的这个账号中保留。', 'Generate with your configured upstream. Saved settings stay in this browser for this account.') }}</p>
        <NForm label-placement="top" :disabled="saving || localLoading">
          <NFormItem label="Base URL" :label-props="{ for: 'studio-base-url' }">
            <NInput v-model:value="connection.baseUrl" :placeholder="t('填写上游 Base URL', 'Enter upstream Base URL')" :input-props="{ id: 'studio-base-url', autocomplete: 'url', spellcheck: false }" />
          </NFormItem>
          <NFormItem label="API Key" :label-props="{ for: 'studio-api-key' }">
            <NInput v-model:value="connection.apiKey" type="password" show-password-on="click" :placeholder="t('填写上游 API Key', 'Enter the upstream API key')" :input-props="{ id: 'studio-api-key', autocomplete: 'off', spellcheck: false }" />
          </NFormItem>
        </NForm>
        <div v-if="endpoint" class="connection-endpoint">
          <span>{{ t('生成地址', 'Generation endpoint') }}</span>
          <code>{{ endpoint }}</code>
        </div>
        <p class="connection-note">{{ t('当前填写的连接立即用于新任务；点击保存可在刷新后恢复。', 'New tasks use the current connection. Save it to restore it after refreshing.') }}</p>
        <NAlert v-if="connectionSave.problem" type="error" :bordered="false">{{ problemMessage(connectionSave.problem) }}</NAlert>
        <div class="connection-actions">
          <NTag size="small" :type="connectionSave.status === 'saved' ? 'success' : 'default'" :bordered="false">{{ saveStatusText(connectionSave.status) }}</NTag>
          <NButton :disabled="saving || localLoading" @click="confirmClear">{{ t('清除连接', 'Clear connection') }}</NButton>
          <NButton type="primary" :loading="saving" :disabled="saving || localLoading" @click="save">{{ t('保存连接', 'Save connection') }}</NButton>
        </div>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.connection-content { display: grid; gap: 14px; }
.connection-note { margin: 0; color: var(--cpa-text-muted); }
.connection-endpoint { display: grid; gap: 6px; padding: 12px; background: var(--cpa-surface-muted); border-radius: var(--cpa-radius-sm); }
.connection-endpoint span { color: var(--cpa-text-muted); font-size: 12px; }
.connection-endpoint code { overflow-wrap: anywhere; font-size: 12px; }
.connection-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; }
.connection-actions .n-tag { margin-right: auto; }
</style>
