import { apiClient } from '@/shared/api/apiClient'
import type {
  ModelPrice,
  ModelPriceCatalogResponse,
  ModelPriceLibraryConflict,
  ModelPriceLibraryConflictPromotePayload,
  ModelPricePayload,
  ModelPriceSyncResponse,
  PriorityMultiplierPayload,
  ModelPriceChannelAlias,
  ModelPriceChannelAliasPayload,
  ModelPriceVersion,
  PriceTimeRule,
  LiteLLMSyncPayload,
  LiteLLMModelOptionsResponse,
  TimePricingBatchPayload,
  TimePricingBatchResult,
} from '@/shared/types/api'

export function listModelPrices(): Promise<ModelPrice[]> {
  return apiClient.get<ModelPrice[]>('/model-prices')
}

export function listModelPriceCatalog(): Promise<ModelPriceCatalogResponse> {
  return apiClient.get<ModelPriceCatalogResponse>('/model-prices/catalog')
}

export function createModelPrice(payload: ModelPricePayload): Promise<ModelPrice> {
  return apiClient.post<ModelPrice>('/model-prices', payload)
}

export function updateModelPrice(id: number, payload: ModelPricePayload): Promise<ModelPrice> {
  return apiClient.put<ModelPrice>(`/model-prices/${id}`, payload)
}

export function listModelPriceLibraryConflicts(): Promise<ModelPriceLibraryConflict[]> {
  return apiClient.get<ModelPriceLibraryConflict[]>('/model-prices/library-conflicts')
}

export function promoteModelPriceLibraryConflict(
  id: number,
  payload: ModelPriceLibraryConflictPromotePayload,
): Promise<ModelPrice> {
  return apiClient.put<ModelPrice>(`/model-prices/library-conflicts/${id}/promote`, payload)
}

export function replaceActiveModelPriceLibraryConflict(id: number): Promise<ModelPrice> {
  return apiClient.put<ModelPrice>(`/model-prices/library-conflicts/${id}/replace-active`, {})
}

export function deleteModelPriceLibraryConflict(id: number): Promise<void> {
  return apiClient.delete(`/model-prices/library-conflicts/${id}`)
}

export function updateModelPricePriorityMultiplier(
  id: number,
  payload: PriorityMultiplierPayload,
): Promise<ModelPrice> {
  return apiClient.put<ModelPrice>(`/model-prices/${id}/priority-multiplier`, payload)
}

export function deleteModelPrice(id: number): Promise<void> {
  return apiClient.delete(`/model-prices/${id}`)
}

export function listModelPriceChannelAliases(): Promise<ModelPriceChannelAlias[]> {
  return apiClient.get<ModelPriceChannelAlias[]>('/model-prices/channel-aliases')
}

export function updateModelPriceChannelAlias(payload: ModelPriceChannelAliasPayload): Promise<ModelPriceChannelAlias> {
  return apiClient.put<ModelPriceChannelAlias>('/model-prices/channel-aliases', payload)
}

export function listLitellmModelOptions(): Promise<LiteLLMModelOptionsResponse> {
  return apiClient.get<LiteLLMModelOptionsResponse>('/model-prices/sync/litellm/options')
}

export function syncLitellmModelPrices(payload: LiteLLMSyncPayload): Promise<ModelPriceSyncResponse> {
  return apiClient.post<ModelPriceSyncResponse>('/model-prices/sync/litellm', payload)
}

export function fetchModelPriceVersions(id: number): Promise<ModelPriceVersion[]> {
  return apiClient.get<ModelPriceVersion[]>(`/model-prices/${id}/versions`)
}

export function getDeepSeekTemplate(): Promise<PriceTimeRule> {
  return apiClient.get<PriceTimeRule>('/model-prices/deepseek-template')
}

export function saveDeepSeekTemplate(rule: PriceTimeRule): Promise<PriceTimeRule> {
  return apiClient.put<PriceTimeRule>('/model-prices/deepseek-template', rule)
}

export function batchDeepSeekTemplate(payload: TimePricingBatchPayload, apply = false): Promise<TimePricingBatchResult> {
  return apiClient.post<TimePricingBatchResult>(`/model-prices/deepseek-template/${apply ? 'apply' : 'preview'}`, payload)
}
