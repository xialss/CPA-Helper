import type {
  AIProviderBrand,
  AIProviderCloak,
  AIProviderItem,
  AIProviderPayload,
  AIProviderRequestErrorRule,
} from '@/shared/types/api'

export interface ProviderAdvancedDraft {
  disable_cooling: boolean | null
  request_retry: number | null
  request_scoped_errors: AIProviderRequestErrorRule[] | null
  alpha_search: boolean | null
  disable_codex_cloaking: boolean | null
  rebuild_mid_system_message: boolean | null
  support_prompt_cache_key: boolean | null
  fingerprint_profile: string | null
  experimental_cch_signing: boolean | null
  cloak: AIProviderCloak | null
}

export type BooleanChoice = 'inherit' | 'enabled' | 'disabled'

export function booleanChoice(value: boolean | null | undefined): BooleanChoice {
  return value == null ? 'inherit' : value ? 'enabled' : 'disabled'
}

export function choiceBoolean(value: BooleanChoice): boolean | null {
  return value === 'inherit' ? null : value === 'enabled'
}

export function cloneAdvancedDraft<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

export function providerAdvancedDraft(provider?: Partial<AIProviderItem>): ProviderAdvancedDraft {
  return {
    disable_cooling: provider?.disable_cooling ?? null,
    request_retry: provider?.request_retry ?? null,
    request_scoped_errors: provider?.request_scoped_errors == null ? null : cloneAdvancedDraft(provider.request_scoped_errors),
    alpha_search: provider?.alpha_search ?? null,
    disable_codex_cloaking: provider?.disable_codex_cloaking ?? null,
    rebuild_mid_system_message: provider?.rebuild_mid_system_message ?? null,
    support_prompt_cache_key: provider?.support_prompt_cache_key ?? null,
    fingerprint_profile: provider?.fingerprint_profile === 'oauth-cli' ? 'claude-code-cli' : provider?.fingerprint_profile || null,
    experimental_cch_signing: provider?.experimental_cch_signing ?? null,
    cloak: provider?.cloak == null ? null : cloneAdvancedDraft(provider.cloak),
  }
}

function changed(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) !== JSON.stringify(right)
}

export function advancedConfigPayload(
  current: ProviderAdvancedDraft,
  original: ProviderAdvancedDraft,
  brand: AIProviderBrand,
  mode: 'create' | 'edit',
): Partial<AIProviderPayload> {
  const result: Partial<AIProviderPayload> = {}
  const fields: (keyof Omit<ProviderAdvancedDraft, 'cloak'>)[] = ['disable_cooling', 'request_retry']
  if (brand !== 'vertex') fields.push('request_scoped_errors')
  if (brand === 'codex') fields.push('alpha_search', 'disable_codex_cloaking')
  if (brand === 'claude') fields.push('rebuild_mid_system_message', 'fingerprint_profile', 'experimental_cch_signing')
  if (brand === 'openai_compatibility') fields.push('support_prompt_cache_key')
  for (const field of fields) {
    if (mode === 'create' ? current[field] != null : changed(current[field], original[field])) {
      Object.assign(result, { [field]: cloneAdvancedDraft(current[field]) })
    }
  }
  if (brand === 'claude' && (mode === 'create' ? current.cloak !== null : changed(current.cloak, original.cloak))) {
    if (current.cloak === null) result.cloak = null
    else if (original.cloak === null || mode === 'create') {
      result.cloak = cloneAdvancedDraft(current.cloak)
      if (result.cloak.sensitive_words) result.cloak.sensitive_words = result.cloak.sensitive_words.filter((word) => word.trim() !== '')
    }
    else {
      const patch: AIProviderCloak = {}
      for (const field of ['mode', 'strict_mode', 'sensitive_words', 'cache_user_id'] as const) {
        if (changed(current.cloak[field], original.cloak[field])) {
          Object.assign(patch, { [field]: field === 'sensitive_words' && current.cloak.sensitive_words ? current.cloak.sensitive_words.filter((word) => word.trim() !== '') : current.cloak[field] ?? null })
        }
      }
      result.cloak = patch
    }
  }
  return result
}
