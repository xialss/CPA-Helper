<script setup lang="ts">
import { computed } from 'vue'
import { NAlert, NButton, NCollapse, NCollapseItem, NIcon, NModal, NSpin, NTag } from 'naive-ui'
import { Copy, Download, RotateCcw } from 'lucide-vue-next'

import { useI18n } from '@/shared/i18n'

import { isTaskActive } from '../services/generationRunner'
import { useStudioImage } from '../state/useStudioImage'
import type { GenerationTask } from '../types'
import { seedComparison } from '../utils/generationParameters'
import { problemMessage } from '../utils/studioErrors'
import { saveStatusText, seedEvidenceText, studioTime, taskStatusText, taskStatusType } from '../utils/studioPresentation'

const props = defineProps<{ show: boolean; task: GenerationTask | null; deleting: boolean }>()
const emit = defineEmits<{
  'update:show': [show: boolean]
  reuse: [id: string]
  download: [id: string]
  copySeed: [seed: string]
  retrySave: [id: string]
  delete: [id: string]
}>()
const { t } = useI18n()
const { url, loading, problem, reload } = useStudioImage(() => props.task?.id ?? null, () => props.show && !!props.task?.image)
const parameters = computed(() => JSON.stringify(props.task?.image?.parameters ?? {}, null, 2))
</script>

<template>
  <NModal :show="show && task !== null" preset="card" :title="t('生成详情', 'Generation details')" :style="{ width: 'min(1080px, calc(100vw - 24px))' }" @update:show="emit('update:show', $event)">
    <div v-if="task" class="detail-content">
      <div class="detail-toolbar">
        <NTag :type="taskStatusType(task.status)" :bordered="false">{{ taskStatusText(task.status) }}</NTag>
        <NTag :type="task.save.status === 'saved' ? 'success' : task.save.status === 'failed' ? 'error' : 'default'" :bordered="false">{{ saveStatusText(task.save.status) }}</NTag>
        <span class="detail-time">{{ studioTime(task.createdAt) }}</span>
        <NButton size="small" secondary @click="emit('reuse', task.id)"><template #icon><NIcon :component="RotateCcw" /></template>{{ t('复用设置', 'Reuse settings') }}</NButton>
        <NButton v-if="task.image" size="small" type="primary" @click="emit('download', task.id)"><template #icon><NIcon :component="Download" /></template>{{ t('下载原图', 'Download original') }}</NButton>
      </div>
      <NAlert v-if="task.problem" type="error" :bordered="false">{{ problemMessage(task.problem) }}</NAlert>
      <NAlert v-if="task.save.problem" type="error" :bordered="false">
        {{ problemMessage(task.save.problem) }}
        <div><NButton size="small" :disabled="deleting || task.save.status === 'saving'" @click="emit('retrySave', task.id)">{{ t('重试保存', 'Retry save') }}</NButton></div>
      </NAlert>
      <div v-if="task.image" class="detail-image">
        <NSpin v-if="loading" size="small" />
        <img v-if="url" :src="url" :alt="task.input.positivePrompt">
        <div v-if="problem" class="image-error"><p>{{ problemMessage(problem) }}</p><NButton size="small" @click="reload">{{ t('重读原图', 'Reload image') }}</NButton></div>
      </div>

      <NAlert v-if="task.image" :type="seedComparison(task) === 'matches' ? 'success' : 'warning'" :bordered="false">{{ seedEvidenceText(task) }}</NAlert>
      <dl class="detail-parameters">
        <div><dt>{{ t('请求模型', 'Request model') }}</dt><dd>{{ task.request?.model ?? task.input.model }}</dd></div>
        <div><dt>{{ t('生成模型', 'Generation model') }}</dt><dd>{{ task.request?.generationModel || task.input.generationModel || task.input.model }}<small v-if="!task.input.generationModel">{{ t('沿用请求模型', 'Follows request model') }}</small></dd></div>
        <div><dt>{{ t('请求分辨率', 'Requested resolution') }}</dt><dd>{{ task.input.width }} × {{ task.input.height }}</dd></div>
        <div><dt>{{ t('实际图片尺寸', 'Actual image dimensions') }}</dt><dd>{{ task.image ? `${task.image.width} × ${task.image.height}` : '—' }}</dd></div>
        <div><dt>CFG</dt><dd>{{ task.input.cfg }}</dd></div>
        <div><dt>{{ t('步数', 'Steps') }}</dt><dd>{{ task.input.steps }}</dd></div>
        <div><dt>{{ t('采样器', 'Sampler') }}</dt><dd>{{ task.input.sampler }}</dd></div>
        <div><dt>{{ t('原始种子输入', 'Original seed input') }}</dt><dd>{{ task.input.seed || t('留空，提交时逐张随机', 'Empty, randomized per image on submit') }}</dd></div>
        <div><dt>{{ t('请求种子', 'Submitted seed') }}</dt><dd class="copy-seed"><code>{{ task.request?.submittedSeed ?? '—' }}</code><NButton v-if="task.request" size="tiny" quaternary :aria-label="t('复制请求种子', 'Copy submitted seed')" @click="emit('copySeed', task.request.submittedSeed)"><template #icon><NIcon :component="Copy" /></template></NButton></dd></div>
        <div><dt>{{ t('PNG 返回种子', 'Returned PNG seed') }}</dt><dd class="copy-seed"><code>{{ task.image?.returnedSeed ?? t('未回传', 'Not returned') }}</code><NButton v-if="task.image?.returnedSeed" size="tiny" quaternary :aria-label="t('复制返回种子', 'Copy returned seed')" @click="emit('copySeed', task.image.returnedSeed)"><template #icon><NIcon :component="Copy" /></template></NButton></dd></div>
      </dl>
      <p class="detail-note">{{ t('复用设置优先回填 PNG 返回种子；未回传时使用具体请求种子。旧记录的参数保持不变。', 'Reuse fills the returned PNG seed when available, otherwise the submitted seed. The original record stays unchanged.') }}</p>
      <section class="prompt-snapshot">
        <h3>{{ t('正向提示词', 'Positive prompt') }}</h3><pre>{{ task.input.positivePrompt }}</pre>
        <h3>{{ t('画师串', 'Artist prompt') }}</h3><pre>{{ task.input.artistPrompt || '—' }}</pre>
        <h3>{{ t('负面提示词', 'Negative prompt') }}</h3><pre>{{ task.input.negativePrompt || '—' }}</pre>
      </section>
      <NCollapse>
        <NCollapseItem v-if="task.request" :title="t('最终发送内容', 'Submitted content')" name="submitted">
          <code class="detail-endpoint">{{ task.endpoint }}</code><pre class="detail-json">{{ task.request.content }}</pre>
        </NCollapseItem>
        <NCollapseItem v-if="task.image" :title="t('上游回传参数', 'Returned parameters')" name="returned">
          <NAlert v-for="(warning, index) in task.image.warnings" :key="index" type="warning" :bordered="false">{{ problemMessage(warning) }}</NAlert>
          <pre class="detail-json">{{ parameters }}</pre>
        </NCollapseItem>
      </NCollapse>
      <div class="detail-footer"><NButton v-if="!isTaskActive(task)" size="small" type="error" secondary :disabled="deleting" @click="emit('delete', task.id)">{{ t('删除本地记录', 'Delete local record') }}</NButton><span v-if="task.retryOf" class="detail-note">{{ t('此记录由手动重试创建', 'Created by a manual retry') }}</span></div>
    </div>
  </NModal>
</template>

<style scoped>
.detail-content { display: grid; gap: 16px; max-height: 80dvh; overflow-y: auto; padding-right: 6px; }
.detail-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.detail-time { flex: 1; color: var(--cpa-text-muted); font-size: 12px; }
.detail-image { display: grid; place-items: center; min-height: 100px; background: var(--cpa-surface-muted); border-radius: var(--cpa-radius-sm); }
.detail-image img { display: block; max-width: 100%; max-height: 64dvh; object-fit: contain; }
.image-error { padding: 16px; color: var(--cpa-danger); }
.detail-parameters { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 24px; margin: 0; }
.detail-parameters > div { min-width: 0; }
.detail-parameters dt { color: var(--cpa-text-muted); font-size: 12px; margin-bottom: 4px; }
.detail-parameters dd { margin: 0; overflow-wrap: anywhere; }
.detail-parameters small { display: block; color: var(--cpa-text-muted); }
.copy-seed { display: flex; align-items: center; gap: 6px; }
.copy-seed code { min-width: 0; overflow-wrap: anywhere; }
.copy-seed .n-button { flex-shrink: 0; }
.detail-note { margin: 0; color: var(--cpa-text-muted); font-size: 12px; }
.prompt-snapshot { display: grid; gap: 6px; }
.prompt-snapshot h3 { margin: 6px 0 0; font-size: 13px; }
.prompt-snapshot pre, .detail-json { margin: 0; padding: 10px; white-space: pre-wrap; overflow-wrap: anywhere; background: var(--cpa-surface-muted); border-radius: var(--cpa-radius-sm); font-size: 12px; line-height: 1.6; }
.detail-endpoint { display: block; margin-bottom: 10px; overflow-wrap: anywhere; font-size: 12px; }
.detail-footer { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
@media (max-width: 600px) { .detail-parameters { grid-template-columns: minmax(0, 1fr); } }
</style>
