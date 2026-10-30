<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NCheckbox, NInput, NTag } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { AIProviderModel } from '@/shared/types/api'
import { excludedModelCatalog, matchesExcludedRule, toggleExactExcludedModel } from '../utils/excludedModelRules'

const props = defineProps<{
  modelValue: string[]
  configured: AIProviderModel[]
  discovered: AIProviderModel[]
}>()
const emit = defineEmits<{ 'update:modelValue': [rules: string[]] }>()
const { t } = useI18n()
const search = ref('')
const newRule = ref('')
const ruleError = ref('')
const editingRule = ref<string | null>(null)
const catalog = computed(() => excludedModelCatalog(props.configured, props.discovered))
const visible = computed(() => {
  const keyword = search.value.trim().toLowerCase()
  return catalog.value.filter((model) => [model.name, model.alias ?? '', model.display_name ?? ''].join(' ').toLowerCase().includes(keyword))
})

function exactSelected(name: string) {
  return props.modelValue.some((rule) => rule.toLowerCase() === name.toLowerCase())
}
function wildcardRules(name: string) {
  return props.modelValue.filter((rule) => rule.includes('*') && matchesExcludedRule(name, rule))
}
function toggle(name: string, checked: boolean) {
  emit('update:modelValue', toggleExactExcludedModel(props.modelValue, name, checked))
}
function addRule() {
  const rule = newRule.value.trim()
  if (!rule || rule === '*') {
    ruleError.value = rule === '*' ? t('单独的 * 由提供商启停控制；请使用更具体的模型规则。', 'A standalone * is owned by provider enable/disable. Use a more specific model rule.') : t('请填写模型名称或通配符规则。', 'Enter a model name or wildcard rule.')
    return
  }
  if (editingRule.value !== null) {
    const index = props.modelValue.indexOf(editingRule.value)
    if (index < 0) {
      ruleError.value = t('原规则已被修改或移除，请重新选择要编辑的规则。', 'The original rule changed or was removed. Select it again to edit.')
      return
    }
    if (props.modelValue.some((item, position) => position !== index && item.toLowerCase() === rule.toLowerCase())) {
      ruleError.value = t('已存在相同的排除规则。', 'This exclusion rule already exists.')
      return
    }
    emit('update:modelValue', props.modelValue.map((item, position) => position === index ? rule : item))
  } else if (!props.modelValue.some((item) => item.toLowerCase() === rule.toLowerCase())) {
    emit('update:modelValue', [...props.modelValue, rule])
  }
  newRule.value = ''
  editingRule.value = null
  ruleError.value = ''
}
function editRule(rule: string) {
  editingRule.value = rule
  newRule.value = rule
  ruleError.value = ''
}
function cancelEdit() {
  editingRule.value = null
  newRule.value = ''
  ruleError.value = ''
}
function removeRule(index: number) {
  emit('update:modelValue', props.modelValue.filter((_, position) => position !== index))
}
</script>

<template>
  <details class="excluded-picker">
    <summary><strong>{{ t('排除模型', 'Excluded models') }}</strong></summary>
    <div class="picker-body">
      <p class="hint">{{ t('搜索并勾选要排除的具体模型，也可在下方编辑通配符和其他规则。候选来自当前配置与已有的模型发现结果，未出现在候选中的规则仍会保留。', 'Search and select exact models, or edit wildcard and other rules below. Candidates come from this configuration and existing discovery results; rules remain even when absent from the list.') }}</p>
      <NInput v-model:value="search" clearable :placeholder="t('搜索模型名称或展示名', 'Search model ID or display name')" />
      <p v-if="visible.length === 0" class="hint">{{ search ? t('没有匹配的模型。', 'No matching models.') : t('当前没有候选模型，可直接添加模型名称或通配符规则。', 'No candidate models. Add an exact model name or wildcard rule below.') }}</p>
      <div v-if="visible.length" class="model-catalog">
        <div v-for="model in visible" :key="model.name.toLowerCase()" class="model-choice">
          <NCheckbox :checked="exactSelected(model.name)" @update:checked="(checked) => toggle(model.name, checked)"><span class="model-id">{{ model.name }}</span></NCheckbox>
          <span v-if="model.display_name || model.alias" class="hint">{{ model.display_name || model.alias }}</span>
          <span v-if="wildcardRules(model.name).length" class="wildcard-note">{{ t(`仍受通配符排除：${wildcardRules(model.name).join(', ')}`, `Excluded by wildcard: ${wildcardRules(model.name).join(', ')}`) }}</span>
        </div>
      </div>
      <div class="rule-editor">
        <strong>{{ t('当前排除项与通配符规则', 'Current exclusions and wildcard rules') }}</strong>
        <p class="hint">{{ t('* 匹配任意长度文本，匹配不区分大小写，其他字符按字面量处理。取消具体项不会抵消匹配它的通配符。', '* matches any length of text, case-insensitively; other characters are literal. Removing an exact item does not override matching wildcards.') }}</p>
        <div class="saved-rules"><span v-for="(rule, index) in modelValue" :key="`${index}:${rule}`" class="saved-rule"><NTag closable @close="removeRule(index)">{{ rule }}</NTag><NButton size="tiny" quaternary @click="editRule(rule)">{{ t('编辑', 'Edit') }}</NButton></span><span v-if="!modelValue.length" class="hint">{{ t('未配置排除项', 'No exclusions configured') }}</span></div>
        <div class="new-rule"><NInput v-model:value="newRule" clearable :placeholder="t('例如 claude-*-thinking', 'For example, claude-*-thinking')" @keyup.enter="addRule" /><NButton secondary @click="addRule">{{ editingRule === null ? t('添加规则', 'Add rule') : t('保存规则', 'Save rule') }}</NButton><NButton v-if="editingRule !== null" quaternary @click="cancelEdit">{{ t('取消', 'Cancel') }}</NButton></div>
        <p v-if="ruleError" role="alert" class="error">{{ ruleError }}</p>
      </div>
    </div>
  </details>
</template>

<style scoped>
.excluded-picker { min-width: 0; }
summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; cursor: pointer; font-size: 14px; }
summary::after { content: '+'; color: var(--cpa-text-muted); }
details[open] summary::after { content: '−'; }
.picker-body, .rule-editor { display: grid; gap: 12px; min-width: 0; }
p { margin: 0; }
.hint { color: var(--cpa-text-muted); font-size: 12px; line-height: 1.6; }
.model-catalog { display: grid; gap: 10px; max-height: 280px; overflow-y: auto; border: 1px solid var(--cpa-border); border-radius: 10px; padding: 12px; }
.model-choice { display: grid; gap: 4px; min-width: 0; }
.model-id, .wildcard-note { overflow-wrap: anywhere; }
.wildcard-note { color: var(--cpa-primary); font-size: 12px; }
.saved-rules, .saved-rule { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.new-rule { display: flex; align-items: start; gap: 8px; }
.new-rule > :first-child { flex: 1; min-width: 0; }
.error { color: var(--cpa-danger); font-size: 12px; }
</style>
