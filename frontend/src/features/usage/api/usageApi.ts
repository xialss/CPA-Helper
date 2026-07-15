import { apiClient } from '@/shared/api/apiClient'
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
} from '@/shared/types/api'

interface UsageOverviewRankingSorts {
  primary: UsageRankingSort
  model: UsageRankingSort
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
  rankingSorts?: UsageOverviewRankingSorts,
): Promise<UsageOverviewResponse> {
  return apiClient.get<UsageOverviewResponse>('/usage/overview', {
    ...filtersToParams(filters),
    primary_ranking_sort: rankingSorts?.primary,
    model_ranking_sort: rankingSorts?.model,
  })
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
