<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { NButton, NIcon, NSpin, NTag } from 'naive-ui'
import { Copy, Download, Image, RotateCcw, Trash2 } from 'lucide-vue-next'

import { useI18n } from '@/shared/i18n'

import { isTaskActive } from '../services/generationRunner'
import { useStudioImage } from '../state/useStudioImage'
import type { GenerationTask } from '../types'
import { problemMessage } from '../utils/studioErrors'
import { saveStatusText, seedEvidenceText, studioTime, taskStatusText, taskStatusType } from '../utils/studioPresentation'

const props = defineProps<{ task: GenerationTask; deleting: boolean; previewBlurred: boolean }>()
const emit = defineEmits<{
  details: [id: string]
  reuse: [id: string]
  download: [id: string]
  copySeed: [seed: string]
  cancel: [id: string]
  retry: [id: string]
  retrySave: [id: string]
  delete: [id: string]
}>()
const { t } = useI18n()
const element = ref<HTMLElement | null>(null)
const visible = ref(false)
const displayFailed = ref(false)
const active = computed(() => isTaskActive(props.task))
const seed = computed(() => props.task.image?.returnedSeed ?? props.task.request?.submittedSeed ?? '')
const model = computed(() => props.task.request?.generationModel || props.task.input.generationModel || props.task.input.model)
const { url, loading, problem, reload } = useStudioImage(() => props.task.id, () => visible.value && props.task.image !== null)
let observer: IntersectionObserver | null = null

onMounted(() => {
  if (typeof IntersectionObserver === 'undefined') { visible.value = true; return }
  observer = new IntersectionObserver((entries) => {
    visible.value = entries.some((entry) => entry.isIntersecting)
  }, { rootMargin: '200px' })
  if (element.value) observer.observe(element.value)
})
onScopeDispose(() => observer?.disconnect())
watch(url, () => { displayFailed.value = false })
</script>

<template>
  <article ref="element" class="task-card panel">
    <button class="image-frame" :class="{ 'preview-blurred': previewBlurred && url && !displayFailed }" type="button" :aria-label="previewBlurred && url ? t('预览已模糊，查看生成详情', 'Preview blurred; view generation details') : t('查看生成详情', 'View generation details')" @click="emit('details', task.id)">
      <img v-if="url && !displayFailed" :src="url" :alt="task.input.positivePrompt" loading="lazy" @error="displayFailed = true">
      <span v-else class="image-placeholder">
        <NSpin v-if="loading || active" size="small" />
        <NIcon v-else :component="Image" :size="34" />
        <span>{{ loading ? t('读取原图…', 'Loading image…') : (problem || displayFailed) ? t('原图读取失败', 'Image unavailable') : taskStatusText(task.status) }}</span>
      </span>
      <NTag v-if="task.status !== 'succeeded'" class="image-status" size="small" :type="taskStatusType(task.status)" :bordered="false">{{ taskStatusText(task.status) }}</NTag>
    </button>
    <div class="task-content">
      <div class="task-meta"><time :datetime="new Date(task.createdAt).toISOString()">{{ studioTime(task.createdAt) }}</time><span>#{{ task.position }}/{{ task.batchSize }}</span></div>
      <div class="task-model" :title="model">{{ model }}</div>
      <div class="task-seed">
        <span><span class="muted">{{ t('种子', 'Seed') }}</span> <code>{{ seed || '—' }}</code></span>
        <NButton v-if="seed" size="tiny" quaternary :aria-label="t('复制种子', 'Copy seed')" :title="t('复制种子', 'Copy seed')" @click="emit('copySeed', seed)"><template #icon><NIcon :component="Copy" /></template></NButton>
      </div>
      <p class="seed-evidence">{{ seedEvidenceText(task) }}</p>
      <div class="task-save"><span>{{ task.image ? `${task.image.width} × ${task.image.height}` : `${task.input.width} × ${task.input.height}` }}</span><NTag size="small" :bordered="false" :type="task.save.status === 'saved' ? 'success' : task.save.status === 'failed' ? 'error' : 'default'">{{ saveStatusText(task.save.status) }}</NTag></div>
      <p v-if="task.problem" class="task-problem" :title="problemMessage(task.problem)">{{ problemMessage(task.problem) }}</p>
      <p v-if="task.save.problem" class="task-problem" :title="problemMessage(task.save.problem)">{{ problemMessage(task.save.problem) }}</p>
      <p v-if="problem" class="task-problem">{{ problemMessage(problem) }}</p>
      <NButton v-if="problem || displayFailed" size="small" secondary @click="reload">{{ t('重读原图', 'Reload image') }}</NButton>
      <div class="task-actions">
        <NButton size="small" secondary @click="emit('details', task.id)">{{ t('详情', 'Details') }}</NButton>
        <NButton size="small" secondary @click="emit('reuse', task.id)"><template #icon><NIcon :component="RotateCcw" /></template>{{ t('复用', 'Reuse') }}</NButton>
        <NButton v-if="task.image" size="small" :aria-label="t('下载原图', 'Download original')" :title="t('下载原图', 'Download original')" @click="emit('download', task.id)"><template #icon><NIcon :component="Download" /></template></NButton>
        <NButton v-if="active" size="small" type="warning" secondary @click="emit('cancel', task.id)">{{ t('取消', 'Cancel') }}</NButton>
        <NButton v-else-if="task.status !== 'succeeded'" size="small" :disabled="deleting" :title="t('使用当前连接重新生成一张', 'Generate one image again using the current connection')" @click="emit('retry', task.id)">{{ t('重试生成', 'Retry generation') }}</NButton>
        <NButton v-if="!active && task.save.status === 'failed'" size="small" :disabled="deleting" @click="emit('retrySave', task.id)">{{ t('重试保存', 'Retry save') }}</NButton>
        <NButton v-if="!active" class="delete-task" size="small" quaternary type="error" :disabled="deleting" :aria-label="t('删除记录', 'Delete record')" :title="t('删除记录', 'Delete record')" @click="emit('delete', task.id)"><template #icon><NIcon :component="Trash2" /></template></NButton>
      </div>
    </div>
  </article>
</template>

<style scoped>
.task-card { display: flex; flex-direction: column; min-width: 0; }
.image-frame { position: relative; display: grid; place-items: center; width: 100%; aspect-ratio: 1; padding: 0; overflow: hidden; border: 0; border-bottom: 1px solid var(--cpa-border); color: var(--cpa-text-muted); background: var(--cpa-surface-muted); cursor: pointer; }
.image-frame:focus-visible { outline: 2px solid var(--cpa-primary); outline-offset: -2px; }
.image-frame img { width: 100%; height: 100%; object-fit: cover; transition: filter 160ms ease, transform 160ms ease; }
.image-frame.preview-blurred img { filter: blur(24px) saturate(0.65); transform: scale(1.16); }
.image-frame.preview-blurred::after { position: absolute; inset: 0; content: ''; pointer-events: none; background: rgb(12 16 20 / 0.22); }
.image-placeholder { display: grid; justify-items: center; gap: 12px; padding: 24px; font-size: 12px; }
.image-status { position: absolute; z-index: 1; top: 10px; left: 10px; }
.task-content { display: grid; align-content: start; gap: 8px; padding: 12px; flex: 1; }
.task-meta, .task-save, .task-seed { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-width: 0; }
.task-meta, .task-save, .muted, .seed-evidence { color: var(--cpa-text-muted); font-size: 11px; }
.task-model { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 650; font-size: 13px; }
.task-seed { font-size: 12px; }
.task-seed > span { min-width: 0; overflow-wrap: anywhere; }
.task-seed .n-button { flex-shrink: 0; }
.seed-evidence { margin: 0; }
.task-problem { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 3; overflow: hidden; overflow-wrap: anywhere; margin: 0; font-size: 12px; color: var(--cpa-danger); }
.task-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding-top: 5px; }
.delete-task { margin-left: auto; }
</style>
