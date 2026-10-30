<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NForm, NFormItem, NInput, NInputNumber, NSelect } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { AIProviderRequestErrorRule, AIProviderRuntimePolicy } from '@/shared/types/api'
import { booleanChoice, choiceBoolean, cloneAdvancedDraft, type BooleanChoice, type ProviderAdvancedDraft } from '../utils/providerAdvancedConfig'

const props = defineProps<{
  modelValue: ProviderAdvancedDraft
  supportsErrors: boolean
  effective?: AIProviderRuntimePolicy | null
}>()
const emit = defineEmits<{ 'update:modelValue': [value: ProviderAdvancedDraft] }>()
const { t } = useI18n()
const coolingOptions = computed(() => [
  { label: t('继承', 'Inherit'), value: 'inherit' },
  { label: t('禁用冷却调度', 'Disable cooling'), value: 'enabled' },
  { label: t('启用冷却调度', 'Enable cooling'), value: 'disabled' },
])
const ruleModes = computed(() => [
  { label: t('继承规则', 'Inherit rules'), value: 'inherit' },
  { label: t('使用自定义规则（可为空）', 'Override rules (may be empty)'), value: 'override' },
])
const actionOptions = computed(() => [
  { label: t('停止', 'Stop'), value: 'stop' },
  { label: t('停止并冷却', 'Stop and cool down'), value: 'stop-and-cooldown' },
  { label: t('继续', 'Continue'), value: 'continue' },
  { label: t('继续并冷却', 'Continue and cool down'), value: 'continue-and-cooldown' },
])
const conditionFields = ['match', 'match_regex'] as const
const overrideDraft = ref<AIProviderRequestErrorRule[]>(cloneAdvancedDraft(props.modelValue.request_scoped_errors ?? []))

function update(patch: Partial<ProviderAdvancedDraft>) {
  emit('update:modelValue', { ...props.modelValue, ...patch })
}

function setCooling(value: BooleanChoice) {
  update({ disable_cooling: choiceBoolean(value) })
}

function setRuleMode(value: 'inherit' | 'override') {
  if (value === 'inherit') {
    overrideDraft.value = cloneAdvancedDraft(props.modelValue.request_scoped_errors ?? [])
    update({ request_scoped_errors: null })
  } else {
    update({ request_scoped_errors: props.modelValue.request_scoped_errors ?? cloneAdvancedDraft(overrideDraft.value) })
  }
}

function updateRule(index: number, patch: Partial<AIProviderRequestErrorRule>) {
  update({ request_scoped_errors: (props.modelValue.request_scoped_errors ?? []).map((rule, position) => position === index ? { ...rule, ...patch } : rule) })
}

function updateCondition(index: number, field: 'match' | 'match_regex', condition: number, value: string) {
  const items = [...(props.modelValue.request_scoped_errors?.[index]?.[field] ?? [])]
  items[condition] = value
  updateRule(index, { [field]: items })
}

function removeCondition(index: number, field: 'match' | 'match_regex', condition: number) {
  updateRule(index, { [field]: (props.modelValue.request_scoped_errors?.[index]?.[field] ?? []).filter((_, position) => position !== condition) })
}

function addCondition(index: number, field: 'match' | 'match_regex') {
  updateRule(index, { [field]: [...(props.modelValue.request_scoped_errors?.[index]?.[field] ?? []), ''] })
}

function moveRule(index: number, direction: -1 | 1) {
  const rules = [...(props.modelValue.request_scoped_errors ?? [])]
  const target = index + direction
  if (target < 0 || target >= rules.length) return
  ;[rules[index], rules[target]] = [rules[target]!, rules[index]!]
  update({ request_scoped_errors: rules })
}
</script>

<template>
  <details class="config-section">
    <summary><strong>{{ t('高级运行策略', 'Advanced runtime policy') }}</strong></summary>
    <div class="policy-body">
      <p class="hint">{{ t('未覆盖的设置沿用 CPA 的继承策略。这里只修改此配置，不修改 CPA 全局策略。', 'Unset values follow CPA inheritance. These controls change this configuration, not CPA global policy.') }}</p>
      <NForm label-placement="top">
        <div class="policy-grid">
          <NFormItem :label="t('冷却调度', 'Cooling')" :content-style="{ alignItems: 'flex-start' }">
            <div class="field-stack">
              <NSelect :value="booleanChoice(modelValue.disable_cooling)" :options="coolingOptions" @update:value="setCooling" />
              <span v-if="modelValue.disable_cooling === null && effective?.disable_cooling != null" class="hint">{{ effective.disable_cooling ? t('继承值：禁用冷却', 'Inherited: cooling disabled') : t('继承值：启用冷却', 'Inherited: cooling enabled') }}</span>
            </div>
          </NFormItem>
          <NFormItem :label="t('请求重试次数', 'Request retries')" :content-style="{ alignItems: 'flex-start' }">
            <div class="field-stack">
              <NInputNumber :value="modelValue.request_retry" :precision="0" clearable :placeholder="t('留空继承', 'Blank to inherit')" @update:value="(value) => update({ request_retry: value })" />
              <span class="hint">{{ t('留空继承；0 不追加重试；负数使用全局重试策略；正数指定重试轮次。', 'Blank inherits; 0 disables extra retries; negative values use global policy; positive values set retry rounds.') }}</span>
              <span v-if="modelValue.request_retry === null && effective?.request_retry != null" class="hint">{{ t(`继承值：${effective.request_retry}`, `Inherited: ${effective.request_retry}`) }}</span>
            </div>
          </NFormItem>
        </div>
        <template v-if="supportsErrors">
          <NFormItem :label="t('请求错误规则', 'Request error rules')">
            <div class="field-stack">
              <NSelect :value="modelValue.request_scoped_errors === null ? 'inherit' : 'override'" :options="ruleModes" @update:value="setRuleMode" />
              <span class="hint">{{ t('自定义规则按顺序匹配，并替代继承列表；空列表表示明确不使用任何继承规则。文本保留空格，正则按 CPA 的 Go 语法解释。', 'Overrides replace the inherited list and keep their order. An empty list explicitly clears inherited rules. Text keeps whitespace; regex uses CPA’s Go syntax.') }}</span>
            </div>
          </NFormItem>
          <div v-if="modelValue.request_scoped_errors !== null" class="rule-list">
            <article v-for="(rule, index) in modelValue.request_scoped_errors" :key="index" class="rule-card">
              <div class="rule-head">
                <strong>{{ t(`规则 ${index + 1}`, `Rule ${index + 1}`) }}</strong>
                <div class="rule-actions">
                  <NButton size="tiny" :disabled="index === 0" @click="moveRule(index, -1)">{{ t('上移', 'Move up') }}</NButton>
                  <NButton size="tiny" :disabled="index === modelValue.request_scoped_errors.length - 1" @click="moveRule(index, 1)">{{ t('下移', 'Move down') }}</NButton>
                  <NButton size="tiny" tertiary type="error" @click="update({ request_scoped_errors: modelValue.request_scoped_errors.filter((_, position) => position !== index) })">{{ t('删除', 'Delete') }}</NButton>
                </div>
              </div>
              <div class="policy-grid">
                <NFormItem :label="t('HTTP 状态码（必填）', 'HTTP status (required)')" :content-style="{ alignItems: 'flex-start' }"><div class="field-stack"><NInputNumber :value="rule.status ?? null" :precision="0" :min="1" clearable @update:value="(value) => updateRule(index, { status: value })" /><span class="hint">{{ t('新增或修改规则时必须填写正整数状态码。', 'New or edited rules require a positive integer status code.') }}</span></div></NFormItem>
                <NFormItem :label="t('匹配后的动作', 'Action on match')" :content-style="{ alignItems: 'flex-start' }"><NSelect :value="rule.action ?? null" :options="actionOptions" @update:value="(value) => updateRule(index, { action: value })" /></NFormItem>
              </div>
              <div v-for="field in conditionFields" :key="field" class="condition-group">
                <div class="rule-head"><span>{{ field === 'match' ? t('文本匹配条件', 'Text conditions') : t('正则匹配条件', 'Regex conditions') }}</span><NButton size="tiny" secondary @click="addCondition(index, field)">{{ t('添加条件', 'Add condition') }}</NButton></div>
                <div v-for="(condition, position) in rule[field] ?? []" :key="position" class="condition-row">
                  <NInput :value="condition" type="textarea" :autosize="{ minRows: 1, maxRows: 4 }" @update:value="(value) => updateCondition(index, field, position, value)" />
                  <NButton size="small" tertiary type="error" @click="removeCondition(index, field, position)">{{ t('移除', 'Remove') }}</NButton>
                </div>
              </div>
              <p class="hint">{{ t('至少填写一个文本或正则匹配条件。', 'Add at least one text or regex condition.') }}</p>
            </article>
            <NButton secondary @click="update({ request_scoped_errors: [...modelValue.request_scoped_errors, { action: 'stop', match: [''] }] })">{{ t('添加错误规则', 'Add error rule') }}</NButton>
          </div>
        </template>
      </NForm>
    </div>
  </details>
</template>

<style scoped>
.config-section { min-width: 0; }
summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; cursor: pointer; font-size: 14px; }
summary::after { content: '+'; color: var(--cpa-text-muted); }
details[open] summary::after { content: '−'; }
.policy-body { display: grid; gap: 12px; min-width: 0; }
p { margin: 0; }
.hint { color: var(--cpa-text-muted); font-size: 12px; line-height: 1.6; }
.policy-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 16px; align-items: start; }
.field-stack, .rule-list, .condition-group { display: grid; gap: 8px; width: 100%; min-width: 0; }
.rule-card { border: 1px solid var(--cpa-border); border-radius: 12px; padding: 14px; display: grid; gap: 12px; min-width: 0; }
.rule-head, .rule-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.condition-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; align-items: start; }
@media (max-width: 720px) { .policy-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
