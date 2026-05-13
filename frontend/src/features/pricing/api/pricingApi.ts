import { apiClient } from '@/shared/api/apiClient'
import type { ModelPrice, ModelPricePayload, ModelPriceSyncResponse } from '@/shared/types/api'

export function listModelPrices(): Promise<ModelPrice[]> {
  return apiClient.get<ModelPrice[]>('/model-prices')
}

export function createModelPrice(payload: ModelPricePayload): Promise<ModelPrice> {
  return apiClient.post<ModelPrice>('/model-prices', payload)
}

export function updateModelPrice(id: number, payload: ModelPricePayload): Promise<ModelPrice> {
  return apiClient.put<ModelPrice>(`/model-prices/${id}`, payload)
}

export function deleteModelPrice(id: number): Promise<void> {
  return apiClient.delete(`/model-prices/${id}`)
}

export function syncLitellmModelPrices(): Promise<ModelPriceSyncResponse> {
  return apiClient.post<ModelPriceSyncResponse>('/model-prices/sync/litellm')
}
