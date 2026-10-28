<script setup lang="ts">
import { computed, h, ref, watch } from 'vue'
import { NAlert, NFormItem, NSelect } from 'naive-ui'

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

function modelIdentity(value: string) {
  return value.toLowerCase()
}

const availableModelNames = computed(() => new Map(
  props.models.map((value) => [modelIdentity(value), value]),
))

function modelOptions(field: 'models' | 'denied_models') {
  const selected = [...new Set(draft.value[field] ?? [])]
  const selectedIdentities = new Set(selected.map(modelIdentity))
  const directoryOptions = [...availableModelNames.value]
    .filter(([identity]) => !selectedIdentities.has(identity))
    .map(([, value]) => ({ label: value, value }))
  const selectedOptions = selected.map((value) => {
    const directoryName = availableModelNames.value.get(modelIdentity(value))
    return {
      label: directoryName ?? `${t('已选模型（当前目录缺失）', 'Selected model (missing from directory)')} · ${value}`,
      value,
    }
  })
  return [...directoryOptions, ...selectedOptions]
}
const allowedModelOptions = computed(() => modelOptions('models'))
const deniedModelOptions = computed(() => modelOptions('denied_models'))
const missingModelValues = computed(() => {
  const selected = new Set([...(draft.value.models ?? []), ...(draft.value.denied_models ?? [])])
  return [...selected].filter((value) => !availableModelNames.value.has(modelIdentity(value)))
})
const selectedCredentialRefs = computed(() => [...new Set([
  ...(draft.value.credential_ids ?? []), ...(draft.value.denied_credential_ids ?? []),
])])
const missingCredentialRefs = computed(() => {
  const available = new Set(props.credentials.map((value) => value.ref))
  return selectedCredentialRefs.value.filter((value) => !available.has(value))
})
const credentialOptions = computed(() => [
  ...props.credentials.map((value) => ({
    label: `${value.label}${value.status && value.status !== 'active' ? ` (${value.status})` : ''}`,
    value: value.ref,
  })),
  ...missingCredentialRefs.value.map((value) => ({
    label: `${t('已选凭据（当前目录缺失）', 'Selected credential (missing from directory)')} · ${value}`,
    value,
  })),
])
const availableProviders = computed<Array<{ source: string; provider: string; label?: string }>>(() => {
  const source = props.credentialProviders.length > 0
    ? props.credentialProviders
    : props.credentials.map((value) => ({ source: value.source, provider: value.provider }))
  return [...new Map(source.map((value) => [`${value.source}/${value.provider}`, value])).values()]
})
const selectedProviders = (field: 'credential_providers' | 'denied_credential_providers') =>
  (draft.value[field] ?? []).map((value) => `${value.source}/${value.provider}`)
const missingProviderValues = computed(() => {
  const available = new Set(availableProviders.value.map((value) => `${value.source}/${value.provider}`))
  return [...new Set([
    ...selectedProviders('credential_providers'), ...selectedProviders('denied_credential_providers'),
  ])].filter((value) => !available.has(value))
})
const providerOptions = computed(() => [
  ...availableProviders.value.map((value) => ({
    label: value.label || `${value.source === 'auth-files' ? t('认证文件', 'Auth file') : t('API 提供商', 'AI provider')} · ${value.provider}`,
    value: `${value.source}/${value.provider}`,
  })),
  ...missingProviderValues.value.map((value) => ({
    label: `${t('已选类别（当前目录缺失）', 'Selected category (missing from directory)')} · ${value}`,
    value,
  })),
])

function renderDirectoryOptionLabel(option: { label: string }) {
  return h('span', { class: 'plugin-directory-option-label', title: option.label }, option.label)
}

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
</script>

<template>
  <p class="plugin-rule-hint">{{ t('关联规则与独立规则的同类列表取并集，禁止优先；某项的允许列表全部为空时，该项才不限制。', 'Linked and independent rules merge matching lists by union. Deny takes precedence; a restriction is absent only when all of its allow lists are empty.') }}</p>
  <NAlert v-if="missingModelValues.length > 0 || missingCredentialRefs.length > 0 || missingProviderValues.length > 0" type="warning" :bordered="false" class="plugin-rule-missing">
    {{ t('部分已选模型、凭据或类别不在当前目录中，原引用仍会保留并保存。请确认后再移除，避免改变路由限制。', 'Some selected models, credentials or categories are missing from the current directory. Their references will be kept when saving. Confirm before removing them to avoid changing routing restrictions.') }}
  </NAlert>
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
      <NSelect multiple filterable :value="(draft.credential_ids as string[] | undefined) ?? []" :options="credentialOptions" :render-label="renderDirectoryOptionLabel" :disabled="disabled" @update:value="setArray('credential_ids', $event)" />
    </NFormItem>
    <NFormItem :label="t('禁止的凭据', 'Denied credentials')">
      <NSelect multiple filterable :value="(draft.denied_credential_ids as string[] | undefined) ?? []" :options="credentialOptions" :render-label="renderDirectoryOptionLabel" :disabled="disabled" @update:value="setArray('denied_credential_ids', $event)" />
    </NFormItem>
    <NFormItem :label="t('允许的凭据类别', 'Allowed credential categories')">
      <NSelect multiple filterable :value="selectedProviders('credential_providers')" :options="providerOptions" :render-label="renderDirectoryOptionLabel" :disabled="disabled" @update:value="setProviders('credential_providers', $event)" />
    </NFormItem>
    <NFormItem :label="t('禁止的凭据类别', 'Denied credential categories')">
      <NSelect multiple filterable :value="selectedProviders('denied_credential_providers')" :options="providerOptions" :render-label="renderDirectoryOptionLabel" :disabled="disabled" @update:value="setProviders('denied_credential_providers', $event)" />
    </NFormItem>
  </div>
</template>

<style scoped>
.plugin-rule-hint { margin: 0 0 12px; color: var(--cpa-text-muted); font-size: 12px; }
.plugin-rule-missing { margin-bottom: 12px; }
:global(.n-base-select-option .plugin-directory-option-label) {
  display: block;
  white-space: normal;
  overflow-wrap: anywhere;
}
.plugin-rule-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (max-width: 720px) {
  .plugin-rule-grid { grid-template-columns: 1fr; }
}
</style>
