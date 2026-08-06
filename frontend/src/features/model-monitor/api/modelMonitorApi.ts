import { apiClient } from '@/shared/api/apiClient'
import type {
  ModelMonitorProxySettings,
  ModelMonitorProxySettingsPayload,
  ModelMonitorResponse,
  ModelMonitorSettings,
  ModelMonitorSettingsPayload,
  ModelMonitorSourceStatus,
} from '@/shared/types/api'

export function getModelMonitor(): Promise<ModelMonitorResponse> {
  return apiClient.get<ModelMonitorResponse>('/model-monitor')
}

export function getModelMonitorSettings(): Promise<ModelMonitorSettings> {
  return apiClient.get<ModelMonitorSettings>('/model-monitor/settings')
}

export function updateModelMonitorSettings(
  payload: ModelMonitorSettingsPayload,
): Promise<ModelMonitorSettings> {
  return apiClient.put<ModelMonitorSettings>('/model-monitor/settings', payload)
}

export function getModelMonitorSource(id: string): Promise<ModelMonitorSourceStatus> {
  return apiClient.get<ModelMonitorSourceStatus>(`/model-monitor/sources/${encodeURIComponent(id)}`)
}

export function getModelMonitorProxySettings(): Promise<ModelMonitorProxySettings> {
  return apiClient.get<ModelMonitorProxySettings>('/model-monitor/proxy')
}

export function updateModelMonitorProxySettings(
  payload: ModelMonitorProxySettingsPayload,
): Promise<ModelMonitorProxySettings> {
  return apiClient.put<ModelMonitorProxySettings>('/model-monitor/proxy', payload)
}
