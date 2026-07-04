import { apiClient } from '@/shared/api/apiClient'
import type {
  AIProviderActionPayload,
  AIProviderActionResponse,
  AIProviderBrand,
  AIProviderItem,
  AIProvidersResponse,
} from '@/shared/types/api'

function providerPath(brand: AIProviderBrand, index: number): string {
  return `/ai-providers/${encodeURIComponent(brand)}/${index}`
}

function providerSelectorQuery(provider: AIProviderItem): string {
  const query = new URLSearchParams()
  if (provider.identity_hash) {
    query.set('identity_hash', provider.identity_hash)
  }
  if (provider.api_key_hash) {
    query.set('api_key_hash', provider.api_key_hash)
  }
  if (provider.name) {
    query.set('name', provider.name)
  }
  if (provider.base_url) {
    query.set('base_url', provider.base_url)
  }
  const text = query.toString()
  return text ? `?${text}` : ''
}

export function listAIProviders(): Promise<AIProvidersResponse> {
  return apiClient.get<AIProvidersResponse>('/ai-providers')
}

export function createAIProvider(brand: AIProviderBrand, payload: AIProviderItem): Promise<AIProvidersResponse> {
  return apiClient.post<AIProvidersResponse>(`/ai-providers/${encodeURIComponent(brand)}`, payload)
}

export function updateAIProvider(provider: AIProviderItem): Promise<AIProvidersResponse> {
  return apiClient.put<AIProvidersResponse>(providerPath(provider.brand, provider.index), provider)
}

export function deleteAIProvider(provider: AIProviderItem): Promise<AIProvidersResponse> {
  return apiClient.delete(`${providerPath(provider.brand, provider.index)}${providerSelectorQuery(provider)}`) as unknown as Promise<AIProvidersResponse>
}

export function discoverAIProviderModels(payload: AIProviderActionPayload): Promise<AIProviderActionResponse> {
  return apiClient.post<AIProviderActionResponse>('/ai-providers/discover-models', payload)
}

export function testAIProvider(payload: AIProviderActionPayload): Promise<AIProviderActionResponse> {
  return apiClient.post<AIProviderActionResponse>('/ai-providers/test', payload)
}
