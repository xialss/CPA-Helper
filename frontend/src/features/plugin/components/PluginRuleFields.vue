<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NFormItem, NSelect } from 'naive-ui'

import { useI18n } from '@/shared/i18n'
import type { CPAHelperPluginCredential, CPAHelperPluginRule } from '@/shared/types/api'

const props = withDefaults(defineProps<{
  modelValue: CPAHelperPluginRule
  models?: string[]
  credentials?: CPAHelperPluginCredential[]
  credentialProviders?: Array<{ source: string; provider: string; label?: string }>
  disabled?: boolean
}>(), {
  models: () => [],
  credentials: () => [],
  credentialProviders: () => [],
  disabled: false,
})
const emit = defineEmits<{ 'update:modelValue': [CPAHelperPluginRule] }>()
const { t } = useI18n()
const draft = ref<CPAHelperPluginRule>({ ...props.modelValue })

watch(() => props.modelValue, (value) => { draft.value = { ...value } }, { deep: true })

function modelOptions(field: 'models' | 'denied_models') {
  const selected = draft.value[field] ?? []
  return [...new Set([...props.models, ...selected])]
    .map((value) => ({ label: value, value }))
}
const allowedModelOptions = computed(() => modelOptions('models'))
const deniedModelOptions = computed(() => modelOptions('denied_models'))
const credentialOptions = computed(() => props.credentials.map((value) => ({
  label: `${value.label}${value.status && value.status !== 'active' ? ` (${value.status})` : ''}`,
  value: value.ref,
})))
const availableProviders = computed<Array<{ source: string; provider: string; label?: string }>>(() => {
  const source = props.credentialProviders.length > 0
    ? props.credentialProviders
    : props.credentials.map((value) => ({ source: value.source, provider: value.provider }))
  return [...new Map(source.map((value) => [`${value.source}/${value.provider}`, value])).values()]
})
const providerOptions = computed(() => availableProviders.value.map((value) => ({
  label: value.label || `${value.source === 'auth-files' ? t('认证文件', 'Auth file') : t('API 提供商', 'AI provider')} · ${value.provider}`,
  value: `${value.source}/${value.provider}`,
})))

function setArray(field: keyof CPAHelperPluginRule, values: string[]) {
  draft.value = { ...draft.value, [field]: [...new Set(values)] }
  emit('update:modelValue', { ...draft.value })
}
function setProviders(field: keyof CPAHelperPluginRule, values: string[]) {
  const mapped = values.map((value) => {
    const [source, ...provider] = value.split('/')
    return { source: source as 'auth-files' | 'ai-providers', provider: provider.join('/') }
  })
  draft.value = { ...draft.value, [field]: mapped }
  emit('update:modelValue', { ...draft.value })
}
const selectedProviders = (field: 'credential_providers' | 'denied_credential_providers') =>
  ((draft.value[field] as Array<{ source: string; provider: string }> | undefined) ?? []).map((value) => `${value.source}/${value.provider}`)
</script>

<template>
  <p class="plugin-rule-hint">{{ t('关联规则与独立规则的同类列表取并集，禁止优先；某项的允许列表全部为空时，该项才不限制。', 'Linked and independent rules merge matching lists by union. Deny takes precedence; a restriction is absent only when all of its allow lists are empty.') }}</p>
  <div class="plugin-rule-grid">
    <NFormItem :label="t('允许的模型', 'Allowed models')">
      <NSelect
        multiple
        filterable
        :value="(draft.models as string[] | undefined) ?? []"
        :options="allowedModelOptions"
        :disabled="disabled"
        :placeholder="t('未设置本项，仍受关联规则限制', 'Unset here; linked rules still apply')"
        @update:value="setArray('models', $event)"
      />
    </NFormItem>
    <NFormItem :label="t('禁止的模型', 'Denied models')">
      <NSelect multiple filterable :value="(draft.denied_models as string[] | undefined) ?? []" :options="deniedModelOptions" :disabled="disabled" @update:value="setArray('denied_models', $event)" />
    </NFormItem>
    <NFormItem :label="t('允许的凭据', 'Allowed credentials')">
      <NSelect multiple filterable :value="(draft.credential_ids as string[] | undefined) ?? []" :options="credentialOptions" :disabled="disabled" @update:value="setArray('credential_ids', $event)" />
    </NFormItem>
    <NFormItem :label="t('禁止的凭据', 'Denied credentials')">
      <NSelect multiple filterable :value="(draft.denied_credential_ids as string[] | undefined) ?? []" :options="credentialOptions" :disabled="disabled" @update:value="setArray('denied_credential_ids', $event)" />
    </NFormItem>
    <NFormItem :label="t('允许的凭据类别', 'Allowed credential categories')">
      <NSelect multiple filterable :value="selectedProviders('credential_providers')" :options="providerOptions" :disabled="disabled" @update:value="setProviders('credential_providers', $event)" />
    </NFormItem>
    <NFormItem :label="t('禁止的凭据类别', 'Denied credential categories')">
      <NSelect multiple filterable :value="selectedProviders('denied_credential_providers')" :options="providerOptions" :disabled="disabled" @update:value="setProviders('denied_credential_providers', $event)" />
    </NFormItem>
  </div>
</template>

<style scoped>
.plugin-rule-hint { margin: 0 0 12px; color: var(--cpa-text-muted); font-size: 12px; }
.plugin-rule-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (max-width: 720px) {
  .plugin-rule-grid { grid-template-columns: 1fr; }
}
</style>
