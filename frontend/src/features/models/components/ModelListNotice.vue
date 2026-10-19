<script setup lang="ts">
import { computed } from 'vue'
import { NAlert } from 'naive-ui'

import { useI18n } from '@/shared/i18n'
import type { AvailableModelsResponse } from '@/shared/types/api'

const props = defineProps<{ status: AvailableModelsResponse['model_list_filter_status'] }>()
const { t } = useI18n()
const notice = computed(() => {
  if (props.status === 'enabled') {
    return t(
      '模型目录已按各 Key 的启用状态及关联、独立模型规则过滤；插件 v0.1.8 起还根据已同步目录检查凭据限制。实际请求仍受路由规则和凭据可用状态约束。',
      'The catalog is filtered by each key’s enabled state, linked rules and individual model rules. Plugin v0.1.8 and later also check credential restrictions against the synced catalog. Actual requests remain subject to routing rules and credential availability.',
    )
  }
  if (props.status === 'disabled') {
    return t(
      '插件模型列表过滤已关闭，当前展示 CPA 原始目录；路由规则仍在实际请求时执行。',
      'Plugin model list filtering is disabled. The original CPA catalog is shown; routing rules still apply to actual requests.',
    )
  }
  if (props.status === 'not_loaded') {
    return t(
      '插件未加载，当前展示 CPA 原始目录，未按插件路由规则过滤。',
      'The plugin is not loaded, so the original CPA catalog is shown without plugin routing-rule filtering.',
    )
  }
  if (props.status === 'not_configured') {
    return t(
      '插件管理密钥未配置，无法确认模型目录是否按路由规则过滤；实际请求结果以 CPA 为准。',
      'The plugin management key is not configured, so catalog filtering cannot be verified; actual request behavior is determined by CPA.',
    )
  }
  if (props.status === 'unsupported') {
    return t(
      '当前插件未提供模型列表过滤能力，无法确认目录已按路由规则过滤；路由限制以实际请求为准。',
      'The plugin does not provide model list filtering capabilities. Catalog filtering cannot be confirmed; routing restrictions are evaluated on actual requests.',
    )
  }
  return ''
})
</script>

<template>
  <NAlert v-if="notice" :type="status === 'enabled' ? 'info' : 'warning'" :bordered="false">
    {{ notice }}
  </NAlert>
</template>
