import { apiClient } from '@/shared/api/apiClient'
import type {
  AIRadarHistoryResponse,
  AIRadarHistoryWindow,
  AIRadarResponse,
} from '@/shared/types/api'

export function getAIRadar(): Promise<AIRadarResponse> {
  return apiClient.get<AIRadarResponse>('/ai-radar')
}

export function getAIRadarHistory(window: AIRadarHistoryWindow): Promise<AIRadarHistoryResponse> {
  return apiClient.get<AIRadarHistoryResponse>('/ai-radar/history', { window })
}

/** Administrator-only. Bypasses the shared cache and forces an upstream fetch. */
export function refreshAIRadar(): Promise<AIRadarResponse> {
  return apiClient.post<AIRadarResponse>('/ai-radar/refresh')
}
