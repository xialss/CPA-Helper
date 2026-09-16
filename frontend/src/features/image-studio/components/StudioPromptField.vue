<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NForm, NFormItem, NInput, NModal, NSelect, useDialog, type SelectGroupOption } from 'naive-ui'

import { useI18n } from '@/shared/i18n'

import { useImageStudio } from '../state/useImageStudio'
import { useStudioFeedback } from '../state/useStudioFeedback'
import type { PresetKind } from '../types'
import { builtinNegativePresets } from '../utils/negativePresets'

const props = defineProps<{ value: string; kind: PresetKind; label: string; placeholder: string }>()
const emit = defineEmits<{ 'update:value': [value: string] }>()
const { t } = useI18n()
const dialog = useDialog()
const beginAction = useStudioFeedback()
const { ownerId, presets, presetsSaving, localLoading, savePreset, deletePreset } = useImageStudio()
const selectedId = ref<string | null>(null)
const editorVisible = ref(false)
const editingId = ref<string | null>(null)
const name = ref('')
const text = ref('')
const fieldId = `studio-${props.kind}-prompt`
const ownPresets = computed(() => presets.value.filter((preset) => preset.kind === props.kind))
const options = computed(() => {
  const personal = ownPresets.value.map((preset) => ({ label: preset.name, value: preset.id }))
  if (props.kind !== 'negative') return personal
  const groups: SelectGroupOption[] = [{
    type: 'group',
    key: 'builtin-negative',
    label: t('内置预设', 'Built-in presets'),
    children: builtinNegativePresets.map((preset) => ({ label: preset.name, value: preset.id })),
  }]
  if (personal.length) groups.push({ type: 'group', key: 'personal-negative', label: t('个人预设', 'Personal presets'), children: personal })
  return groups
})
const selected = computed(() => ownPresets.value.find((preset) => preset.id === selectedId.value))

watch(ownerId, () => {
  selectedId.value = null
  editorVisible.value = false
  editingId.value = null
  name.value = ''
  text.value = ''
}, { flush: 'sync' })

function selectPreset(id: string | null): void {
  selectedId.value = id
  const preset = (props.kind === 'negative' ? builtinNegativePresets.find((item) => item.id === id) : undefined)
    ?? ownPresets.value.find((item) => item.id === id)
  if (preset) emit('update:value', preset.text)
}

function openEditor(edit: boolean): void {
  if (edit && !selected.value) return
  editingId.value = edit && selected.value ? selected.value.id : null
  name.value = edit && selected.value ? selected.value.name : ''
  text.value = props.value
  editorVisible.value = true
}

async function save(): Promise<void> {
  const action = beginAction()
  try {
    const preset = await savePreset(props.kind, name.value, text.value, editingId.value)
    if (!preset || !action.current()) return
    selectedId.value = preset.id
    emit('update:value', preset.text)
    editorVisible.value = false
    action.success('预设已保存', 'Preset saved')
  } catch (error) { action.error(error) }
}

function confirmDelete(): void {
  const id = editingId.value
  if (!id) return
  const action = beginAction()
  dialog.warning({
    title: t('删除个人预设', 'Delete personal preset'),
    content: t('删除此浏览器中当前账号的这个预设？已生成记录会保留原始提示词。', 'Delete this preset for the current account in this browser? Generation records retain their original prompts.'),
    positiveText: t('删除', 'Delete'),
    negativeText: t('取消', 'Cancel'),
    onPositiveClick: async () => {
      if (!action.current()) return
      try {
        if (!await deletePreset(id) || !action.current()) return false
        selectedId.value = null
        editorVisible.value = false
        action.success('预设已删除', 'Preset deleted')
      } catch (error) { action.error(error); return false }
    },
  })
}
</script>

<template>
  <div class="prompt-field">
    <label class="prompt-label" :for="fieldId">{{ label }}</label>
    <NInput :value="value" type="textarea" :autosize="{ minRows: 2, maxRows: 6 }" :placeholder="placeholder" :input-props="{ id: fieldId }" @update:value="emit('update:value', $event)" />
    <div class="preset-controls">
      <NSelect :value="selectedId" :options="options" clearable filterable size="small" :placeholder="kind === 'negative' ? t('选择预设', 'Select a preset') : t('个人预设', 'Personal presets')" :input-props="{ 'aria-label': `${label} ${t('预设', 'presets')}` }" @update:value="selectPreset" />
      <NButton size="small" secondary :disabled="!value.trim() || presetsSaving || localLoading" @click="openEditor(false)">{{ t('另存预设', 'Save preset') }}</NButton>
      <NButton size="small" :disabled="!selected || presetsSaving || localLoading" @click="openEditor(true)">{{ t('编辑', 'Edit') }}</NButton>
    </div>
    <NModal v-model:show="editorVisible" preset="card" :title="editingId ? t('编辑预设', 'Edit preset') : t('保存为个人预设', 'Save personal preset')" :style="{ width: 'min(540px, calc(100vw - 32px))' }" :mask-closable="!presetsSaving" :closable="!presetsSaving" :close-on-esc="!presetsSaving">
      <NForm label-placement="top" :disabled="presetsSaving">
        <NFormItem :label="t('预设名称', 'Preset name')" :label-props="{ for: `studio-${kind}-preset-name` }">
          <NInput v-model:value="name" :input-props="{ id: `studio-${kind}-preset-name` }" :placeholder="t('给这组提示词取一个名字', 'Name this prompt preset')" />
        </NFormItem>
        <NFormItem :label="label" :label-props="{ for: `studio-${kind}-preset-text` }">
          <NInput v-model:value="text" :input-props="{ id: `studio-${kind}-preset-text` }" type="textarea" :autosize="{ minRows: 4, maxRows: 12 }" />
        </NFormItem>
      </NForm>
      <div class="preset-actions">
        <NButton v-if="editingId" type="error" secondary :disabled="presetsSaving" @click="confirmDelete">{{ t('删除预设', 'Delete preset') }}</NButton>
        <NButton :disabled="presetsSaving" @click="editorVisible = false">{{ t('取消', 'Cancel') }}</NButton>
        <NButton type="primary" :loading="presetsSaving" :disabled="presetsSaving || !name.trim() || !text.trim()" @click="save">{{ t('保存', 'Save') }}</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.prompt-field { display: grid; gap: 8px; min-width: 0; }
.prompt-label { font-weight: 600; color: var(--cpa-text-strong); }
.preset-controls { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: 6px; }
.preset-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.preset-actions .n-button:first-child:not(:last-child) { margin-right: auto; }
@media (max-width: 400px) {
  .preset-controls { grid-template-columns: minmax(0, 1fr) auto; }
  .preset-controls .n-select { grid-column: 1 / -1; }
}
</style>
