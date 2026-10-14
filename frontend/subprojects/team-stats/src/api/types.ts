/**
 * sub2api admin API 响应类型。
 * 字段名来自 backend/ent/schema 与 backend/internal/handler/admin。
 * 只声明本项目用到的字段，其余允许扩展。
 */

export type Granularity = 'hour' | 'day';
export type Period = 'today' | 'week' | 'month';

// ---- 通用 ----
export interface Paginated<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

// ---- /admin/dashboard/stats ----
export interface DashboardStats {
  total_users: number;
  today_new_users: number;
  active_users: number;

  total_api_keys: number;
  active_api_keys: number;

  total_accounts: number;
  normal_accounts: number;
  error_accounts: number;
  ratelimit_accounts: number;
  overload_accounts: number;

  total_requests: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_creation_tokens: number;
  total_cache_read_tokens: number;
  total_tokens: number;
  total_cost: number;
  total_actual_cost: number;

  today_requests: number;
  today_input_tokens: number;
  today_output_tokens: number;
  today_cache_creation_tokens: number;
  today_cache_read_tokens: number;
  today_tokens: number;
  today_cost: number;
  today_actual_cost: number;

  average_duration_ms: number;
  uptime: number;
  rpm: number;
  tpm: number;

  hourly_active_users?: number;
  stats_updated_at?: string;
  stats_stale?: boolean;
}

// ---- /admin/dashboard/trend ----
export interface TrendBucket {
  timestamp: string; // ISO
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
  total_tokens: number;
  total_cost: number;
  actual_cost: number;
  requests: number;
}

export interface TrendResponse {
  trend: TrendBucket[];
  start_date: string;
  end_date: string;
  granularity: Granularity;
}

// ---- /admin/dashboard/api-keys-usage ----
export interface ApiKeyUsageStats {
  api_key_id: number;
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
  total_tokens: number;
  total_cost: number;
  actual_cost: number;
  duration_ms_sum?: number;
}

export interface ApiKeysUsageResponse {
  stats: Record<string, ApiKeyUsageStats>;
}

// ---- /admin/dashboard/models ----
export interface ModelUsage {
  model: string;
  requests: number;
  total_tokens: number;
  total_cost: number;
  actual_cost: number;
}

export interface ModelsResponse {
  models: ModelUsage[];
  start_date: string;
  end_date: string;
}

// ---- /admin/dashboard/users-ranking ----
export interface UserRankingItem {
  user_id: number;
  email: string;
  requests: number;
  total_tokens: number;
  total_cost: number;
  actual_cost: number;
}

export interface UsersRankingResponse {
  ranking: UserRankingItem[];
  total_actual_cost: number;
  total_requests: number;
  total_tokens: number;
  start_date: string;
  end_date: string;
}

// ---- /admin/usage 明细 ----
export interface UsageLog {
  id: number;
  user_id: number;
  api_key_id: number;
  account_id: number;
  request_id: string;
  model: string;
  requested_model?: string;
  upstream_model?: string;
  group_id?: number;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
  cache_creation_5m_tokens?: number;
  cache_creation_1h_tokens?: number;
  input_cost: number;
  output_cost: number;
  cache_creation_cost: number;
  cache_read_cost: number;
  total_cost: number;
  actual_cost: number;
  billing_type?: number;
  billing_mode?: string;
  stream: boolean;
  duration_ms?: number;
  first_token_ms?: number;
  created_at: string; // ISO
  user_agent?: string;
  ip_address?: string;
  image_count?: number;
}

// ---- /admin/usage/stats ----
export interface UsageStats {
  total_requests: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_creation_tokens: number;
  total_cache_read_tokens: number;
  total_tokens: number;
  total_cost: number;
  total_actual_cost: number;
  total_duration_ms?: number;
  first_request_at?: string;
  last_request_at?: string;
}

// ---- /admin/groups ----
export interface AdminGroup {
  id: number;
  name: string;
  description?: string;
  platform: string;
  status: string;
  is_exclusive: boolean;
  sort_order: number;
  rate_multiplier: number;
}

// ---- /admin/groups/:id/api-keys ----
export interface ApiKey {
  id: number;
  user_id: number;
  name: string;
  group_id?: number;
  status: string;
  last_used_at?: string;
  quota: number;
  quota_used: number;
  expires_at?: string;
  created_at: string;
}

// ---- /admin/usage/search-* ----
export interface SearchUserHit {
  id: number;
  email: string;
  deleted: boolean;
}

export interface SearchApiKeyHit {
  id: number;
  name: string;
  user_id: number;
}
