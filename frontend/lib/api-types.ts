/**
 * API response shapes for UpstreamOps backend.
 * Keep in sync with backend/storage/*.go and backend/api/*.go.
 */

export type UpstreamSiteType = "newapi" | "sub2api"

export type CredentialMode = "password" | "token"

export type RechargeMultiplierMode = "divide" | "multiply"

export type NotificationChannelType =
  | "telegram"
  | "webhook"
  | "email"
  | "wecom"
  | "dingtalk"
  | "feishu"
  | "serverchan3"

export type CaptchaProviderType =
  | "capsolver"
  | "2captcha"
  | "anticaptcha"
  | "yescaptcha"

export type MonitorJob = "login" | "balance" | "rates"

export type NotificationEvent =
  | "balance_low"
  | "rate_changed"
  | "rate_structure_changed"
  | "rate_added"
  | "rate_removed"
  | "announcement"
  | "login_failed"
  | "captcha_failed"
  | "monitor_failed"
  | "subscription_daily_remaining_low"
  | "subscription_weekly_remaining_low"
  | "subscription_monthly_remaining_low"
  | "subscription_expiring"
  | "upstream_sync_group_changed"

export interface UpstreamAccount {
  id: number
  site_id: number
  alias: string
  username: string
  sort_order: number
  user_id?: string
  credential_mode: CredentialMode
  login_extra_params: string
  turnstile_enabled: boolean
  subscription_enabled: boolean
  proxy_enabled: boolean
  captcha_config_id?: number | null
  balance_threshold: number
  recharge_multiplier?: number | null
  recharge_multiplier_mode: RechargeMultiplierMode
  monitor_enabled: boolean
  last_balance?: number | null
  last_balance_at?: string | null
  today_cost?: number | null
  total_cost?: number | null
  last_error?: string
  created_at: string
  updated_at: string
}

export interface UpstreamSite {
  id: number
  name: string
  type: UpstreamSiteType
  base_url: string
  sort_order: number
  default_account_id: number
  ignore_announcements: boolean
  accounts: UpstreamAccount[]
  account_count: number
  uncollected_count: number
  error_account_count: number
  total_balance?: number | null
  today_cost?: number | null
  lowest_balance?: number | null
  lowest_account_id?: number
  lowest_account_alias?: string
  last_sync_at?: string | null
  created_at: string
  updated_at: string
}

export type SiteAccountBundleImportStrategy = "create_only" | "upsert"
export type SiteAccountBundlePlanAction = "create" | "update" | "skip" | "conflict"

export interface SiteAccountBundleFieldChange {
  field: string
  before?: unknown
  after?: unknown
}

export interface SiteAccountBundlePlanItem {
  scope: "site" | "account"
  key: string
  parent_key?: string
  name: string
  action: SiteAccountBundlePlanAction
  blocking: boolean
  needs_credential: boolean
  changes?: SiteAccountBundleFieldChange[]
  warnings?: string[]
  message?: string
}

export interface SiteAccountBundlePlanSummary {
  create: number
  update: number
  skip: number
  conflict: number
  warnings: number
  need_credential: number
}

export interface SiteAccountBundleBaseURLChange {
  site_key: string
  site_name: string
  before: string
  after: string
  affected_account_count: number
}

export interface SiteAccountBundleImportPlan {
  bundle_digest: string
  digest: string
  strategy: SiteAccountBundleImportStrategy
  summary: SiteAccountBundlePlanSummary
  has_conflicts: boolean
  requires_base_url_confirmation: boolean
  base_url_changes: SiteAccountBundleBaseURLChange[]
  items: SiteAccountBundlePlanItem[]
  warnings?: string[]
}

export interface SiteAccountBundleImportResult {
  digest: string
  summary: SiteAccountBundlePlanSummary
  items: SiteAccountBundlePlanItem[]
  warnings?: string[]
}

export interface UpstreamAccountPage {
  items: UpstreamAccount[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface CaptchaConfig {
  id: number
  name: string
  type: CaptchaProviderType
  endpoint?: string
  extra?: string
  enabled: boolean
  proxy_enabled: boolean
  last_balance?: number | null
  balance_unit?: string
  balance_at?: string | null
  balance_error?: string
  created_at: string
  updated_at: string
}

export interface RateSnapshot {
  id: number
  account_id: number
  remote_group_id?: number | null
  model_name: string
  description?: string
  ratio: number
  completion_ratio: number
  first_seen_at: string
  last_seen_at: string
}

export interface RateChangeGroupAccount {
  account_id: number
  account_alias?: string
}

export interface RateChangeGroup {
  id: number
  site_id: number
  site_name?: string
  scan_run_id?: string
  stable_group_key?: string
  change_type?: string
  model_name: string
  old_ratio?: number | null
  new_ratio: number
  old_completion_ratio?: number | null
  new_completion_ratio?: number
  changed_at: string
  accounts: RateChangeGroupAccount[]
}

export interface RateChangeGroupPage {
  items: RateChangeGroup[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface BalanceSnapshot {
  id: number
  account_id: number
  balance: number
  sampled_at: string
}

export interface NotificationSubscription {
  site_ids: number[]
  account_ids: number[]
  mode: "all" | "groups"
  groups?: string[]
  events?: NotificationEvent[]
}

export interface NotificationChannel {
  id: number
  name: string
  type: NotificationChannelType
  enabled: boolean
  proxy_enabled: boolean
  subscriptions?: string
  created_at: string
  updated_at: string
}

export interface NotificationLog {
  id: number
  channel_id: number
  account_id?: number
  site_id?: number
  channel_name?: string
  channel_type?: string
  event: NotificationEvent
  subject: string
  body: string
  success: boolean
  error_message?: string
  sent_at: string
}

export interface UpstreamAnnouncement {
  id: number
  site_id: number
  source_key: string
  title?: string
  content: string
  type?: string
  link?: string
  published_at?: string | null
  source_updated_at?: string | null
  first_seen_at: string
}

export interface MonitorLog {
  id: number
  account_id: number
  job: MonitorJob
  success: boolean
  error_message?: string
  duration_ms: number
  started_at: string
  finished_at: string
}

export interface DashboardLowest {
  site_id: number
  site_name: string
  account_id: number
  account_alias: string
  balance: number | null
}

export interface DashboardSummary {
  total_sites: number
  total_accounts: number
  active_accounts: number
  failed_accounts: number
  total_balance: number
  today_total_cost: number
  total_cost: number
  lowest_balance: DashboardLowest | null
  recent_rate_changes: RateChangeGroup[]
}

export interface BalanceTrendPoint {
  day: string
  balance: number
}

export interface CostTrendPoint {
  day: string
  cost: number
}

export interface SystemAuthConfig {
  enabled: boolean
  username: string
  password: string
  tokenSecret: string
  sessionTTLHours: number
}

export interface AppConfig {
  title: string
  notificationPrefix: string
}

export interface SystemSchedulerRetentionConfig {
  cron: string
  monitorLogsDays: number
  balanceSnapshotsDays: number
  notificationLogsDays: number
  announcementsDays: number
}

export interface SystemSchedulerConfig {
  balanceCron: string
  rateCron: string
  balanceTimeoutSeconds: number
  rateTimeoutSeconds: number
  concurrency: number
  retention: SystemSchedulerRetentionConfig
}

export interface SystemNotificationsConfig {
  batchRateChanges: boolean
  minChangePct: number
  balanceLowCooldownMinutes: number
  subscriptionDailyRemainingThresholdPct: number
  subscriptionWeeklyRemainingThresholdPct: number
  subscriptionMonthlyRemainingThresholdPct: number
  subscriptionExpiryThresholdHours: number
  subscriptionAlertCooldownMinutes: number
  sendMaxAttempts: number
}

export interface SystemProxyConfig {
  enabled: boolean
  versionCheckEnabled: boolean
  protocol: "http" | "https" | "socks5"
  host: string
  port: number
  username: string
  password: string
}

export interface SystemUpstreamConfig {
  timeoutSeconds: number
  userAgent: string
}

export interface SystemConfig {
  app: AppConfig
  auth: SystemAuthConfig
  scheduler: SystemSchedulerConfig
  notifications: SystemNotificationsConfig
  proxy: SystemProxyConfig
  upstream: SystemUpstreamConfig
}

export interface SystemConfigResponse {
  config_path: string
  config: SystemConfig
}

export interface AppVersion {
  name: string
  title: string
  version: string
  latest_version?: string
  update_available?: boolean
  repo_url?: string
  release_url?: string
  update_error?: string
}

export interface ApplyConfigResult {
  applied_sections: string[]
  message: string
}

export interface AccountRedeemResult {
  message: string
  type: string
  value: number
  new_balance?: number
  new_concurrency?: number
  group_name?: string
  validity_days?: number
}

export type RechargePaymentMethod = "alipay" | "wxpay"
export type SubscriptionPaymentMethod =
  | "balance"
  | "alipay"
  | "wxpay"
  | "stripe"
  | "creem"
  | "waffo_pancake"
  | string

export interface AccountRechargeMethod {
  type: RechargePaymentMethod
  name: string
  min_amount: number
  max_amount: number
}

export interface AccountRechargeInfo {
  amount_label: string
  amount_step: number
  min_amount: number
  max_amount: number
  preset_amounts: number[]
  help_text?: string
  help_image_url?: string
  alipay_force_qrcode: boolean
  methods: AccountRechargeMethod[]
}

export interface AccountRechargeLaunch {
  mode: "qrcode" | "redirect" | "form" | "success"
  qr_code?: string
  pay_url?: string
  form_action?: string
  form_fields?: Record<string, string>
  expires_at?: string
}

export interface AccountSubscriptionMethod {
  type: SubscriptionPaymentMethod
  name: string
}

export interface AccountSubscriptionPlan {
  id: string
  name: string
  description?: string
  price: number
  currency?: string
  validity?: string
  group_name?: string
  quota?: number
  daily_limit_usd?: number | null
  weekly_limit_usd?: number | null
  monthly_limit_usd?: number | null
  features?: string[]
  payment_methods?: string[]
}

export interface AccountSubscriptionInfo {
  plans: AccountSubscriptionPlan[]
  methods: AccountSubscriptionMethod[]
}

export type AccountSubscriptionLaunch = AccountRechargeLaunch

export interface AccountSubscriptionUsageWindow {
  limit_usd: number
  used_usd: number
  remaining_usd: number
  remaining_percent: number
  used_percent: number
  window_start?: string | null
  resets_at?: string | null
  resets_in_seconds: number
}

export interface AccountSubscriptionUsage {
  id: number
  group_id: number
  group_name: string
  status: string
  starts_at?: string | null
  expires_at?: string | null
  expires_in_days: number
  daily?: AccountSubscriptionUsageWindow | null
  weekly?: AccountSubscriptionUsageWindow | null
  monthly?: AccountSubscriptionUsageWindow | null
}

export interface AccountSubscriptionUsageInfo {
  items: AccountSubscriptionUsage[]
}

export type AccountAPIKeyStatus = "active" | "disabled" | "expired" | "quota_exhausted" | "unknown"

export interface AccountAPIKey {
  id: number
  key: string
  name: string
  status: AccountAPIKeyStatus | string
  group?: string
  group_name?: string
  group_description?: string
  group_ratio: number
  group_id?: number | null
  quota: number
  quota_used: number
  unlimited_quota: boolean
  expired_time: number
  expires_at?: string | null
  created_at?: string | null
  updated_at?: string | null
  last_used_at?: string | null
  allow_ips?: string
  ip_whitelist?: string[]
  ip_blacklist?: string[]
  model_limits_enabled: boolean
  model_limits?: string
  cross_group_retry: boolean
  rate_limit_5h: number
  rate_limit_1d: number
  rate_limit_7d: number
  usage_5h: number
  usage_1d: number
  usage_7d: number
}

export interface AccountAPIKeyPage {
  items: AccountAPIKey[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface NotificationLogPage {
  items: NotificationLog[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface UpstreamAnnouncementPage {
  items: UpstreamAnnouncement[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface AccountAPIKeyGroup {
  id?: number | null
  name: string
  description?: string
  ratio: number
}

export interface AccountAPIKeyReveal {
  key: string
}

export interface UpstreamSyncTarget {
  id: number
  name: string
  base_url: string
  enabled: boolean
  last_check_status?: string
  last_check_at?: string | null
  last_check_error?: string
}

export interface UpstreamSyncTargetGroup {
  id: number
  target_id: number
  remote_group_id: number
  name: string
  platform?: string
  ratio: number
  status: string
  sort: number
  description?: string
  last_sync_at?: string | null
}

export interface UpstreamSyncTargetProxy {
  id: number
  name: string
  protocol: string
  host: string
  port: number
  status: string
}

export type UpstreamSyncRateConvertMode = "raw" | "multiply_100" | "divide_100" | "custom"

export interface UpstreamSyncAccount {
  id?: number
  source_site_id: number
  source_account_id: number
  source_group_id?: number | null
  source_group_name?: string
  proxy_id?: number | null
  concurrency: number
  weight: number
  rate_convert_mode: UpstreamSyncRateConvertMode
  rate_convert_value: number
  enabled: boolean
  test_enabled: boolean
  test_model?: string
}

export interface UpstreamSyncGroup {
  id: number
  display_name: string
  name_template: string
  name: string
  target_id: number
  target_group_ids: number[]
  platform: string
  model_limits_mode: string
  model_limits?: string
  pool_mode_enabled: boolean
  pool_mode_retry_count: number
  pool_mode_retry_status_codes?: string
  custom_error_codes_enabled: boolean
  custom_error_codes?: string
  rate_sort_direction: "asc" | "desc"
  accounts: UpstreamSyncAccount[]
  enabled: boolean
  apply_status?: string
  apply_error?: string
  last_applied_at?: string | null
}

export interface UpstreamSyncLog {
  id: number
  sync_group_id: number
  target_id: number
  action: string
  success: boolean
  message?: string
  created_at: string
}

export interface UpstreamSyncLogPage {
  items: UpstreamSyncLog[]
  total: number
  page: number
  page_size: number
  pages: number
}
