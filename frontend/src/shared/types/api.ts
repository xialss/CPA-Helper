export type ThemePreference = 'system' | 'light' | 'dark'

export interface AuthUser {
  id: number
  username: string
  is_admin: boolean
  is_super_admin: boolean
  must_change_password: boolean
}

export interface LoginPayload {
  username: string
  password: string
}

export interface ChangeCredentialsPayload {
  password: string
  current_password?: string | undefined
}

export interface SetupState {
  setup_required: boolean
}

export interface FirstAdminSetupPayload {
  username: string
  password: string
  nickname: string
}

export interface SettingsResponse {
  cliaproxy_url: string
  model_request_url: string
  management_key: string
  management_key_set: boolean
  collector_enabled: boolean
  queue_name: string
  batch_size: number
  poll_interval_seconds: number
  retry_interval_seconds: number
}

export interface SettingsUpdatePayload {
  cliaproxy_url?: string
  model_request_url?: string
  management_key?: string
  collector_enabled?: boolean
  queue_name?: string
  batch_size?: number
  poll_interval_seconds?: number
  retry_interval_seconds?: number
}

export interface ModelRequestGuide {
  model_request_url: string
  openai_base_url: string
  chat_completions_url: string
}

export type ModelRequestEndpoint = 'chat_completions' | 'responses' | 'claude_messages'

export interface ModelRequestTestPayload {
  api_key_hash: string
  endpoint: ModelRequestEndpoint
  model: string
  message: string
}

export interface ModelRequestTestResponse {
  endpoint: ModelRequestEndpoint
  model: string
  reply: string
  status_code: number
  duration_ms: number
  usage?: Record<string, unknown>
}

export type AIProviderBrand = 'gemini' | 'codex' | 'claude' | 'openai_compatibility' | 'vertex' | 'xai'

export interface AIProviderModel {
  name: string
  alias?: string
  display_name?: string | null
  max_context_length?: number | null
  force_mapping?: boolean | null
  is_compat?: boolean | null
  image?: boolean | null
  thinking?: Record<string, unknown> | null
}

export interface AIProviderHeader {
  name: string
  value: string
}

export interface AIProviderKeyEntry {
  api_key?: string
  api_key_hash?: string | null
  api_key_masked?: string | null
  proxy_url?: string | null
}

export interface AIProviderCloak {
  mode?: string | null
  strict_mode?: boolean | null
  sensitive_words: string[]
  cache_user_id?: boolean | null
}

export interface AIProviderRecentRequestBucket {
  time?: string | null
  success: number
  failed: number
}

export interface AIProviderItem {
  brand: AIProviderBrand
  brand_label: string
  index: number
  identity_hash: string
  api_key?: string
  api_key_hash?: string | null
  api_key_masked?: string | null
  auth_index?: string | null
  name?: string | null
  local_alias: string
  channel_key: string
  priority?: number | null
  weight?: number | null
  disabled?: boolean | null
  prefix?: string | null
  base_url?: string | null
  original_base_url?: string | null
  proxy_url?: string | null
  models: AIProviderModel[]
  headers: AIProviderHeader[]
  excluded_models: string[]
  disable_cooling?: boolean | null
  websockets?: boolean | null
  rebuild_mid_system_message?: boolean | null
  experimental_cch_signing?: boolean | null
  cloak?: AIProviderCloak | null
  api_key_entries: AIProviderKeyEntry[]
  recent_success: number
  recent_failure: number
  recent_status: 'healthy' | 'failing' | 'unknown' | 'unavailable' | string
  recent_status_available: boolean
  recent_requests: AIProviderRecentRequestBucket[]
  metadata?: Record<string, unknown>
}

export type AIProviderPayload = Omit<AIProviderItem, 'channel_key' | 'local_alias'>

export interface AIProviderUsage {
  provider?: string
  api_key_hash?: string | null
  api_key_masked?: string | null
  auth_index?: string | null
  name?: string | null
  base_url?: string | null
  success_count: number
  failure_count: number
  total_count: number
  last_seen?: string | null
  identity_hash?: string | null
  upstream_label?: string | null
  recent_requests: AIProviderRecentRequestBucket[]
  recent_requests_available: boolean
}

export interface AIProviderSummary {
  total: number
  gemini: number
  codex: number
  claude: number
  openai_compatibility: number
  vertex: number
  xai: number
  recent_success: number
  recent_failure: number
}

export interface AIProvidersResponse {
  providers: AIProviderItem[]
  usage: AIProviderUsage[]
  summary: AIProviderSummary
  usage_error?: string | null
}

/** Non-secret optimistic-concurrency reference used when reordering a provider list. */
export interface AIProviderOrderItem {
  index: number
  identity_hash?: string | null
  api_key_hash?: string | null
  name?: string | null
  base_url?: string | null
}

export interface AIProviderActionPayload {
  brand: AIProviderBrand
  provider: AIProviderPayload
  model?: string
  message?: string
}

export interface AIProviderActionResponse {
  ok: boolean
  status: string
  status_code: number
  duration_ms: number
  models?: AIProviderModel[]
  reply?: string
  error?: string
}

export interface CollectorStatus {
  enabled: boolean
  running: boolean
  queue_name: string
  batch_size: number
  poll_interval_seconds: number
  retry_interval_seconds: number
  last_poll_at: string | null
  last_success_at: string | null
  last_error: string | null
  remote_enabled: boolean | null
  records_collected: number
}

export interface CodexKeeperPriorityRule {
  account_type: string
  priority: number
}

export interface CodexKeeperSettings {
  cliaproxy_url: string
  management_key_set: boolean
  schedule_cron: string
  next_run_times: string[]
  quota_threshold: number
  usage_timeout_seconds: number
  cpa_timeout_seconds: number
  max_retries: number
  worker_threads: number
  conditional_refresh_interval_seconds: number
  account_refresh_cache_minutes: number
  dry_run: boolean
  enable_credential_websockets: boolean
  auto_start_daemon: boolean
  priority_rules: CodexKeeperPriorityRule[]
}

export interface CodexKeeperSettingsUpdatePayload {
  schedule_cron?: string
  quota_threshold?: number
  usage_timeout_seconds?: number
  cpa_timeout_seconds?: number
  max_retries?: number
  worker_threads?: number
  conditional_refresh_interval_seconds?: number
  account_refresh_cache_minutes?: number
  dry_run?: boolean
  enable_credential_websockets?: boolean
  auto_start_daemon?: boolean
  priority_rules?: CodexKeeperPriorityRule[]
}

export interface CodexKeeperCronPreviewPayload {
  schedule_cron: string
}

export interface CodexKeeperCronPreviewResponse {
  schedule_cron: string
  next_run_times: string[]
}

export interface CodexKeeperStats {
  total: number
  healthy: number
  status_disabled: number
  status_enabled: number
  priority_degraded: number
  priority_restored: number
  skipped: number
  network_error: number
}

export interface CodexKeeperStatus {
  running: boolean
  running_modes: string[]
  daemon_running: boolean
  state: string
  detail: string
  mode: string | null
  last_started_at: string | null
  last_finished_at: string | null
  stats: CodexKeeperStats
  logs: string[]
}

export interface CodexKeeperQuotaWindowUsage {
  window_start: string
  window_end: string
  reset_at: string
  window_seconds: number
  records: number
  success_records: number
  failed_records: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  total_tokens: number
  estimated_cost_usd: number
  unpriced_records: number
  stale: boolean
  window_source: string
}

export interface CodexKeeperAccount {
  name: string
  email: string | null
  account_type: string | null
  disabled: boolean
  priority: number | null
  primary_used_percent: number | null
  secondary_used_percent: number | null
  primary_reset_at: string | null
  secondary_reset_at: string | null
  primary_window_seconds: number | null
  secondary_window_seconds: number | null
  primary_window_usage: CodexKeeperQuotaWindowUsage | null
  secondary_window_usage: CodexKeeperQuotaWindowUsage | null
  quota_threshold: number | null
  last_status_code: number | null
  last_error: string | null
  latest_action: string | null
  last_checked_at: string | null
  last_healthy_at: string | null
}

export interface CodexKeeperAccountsResponse {
  items: CodexKeeperAccount[]
}

export interface CodexKeeperBulkDeletePayload {
  auth_names: string[]
}

export interface CodexKeeperRefreshPayload {
  auth_names: string[]
}

export interface CodexKeeperBulkDeleteFailure {
  name: string
  message: string
}

export interface CodexKeeperBulkDeleteResponse {
  status: string
  deleted: string[]
  failed: CodexKeeperBulkDeleteFailure[]
}

export interface UsageFilters {
  scope?: 'admin' | 'account' | undefined
  start?: string | undefined
  end?: string | undefined
  user_id?: number | undefined
  api_key_description?: string | undefined
  provider?: string | undefined
  model?: string | undefined
  source_key?: string | undefined
  endpoint?: string | undefined
  failed?: boolean | undefined
  request_id?: string | undefined
}

export interface UsageSummary {
  start: string
  end: string
  total_records: number
  failed_records: number
  success_records: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  normal_input_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  total_tokens: number
  average_ttft_ms: number | null
  estimated_cost_usd: number
  unpriced_records: number
}

export interface TrendPoint {
  bucket: string
  records: number
  failed_records: number
  total_tokens: number
  estimated_cost_usd: number
}

export interface RankingItem {
  key: string
  label: string
  records: number
  failed_records: number
  total_tokens: number
  estimated_cost_usd: number
  user_id: number | null
  api_key_description: string | null
}

export type UsageRankingSort = 'tokens' | 'cost' | 'records'

export interface UsageRankingsResponse {
  group_by: 'api_key_description' | 'model' | 'user'
  items: RankingItem[]
}

export interface DistributionItem {
  key: string
  label: string
  records: number
  total_tokens: number
  estimated_cost_usd: number
}

export interface UsageChannelCostItem {
  key: string
  label: string
  label_fallback: boolean
  channel_auth_type: 'apikey' | 'oauth'
  channel_brand: string
  estimated_cost_usd: number
}

export interface UsageDistributionsResponse {
  providers: DistributionItem[]
  models: DistributionItem[]
  endpoints: DistributionItem[]
  channel_costs: UsageChannelCostItem[]
}

export interface UsageOptionsResponse {
  users: RankingItem[]
  api_key_descriptions: RankingItem[]
  providers: string[]
  models: string[]
  sources: UsageSourceOption[]
  endpoints: string[]
}

export interface UsageSourceOption {
  key: string
  label: string
}

export interface UsageOverviewResponse {
  summary: UsageSummary
  trends: TrendPoint[]
  user_ranking: UsageRankingsResponse
  api_key_description_ranking?: UsageRankingsResponse
  api_key_ranking?: UsageRankingsResponse
  model_ranking: UsageRankingsResponse
  distributions: UsageDistributionsResponse
  options?: UsageOptionsResponse
}

export type ModelPriceBillingUnit = 'token' | 'request'

export interface UsageTokenCostBreakdownItem {
  kind: 'input' | 'cache_read' | 'cache_creation' | 'output'
  tokens: number
  usd_per_million: number
  subtotal_usd: number
}

export interface UsageRequestCostBreakdownItem {
  kind: 'request'
  requests: number
  usd_per_request: number
  subtotal_usd: number
}

export type UsageCostBreakdownItem =
  | UsageTokenCostBreakdownItem
  | UsageRequestCostBreakdownItem

export interface UsageCostBreakdown {
  billing_unit: ModelPriceBillingUnit
  normal_input_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  output_tokens: number
  items: UsageCostBreakdownItem[]
  total_usd: number
  unpriced: boolean
  unpriced_reason: string | null
  tier_multiplier?: number
  context_input_tokens: number
  long_context_threshold_tokens: number | null
  long_context_applied: boolean
}

export interface UsageRecordListItem {
  id: number
  timestamp: string
  api_key_description: string | null
  user_id: number | null
  user_label: string
  provider: string | null
  model: string | null
  service_tier: string | null
  reasoning_effort: string | null
  endpoint: string | null
  source: string | null
  source_label: string | null
  request_id: string | null
  auth_index: string | null
  auth: string | null
  latency_ms: number | null
  ttft_ms: number | null
  failed: boolean
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  total_tokens: number
  estimated_cost_usd: number
  unpriced: boolean
  cost_breakdown: UsageCostBreakdown
}

export interface UsageRecordsResponse {
  items: UsageRecordListItem[]
  total: number
  page: number
  page_size: number
  start: string
  end: string
}

export interface UsageRecordDetail extends UsageRecordListItem {
  raw_json: Record<string, unknown> | unknown[] | string
}

export interface ModelPriceLongContext {
  threshold_input_tokens: number
  input_usd_per_million: number
  output_usd_per_million: number
  cache_read_usd_per_million: number
  cache_creation_usd_per_million: number
}

export type ModelPriceLongContextRateField = Exclude<keyof ModelPriceLongContext, 'threshold_input_tokens'>

export interface PriceTimeRule {
  timezone: string
  peak_windows: { weekdays: number[]; start: string; end: string }[]
  offpeak_mode: 'multiplier' | 'explicit'
  offpeak_multiplier: number
  offpeak_rates?: Pick<ModelPriceLongContext, 'input_usd_per_million' | 'output_usd_per_million' | 'cache_read_usd_per_million' | 'cache_creation_usd_per_million'>
  long_context_multiplier: number
}

export interface ModelPrice {
  time_pricing: PriceTimeRule | null
  id: number
  provider: string
  model: string
  price_scope: 'library' | 'channel'
  channel_auth_type: 'apikey' | 'oauth' | null
  channel_brand: string | null
  channel_key: string | null
  input_usd_per_million: number
  output_usd_per_million: number
  cache_read_usd_per_million: number
  cache_creation_usd_per_million: number
  request_usd: number | null
  priority_multiplier: number | null
  long_context: ModelPriceLongContext | null
  preserved_long_context: ModelPriceLibraryConflictLongContext | null
  billing_unit: ModelPriceBillingUnit
  source: 'manual' | 'litellm' | string
  source_model: string | null
  auto_synced: boolean
  last_synced_at: string | null
  updated_at: string
}

export interface ModelPricePayload {
  time_pricing?: PriceTimeRule | null
  time_pricing_set?: boolean
  provider: string
  model: string
  price_scope: 'library' | 'channel'
  channel_auth_type: 'apikey' | 'oauth' | null
  channel_brand: string | null
  channel_key: string | null
  channel_identity_hash: string | null
  billing_unit: ModelPriceBillingUnit
  input_usd_per_million: number
  output_usd_per_million: number
  cache_read_usd_per_million: number
  cache_creation_usd_per_million: number
  request_usd: number | null
  long_context: ModelPriceLongContext | null
  preserve_invalid_long_context?: boolean
  correct_latest_version?: boolean
}

export interface ModelPriceLibraryConflict {
  original_id: number
  selected_price_id: number
  conflict_reason: string
  price: ModelPrice
  archived_long_context: ModelPriceLibraryConflictLongContext | null
}

export interface ModelPriceLibraryConflictLongContext {
  threshold_input_tokens: number | null
  input_usd_per_million: number | null
  output_usd_per_million: number | null
  cache_read_usd_per_million: number | null
  cache_creation_usd_per_million: number | null
  non_finite_fields?: Partial<Record<ModelPriceLongContextRateField, 'NaN' | '+Inf' | '-Inf'>>
}

export interface ModelPriceLibraryConflictPromotePayload {
  provider: string
  model: string
}

export interface PriorityMultiplierPayload {
  priority_multiplier: number
  correct_latest_version?: boolean
}

export interface ModelPriceSyncResponse {
  source_url: string
  total_entries: number
  imported: number
  created: number
  updated: number
  unchanged: number
  skipped_manual: number
  skipped_invalid: number
  payload_bytes: number
  selected_entries: number
  download_ms: number
  parse_ms: number
  transaction_ms: number
}

export interface ModelPriceChannelAlias {
  auth_type: 'apikey'
  channel_brand: AIProviderBrand
  channel_key: string
  label: string
  channel_identity_hash?: string
}

export interface ModelPriceChannelAliasPayload {
  auth_type: 'apikey'
  channel_brand: AIProviderBrand
  channel_key: string
  label: string
  channel_identity_hash: string
}

export interface ModelPriceVersion {
  time_pricing: PriceTimeRule | null
  id: number
  effective_at: string
  baseline: boolean
  input_usd_per_million: number
  output_usd_per_million: number
  cache_read_usd_per_million: number
  cache_creation_usd_per_million: number
  request_usd: number | null
  billing_unit: ModelPriceBillingUnit
  priority_multiplier: number | null
  long_context: ModelPriceLongContext | null
  preserved_long_context: ModelPriceLibraryConflictLongContext | null
}

export interface LiteLLMSyncPayload {
  models: string[]
}

export interface LiteLLMModelOption {
  model: string
  price_model: string
  provider: string
  matched_current: boolean
}

export interface LiteLLMModelOptionsResponse {
  models: LiteLLMModelOption[]
  payload_bytes: number
  total_entries: number
  channel_error: string | null
  oauth_channel_error: string | null
}

export interface TimePricingBatchPayload {
  price_ids: number[]
  rule: PriceTimeRule | null
  effective_at?: string
}

export interface TimePricingBatchItem {
  before: ModelPrice
  after: ModelPrice
}

export interface TimePricingBatchResult {
  effective_at: string
  items: TimePricingBatchItem[]
  applied: boolean
}

export interface ModelPriceCatalogItem {
  id: string
  name: string
  alias: string | null
  object: string | null
  owner: string | null
  created: number | null
  metadata: Record<string, string | number | boolean | null>
  suggested_provider: string
  channel_auth_type: 'apikey' | 'oauth'
  channel_brand: string
  channel_key: string
  channel_label: string
  channel_alias: string
  channel_api_key_masked: string | null
  channel_identity_hash: string
  channel_disabled: boolean
  channel_status: 'ready' | 'missing_selector' | 'conflict' | string
  channel_label_fallback: boolean
  channel_account_count: number
  price: ModelPrice | null
  template_price: ModelPrice | null
  sources: AvailableModelSource[]
}

export interface ModelPriceCatalogResponse {
  has_api_keys: boolean
  api_key_count: number
  queryable_api_key_count: number
  channels_available: boolean
  channel_error: string | null
  oauth_channels_available: boolean
  oauth_channel_error: string | null
  models: ModelPriceCatalogItem[]
  errors: AvailableModelKeyError[]
  priced_models: number
  unpriced_models: number
}

export interface UserApiKeySummary {
  api_key_hash: string
  api_key: string | null
  description: string
  user_id: number | null
  user_name: string | null
  created_at: string | null
  updated_at: string | null
  records: number
  success_records: number
  failed_records: number
  total_tokens: number
  today_records: number
  today_success_records: number
  today_failed_records: number
  today_input_tokens: number
  today_output_tokens: number
  today_cached_tokens: number
  today_reasoning_tokens: number
  today_total_tokens: number
  today_estimated_cost_usd: number
  today_unpriced_records: number
  first_seen_at: string | null
  last_seen_at: string | null
  last_provider: string | null
  last_model: string | null
  providers: string[]
  models: string[]
}

export interface UserQuotaStatus {
  unlimited: boolean
  lifetime_quota_usd: number | null
  lifetime_remaining_usd: number | null
  monthly_quota_usd: number | null
  monthly_used_usd: number
  monthly_remaining_usd: number | null
  quota_month: string
  paused: boolean
  paused_at: string | null
  pause_reason: string | null
  sync_error: string | null
  unpriced_records: number
  can_create_keys: boolean
  started_at: string | null
}

export interface AvailableModelSource {
  api_key_hash: string
  api_key_preview: string
  description: string
  user_id?: number
  user_label?: string
}

export interface AvailableModelPrice {
  provider: string
  model: string
  input_usd_per_million: number
  output_usd_per_million: number
  cache_read_usd_per_million: number
  cache_creation_usd_per_million: number
  request_usd: number | null
  long_context: ModelPriceLongContext | null
  billing_unit: ModelPriceBillingUnit
}

export interface AvailableModel {
  id: string
  name: string
  object: string | null
  owner: string | null
  created: number | null
  metadata: Record<string, string | number | boolean | null>
  price: AvailableModelPrice | null
  sources: AvailableModelSource[]
}

export interface AvailableModelKeyError {
  api_key_hash: string
  api_key_preview: string
  description: string
  message: string
}

export interface AvailableModelsResponse {
  has_api_keys: boolean
  api_key_count: number
  queryable_api_key_count: number
  models: AvailableModel[]
  errors: AvailableModelKeyError[]
}

export interface UserSummary {
  id: number
  username: string
  is_admin: boolean
  is_super_admin: boolean
  nickname: string
  disabled_at: string | null
  password_set: boolean
  created_at: string
  updated_at: string
  api_keys: UserApiKeySummary[]
  key_count: number
  records: number
  success_records: number
  failed_records: number
  total_tokens: number
  today_records: number
  today_success_records: number
  today_failed_records: number
  today_input_tokens: number
  today_output_tokens: number
  today_cached_tokens: number
  today_reasoning_tokens: number
  today_total_tokens: number
  today_estimated_cost_usd: number
  today_unpriced_records: number
  total_estimated_cost_usd: number
  total_unpriced_records: number
  first_seen_at: string | null
  last_seen_at: string | null
  last_provider: string | null
  last_model: string | null
  providers: string[]
  models: string[]
  quota: UserQuotaStatus
}

export interface UserPayload {
  username: string
  password?: string | undefined
  is_admin: boolean
  is_super_admin: boolean
  nickname: string
  lifetime_quota_usd?: number | null | undefined
  monthly_quota_usd?: number | null | undefined
}

export interface UserListPage {
  items: UserSummary[]
  total: number
  page: number
  page_size: number
}

export interface UserQuotaPayload {
  lifetime_quota_usd: number | null
  monthly_quota_usd: number | null
}

export interface UserApiKeyBindPayload {
  api_key?: string
  api_key_hash?: string
  description: string
}

export interface ApiKeyCreatePayload {
  description: string
}

export interface ApiKeyUpdatePayload {
  description: string
}

export type ModelMonitorSourceType = 'built_in'
export type ModelMonitorCollectionState = 'ok' | 'error'
export type ModelMonitorHistoryGranularity = 'day' | 'minute'
export type ModelMonitorStatus =
  | 'operational'
  | 'degraded_performance'
  | 'partial_outage'
  | 'major_outage'
  | 'maintenance'
  | 'unknown'

export interface ModelMonitorSample {
  timestamp: string
  status: ModelMonitorStatus
  latency_ms: number | null
  error: string | null
  related_incidents: string[]
}

export interface ModelMonitorServiceStatus {
  id: string
  name: string
  status: ModelMonitorStatus
  uptime_percent: number | null
  last_status: ModelMonitorStatus | null
  last_latency_ms: number | null
  last_error: string | null
  samples: ModelMonitorSample[]
}

export interface ModelMonitorServiceGroup {
  id: string
  name: string
  status: ModelMonitorStatus
  uptime_percent: number | null
  samples: ModelMonitorSample[]
  services: ModelMonitorServiceStatus[]
}

export interface ModelMonitorIncident {
  id: string
  name: string
  status: string
  impact: string
  url: string | null
  updated_at: string | null
}

export interface ModelMonitorSourceStatus {
  id: string
  name: string
  source_type: ModelMonitorSourceType
  status_page_url: string
  collection_state: ModelMonitorCollectionState
  collection_error: string | null
  overall_status: ModelMonitorStatus
  source_updated_at: string | null
  checked_at: string
  last_success_at: string | null
  history_granularity: ModelMonitorHistoryGranularity
  history_window_label: string
  groups: ModelMonitorServiceGroup[]
  services: ModelMonitorServiceStatus[]
  incidents: ModelMonitorIncident[]
}

export interface ModelMonitorResponse {
  sources: ModelMonitorSourceStatus[]
}

export interface ModelMonitorBuiltInSource {
  id: string
  name: string
  source_type: ModelMonitorSourceType
  status_page_url: string
  history_granularity: ModelMonitorHistoryGranularity
  history_window_label: string
  enabled: boolean
}

export interface ModelMonitorSettings {
  enabled_source_ids: string[]
  sources: ModelMonitorBuiltInSource[]
}

export interface ModelMonitorSettingsPayload {
  enabled_source_ids: string[]
}

export interface ModelMonitorProxySettings {
  enabled: boolean
  proxy_url: string
}

export interface ModelMonitorProxySettingsPayload {
  enabled?: boolean
  proxy_url?: string
}

// AI radar. Every nullable field is null when the upstream omits it; a missing
// measurement is never reported as zero.
export type AIRadarEffort = 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'ultra'

export interface AIRadarPoint {
  model: string
  effort: AIRadarEffort
  iq: number | null
  passed: number | null
  total: number | null
  average_price_usd: number | null
  average_minutes: number | null
  combined_cost_index: number | null
  average_agent_steps: number | null
  average_total_tokens: number | null
  cache_hit_rate: number | null
  runs_24h: number | null
  runs_48h: number | null
  runs_total: number | null
  low_confidence: boolean
}

export interface AIRadarResponse {
  available: boolean
  stale: boolean
  source_label: string
  source_url: string
  benchmark_id: string
  scoring_mode: string
  score_label: string
  source_updated_at: string | null
  fetched_at: string | null
  runs_24h_total: number | null
  low_sample_runs: number
  points: AIRadarPoint[]
  message: string | null
}

export interface AIRadarHistoryPoint {
  observed_at: string
  iq: number | null
  average_price_usd: number | null
  average_minutes: number | null
  combined_cost_index: number | null
  average_agent_steps: number | null
}

export interface AIRadarHistorySeries {
  model: string
  effort: AIRadarEffort
  points: AIRadarHistoryPoint[]
}

export interface AIRadarHistoryResponse {
  available: boolean
  source_label: string
  source_url: string
  interval_hours: number
  series: AIRadarHistorySeries[]
  message: string | null
}

export type AIRadarHistoryWindow = '24h' | '7d' | '30d' | 'all'
