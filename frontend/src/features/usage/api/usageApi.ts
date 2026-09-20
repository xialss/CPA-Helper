import { apiClient, isApiRequestError } from '@/shared/api/apiClient'
import type {
  TrendPoint,
  UsageDistributionsResponse,
  UsageFilters,
  UsageOptionsResponse,
  UsageOverviewResponse,
  UsageRankingSort,
  UsageRankingsResponse,
  UsageRecordDetail,
  UsageRecordsResponse,
  UsageSummary,
  UsageModelAuditResponse,
  UsageLogPreview,
} from '@/shared/types/api'

export function getUsageModelAudit(id: number, signal?: AbortSignal): Promise<UsageModelAuditResponse> {
  return apiClient.get<UsageModelAuditResponse>(`/usage/records/${id}/model-audit`, {}, signal)
}

export function auditUsageModel(id: number, refresh: boolean, acknowledgement?: string, signal?: AbortSignal): Promise<UsageModelAuditResponse> {
  return apiClient.post<UsageModelAuditResponse>(`/usage/records/${id}/model-audit`, {
    refresh, acknowledge_source: acknowledgement,
  }, signal)
}

export function getUsageLogPreview(id: number, acknowledgement?: string, signal?: AbortSignal): Promise<UsageLogPreview> {
  return apiClient.get<UsageLogPreview>(`/usage/records/${id}/cpa-log`, { acknowledge_source: acknowledgement }, signal)
}

export function downloadUsageLog(id: number, acknowledgement?: string, signal?: AbortSignal): Promise<Blob> {
  return apiClient.getBlob(`/usage/records/${id}/cpa-log`, { download: 1, acknowledge_source: acknowledgement }, signal)
}

interface UsageOverviewRequestOptions {
  primary?: UsageRankingSort
  model?: UsageRankingSort
  includeOptions?: boolean
}

interface UsageRecordsRequestOptions {
  range?: 'all' | undefined
}

export function isUsageRecordsLegacyRangeValidationError(error: unknown): boolean {
  return (
    isApiRequestError(error) &&
    error.status === 422 &&
    error.code === 'validation_error'
  )
}

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
    source_key: filters.source_key,
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
  sortBy: UsageRankingSort = 'tokens',
): Promise<UsageRankingsResponse> {
  return apiClient.get<UsageRankingsResponse>('/usage/rankings', {
    ...filtersToParams(filters),
    group_by: groupBy,
    sort_by: sortBy,
  })
}

export function getUsageDistributions(
  filters: UsageFilters,
): Promise<UsageDistributionsResponse> {
  return apiClient.get<UsageDistributionsResponse>('/usage/distributions', filtersToParams(filters))
}

export function getUsageOverview(
  filters: UsageFilters,
  options?: UsageOverviewRequestOptions,
): Promise<UsageOverviewResponse> {
  return apiClient.get<UsageOverviewResponse>('/usage/overview', {
    ...filtersToParams(filters),
    primary_ranking_sort: options?.primary,
    model_ranking_sort: options?.model,
    include_options: options?.includeOptions,
  })
}

export function getUsageRecords(
  filters: UsageFilters,
  page: number,
  pageSize: number,
  options?: UsageRecordsRequestOptions,
): Promise<UsageRecordsResponse> {
  return apiClient.get<UsageRecordsResponse>('/usage/records', {
    ...filtersToParams(filters),
    page,
    page_size: pageSize,
    range: options?.range,
  })
}

export function getUsageRecord(
  recordId: number,
  scope?: UsageFilters['scope'],
): Promise<UsageRecordDetail> {
  return apiClient.get<UsageRecordDetail>(`/usage/records/${recordId}`, { scope })
}

type UsageOptionsFilters = Pick<UsageFilters, 'scope' | 'start' | 'end'>

export function getUsageOptions(
  filters: UsageOptionsFilters = {},
): Promise<UsageOptionsResponse> {
  return apiClient.get<UsageOptionsResponse>('/usage/options', {
    scope: filters.scope,
    start: filters.start,
    end: filters.end,
  })
}
