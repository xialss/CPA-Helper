import { apiClient } from '@/shared/api/apiClient'
import type { AvailableModelsResponse } from '@/shared/types/api'

export function listAvailableModels(signal?: AbortSignal): Promise<AvailableModelsResponse> {
  return apiClient.get<AvailableModelsResponse>('/account/models', {}, signal)
}
