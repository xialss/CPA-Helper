<script setup lang="ts">
import { computed } from 'vue'
import { NForm, NFormItem, NSelect } from 'naive-ui'
import { useI18n } from '@/shared/i18n'
import type { AIProviderBrand } from '@/shared/types/api'
import { booleanChoice, choiceBoolean, type BooleanChoice, type ProviderAdvancedDraft } from '../utils/providerAdvancedConfig'

const props = defineProps<{ brand: AIProviderBrand; modelValue: ProviderAdvancedDraft }>()
const emit = defineEmits<{ 'update:modelValue': [value: ProviderAdvancedDraft] }>()
const { t } = useI18n()
const booleanOptions = computed(() => [
  { label: t('继承 / 未设置', 'Inherit / unset'), value: 'inherit' },
  { label: t('启用', 'Enabled'), value: 'enabled' },
  { label: t('禁用', 'Disabled'), value: 'disabled' },
])
const cloakingOptions = computed(() => [
  { label: t('继承', 'Inherit'), value: 'inherit' },
  { label: t('禁用请求伪装', 'Disable request cloaking'), value: 'enabled' },
  { label: t('启用请求伪装', 'Enable request cloaking'), value: 'disabled' },
])
const fingerprintOptions = computed(() => {
  const options = [
    { label: t('默认（调用方控制）', 'Default (caller controlled)'), value: '' },
    { label: 'Claude Code CLI', value: 'claude-code-cli' },
  ]
  const value = props.modelValue.fingerprint_profile
  if (value && !options.some((option) => option.value === value)) options.push({ label: t(`已有自定义值：${value}`, `Existing value: ${value}`), value })
  return options
})
function setBoolean(field: 'alpha_search' | 'disable_codex_cloaking' | 'rebuild_mid_system_message' | 'support_prompt_cache_key', value: BooleanChoice) {
  emit('update:modelValue', { ...props.modelValue, [field]: choiceBoolean(value) })
}
function setFingerprint(value: string) {
  emit('update:modelValue', { ...props.modelValue, fingerprint_profile: value || null })
}
</script>

<template>
  <section v-if="brand === 'codex' || brand === 'claude' || brand === 'openai_compatibility'" class="behavior-section">
    <h3>{{ t('提供商专属行为', 'Provider-specific behavior') }}</h3>
    <NForm label-placement="top">
      <div v-if="brand === 'codex'" class="behavior-grid">
        <NFormItem label="Alpha Search"><div class="field-stack"><NSelect :value="booleanChoice(modelValue.alpha_search)" :options="booleanOptions" @update:value="(value) => setBoolean('alpha_search', value)" /><p>{{ t('允许此凭据参与 CPA 的 Alpha Search。', 'Allow this credential to participate in CPA Alpha Search.') }}</p></div></NFormItem>
        <NFormItem :label="t('Codex 请求伪装', 'Codex request cloaking')"><div class="field-stack"><NSelect :value="booleanChoice(modelValue.disable_codex_cloaking)" :options="cloakingOptions" @update:value="(value) => setBoolean('disable_codex_cloaking', value)" /><p>{{ t('继承 CPA 配置，或明确启用 / 禁用此凭据的请求伪装。', 'Inherit CPA settings or explicitly enable / disable cloaking for this credential.') }}</p></div></NFormItem>
      </div>
      <NFormItem v-if="brand === 'claude'" :label="t('重建系统消息', 'Rebuild system messages')"><div class="field-stack"><NSelect :value="booleanChoice(modelValue.rebuild_mid_system_message)" :options="booleanOptions" @update:value="(value) => setBoolean('rebuild_mid_system_message', value)" /><p>{{ t('将消息列表中的 system 消息移到顶层 system 字段。', 'Move system messages in the message list into the top-level system field.') }}</p></div></NFormItem>
      <NFormItem v-if="brand === 'openai_compatibility'" :label="t('提示词缓存键注入', 'Prompt cache key injection')"><div class="field-stack"><NSelect :value="booleanChoice(modelValue.support_prompt_cache_key)" :options="booleanOptions" @update:value="(value) => setBoolean('support_prompt_cache_key', value)" /><p>{{ t('为支持的请求派生并注入 prompt_cache_key，需上游支持该字段。', 'Derive and inject prompt_cache_key for supported requests. The upstream must support this field.') }}</p></div></NFormItem>
      <NFormItem v-if="brand === 'claude'" :label="t('请求指纹', 'Request fingerprint')"><div class="field-stack"><NSelect :value="modelValue.fingerprint_profile ?? ''" :options="fingerprintOptions" @update:value="setFingerprint" /><p>{{ t('默认保留调用方控制的请求指纹；Claude Code CLI 使用 CPA 的对应指纹配置，不会将 API Key 转成 OAuth。', 'Default preserves the caller-controlled fingerprint. Claude Code CLI selects CPA’s matching profile; it does not convert an API key to OAuth.') }}</p></div></NFormItem>
    </NForm>
  </section>
</template>

<style scoped>
.behavior-section, .field-stack { display: grid; gap: 12px; width: 100%; min-width: 0; }
h3, p { margin: 0; }
p { color: var(--cpa-text-muted); font-size: 12px; line-height: 1.6; }
.behavior-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
@media (max-width: 720px) { .behavior-grid { grid-template-columns: minmax(0, 1fr); } }
</style>
