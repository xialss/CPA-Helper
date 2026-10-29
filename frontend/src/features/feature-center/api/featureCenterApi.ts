import { apiClient } from '@/shared/api/apiClient'
import { localize } from '@/shared/i18n'
import type { FeatureCenterSnapshot } from '@/shared/types/api'

export function parseFeatureSnapshot(raw: unknown): FeatureCenterSnapshot {
  if (!raw || typeof raw !== 'object') throw new Error(localize('功能状态响应无效', 'Invalid feature state response'))
  const value = raw as Record<string, unknown>
  if (!Number.isSafeInteger(value.revision) || Number(value.revision) < 1 || typeof value.image_studio_enabled !== 'boolean' || typeof value.ai_radar_enabled !== 'boolean') {
    throw new Error(localize('功能状态响应无效', 'Invalid feature state response'))
  }
  return { revision: Number(value.revision), image_studio_enabled: value.image_studio_enabled, ai_radar_enabled: value.ai_radar_enabled }
}

export async function getFeatures(signal?: AbortSignal): Promise<FeatureCenterSnapshot> {
  return parseFeatureSnapshot(await apiClient.get<unknown>('/features', {}, signal))
}

export async function getFeatureSettings(signal?: AbortSignal): Promise<FeatureCenterSnapshot> {
  return parseFeatureSnapshot(await apiClient.get<unknown>('/settings/features', {}, signal))
}

export async function updateFeatureSettings(payload: FeatureCenterSnapshot): Promise<FeatureCenterSnapshot> {
  return parseFeatureSnapshot(await apiClient.put<unknown>('/settings/features', payload))
}
