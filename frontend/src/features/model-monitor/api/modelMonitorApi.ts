import { apiClient } from '@/shared/api/apiClient'
import type {
  ModelMonitorProxySettings,
  ModelMonitorProxySettingsPayload,
  ModelMonitorResponse,
} from '@/shared/types/api'

export function getModelMonitor(): Promise<ModelMonitorResponse> {
  return apiClient.get<ModelMonitorResponse>('/model-monitor')
}

export function getModelMonitorProxySettings(): Promise<ModelMonitorProxySettings> {
  return apiClient.get<ModelMonitorProxySettings>('/model-monitor/proxy')
}

export function updateModelMonitorProxySettings(
  payload: ModelMonitorProxySettingsPayload,
): Promise<ModelMonitorProxySettings> {
  return apiClient.put<ModelMonitorProxySettings>('/model-monitor/proxy', payload)
}
