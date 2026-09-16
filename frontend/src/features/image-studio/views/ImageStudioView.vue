<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { NAlert, NButton, NEmpty, NIcon, NSpin, NSwitch, NTag, useDialog } from 'naive-ui'
import { Images, RefreshCw, Settings2, SlidersHorizontal } from 'lucide-vue-next'

import { useI18n } from '@/shared/i18n'
import { copyToClipboard } from '@/shared/utils/clipboard'

import StudioConnectionDrawer from '../components/StudioConnectionDrawer.vue'
import StudioParameters from '../components/StudioParameters.vue'
import StudioTaskCard from '../components/StudioTaskCard.vue'
import StudioTaskDetail from '../components/StudioTaskDetail.vue'
import { isTaskActive } from '../services/generationRunner'
import { useImageStudio } from '../state/useImageStudio'
import { useStudioFeedback } from '../state/useStudioFeedback'
import type { GenerationTask } from '../types'
import { problemMessage } from '../utils/studioErrors'
import { studioTime } from '../utils/studioPresentation'

const { t } = useI18n()
const dialog = useDialog()
const beginAction = useStudioFeedback()
const studio = useImageStudio()
const {
  ownerId, tasks, batchIds, activeCount, connection, modelsLoaded, localLoading,
  loadProblems, historyProblem, historyLoading, nextCursor, deleting, previewBlurred, previewBlurSaving,
} = studio
const connectionVisible = ref(false)
const parametersExpanded = ref(true)
const parameterPanel = ref<HTMLElement | null>(null)
const selectedId = ref<string | null>(null)
const selectedTask = computed(() => tasks.value.find((task) => task.id === selectedId.value) ?? null)
const detailVisible = computed({
  get: () => selectedTask.value !== null,
  set: (value: boolean) => { if (!value) selectedId.value = null },
})
const batches = computed(() => {
  const groups = new Map<string, GenerationTask[]>()
  for (const task of tasks.value) {
    const group = groups.get(task.batchId) ?? []
    group.push(task)
    groups.set(task.batchId, group)
  }
  return batchIds.value.flatMap((id) => {
    const items = groups.get(id)
    const first = items?.[0]
    if (!items || !first) return []
    const active = items.filter(isTaskActive).length
    return [{ id, createdAt: first.createdAt, total: first.batchSize, active, done: first.batchSize - active, failed: items.filter((task) => task.status === 'failed').length }]
  })
})

watch(ownerId, () => {
  connectionVisible.value = false
  selectedId.value = null
  parametersExpanded.value = true
}, { flush: 'sync' })

async function reuse(id: string): Promise<void> {
  if (!studio.reuseTask(id)) return
  const action = beginAction()
  detailVisible.value = false
  parametersExpanded.value = true
  await nextTick()
  if (action.current()) {
    const scrollArea = parameterPanel.value?.querySelector<HTMLElement>('[id="studio-parameter-fields"]')
    if (scrollArea) scrollArea.scrollTop = 0
  }
  action.success('已回填设置和具体种子', 'Settings and the concrete seed have been restored')
}

async function copySeed(seed: string): Promise<void> {
  const action = beginAction()
  try {
    await copyToClipboard(seed)
    action.success('种子已复制', 'Seed copied')
  } catch (error) { action.error(error) }
}

async function download(id: string): Promise<void> {
  const task = tasks.value.find((item) => item.id === id)
  if (!task?.image) return
  const action = beginAction()
  try {
    const blob = await studio.getImage(id)
    if (!action.current()) return
    const extension = ({ 'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp', 'image/gif': 'gif', 'image/avif': 'avif' } as Record<string, string>)[task.image.mimeType] ?? 'img'
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `image-${task.request?.submittedSeed ?? task.id}.${extension}`
    document.body.appendChild(link)
    try { link.click() } finally {
      link.remove()
      // Let the browser consume the click before releasing the download URL.
      setTimeout(() => URL.revokeObjectURL(url), 0)
    }
  } catch (error) { action.error(error) }
}

function retry(id: string): void {
  const action = beginAction()
  try { studio.retry(id) } catch (error) { action.error(error) }
}

async function retrySave(id: string): Promise<void> {
  const action = beginAction()
  try {
    if (await studio.retrySave(id)) action.success('记录和原图已保存', 'Record and original image saved')
  } catch (error) { action.error(error) }
}

function confirmDelete(id: string): void {
  const task = tasks.value.find((item) => item.id === id)
  if (!task || isTaskActive(task)) return
  const action = beginAction()
  dialog.warning({
    title: t('删除本地记录', 'Delete local record'),
    content: t('删除此浏览器中当前账号的这条记录和原图？此操作无法撤销。', 'Delete this record and its original image for this account in this browser? This cannot be undone.'),
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      if (!action.current()) return
      try {
        if (!await studio.deleteTasks([id])) return false
        if (action.current() && selectedId.value === id) selectedId.value = null
        action.success('本地记录已删除', 'Local record deleted')
      } catch (error) { action.error(error); return false }
    },
  })
}

async function refreshHistory(): Promise<void> {
  const action = beginAction()
  try { await studio.refreshHistory() } catch (error) { action.error(error) }
}

async function updatePreviewBlurred(enabled: boolean): Promise<void> {
  const action = beginAction()
  try { await studio.setPreviewBlurred(enabled) } catch (error) { action.error(error) }
}
</script>

<template>
  <div class="page image-studio">
    <header class="page-header">
      <div><h1 class="page-title">{{ t('生图广场', 'Image Studio') }}</h1><p class="page-subtitle">{{ t('编写提示词、调整参数，让灵感成为画面。', 'Shape your ideas with prompts, precise settings and a personal gallery.') }}</p></div>
      <div class="studio-header-actions">
        <NTag :type="activeCount ? 'info' : 'default'" :bordered="false" aria-live="polite">{{ t(`${activeCount} 张生成中`, `${activeCount} generating`) }}</NTag>
        <NButton secondary @click="connectionVisible = true"><template #icon><NIcon :component="Settings2" /></template>{{ t('上游连接', 'Connection') }}</NButton>
      </div>
    </header>
    <div class="studio-context">
      <span class="connection-status"><span class="status-dot" :class="{ configured: modelsLoaded }" />{{ modelsLoaded ? t('已读取当前上游模型', 'Current upstream models loaded') : connection.apiKey ? t('连接已填写，可拉取模型', 'Connection entered; ready to fetch models') : t('请先配置上游连接', 'Configure an upstream connection to begin') }}</span>
      <span>{{ t('配置、预设与原图保存在此浏览器的当前账号中', 'Settings, presets and originals stay in this browser for this account') }}</span>
    </div>
    <section v-if="loadProblems.length" class="studio-load-problems" tabindex="0" :aria-label="t('本地数据读取错误', 'Local data loading errors')">
      <NAlert v-for="(problem, index) in loadProblems" :key="index" type="warning" :bordered="false">{{ problemMessage(problem) }}</NAlert>
    </section>

    <div class="studio-layout" :class="{ 'parameters-expanded': parametersExpanded }">
      <aside ref="parameterPanel" class="studio-controls panel">
        <NButton class="mobile-settings-toggle" text :aria-expanded="parametersExpanded" aria-controls="studio-parameter-fields" @click="parametersExpanded = !parametersExpanded">
          <template #icon><NIcon :component="parametersExpanded ? Images : SlidersHorizontal" /></template>{{ parametersExpanded ? t('收起设置，查看图库', 'Hide settings, view gallery') : t('提示词与生成设置', 'Prompts and settings') }}
        </NButton>
        <div class="studio-parameter-panel">
          <StudioParameters :collapsed="!parametersExpanded" @connection="connectionVisible = true" @submitted="parametersExpanded = false" />
        </div>
      </aside>

      <main class="studio-results" tabindex="0" :aria-label="t('生成记录与图库', 'Generation records and gallery')">
        <section v-if="batches.length" class="batch-panel panel">
          <div class="section-heading"><h2>{{ t('本次批次', 'Session batches') }}</h2><span>{{ t('可随时提交新批次', 'Submit another batch at any time') }}</span></div>
          <div class="batch-list">
            <div v-for="batch in batches" :key="batch.id" class="batch-row">
              <div class="batch-summary"><time :datetime="new Date(batch.createdAt).toISOString()">{{ studioTime(batch.createdAt) }}</time><p>{{ t(`已结束 ${batch.done} / ${batch.total}`, `${batch.done} / ${batch.total} finished`) }}<span v-if="batch.failed"> · {{ t(`${batch.failed} 失败`, `${batch.failed} failed`) }}</span></p></div>
              <NTag size="small" :type="batch.active ? 'info' : 'default'" :bordered="false">{{ batch.active ? t(`${batch.active} 生成中`, `${batch.active} generating`) : t('已结束', 'Finished') }}</NTag>
              <NButton v-if="batch.active" size="small" secondary type="warning" @click="studio.cancelBatch(batch.id)">{{ t('取消本批', 'Cancel batch') }}</NButton>
            </div>
          </div>
          <p class="cancellation-note">{{ t('取消或 120 秒超时仅结束本地等待；上游仍可能生成或计费。', 'Cancel or the 120-second timeout ends local waiting; the upstream may still generate or charge.') }}</p>
        </section>

        <section class="studio-gallery" :aria-label="t('我的图库', 'My gallery')">
          <div class="gallery-heading">
            <div><h2>{{ t('我的图库', 'My gallery') }}</h2><span>{{ t(`已载入 ${tasks.length} 条记录`, `${tasks.length} records loaded`) }}</span></div>
            <div class="gallery-tools">
              <div class="preview-blur-control"><span id="studio-preview-blur-label">{{ t('预览模糊', 'Blur previews') }}</span><NSwitch :value="previewBlurred" :disabled="localLoading || previewBlurSaving || ownerId === null" aria-labelledby="studio-preview-blur-label" :aria-label="t('预览模糊', 'Blur previews')" @update:value="updatePreviewBlurred" /></div>
              <NButton size="small" :loading="historyLoading || localLoading" :disabled="historyLoading || localLoading || deleting" @click="refreshHistory"><template #icon><NIcon :component="RefreshCw" /></template>{{ t('刷新图库', 'Refresh gallery') }}</NButton>
            </div>
          </div>
          <NAlert v-if="historyProblem" class="history-problem" type="error" :bordered="false">{{ problemMessage(historyProblem) }}</NAlert>
          <div v-if="localLoading && !tasks.length" class="empty-gallery panel"><NSpin size="small" /><p>{{ t('读取本地图库…', 'Loading the local gallery…') }}</p></div>
          <div v-else-if="!tasks.length" class="empty-gallery panel"><NEmpty :description="t('第一张画，从这里开始', 'Your first image starts here')"><template #icon><NIcon :component="Images" :size="44" /></template><template #extra><p>{{ t('设置上游，输入提示词，再点击开始生成。', 'Set your connection, write a prompt, then generate.') }}</p></template></NEmpty></div>
          <div v-else class="gallery-grid">
            <StudioTaskCard v-for="task in tasks" :key="`${task.ownerId}:${task.id}`" :task="task" :deleting="deleting" :preview-blurred="previewBlurred" @details="selectedId = $event" @reuse="reuse" @download="download" @copy-seed="copySeed" @cancel="studio.cancelTask" @retry="retry" @retry-save="retrySave" @delete="confirmDelete" />
          </div>
          <NButton v-if="nextCursor" class="load-more" :loading="historyLoading" :disabled="historyLoading || deleting" @click="studio.loadMoreHistory">{{ t('加载更多记录', 'Load more records') }}</NButton>
        </section>
      </main>
    </div>
    <StudioConnectionDrawer v-model:show="connectionVisible" />
    <StudioTaskDetail v-model:show="detailVisible" :task="selectedTask" :deleting="deleting" @reuse="reuse" @download="download" @copy-seed="copySeed" @retry-save="retrySave" @delete="confirmDelete" />
  </div>
</template>

<style scoped>
.image-studio { display: flex; flex-direction: column; flex: 1; min-height: 0; gap: 16px; overflow: hidden; }
.image-studio > :not(.studio-layout) { flex-shrink: 0; }
.studio-load-problems { display: grid; align-content: start; gap: 8px; min-height: 0; max-height: 20%; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; }
.studio-header-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.studio-context { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px 20px; color: var(--cpa-text-muted); font-size: 12px; }
.connection-status { display: inline-flex; align-items: center; gap: 7px; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--cpa-text-muted); }
.status-dot.configured { background: var(--cpa-success); }
.studio-layout { display: grid; flex: 1; min-height: 0; grid-template-columns: minmax(320px, 390px) minmax(0, 1fr); grid-template-rows: minmax(0, 1fr); gap: 18px; }
.studio-controls, .studio-parameter-panel { display: flex; flex-direction: column; min-height: 0; }
.studio-parameter-panel { flex: 1; }
.studio-results, .studio-gallery { display: grid; align-content: start; gap: 16px; min-width: 0; }
.studio-results { grid-auto-rows: max-content; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; scrollbar-gutter: stable; padding-right: 4px; }
.section-heading, .gallery-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.section-heading h2, .gallery-heading h2 { margin: 0; font-size: 16px; color: var(--cpa-text-strong); }
.section-heading span, .gallery-heading span { color: var(--cpa-text-muted); font-size: 12px; }
.gallery-heading > div { display: grid; gap: 3px; }
.gallery-heading .gallery-tools { display: flex; align-items: center; justify-content: flex-end; gap: 12px; }
.preview-blur-control { display: inline-flex; align-items: center; gap: 8px; color: var(--cpa-text-muted); font-size: 12px; white-space: nowrap; }
.batch-panel { padding: 14px; }
.batch-list { display: grid; max-height: 240px; overflow-y: auto; margin-top: 10px; }
.batch-row { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-top: 1px solid var(--cpa-border); }
.batch-summary { flex: 1; min-width: 0; }
.batch-row time { font-size: 12px; font-weight: 600; }
.batch-row p { margin: 3px 0 0; font-size: 12px; color: var(--cpa-text-muted); }
.cancellation-note { margin: 8px 0 0; font-size: 11px; color: var(--cpa-text-muted); }
.gallery-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; align-items: start; }
.empty-gallery { display: grid; place-content: center; min-height: 410px; padding: 36px 20px; text-align: center; background: var(--cpa-surface-muted); }
.empty-gallery p { max-width: 290px; margin: 10px auto 0; font-size: 13px; color: var(--cpa-text-muted); }
.load-more { justify-self: center; }
.mobile-settings-toggle { display: none; }
@media (min-width: 1500px) { .studio-layout { grid-template-columns: 410px minmax(0, 1fr); } }
@media (max-width: 1180px) { .studio-layout { grid-template-columns: minmax(310px, 350px) minmax(0, 1fr); gap: 14px; } }
@media (max-width: 980px) {
  .studio-layout { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); gap: 12px; }
  .studio-layout.parameters-expanded { grid-template-rows: minmax(0, 1fr); }
  .parameters-expanded .studio-results { display: none; }
  .mobile-settings-toggle { display: flex; flex-shrink: 0; width: 100%; height: auto; min-height: 40px; padding: 10px 16px; justify-content: flex-start; }
}
@media (max-width: 600px) {
  .page-header { align-items: flex-start; flex-direction: column; gap: 12px; }
  .studio-header-actions { width: 100%; justify-content: space-between; }
  .gallery-grid { grid-template-columns: minmax(0, 1fr); }
  .section-heading, .gallery-heading { align-items: flex-start; flex-wrap: wrap; }
  .gallery-heading .gallery-tools { width: 100%; justify-content: space-between; }
  .batch-row { flex-wrap: wrap; }
  .empty-gallery { min-height: 260px; }
}
@media (max-height: 650px) {
  .image-studio { gap: 10px; }
  .studio-load-problems { max-height: 10%; }
  .page-subtitle, .studio-context > span:last-child { display: none; }
  .page-header { flex-direction: row; align-items: center; gap: 8px; }
  .page-title { font-size: 22px; }
  .studio-header-actions { width: auto; gap: 6px; }
  .studio-header-actions .n-tag { display: none; }
}
</style>
