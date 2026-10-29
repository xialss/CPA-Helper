import { apiClient } from '@/shared/api/apiClient'
import type {
  AIRadarHistoryResponse,
  AIRadarHistoryWindow,
  AIRadarResponse,
} from '@/shared/types/api'

export function getAIRadar(signal?: AbortSignal): Promise<AIRadarResponse> {
  return apiClient.get<AIRadarResponse>('/ai-radar', {}, signal)
}

export function getAIRadarHistory(window: AIRadarHistoryWindow, signal?: AbortSignal): Promise<AIRadarHistoryResponse> {
  return apiClient.get<AIRadarHistoryResponse>('/ai-radar/history', { window }, signal)
}

/** Administrator-only. Bypasses the shared cache and forces an upstream fetch. */
export function refreshAIRadar(signal?: AbortSignal): Promise<AIRadarResponse> {
  return apiClient.post<AIRadarResponse>('/ai-radar/refresh', undefined, signal)
}
