import { apiClient } from '@/shared/api/apiClient'
import { localize } from '@/shared/i18n'
import type {
  CPAHelperPluginGroup,
  CPAHelperPluginKey,
  CPAHelperPluginKeyPolicy,
  CPAHelperPluginKeysSummaryResponse,
  CPAHelperPluginConcurrencySummary,
  CPAHelperPluginDirectory,
  CPAHelperPluginRule,
  CPAHelperPluginPolicy,
  CPAHelperPluginStatus,
  CPAHelperPluginClaudeTTLFeature,
  CPAHelperResponseModelConfig,
} from '@/shared/types/api'

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

function normalizeRule(value: unknown): CPAHelperPluginRule {
  const source = isRecord(value) ? value : {}
  return {
    ...source,
    models: stringArray(source.models),
    denied_models: stringArray(source.denied_models),
    credential_ids: stringArray(source.credential_ids),
    denied_credential_ids: stringArray(source.denied_credential_ids),
    credential_providers: Array.isArray(source.credential_providers) ? source.credential_providers : [],
    denied_credential_providers: Array.isArray(source.denied_credential_providers) ? source.denied_credential_providers : [],
  } as CPAHelperPluginRule
}

export function normalizeCPAHelperPluginKey(value: unknown): CPAHelperPluginKey {
  const source = isRecord(value) ? value : {}
  if (Object.prototype.hasOwnProperty.call(source, 'claude_cache_ttl') && source.claude_cache_ttl !== '' && source.claude_cache_ttl !== '5m' && source.claude_cache_ttl !== '1h') {
    throw new Error(localize('API Key 缓存 TTL 格式无效', 'Invalid API key cache TTL'))
  }
  const concurrency = typeof source.max_concurrency === 'number' && Number.isFinite(source.max_concurrency)
    ? Math.max(0, Math.trunc(source.max_concurrency))
    : 0
  return {
    ...source,
    id: typeof source.id === 'string' ? source.id : '',
    label: typeof source.label === 'string' ? source.label : undefined,
    enabled: source.enabled !== false,
    group_ids: stringArray(source.group_ids),
    max_concurrency: concurrency,
    rule: normalizeRule(source.rule),
  } as CPAHelperPluginKey
}

function normalizeGroup(value: unknown): CPAHelperPluginGroup {
  const source = isRecord(value) ? value : {}
  return {
    ...source,
    id: typeof source.id === 'string' ? source.id : '',
    name: typeof source.name === 'string' ? source.name : '',
    note: typeof source.note === 'string' ? source.note : '',
    rule: normalizeRule(source.rule),
  } as CPAHelperPluginGroup
}

export function normalizeCPAHelperPluginPolicy(value: unknown): CPAHelperPluginPolicy {
  const source = isRecord(value) ? value : {}
  return {
    ...source,
    contract_version: typeof source.contract_version === 'string' ? source.contract_version : 'v1',
    plugin_version: typeof source.plugin_version === 'string' ? source.plugin_version : '',
    policy_revision: typeof source.policy_revision === 'number' && Number.isFinite(source.policy_revision) ? source.policy_revision : 0,
    generated_at: typeof source.generated_at === 'string' ? source.generated_at : '',
    groups: Array.isArray(source.groups) ? source.groups.map(normalizeGroup) : [],
    keys: Array.isArray(source.keys) ? source.keys.map(normalizeCPAHelperPluginKey) : [],
  } as CPAHelperPluginPolicy
}

function normalizeKeyPolicy(value: unknown): CPAHelperPluginKeyPolicy {
  const source = isRecord(value) ? value : {}
  return {
    policy_revision: typeof source.policy_revision === 'number' && Number.isFinite(source.policy_revision) ? source.policy_revision : 0,
    groups: Array.isArray(source.groups) ? source.groups.map(normalizeGroup) : [],
    key: normalizeCPAHelperPluginKey(source.key),
  }
}

export async function getCPAHelperPluginStatus(): Promise<CPAHelperPluginStatus> {
  const response = await apiClient.get<unknown>('/cpa-helper-plugin/status')
  if (!isRecord(response) || typeof response.loaded !== 'boolean'
    || !isRecord(response.capabilities) || !isRecord(response.health)
    || typeof response.response_model_verification_supported !== 'boolean') {
    throw new Error(localize('插件能力响应格式无效', 'Invalid plugin capability response'))
  }
  const feature = response.claude_cache_ttl
  if (!isRecord(feature) || typeof feature.supported !== 'boolean'
    || typeof feature.status !== 'string' || !['configured', 'disabled', 'unavailable', 'unsupported'].includes(feature.status)
    || (feature.metadata_version !== undefined && feature.metadata_version !== 'v1')
    || (feature.supported && (feature.status === 'unsupported' || feature.metadata_version !== 'v1'))
    || (!feature.supported && feature.status !== 'unsupported')) {
    throw new Error(localize('Claude 缓存 TTL 能力响应格式无效', 'Invalid Claude cache TTL capability response'))
  }
  return {
    loaded: response.loaded,
    capabilities: response.capabilities,
    health: response.health,
    response_model_verification_supported: response.response_model_verification_supported,
    claude_cache_ttl: {
      supported: feature.supported,
      status: feature.status as CPAHelperPluginClaudeTTLFeature['status'],
      ...(feature.metadata_version === 'v1' ? { metadata_version: 'v1' as const } : {}),
    },
  }
}

export async function getCPAHelperPluginKeysSummary(signal?: AbortSignal): Promise<CPAHelperPluginKeysSummaryResponse> {
  const response = await apiClient.get<unknown>('/cpa-helper-plugin/keys-summary', {}, signal)
  if (!isRecord(response) || !Array.isArray(response.keys)) {
    throw new Error(localize('API Key 插件摘要响应格式无效', 'Invalid API key plugin summary response'))
  }
  const keys = response.keys.map((key) => {
    if (!isRecord(key)
      || typeof key.api_key_hash !== 'string' || !key.api_key_hash
      || typeof key.enabled !== 'boolean'
      || !Array.isArray(key.group_names) || !key.group_names.every((name) => typeof name === 'string')
      || typeof key.has_custom_rule !== 'boolean'
      || typeof key.current_concurrency !== 'number' || !Number.isSafeInteger(key.current_concurrency) || key.current_concurrency < 0
      || typeof key.max_concurrency !== 'number' || !Number.isSafeInteger(key.max_concurrency) || key.max_concurrency < 0) {
      throw new Error(localize('API Key 插件摘要响应格式无效', 'Invalid API key plugin summary response'))
    }
    return {
      api_key_hash: key.api_key_hash,
      enabled: key.enabled,
      group_names: key.group_names as string[],
      has_custom_rule: key.has_custom_rule,
      current_concurrency: key.current_concurrency,
      max_concurrency: key.max_concurrency,
    }
  })
  return { keys }
}

export async function getCPAHelperPluginConcurrencySummary(
  scope: 'admin' | 'account',
  signal?: AbortSignal,
): Promise<CPAHelperPluginConcurrencySummary> {
  const response = await apiClient.get<unknown>('/cpa-helper-plugin/concurrency-summary', { scope }, signal)
  if (!isRecord(response)
    || (response.scope !== 'admin' && response.scope !== 'account')
    || typeof response.current_concurrency !== 'number'
    || !Number.isSafeInteger(response.current_concurrency)
    || response.current_concurrency < 0) {
    throw new Error(localize('插件并发摘要响应格式无效', 'Invalid plugin concurrency summary response'))
  }
  return {
    scope: response.scope,
    current_concurrency: response.current_concurrency,
  }
}

export function getCPAHelperPluginPolicy(): Promise<CPAHelperPluginPolicy> {
  return apiClient.get<CPAHelperPluginPolicy>('/cpa-helper-plugin/policy').then(normalizeCPAHelperPluginPolicy)
}

export function updateCPAHelperPluginPolicy(policy: CPAHelperPluginPolicy): Promise<CPAHelperPluginPolicy> {
  const payload = { ...policy }
  delete payload.helper_key_metadata
  return apiClient.put<CPAHelperPluginPolicy>('/cpa-helper-plugin/policy', payload).then(normalizeCPAHelperPluginPolicy)
}

export function getCPAHelperPluginKeyPolicy(apiKeyHash: string): Promise<CPAHelperPluginKeyPolicy> {
  return apiClient.get<CPAHelperPluginKeyPolicy>(`/cpa-helper-plugin/policy/key/${encodeURIComponent(apiKeyHash)}`).then(normalizeKeyPolicy)
}

export function updateCPAHelperPluginKeyPolicy(apiKeyHash: string, policyRevision: number, key: CPAHelperPluginKey): Promise<CPAHelperPluginKeyPolicy> {
  return apiClient.put<CPAHelperPluginKeyPolicy>(`/cpa-helper-plugin/policy/key/${encodeURIComponent(apiKeyHash)}`, {
    policy_revision: policyRevision,
    key,
  }).then(normalizeKeyPolicy)
}

export function getCPAHelperPluginDirectory(apiKeyHash?: string): Promise<CPAHelperPluginDirectory> {
  return apiClient.get<CPAHelperPluginDirectory>('/cpa-helper-plugin/directory', { api_key_hash: apiKeyHash })
}

export function getCPAHelperResponseModelConfig(): Promise<CPAHelperResponseModelConfig> {
  return apiClient.get<CPAHelperResponseModelConfig>('/cpa-helper-plugin/response-model')
}

export function updateCPAHelperResponseModelConfig(config: CPAHelperResponseModelConfig): Promise<CPAHelperResponseModelConfig> {
  return apiClient.put<CPAHelperResponseModelConfig>('/cpa-helper-plugin/response-model', config)
}

export interface CPAHelperPluginRuleSet {
  groups: CPAHelperPluginGroup[]
  keys: CPAHelperPluginKey[]
  policy_revision: number
}
