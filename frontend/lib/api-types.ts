/**
 * API response shapes for upstream-hub backend.
 * Keep in sync with backend/internal/storage/*.go and backend/internal/api/*.go.
 */

export type ChannelType = "newapi" | "sub2api"

export type CredentialMode = "password" | "token"

export type NotificationChannelType =
  | "telegram"
  | "webhook"
  | "email"
  | "wecom"
  | "dingtalk"
  | "feishu"
  | "bark"

export type CaptchaProviderType =
  | "capsolver"
  | "2captcha"
  | "anticaptcha"
  | "yescaptcha"

export type MonitorJob = "login" | "balance" | "rates" | "usage"

export interface MonitorState {
  channel_id: number
  failure_count: number
  next_attempt_at?: string | null
  last_failure_type?: "auth" | "transient" | string
  last_error?: string
  last_checked_at?: string | null
  last_success_at?: string | null
  updated_at: string
}

export type NotificationEvent =
  | "balance_low"
  | "rate_changed"
  | "login_failed"
  | "captcha_failed"
  | "monitor_failed"

export interface Channel {
  id: number
  name: string
  type: ChannelType
  site_url: string
  username: string
  credential_mode: CredentialMode
  turnstile_enabled: boolean
  captcha_config_id?: number | null
  balance_threshold: number
  monitor_enabled: boolean
  last_balance?: number | null
  last_balance_at?: string | null
  last_usage_total?: number | null
  last_usage_today?: number | null
  usage_currency?: string
  last_usage_at?: string | null
  last_error?: string
  monitor_state?: MonitorState | null
  created_at: string
  updated_at: string
}

export interface CaptchaConfig {
  id: number
  name: string
  type: CaptchaProviderType
  endpoint?: string
  extra?: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface RateSnapshot {
  id: number
  channel_id: number
  model_name: string
  description?: string
  ratio: number
  completion_ratio: number
  first_seen_at: string
  last_seen_at: string
}

export interface RateChangeLog {
  id: number
  channel_id: number
  model_name: string
  old_ratio: number | null
  new_ratio: number
  old_completion_ratio?: number | null
  new_completion_ratio?: number
  changed_at: string
}

export interface BalanceSnapshot {
  id: number
  channel_id: number
  balance: number
  sampled_at: string
}

export interface NotificationSubscription {
  channel_id: number
  mode: "all" | "groups"
  groups?: string[]
}

export interface NotificationChannel {
  id: number
  name: string
  type: NotificationChannelType
  enabled: boolean
  subscriptions?: string
  created_at: string
  updated_at: string
}

export interface NotificationLog {
  id: number
  channel_id: number
  event: NotificationEvent
  subject: string
  body: string
  success: boolean
  error_message?: string
  sent_at: string
}

export interface MonitorLog {
  id: number
  channel_id: number
  job: MonitorJob
  success: boolean
  error_message?: string
  duration_ms: number
  started_at: string
  finished_at: string
}

export interface DashboardLowest {
  channel_id: number
  name: string
  balance: number | null
}

export interface DashboardChannelStat {
  id: number
  name: string
  type: string
  monitor_enabled: boolean
  last_balance?: number | null
  last_error?: string
}

export interface DashboardSummary {
  total_channels: number
  active_channels: number
  failed_channels: number
  total_balance: number
  total_usage: number
  lowest_balance: DashboardLowest | null
  channels: DashboardChannelStat[]
  recent_rate_changes: RateChangeLog[]
  recent_notification_logs: NotificationLog[]
  profit?: {
    start_at: string
    end_at: string
    currency: "CNY" | string
    summary: ProfitSummary
  }
}

export interface ProfitSummary {
  sale_cny: number
  cost_cny: number
  cost_usd?: number
  stage_usage_cost_cny?: number
  sales_detail_cny?: number
  cost_detail_cny?: number
  allocated_cost_cny?: number
  unmatched_cost_cny?: number
  reconciliation_delta_cny?: number
  profit_cny: number
  profit_margin: number
  settled_sale_cny: number
  unmapped_sale_cny: number
  unsettled_sale_cny: number
  bucket_count: number
  settled_bucket_count: number
  unmapped_bucket_count: number
  complete: boolean
}

export type ProfitTrendRange = "24h" | "7d" | "30d"

export type ProfitDetailKind = "sales" | "cost" | "unmapped" | "reconciliation"

export interface ProfitSaleDetail {
  source: string
  source_log_id?: number | null
  created_at: string
  bucket_start: string
  bucket_end: string
  channel_id: number
  channel_name?: string
  newapi_channel_id?: number
  newapi_channel_name?: string
  upstream_channel_id?: number | null
  upstream_channel_name?: string
  mapping_status: string
  event_type?: "consume" | "refund" | string
  group: string
  model_name: string
  effective_group_ratio?: number
  ratio_source?: string
  normalization_status: string
  quota: number
  charged_usd?: number
  normalized_usd?: number
  credit_usd_per_cny?: number
  sale_cny: number
  cost_cny?: number
  profit_cny?: number
  event_count?: number
  user_id?: number
  token_name?: string
  request_id?: string
  upstream_request_id?: string
}

export interface ProfitCostDetail {
  channel_id: number
  channel_name?: string
  bucket_start: string
  bucket_end: string
  resolution_seconds: number
  amount: number
  currency: string
  cost_cny: number
  source: string
  quality: string
  complete: boolean
  collected_at: string
}

export interface ProfitReconciliation {
  start_at: string
  end_at: string
  sales_cny: number
  sales_detail_cny: number
  stage_usage_cost_usd: number
  stage_usage_cost_cny: number
  cost_detail_cny: number
  allocated_cost_cny: number
  unmatched_cost_cny: number
  unmapped_sales_cny: number
  profit_cny: number
  reconciliation_delta_cny: number
  currency: string
  complete: boolean
  details_available: boolean
}

export interface ProfitDetailsResponse {
  kind: ProfitDetailKind
  start_at: string
  end_at: string
  usage_resolution_seconds: number
  page: number
  page_size: number
  total: number
  has_more: boolean
  items: Array<ProfitSaleDetail | ProfitCostDetail | ProfitReconciliation>
  reconciliation: ProfitReconciliation
}

export interface ProfitTrendPoint {
  start_at: string
  end_at: string
  sale_cny: number
  cost_cny: number
  profit_cny: number
  settled_sale_cny: number
  unmapped_sale_cny: number
  unsettled_sale_cny: number
  complete: boolean
}

export interface ProfitTrendResponse {
  range: ProfitTrendRange
  start_at: string
  end_at: string
  output_resolution_seconds: number
  currency: "CNY" | string
  points: ProfitTrendPoint[]
  summary: ProfitSummary
  complete: boolean
}

export interface BalanceTrendPoint {
  day: string
  balance: number
}

export type UsageTrendRange = "1h" | "today" | "24h" | "7d" | "30d" | "6m" | "1y"
export type UsagePointQuality = "exact" | "observed" | "mixed" | "missing"

export interface UsageTrendChannel {
  id: number
  name: string
  type: ChannelType
  currency: string
}

export interface UsageTrendPoint {
  start_at: string
  end_at: string
  total_amount: number | null
  channel_amounts: Record<string, number>
  quality: UsagePointQuality
  complete: boolean
  missing_channel_ids: number[]
}

export interface UsageTrendResponse {
  range: UsageTrendRange
  start_at: string
  end_at: string
  source_resolution_seconds: number
  output_resolution_seconds: number
  currency: string
  channels: UsageTrendChannel[]
  points: UsageTrendPoint[]
  range_total_amount: number | null
  channel_totals: Record<string, number>
  complete: boolean
}
