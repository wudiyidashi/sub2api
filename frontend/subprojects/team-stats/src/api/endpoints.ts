import { api } from './client';
import { isMockMode, mockEndpoints } from './mock';
import type {
  AdminGroup,
  ApiKey,
  ApiKeysUsageResponse,
  DashboardStats,
  Granularity,
  ModelsResponse,
  Paginated,
  SearchApiKeyHit,
  SearchUserHit,
  TrendResponse,
  UsageLog,
  UsageStats,
  UsersRankingResponse,
} from './types';

/** Uses the host's authenticated admin API; no standalone BFF or admin key. */

export interface TimeRangeParams {
  start_date?: string;
  end_date?: string;
  timezone?: string;
}

export interface TrendParams extends TimeRangeParams {
  granularity?: Granularity;
  api_key_id?: number;
  user_id?: number;
  group_id?: number;
  model?: string;
}

export interface UsageListParams extends TimeRangeParams {
  page?: number;
  page_size?: number;
  sort_by?: 'created_at' | 'duration_ms' | 'total_cost' | 'total_tokens';
  sort_order?: 'asc' | 'desc';
  api_key_id?: number;
  user_id?: number;
  group_id?: number;
  account_id?: number;
  model?: string;
  stream?: boolean;
}

export interface MeInfo {
  user_id: number;
  email: string;
  role: string;
}

function ensureArray<T>(v: unknown): T[] {
  return Array.isArray(v) ? (v as T[]) : [];
}

function asObject<T extends object>(v: unknown, fallback: T): T {
  return v && typeof v === 'object' ? ({ ...fallback, ...v } as T) : fallback;
}

export const endpoints = {
  me: () => api.get<MeInfo>('/auth/me', { noCache: true, cacheTtl: 0 }),
  loadConfig: () => api.get<unknown>('/admin/team-stats/config', { noCache: true, cacheTtl: 0 }),
  saveConfig: (data: unknown) =>
    api.put('/admin/team-stats/config', data, { noCache: true, cacheTtl: 0 }),

  dashboardStats: async (): Promise<DashboardStats> => {
    if (isMockMode()) return mockEndpoints.dashboardStats();
    const r = await api.get<DashboardStats>('/admin/dashboard/stats');
    return asObject<DashboardStats>(r, {
      total_users: 0, today_new_users: 0, active_users: 0,
      total_api_keys: 0, active_api_keys: 0,
      total_accounts: 0, normal_accounts: 0, error_accounts: 0,
      ratelimit_accounts: 0, overload_accounts: 0,
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_creation_tokens: 0, total_cache_read_tokens: 0,
      total_tokens: 0, total_cost: 0, total_actual_cost: 0,
      today_requests: 0, today_input_tokens: 0, today_output_tokens: 0,
      today_cache_creation_tokens: 0, today_cache_read_tokens: 0,
      today_tokens: 0, today_cost: 0, today_actual_cost: 0,
      average_duration_ms: 0, uptime: 0, rpm: 0, tpm: 0,
    });
  },

  dashboardTrend: async (params: TrendParams): Promise<TrendResponse> => {
    const res = isMockMode()
      ? await mockEndpoints.dashboardTrend(params)
      : await api.get<TrendResponse>('/admin/dashboard/trend', { query: params });
    const raw = (res || {}) as Partial<TrendResponse>;
    const trendArr = ensureArray<TrendResponse['trend'][number]>(raw.trend);
    return {
      ...raw,
      start_date: raw.start_date || '',
      end_date: raw.end_date || '',
      granularity: raw.granularity || params.granularity || 'day',
      trend: trendArr.map((b) => {
        const anyB = b as TrendResponse['trend'][number] & {
          date?: string;
          cost?: number;
        };
        return {
          ...b,
          timestamp: anyB.timestamp || anyB.date || '',
          total_cost: anyB.total_cost ?? anyB.cost ?? 0,
        };
      }),
    } as TrendResponse;
  },

  apiKeysUsage: async (api_key_ids: number[]): Promise<ApiKeysUsageResponse> => {
    if (isMockMode()) return mockEndpoints.apiKeysUsage(api_key_ids);
    const r = await api.post<ApiKeysUsageResponse>(
      '/admin/dashboard/api-keys-usage',
      { api_key_ids },
    );
    return { stats: (r && typeof r.stats === 'object' && r.stats) || {} };
  },

  dashboardModels: async (params: TrendParams): Promise<ModelsResponse> => {
    if (isMockMode()) return mockEndpoints.dashboardModels(params);
    const r = await api.get<ModelsResponse>('/admin/dashboard/models', {
      query: params,
    });
    return {
      models: ensureArray<ModelsResponse['models'][number] & { cost?: number }>((r as Partial<ModelsResponse> | null)?.models).map(m => ({ ...m, total_cost: m.total_cost ?? m.cost ?? 0 })),
      start_date: r?.start_date || '',
      end_date: r?.end_date || '',
    };
  },

  usersRanking: async (
    params: TimeRangeParams & { limit?: number },
  ): Promise<UsersRankingResponse> => {
    if (isMockMode()) return mockEndpoints.usersRanking(params);
    const r = await api.get<UsersRankingResponse>(
      '/admin/dashboard/users-ranking',
      { query: params },
    );
    return {
      ranking: ensureArray((r as Partial<UsersRankingResponse> | null)?.ranking),
      total_actual_cost: r?.total_actual_cost || 0,
      total_requests: r?.total_requests || 0,
      total_tokens: r?.total_tokens || 0,
      start_date: r?.start_date || '',
      end_date: r?.end_date || '',
    };
  },

  usageStatsByPeriod: async (
    period: 'today' | 'week' | 'month',
    extra?: Partial<UsageListParams>,
  ): Promise<UsageStats> => {
    if (isMockMode()) return mockEndpoints.usageStatsByPeriod(period, extra);
    const r = await api.get<UsageStats>('/admin/usage/stats', {
      query: { period, ...extra },
    });
    return asObject<UsageStats>(r, defaultUsageStats());
  },

  usageStatsByRange: async (
    params: TimeRangeParams & Partial<UsageListParams>,
  ): Promise<UsageStats> => {
    if (isMockMode()) return mockEndpoints.usageStatsByRange(params);
    const r = await api.get<UsageStats>('/admin/usage/stats', { query: params });
    return asObject<UsageStats>(r, defaultUsageStats());
  },

  usageList: async (params: UsageListParams): Promise<Paginated<UsageLog>> => {
    if (isMockMode()) return mockEndpoints.usageList(params);
    const r = await api.get<Paginated<UsageLog>>('/admin/usage', { query: params });
    return {
      items: ensureArray<UsageLog>((r as Partial<Paginated<UsageLog>> | null)?.items),
      total: r?.total || 0,
      page: r?.page || params.page || 1,
      page_size: r?.page_size || params.page_size || 50,
    };
  },

  groupsAll: async (platform?: string): Promise<AdminGroup[]> => {
    if (isMockMode()) return mockEndpoints.groupsAll(platform);
    const r = await api.get<AdminGroup[]>('/admin/groups/all', {
      query: { platform },
    });
    return ensureArray<AdminGroup>(r);
  },

  groupApiKeys: async (
    groupId: number,
    page = 1,
    pageSize = 100,
  ): Promise<Paginated<ApiKey>> => {
    if (isMockMode()) return mockEndpoints.groupApiKeys(groupId, page, pageSize);
    const r = await api.get<Paginated<ApiKey>>(
      `/admin/groups/${groupId}/api-keys`,
      { query: { page, page_size: pageSize } },
    );
    return {
      items: ensureArray<ApiKey>((r as Partial<Paginated<ApiKey>> | null)?.items),
      total: r?.total || 0,
      page: r?.page || page,
      page_size: r?.page_size || pageSize,
    };
  },

  groupAllApiKeys: async (groupId: number): Promise<ApiKey[]> => {
    const items: ApiKey[] = [];
    let page = 1;
    for (;;) {
      const result = await endpoints.groupApiKeys(groupId, page, 100);
      items.push(...result.items);
      if (items.length >= result.total || result.items.length === 0) break;
      page++;
    }
    return items;
  },

  searchApiKeys: async (q: string, user_id?: number): Promise<SearchApiKeyHit[]> => {
    if (isMockMode()) return mockEndpoints.searchApiKeys(q, user_id);
    const r = await api.get<SearchApiKeyHit[]>('/admin/usage/search-api-keys', {
      query: { q, user_id },
    });
    return ensureArray<SearchApiKeyHit>(r);
  },

  searchUsers: async (q: string): Promise<SearchUserHit[]> => {
    if (isMockMode()) return mockEndpoints.searchUsers(q);
    const r = await api.get<SearchUserHit[]>('/admin/usage/search-users', {
      query: { q },
    });
    return ensureArray<SearchUserHit>(r);
  },

  ping: () => api.get<MeInfo>('/auth/me', { noCache: true, cacheTtl: 0 }),
};

function defaultUsageStats(): UsageStats {
  return {
    total_requests: 0,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_cache_creation_tokens: 0,
    total_cache_read_tokens: 0,
    total_tokens: 0,
    total_cost: 0,
    total_actual_cost: 0,
  };
}
