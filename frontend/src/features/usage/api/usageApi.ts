import { apiClient } from '@/shared/api/apiClient'
import type {
  TrendPoint,
  UsageDistributionsResponse,
  UsageFilters,
  UsageOptionsResponse,
  UsageOverviewResponse,
  UsageRankingsResponse,
  UsageRecordDetail,
  UsageRecordsResponse,
  UsageSummary,
} from '@/shared/types/api'

function filtersToParams(
  filters: UsageFilters,
): Record<string, string | number | boolean | undefined> {
  return {
    scope: filters.scope,
    start: filters.start,
    end: filters.end,
    user_id: filters.user_id,
    api_key_description: filters.api_key_description,
    provider: filters.provider,
    model: filters.model,
    endpoint: filters.endpoint,
    failed: filters.failed,
    request_id: filters.request_id,
  }
}

export function getUsageSummary(filters: UsageFilters): Promise<UsageSummary> {
  return apiClient.get<UsageSummary>('/usage/summary', filtersToParams(filters))
}

export function getUsageTrends(filters: UsageFilters): Promise<TrendPoint[]> {
  return apiClient.get<TrendPoint[]>('/usage/trends', filtersToParams(filters))
}

export function getUsageRankings(
  filters: UsageFilters,
  groupBy: 'api_key_description' | 'model' | 'user',
): Promise<UsageRankingsResponse> {
  return apiClient.get<UsageRankingsResponse>('/usage/rankings', {
    ...filtersToParams(filters),
    group_by: groupBy,
  })
}

export function getUsageDistributions(
  filters: UsageFilters,
): Promise<UsageDistributionsResponse> {
  return apiClient.get<UsageDistributionsResponse>('/usage/distributions', filtersToParams(filters))
}

export function getUsageOverview(filters: UsageFilters): Promise<UsageOverviewResponse> {
  return apiClient.get<UsageOverviewResponse>('/usage/overview', filtersToParams(filters))
}

export function getUsageRecords(
  filters: UsageFilters,
  page: number,
  pageSize: number,
): Promise<UsageRecordsResponse> {
  return apiClient.get<UsageRecordsResponse>('/usage/records', {
    ...filtersToParams(filters),
    page,
    page_size: pageSize,
  })
}

export function getUsageRecord(
  recordId: number,
  scope?: UsageFilters['scope'],
): Promise<UsageRecordDetail> {
  return apiClient.get<UsageRecordDetail>(`/usage/records/${recordId}`, { scope })
}

export function getUsageOptions(scope?: UsageFilters['scope']): Promise<UsageOptionsResponse> {
  return apiClient.get<UsageOptionsResponse>('/usage/options', { scope })
}
