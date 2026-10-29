<script setup lang="ts">
import { computed } from 'vue'
import { NAlert, NButton } from 'naive-ui'

import { useI18n } from '@/shared/i18n'
import { useFeatureState } from '../state/useFeatureState'
import type { FeatureName } from '../state/featureController'

const props = defineProps<{ feature?: FeatureName }>()
const { state, imageStudio, aiRadar, reconnect } = useFeatureState()
const { t } = useI18n()
const availability = computed(() => props.feature === 'image_studio' ? imageStudio.value : props.feature === 'ai_radar' ? aiRadar.value : state.value.verified ? 'enabled' : 'unknown')

function retry(): void { void reconnect().catch(() => { /* The shared state renders the error. */ }) }
</script>

<template>
  <NAlert v-if="availability !== 'enabled'" :type="availability === 'disabled' ? 'info' : 'warning'" :bordered="false" aria-live="polite">
    <p v-if="availability === 'disabled'">{{ t('此功能已被管理员关闭。历史、设置和已完成的原图仍会保留；重新开启后不会自动重放已中断的任务。', 'This feature has been disabled by an administrator. History, settings and completed originals are retained. Interrupted tasks will not replay when enabled again.') }}</p>
    <template v-else>
      <p>{{ state.connecting ? t('正在验证功能状态，相关功能暂时暂停…', 'Verifying feature state. Features are paused…') : state.error || t('功能状态未验证，相关功能已暂停。', 'Feature state is unverified. Features are paused.') }}</p>
      <NButton size="small" :loading="state.connecting" :disabled="state.connecting" @click="retry">{{ t('重新连接', 'Reconnect') }}</NButton>
    </template>
  </NAlert>
</template>

<style scoped>
p { margin: 0 0 8px; }
p:last-child { margin-bottom: 0; }
</style>
