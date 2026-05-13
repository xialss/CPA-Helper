import { apiClient } from '@/shared/api/apiClient'
import type { CollectorStatus, SettingsResponse, SettingsUpdatePayload } from '@/shared/types/api'

export function getSettings(): Promise<SettingsResponse> {
  return apiClient.get<SettingsResponse>('/settings')
}

export function updateSettings(payload: SettingsUpdatePayload): Promise<SettingsResponse> {
  return apiClient.put<SettingsResponse>('/settings', payload)
}

export function getCollectorStatus(): Promise<CollectorStatus> {
  return apiClient.get<CollectorStatus>('/collector/status')
}
