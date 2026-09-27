<script setup lang="ts">
import { computed, h, nextTick, ref, watch, type FunctionDirective } from 'vue'
import {
  NAlert, NButton, NCollapse, NCollapseItem, NForm, NFormItem, NIcon, NInput, NInputNumber,
  NSelect, NSlider, NSwitch, NTag, NTooltip, type SelectGroupOption, type SelectOption,
} from 'naive-ui'
import { ArrowDown, ArrowUp, ChevronDown, ChevronUp, Plus, RefreshCw, Save, Sparkles, Trash2 } from 'lucide-vue-next'

import { useI18n } from '@/shared/i18n'

import { upstreamApiUrl } from '../api/imageStudioApi'
import { useImageStudio } from '../state/useImageStudio'
import { useStudioFeedback } from '../state/useStudioFeedback'
import type { CharacterPrompt, StudioProblem } from '../types'
import { assemblePrompt, createStudioId, imageResolutions, normalizePositionHint, novelAISamplers, validateDraft } from '../utils/generationParameters'
import { problemMessage, toStudioProblem } from '../utils/studioErrors'
import { saveStatusText } from '../utils/studioPresentation'
import StudioPromptField from './StudioPromptField.vue'
import StudioTagMarket from './StudioTagMarket.vue'

defineProps<{ collapsed?: boolean }>()
const emit = defineEmits<{ submitted: [batchId: string]; connection: [] }>()
const { t } = useI18n()
const beginAction = useStudioFeedback()
const studio = useImageStudio()
const {
  ownerId, draft, connection, draftSave, localLoading, visibleModels, otherModels, otherModelsExpanded,
  modelsLoading, modelsLoaded, modelProblem, models,
} = studio
const submitProblem = ref<StudioProblem | null>(null)
const tagMarketVisible = ref(false)
const pickerVersion = ref(0)
const modelOptions = computed(() => visibleModels.value.map((model) => ({ label: model, value: model })))
const samplerOptions = novelAISamplers.map((sampler) => ({ label: sampler, value: sampler }))
const selectedCharacterId = ref<string | null>(null)
const positionRows = computed(() => [
  { label: t('上', 'Top'), hint: 'at the top' },
  { label: t('偏上', 'Upper'), hint: 'slightly above center' },
  { label: t('居中', 'Center'), hint: '' },
  { label: t('偏下', 'Lower'), hint: 'slightly below center' },
  { label: t('下', 'Bottom'), hint: 'at the bottom' },
])
const positionColumns = computed(() => [
  { label: t('左', 'Left'), hint: 'on the left' },
  { label: t('偏左', 'Left of center'), hint: 'slightly left of center' },
  { label: t('居中', 'Center'), hint: '' },
  { label: t('偏右', 'Right of center'), hint: 'slightly right of center' },
  { label: t('右', 'Right'), hint: 'on the right' },
])
function gridPosition(row: number, column: number): string {
  return [positionRows.value[row]?.hint, positionColumns.value[column]?.hint].filter(Boolean).join(', ') || 'in the center'
}
function positionCoordinates(hint: string): { row: number; column: number } | null {
  if (!hint) return null
  for (let row = 0; row < 5; row++) {
    for (let column = 0; column < 5; column++) {
      if (gridPosition(row, column) === hint) return { row, column }
    }
  }
  return null
}
const characterIds = new WeakMap<CharacterPrompt, string>()
function characterId(character: CharacterPrompt): string {
  let id = characterIds.get(character)
  if (!id) { id = `studio-character-${createStudioId()}`; characterIds.set(character, id) }
  return id
}
const activeCharacter = computed(() => draft.value.characters.find((character) => characterId(character) === selectedCharacterId.value) ?? draft.value.characters[0])
const activeCharacterNumber = computed(() => draft.value.characters.findIndex((character) => character === activeCharacter.value) + 1)
function selectCharacter(character: CharacterPrompt): void {
  selectedCharacterId.value = characterId(character)
}
function charactersAt(row: number, column: number): { character: CharacterPrompt; index: number }[] {
  return draft.value.characters.flatMap((character, index) => {
    const position = positionCoordinates(normalizePositionHint(character.positionHint)) ?? { row: 2, column: 2 }
    return position.row === row && position.column === column ? [{ character, index }] : []
  })
}
function assignPosition(character: CharacterPrompt | undefined, row: number, column: number): void {
  if (!character) return
  selectCharacter(character)
  character.positionHint = gridPosition(row, column)
}
function setPositionAxis(character: CharacterPrompt, axis: 'row' | 'column', value: number | null): void {
  if (value === null) { character.positionHint = ''; return }
  const position = positionCoordinates(character.positionHint) ?? { row: 2, column: 2 }
  assignPosition(character, axis === 'row' ? value : position.row, axis === 'column' ? value : position.column)
}
function startCharacterDrag(event: DragEvent, character: CharacterPrompt): void {
  selectCharacter(character)
  event.dataTransfer?.setData('text/plain', characterId(character))
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}
function dropCharacter(event: DragEvent, row: number, column: number): void {
  const id = event.dataTransfer?.getData('text/plain')
  const character = draft.value.characters.find((item) => characterId(item) === id)
  assignPosition(character, row, column)
}
async function moveCharacter(index: number, offset: number, event: MouseEvent): Promise<void> {
  const characters = draft.value.characters
  const next = index + offset
  if (next < 0 || next >= characters.length) return
  const button = event.currentTarget as HTMLElement | null
  const row = button?.closest<HTMLElement>('.character-row')
  const scroller = button?.closest<HTMLElement>('.parameter-scroll')
  const scrollTop = scroller?.scrollTop
  button?.blur()
  const [character] = characters.splice(index, 1)
  if (character) characters.splice(next, 0, character)
  await nextTick()
  row?.focus({ preventScroll: true })
  if (scroller && scrollTop !== undefined) scroller.scrollTop = scrollTop
}
const resolutionOptions = computed(() => imageResolutions.map(({ width, height }) => ({
  width,
  height,
  label: `${width} x ${height} (${width === height ? '1:1' : width > height ? t('横版', 'Landscape') : t('竖版', 'Portrait')})`,
})))
// NSlider forwards attributes to its root, so also label the focusable handle.
const vSliderLabel: FunctionDirective<HTMLElement, string> = (element, { value }) => {
  element.querySelector('[role="slider"]')?.setAttribute('aria-label', value)
}
const cfgSliderMin = ref(0)
const cfgSliderMax = ref(20)
const stepsSliderMax = ref(50)
const cfgSliderValue = computed(() => draft.value.cfg !== null && Number.isFinite(draft.value.cfg) ? draft.value.cfg : undefined)
const stepsSliderValue = computed(() => draft.value.steps !== null && Number.isSafeInteger(draft.value.steps) && draft.value.steps > 0 ? draft.value.steps : undefined)

watch([ownerId, cfgSliderValue, stepsSliderValue], ([owner, cfg, steps], [previousOwner]) => {
  if (owner !== previousOwner) {
    cfgSliderMin.value = 0
    cfgSliderMax.value = 20
    stepsSliderMax.value = 50
  }
  // Expand for manual/saved values, but never shrink the rail while the user is dragging.
  if (cfg !== undefined) {
    cfgSliderMin.value = Math.min(cfgSliderMin.value, Math.floor(cfg))
    cfgSliderMax.value = Math.max(cfgSliderMax.value, Math.ceil(cfg))
  }
  if (steps !== undefined) stepsSliderMax.value = Math.max(stepsSliderMax.value, steps)
}, { immediate: true })

const preview = computed(() => {
  try {
    const input = validateDraft(draft.value)
    return {
      endpoint: upstreamApiUrl(connection.value.baseUrl, 'chat/completions'),
      text: JSON.stringify({
        model: input.model,
        messages: [{ role: 'user', content: assemblePrompt(input, input.seed || t('[提交时逐张随机]', '[randomized per image on submit]')) }],
        stream: false,
      }, null, 2),
      problem: null,
    }
  } catch (error) {
    return { endpoint: '', text: '', problem: toStudioProblem(error, connection.value.apiKey) }
  }
})

watch(draft, () => { submitProblem.value = null }, { deep: true })
watch(connection, () => {
  submitProblem.value = null
  // NSelect keeps manually-created options internally; remount when the connection changes.
  pickerVersion.value++
}, { deep: true, flush: 'sync' })
watch(ownerId, () => {
  submitProblem.value = null
  tagMarketVisible.value = false
  selectedCharacterId.value = null
}, { flush: 'sync' })

function renderModelLabel(option: SelectOption | SelectGroupOption) {
  const text = String(option.label ?? option.value ?? '')
  return h('span', { title: text }, text)
}

function submit(): void {
  submitProblem.value = null
  try { emit('submitted', studio.submit()) } catch (error) {
    submitProblem.value = toStudioProblem(error, connection.value.apiKey)
  }
}

async function saveSettings(): Promise<void> {
  const action = beginAction()
  try {
    if (await studio.saveDraft()) action.success('生成设置已保存', 'Generation settings saved')
  } catch (error) { action.error(error) }
}
</script>

<template>
  <NForm class="studio-parameters" label-placement="top" @submit.prevent="submit">
    <div class="parameter-content" :class="{ 'mobile-collapsed': collapsed }">
      <div class="parameter-heading">
        <h2>{{ t('生成设置', 'Generation settings') }}</h2>
        <NButton size="small" quaternary @click="emit('connection')">{{ t('上游连接', 'Connection') }}</NButton>
      </div>

      <div id="studio-parameter-fields" class="parameter-scroll" tabindex="0" :aria-label="t('生成设置内容', 'Generation settings fields')">
        <section class="model-section" :aria-label="t('模型选择', 'Model selection')">
          <div class="model-toolbar">
            <span class="field-heading">{{ t('模型', 'Models') }}</span>
            <NButton size="small" secondary :loading="modelsLoading" :disabled="modelsLoading || localLoading" @click="studio.loadModels">
              <template #icon><NIcon :component="RefreshCw" /></template>
              {{ t('拉取模型', 'Fetch models') }}
            </NButton>
          </div>
          <NFormItem :label="t('请求模型', 'Request model')" :label-props="{ for: 'studio-request-model' }" :show-feedback="false">
            <NSelect :key="`request-${pickerVersion}`" :value="draft.model || null" :options="modelOptions" filterable tag clearable :render-label="renderModelLabel" :placeholder="t('选择或输入模型名，按 Enter 确认', 'Select or type a model, then Enter')" :input-props="{ id: 'studio-request-model', 'aria-label': t('请求模型', 'Request model') }" @update:value="draft.model = $event ?? ''" />
          </NFormItem>
          <NFormItem :label="t('生成模型', 'Generation model')" :label-props="{ for: 'studio-generation-model' }" :show-feedback="false">
            <NSelect :key="`generation-${pickerVersion}`" :value="draft.generationModel || null" :options="modelOptions" filterable tag clearable :render-label="renderModelLabel" :placeholder="t('留空沿用请求模型', 'Empty: follow request model')" :input-props="{ id: 'studio-generation-model', 'aria-label': t('生成模型', 'Generation model') }" @update:value="draft.generationModel = $event ?? ''" />
          </NFormItem>
          <p class="field-note">{{ t('请求模型决定上游路由，生成模型写入 Parameter；两者可以不同。', 'The request model selects the upstream route; the generation model goes in Parameter. They can differ.') }}</p>
          <p v-if="!draft.generationModel" class="model-follow">{{ t('当前生成模型：', 'Current generation model: ') }}<code>{{ draft.model || '—' }}</code></p>
          <NAlert v-if="modelProblem" type="error" :bordered="false">{{ problemMessage(modelProblem) }}</NAlert>
          <template v-else-if="modelsLoaded">
            <p class="field-note">{{ t(`已读取 ${models.length} 个模型。默认仅显示名称含 nai 的模型；可手动输入其他名称。`, `Loaded ${models.length} models. Names containing nai are shown by default; you can enter other names manually.`) }}</p>
            <NButton v-if="otherModels.length" class="other-models-toggle" size="small" text :aria-expanded="otherModelsExpanded" @click="otherModelsExpanded = !otherModelsExpanded">
              <template #icon><NIcon :component="otherModelsExpanded ? ChevronUp : ChevronDown" /></template>
              {{ otherModelsExpanded ? t('收起其他模型', 'Hide other models') : t(`展开其他模型（${otherModels.length}）`, `Show other models (${otherModels.length})`) }}
            </NButton>
          </template>
          <p v-else class="field-note">{{ t('从当前连接拉取候选，或直接输入完整名称。模型可见不代表支持全部生成参数。', 'Fetch candidates from the current connection, or enter a full name. A listed model may not support all generation parameters.') }}</p>
        </section>

        <div class="positive-prompt-field">
          <div class="positive-prompt-heading">
            <label class="prompt-label" for="studio-positive-prompt">{{ t('正向提示词', 'Positive prompt') }}</label>
            <NButton size="small" secondary :disabled="ownerId === null || localLoading" @click="tagMarketVisible = true">{{ t('标签市场', 'Tag market') }}</NButton>
          </div>
          <NInput v-model:value="draft.positivePrompt" type="textarea" :autosize="{ minRows: 4, maxRows: 12 }" :placeholder="t('描述画面主体、构图、光线与细节…', 'Describe the subject, composition, lighting and details…')" :input-props="{ id: 'studio-positive-prompt' }" />
        </div>
        <div class="character-mode">
          <label for="studio-character-mode" class="field-heading">{{ t('独立角色提示', 'Independent character prompts') }}</label>
          <NSwitch id="studio-character-mode" v-model:value="draft.characterPromptsEnabled" :disabled="localLoading" :aria-label="t('独立角色提示', 'Independent character prompts')" />
        </div>
        <section v-if="draft.characterPromptsEnabled" class="character-editor" :aria-label="t('角色提示', 'Character prompts')">
          <div class="shared-position">
            <div class="position-board-heading">
              <span class="field-heading">{{ t('角色位置', 'Character positions') }}</span>
              <span class="field-note">{{ t(`已选角色 ${activeCharacterNumber}`, `Selected character ${activeCharacterNumber}`) }}</span>
            </div>
            <div class="position-grid" role="group" :aria-label="t('共享角色位置棋盘', 'Shared character position board')">
              <span aria-hidden="true" />
              <span v-for="(column, columnIndex) in positionColumns" :key="columnIndex" class="position-axis">{{ column.label }}</span>
              <template v-for="(row, rowIndex) in positionRows" :key="rowIndex">
                <span class="position-axis">{{ row.label }}</span>
                <div v-for="(column, columnIndex) in positionColumns" :key="columnIndex" class="position-cell" :class="{ 'position-cell-crowded': charactersAt(rowIndex, columnIndex).length > 2 }" @dragover.prevent @drop.prevent="dropCharacter($event, rowIndex, columnIndex)">
                  <button type="button" class="position-cell-target" :aria-label="t(`${row.label}、${column.label}，放置已选角色`, `${row.label}, ${column.label}, place selected character`)" @click="assignPosition(activeCharacter, rowIndex, columnIndex)" />
                  <button v-for="item in charactersAt(rowIndex, columnIndex)" :key="characterId(item.character)" type="button" class="position-marker" :class="{ selected: activeCharacter === item.character, unassigned: !normalizePositionHint(item.character.positionHint) }" :aria-label="t(`选择角色 ${item.index + 1}`, `Select character ${item.index + 1}`)" :aria-pressed="activeCharacter === item.character" draggable="true" @click.stop="selectCharacter(item.character)" @dragstart.stop="startCharacterDrag($event, item.character)">{{ item.index + 1 }}</button>
                </div>
              </template>
            </div>
          </div>
          <div v-for="(character, index) in draft.characters" :key="characterId(character)" class="character-row" :class="{ 'character-row-selected': activeCharacter === character }" tabindex="-1" @click="selectCharacter(character)" @focusin="selectCharacter(character)">
            <div class="character-toolbar">
              <span class="field-heading">{{ t(`角色 ${index + 1}`, `Character ${index + 1}`) }}</span>
              <div class="character-actions">
                <NTooltip><template #trigger><NButton size="small" quaternary :disabled="index === 0" :aria-label="t('上移角色', 'Move character up')" @click="moveCharacter(index, -1, $event)"><template #icon><NIcon :component="ArrowUp" /></template></NButton></template>{{ t('上移角色', 'Move character up') }}</NTooltip>
                <NTooltip><template #trigger><NButton size="small" quaternary :disabled="index === draft.characters.length - 1" :aria-label="t('下移角色', 'Move character down')" @click="moveCharacter(index, 1, $event)"><template #icon><NIcon :component="ArrowDown" /></template></NButton></template>{{ t('下移角色', 'Move character down') }}</NTooltip>
                <NTooltip><template #trigger><NButton size="small" quaternary :disabled="draft.characters.length === 1" :aria-label="t('删除角色', 'Delete character')" @click="draft.characters.splice(index, 1)"><template #icon><NIcon :component="Trash2" /></template></NButton></template>{{ t('删除角色', 'Delete character') }}</NTooltip>
              </div>
            </div>
            <NFormItem :label="t('角色提示词', 'Character prompt')" :label-props="{ for: characterId(character) }" :show-feedback="false">
              <NInput v-model:value="character.prompt" type="textarea" :autosize="{ minRows: 2, maxRows: 8 }" :input-props="{ id: characterId(character) }" />
            </NFormItem>
            <div class="position-selectors">
              <NFormItem :label="t('水平位置', 'Horizontal position')" :label-props="{ for: `${characterId(character)}-horizontal` }" :show-feedback="false">
                <NSelect :value="positionCoordinates(character.positionHint)?.column ?? null" :options="positionColumns.map((column, value) => ({ label: column.label, value }))" :placeholder="t('未指定', 'Unspecified')" :input-props="{ id: `${characterId(character)}-horizontal`, 'aria-label': t(`角色 ${index + 1} 水平位置`, `Character ${index + 1} horizontal position`) }" @update:value="setPositionAxis(character, 'column', $event)" />
              </NFormItem>
              <NFormItem :label="t('垂直位置', 'Vertical position')" :label-props="{ for: `${characterId(character)}-vertical` }" :show-feedback="false">
                <NSelect :value="positionCoordinates(character.positionHint)?.row ?? null" :options="positionRows.map((row, value) => ({ label: row.label, value }))" :placeholder="t('未指定', 'Unspecified')" :input-props="{ id: `${characterId(character)}-vertical`, 'aria-label': t(`角色 ${index + 1} 垂直位置`, `Character ${index + 1} vertical position`) }" @update:value="setPositionAxis(character, 'row', $event)" />
              </NFormItem>
            </div>
          </div>
          <NButton size="small" secondary class="add-character" @click="draft.characters.push({ prompt: '', positionHint: '' })"><template #icon><NIcon :component="Plus" /></template>{{ t('添加角色', 'Add character') }}</NButton>
        </section>
        <StudioPromptField v-model:value="draft.artistPrompt" kind="artist" :label="t('画师串', 'Artist prompt')" :placeholder="t('例如：1.15::art nouveau::，保留你的权重语法', 'For example: 1.15::art nouveau:: — weights are preserved')" />
        <p class="field-note">{{ t('画师串自动拼接到正向正文前；负面词单独发送。', 'The artist prompt is prepended to the positive prompt; negative terms stay separate.') }}</p>
        <StudioPromptField v-model:value="draft.negativePrompt" kind="negative" :label="t('负面提示词', 'Negative prompt')" placeholder="lowres, blurry, text, watermark" />

        <section class="resolution-section" :aria-label="t('分辨率', 'Resolution')">
          <span class="field-heading">{{ t('分辨率', 'Resolution') }}</span>
          <div class="resolution-options">
            <NButton v-for="size in resolutionOptions" :key="size.width" size="small" :type="draft.width === size.width && draft.height === size.height ? 'primary' : 'default'" :secondary="draft.width !== size.width || draft.height !== size.height" :aria-pressed="draft.width === size.width && draft.height === size.height" @click="draft.width = size.width; draft.height = size.height">
              {{ size.label }}
            </NButton>
          </div>
        </section>

        <div class="numeric-settings">
          <NFormItem :label="t('CFG (默认5)', 'CFG (default 5)')" :label-props="{ for: 'studio-cfg' }" :show-feedback="false">
            <div class="number-field">
              <NSlider v-slider-label="t('CFG 滑条', 'CFG slider')" :value="cfgSliderValue ?? cfgSliderMin" :min="cfgSliderMin" :max="cfgSliderMax" :step="0.1" :disabled="cfgSliderValue === undefined" :aria-label="t('CFG 滑条', 'CFG slider')" @update:value="draft.cfg = $event" />
              <NInputNumber v-model:value="draft.cfg" :step="0.1" :input-props="{ id: 'studio-cfg', 'aria-label': 'CFG' }" />
            </div>
          </NFormItem>
          <NFormItem :label="t('步数 (默认28)', 'Steps (default 28)')" :label-props="{ for: 'studio-steps' }" :show-feedback="false">
            <div class="number-field">
              <NSlider v-slider-label="t('步数滑条', 'Steps slider')" :value="stepsSliderValue ?? 1" :min="1" :max="stepsSliderMax" :step="1" :disabled="stepsSliderValue === undefined" :aria-label="t('步数滑条', 'Steps slider')" @update:value="draft.steps = $event" />
              <NInputNumber v-model:value="draft.steps" :step="1" :min="1" :input-props="{ id: 'studio-steps', 'aria-label': t('步数', 'Steps') }" />
            </div>
          </NFormItem>
        </div>
        <NFormItem :label="t('采样器', 'Sampler')" :label-props="{ for: 'studio-sampler' }" :show-feedback="false">
          <NSelect v-model:value="draft.sampler" :options="samplerOptions" filterable :input-props="{ id: 'studio-sampler', 'aria-label': t('采样器', 'Sampler') }" />
        </NFormItem>
        <NFormItem :label="t('种子', 'Seed')" :label-props="{ for: 'studio-seed' }" :show-feedback="false">
          <NInput v-model:value="draft.seed" clearable :placeholder="t('留空时逐张随机', 'Empty: random for each image')" :input-props="{ id: 'studio-seed', spellcheck: false }" />
        </NFormItem>
        <p class="field-note">{{ t('固定种子原样发送；留空时每张先生成具体种子并记录。', 'A fixed seed is sent unchanged. Empty seeds become a recorded random value for each image.') }}</p>

        <NCollapse>
          <NCollapseItem :title="t('最终请求预览', 'Final request preview')" name="request-preview">
            <p class="field-note">{{ t('Parameter 为此上游的生图扩展。随机种子的具体值在提交时确定。', 'Parameter is this upstream’s image extension. Random seeds are finalized when submitted.') }}</p>
            <template v-if="preview.text">
              <code class="request-endpoint">{{ preview.endpoint }}</code>
              <pre class="request-preview">{{ preview.text }}</pre>
            </template>
            <p v-else-if="preview.problem" class="field-note">{{ problemMessage(preview.problem) }}</p>
          </NCollapseItem>
        </NCollapse>

        <div class="save-settings">
          <NTag size="small" :bordered="false" :type="draftSave.status === 'saved' ? 'success' : 'default'">{{ saveStatusText(draftSave.status) }}</NTag>
          <NButton size="small" :loading="draftSave.status === 'saving'" :disabled="draftSave.status === 'saving' || localLoading" @click="saveSettings">
            <template #icon><NIcon :component="Save" /></template>{{ t('保存设置', 'Save settings') }}
          </NButton>
        </div>
        <NAlert v-if="draftSave.problem" type="error" :bordered="false">{{ problemMessage(draftSave.problem) }}</NAlert>
      </div>
    </div>

    <div class="generate-bar">
      <NAlert v-if="submitProblem" type="error" :bordered="false" class="submit-problem">{{ problemMessage(submitProblem) }}</NAlert>
      <label for="studio-image-count" class="field-heading">{{ t('生成张数', 'Images') }}</label>
      <div class="generate-controls">
        <NInputNumber v-model:value="draft.count" :step="1" :min="1" :input-props="{ id: 'studio-image-count', 'aria-label': t('生成张数', 'Image count') }" />
        <NButton type="primary" attr-type="submit" size="large" :disabled="ownerId === null || localLoading">
          <template #icon><NIcon :component="Sparkles" /></template>{{ t('开始生成', 'Generate') }}
        </NButton>
      </div>
    </div>
  </NForm>
  <StudioTagMarket v-model:show="tagMarketVisible" v-model:value="draft.positivePrompt" :owner-id="ownerId" />
</template>

<style scoped>
.studio-parameters, .parameter-content { display: flex; flex: 1; flex-direction: column; min-height: 0; min-width: 0; }
.parameter-heading { flex-shrink: 0; padding: 14px 18px; }
.parameter-scroll { display: grid; flex: 1; align-content: start; gap: 16px; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; padding: 0 18px 18px; }
.parameter-heading, .model-toolbar, .save-settings, .positive-prompt-heading { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.parameter-heading h2 { margin: 0; font-size: 16px; color: var(--cpa-text-strong); }
.positive-prompt-field { display: grid; gap: 8px; min-width: 0; }
.prompt-label { font-weight: 600; color: var(--cpa-text-strong); }
.character-mode, .character-toolbar, .character-actions { display: flex; align-items: center; gap: 8px; }
.character-mode, .character-toolbar { justify-content: space-between; }
.character-editor, .character-row { display: grid; gap: 10px; min-width: 0; }
.character-row { border-top: 1px solid var(--cpa-border); padding-top: 10px; }
.character-row:focus { outline: none; }
.character-row-selected { border-top-color: var(--cpa-primary); }
.character-actions { flex-shrink: 0; gap: 2px; }
.add-character { justify-self: start; }
.shared-position { display: grid; gap: 8px; min-width: 0; }
.position-board-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; }
.position-grid { display: grid; grid-template-columns: 38px repeat(5, minmax(0, 1fr)); gap: 3px; align-items: stretch; width: min(100%, 280px); }
.position-axis { display: grid; place-items: center; text-align: center; font-size: 10px; line-height: 1.2; color: var(--cpa-text-muted); overflow-wrap: anywhere; }
.position-cell { position: relative; display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 2px; width: 100%; min-height: 44px; padding: 2px; border: 1px solid var(--cpa-border); background: var(--cpa-surface-muted); }
.position-cell-crowded { min-height: 64px; }
.position-cell:hover, .position-cell:focus-within { border-color: var(--cpa-primary); }
.position-cell-target { position: absolute; inset: 0; width: 100%; border: 0; background: transparent; cursor: pointer; }
.position-cell-target:focus-visible { outline: 2px solid var(--cpa-primary); outline-offset: -2px; }
.position-marker { position: relative; z-index: 1; display: grid; place-items: center; width: 18px; height: 18px; flex: 0 0 18px; padding: 0; border: 1px solid var(--cpa-primary); border-radius: 50%; color: var(--cpa-primary); background: var(--cpa-surface); font: inherit; font-size: 10px; font-weight: 700; cursor: grab; }
.position-marker.selected { color: var(--cpa-surface); background: var(--cpa-primary); }
.position-marker.unassigned { border-style: dashed; }
.position-marker:focus-visible { outline: 2px solid var(--cpa-primary); outline-offset: 2px; }
.position-selectors { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; min-width: 0; }
.field-heading { font-size: 13px; font-weight: 650; color: var(--cpa-text-strong); }
.field-note { margin: 0; color: var(--cpa-text-muted); font-size: 12px; line-height: 1.6; }
.model-section { display: grid; gap: 10px; padding: 12px; border: 1px solid var(--cpa-border); border-radius: var(--cpa-radius-sm); background: var(--cpa-surface-muted); }
.model-follow { margin: 0; font-size: 12px; overflow-wrap: anywhere; }
.other-models-toggle { justify-self: start; }
.resolution-section, .number-field { display: grid; gap: 8px; min-width: 0; width: 100%; }
.resolution-options { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; }
.resolution-options .n-button { width: 100%; padding-inline: 8px; font-size: 12px; }
.numeric-settings { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.request-endpoint { display: block; margin-top: 10px; overflow-wrap: anywhere; font-size: 11px; }
.request-preview { white-space: pre-wrap; overflow-wrap: anywhere; margin: 10px 0 0; padding: 10px; max-height: 360px; overflow: auto; border-radius: var(--cpa-radius-sm); background: var(--cpa-surface-muted); font-size: 11px; line-height: 1.6; }
.generate-bar { display: grid; flex-shrink: 0; gap: 8px; padding: 14px 18px; border-top: 1px solid var(--cpa-border); background: var(--cpa-surface); }
.generate-controls { display: grid; grid-template-columns: minmax(90px, 0.8fr) minmax(0, 1.2fr); gap: 10px; align-items: center; }
.submit-problem { margin-bottom: 4px; max-height: 96px; overflow-y: auto; }
@media (max-width: 980px) {
  .parameter-content.mobile-collapsed { display: none; }
}
@media (max-height: 650px) {
  .parameter-heading { padding-block: 8px; }
  .generate-bar { gap: 4px; padding-block: 8px; }
}
</style>
