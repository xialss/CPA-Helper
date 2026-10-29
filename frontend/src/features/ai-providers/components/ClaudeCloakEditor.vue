<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NForm, NFormItem, NInput, NSelect, NSwitch } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { AIProviderCloak } from '@/shared/types/api'
import { booleanChoice, choiceBoolean, type BooleanChoice } from '../utils/providerAdvancedConfig'

const props = defineProps<{ modelValue: AIProviderCloak | null; legacySigning: boolean | null }>()
const emit = defineEmits<{
  'update:modelValue': [value: AIProviderCloak | null]
  'update:legacySigning': [value: boolean | null]
}>()
const { t } = useI18n()
const modeOptions = computed(() => {
  const options = [
    { label: t('默认模式', 'Default mode'), value: '' },
    { label: t('自动（auto）', 'Auto'), value: 'auto' },
    { label: t('始终（always）', 'Always'), value: 'always' },
    { label: t('禁用（never）', 'Never'), value: 'never' },
    { label: t('去除身份伪装（uncloak）', 'Uncloak'), value: 'uncloak' },
  ]
  const mode = props.modelValue?.mode
  if (mode && !options.some((option) => option.value === mode)) options.push({ label: t(`已有值：${mode}`, `Existing value: ${mode}`), value: mode })
  return options
})
const cacheOptions = computed(() => [
  { label: t('继承 / 未设置', 'Inherit / unset'), value: 'inherit' },
  { label: t('按 API Key 复用', 'Reuse per API key'), value: 'enabled' },
  { label: t('每次请求重新生成', 'Generate for each request'), value: 'disabled' },
])
const signingOptions = computed(() => [
  { label: t('保留默认', 'Keep default'), value: 'inherit' },
  { label: t('启用', 'Enabled'), value: 'enabled' },
  { label: t('禁用', 'Disabled'), value: 'disabled' },
])
const modeHint = computed(() => {
  switch (props.modelValue?.mode) {
    case 'always': return t('对尚未确认的客户端应用伪装；已确认的原生 Claude Code 请求仍直接传递。', 'Cloak unconfirmed clients; confirmed native Claude Code requests remain passthrough.')
    case 'never':
    case 'uncloak': return t('不应用伪装；非 CLI API Key 还会去除传入的 Claude Code 身份与计费归属信息。', 'Do not cloak. Non-CLI API keys also strip incoming Claude Code identity and billing attribution.')
    default: return t('CPA 自动识别请求；有可靠信号确认原生入口时直接传递，其余请求应用伪装。', 'CPA checks request signals: verified native entrypoints pass through; other requests are cloaked.')
  }
})
const summary = computed(() => props.modelValue === null
  ? t('未自定义', 'Not customized')
  : modeOptions.value.find((option) => option.value === (props.modelValue?.mode ?? ''))?.label ?? props.modelValue.mode)

function update(patch: Partial<AIProviderCloak>) {
  emit('update:modelValue', { ...props.modelValue, ...patch })
}
function setCustom(enabled: boolean) {
  emit('update:modelValue', enabled ? {} : null)
}
function setCache(value: BooleanChoice) {
  update({ cache_user_id: choiceBoolean(value) })
}
function setLegacy(value: BooleanChoice) {
  emit('update:legacySigning', choiceBoolean(value))
}
function setWord(index: number, word: string) {
  const words = [...(props.modelValue?.sensitive_words ?? [])]
  words[index] = word
  update({ sensitive_words: words })
}
</script>

<template>
  <details class="cloak-section">
    <summary><strong>{{ t('请求伪装（Cloak）', 'Request cloaking (Cloak)') }}</strong><span class="summary-value">{{ summary }}</span></summary>
    <div class="cloak-body">
      <p class="hint">{{ t('控制 CPA 如何处理 Claude 请求中的客户端身份与系统提示。展开不会修改配置；仅在需要覆盖现有行为时开启自定义。', 'Control how CPA handles client identity and system prompts in Claude requests. Expanding changes nothing; customize only when you need to override existing behavior.') }}</p>
      <div class="custom-row"><span>{{ t('自定义请求伪装', 'Customize request cloaking') }}</span><NSwitch :value="modelValue !== null" @update:value="setCustom" /></div>
      <NForm v-if="modelValue !== null" label-placement="top">
        <div class="cloak-grid">
          <NFormItem :label="t('伪装模式', 'Cloaking mode')"><div class="field-stack"><NSelect :value="modelValue.mode ?? ''" :options="modeOptions" @update:value="(value) => update({ mode: value || null })" /><p class="hint">{{ modeHint }}</p></div></NFormItem>
          <NFormItem :label="t('user_id 缓存', 'user_id caching')"><div class="field-stack"><NSelect :value="booleanChoice(modelValue.cache_user_id)" :options="cacheOptions" @update:value="setCache" /><p class="hint">{{ t('复用时同一 API Key 使用缓存的 user_id；禁用时每次请求生成新的随机值。继承不新增覆盖。', 'Reuse caches user_id per API key; disabling creates a fresh random value for each request. Inherit adds no override.') }}</p></div></NFormItem>
          <NFormItem class="wide-field" :label="t('严格处理系统提示', 'Strict system prompt handling')"><div class="field-stack"><NSwitch :value="modelValue.strict_mode ?? false" @update:value="(value) => update({ strict_mode: value })" /><p class="hint">{{ t('开启后移除调用方的系统提示，仅保留 Claude Code 计费与身份块。关闭时，CPA 根据模型将原提示保留为提醒或会话中的系统消息。', 'When enabled, strip caller system prompts and retain only Claude Code billing and identity blocks. Otherwise CPA preserves them as a reminder or a mid-conversation system message depending on the model.') }}</p></div></NFormItem>
        </div>
        <div class="words-head"><strong>{{ t('敏感词混淆', 'Sensitive-word obfuscation') }}</strong><NButton size="small" secondary @click="update({ sensitive_words: [...(modelValue.sensitive_words ?? []), ''] })">{{ t('添加词语', 'Add word') }}</NButton></div>
        <p class="hint">{{ t('CPA 会在匹配的词语中插入零宽字符；每项填写一个词或短语，空项不会提交。', 'CPA inserts zero-width characters into matching words. Use one word or phrase per item; blank items are omitted.') }}</p>
        <div v-for="(word, index) in modelValue.sensitive_words ?? []" :key="index" class="word-row"><NInput :value="word" @update:value="(value) => setWord(index, value)" /><NButton tertiary type="error" @click="update({ sensitive_words: modelValue.sensitive_words?.filter((_, position) => position !== index) ?? [] })">{{ t('删除', 'Delete') }}</NButton></div>
      </NForm>
      <NForm label-placement="top" class="legacy-section"><NFormItem :label="t('旧版 CCH 签名兼容', 'Legacy CCH signing')"><div class="field-stack"><NSelect :value="booleanChoice(legacySigning)" :options="signingOptions" @update:value="setLegacy" /><p class="hint">{{ t('保留旧版配置的兼容选项；这与上方独立的“请求指纹”不同。', 'Compatibility with existing legacy settings. This is separate from the Request fingerprint setting above.') }}</p></div></NFormItem></NForm>
    </div>
  </details>
</template>

<style scoped>
.cloak-section { border: 1px solid var(--cpa-border); border-radius: 12px; }
summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; cursor: pointer; padding: 16px; }
summary::after { content: '+'; color: var(--cpa-text-muted); }
details[open] summary::after { content: '−'; }
.summary-value { margin-left: auto; color: var(--cpa-text-muted); font-size: 12px; }
.cloak-body { display: grid; gap: 14px; padding: 0 16px 16px; }
.hint { margin: 0; color: var(--cpa-text-muted); font-size: 12px; line-height: 1.65; }
.custom-row, .words-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.cloak-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 16px; }
.field-stack { display: grid; gap: 8px; width: 100%; min-width: 0; }
.wide-field { grid-column: 1 / -1; }
.word-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: start; gap: 8px; margin-top: 8px; }
.legacy-section { border-top: 1px solid var(--cpa-border); padding-top: 14px; }
@media (max-width: 720px) { .cloak-grid { grid-template-columns: minmax(0, 1fr); } summary { flex-wrap: wrap; } }
</style>
