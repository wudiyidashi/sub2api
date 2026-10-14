/**
 * Mock 模式：URL 带 ?mock=1 时启用，方便没有真实 admin key 时演示与 UI 调试。
 * 数据是模拟的，仅用于前端开发。
 */
import type {
  AdminGroup,
  ApiKey,
  ApiKeysUsageResponse,
  DashboardStats,
  ModelsResponse,
  Paginated,
  SearchApiKeyHit,
  SearchUserHit,
  TrendResponse,
  UsageLog,
  UsageStats,
  UsersRankingResponse,
} from './types';
import type { TrendParams, TimeRangeParams, UsageListParams } from './endpoints';

export function isMockMode(): boolean {
  if (typeof window === 'undefined') return false;
  return import.meta.env.DEV && new URLSearchParams(window.location.search).get('mock') === '1';
}

const NAMES = ['张三', '李四', '王五', '赵六', '钱七', '孙八'];
// 公网 IP 样例（运营商 DNS 等公开地址），用于演示 IP 归属地地图
const MOCK_IPS = [
  '114.114.114.114', // 江苏南京
  '223.5.5.5', // 浙江杭州
  '1.2.4.8', // 北京
  '202.96.209.5', // 上海
  '119.29.29.29', // 广东
  '8.8.8.8', // 美国
  '192.168.1.10', // 内网
];
const MOCK_UAS = [
  'claude-cli/1.0.58 (external, cli)',
  'claude-cli/1.0.58 (external, cli)',
  'Cline/3.12.0',
  'CherryStudio/1.2.10',
  'anthropic-sdk-python/0.49.0',
  'axios/1.7.2',
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64)',
];
const MODELS = [
  'claude-opus-4',
  'claude-sonnet-4',
  'claude-haiku-4',
  'gpt-4o',
  'gpt-4o-mini',
  'gemini-2.0-pro',
];

function rand(min: number, max: number) {
  return Math.floor(min + Math.random() * (max - min));
}

function delay<T>(data: T, ms = 200): Promise<T> {
  return new Promise((r) => setTimeout(() => r(data), ms));
}

function mockApiKeyIds(): number[] {
  // 15 个 key
  return Array.from({ length: 15 }, (_, i) => 100 + i);
}

export const mockEndpoints = {
  dashboardStats: (): Promise<DashboardStats> =>
    delay({
      total_users: 12,
      today_new_users: 1,
      active_users: 7,
      total_api_keys: 15,
      active_api_keys: 11,
      total_accounts: 4,
      normal_accounts: 3,
      error_accounts: 0,
      ratelimit_accounts: 1,
      overload_accounts: 0,
      total_requests: 18432,
      total_input_tokens: 12_345_678,
      total_output_tokens: 4_567_890,
      total_cache_creation_tokens: 234_567,
      total_cache_read_tokens: 6_789_012,
      total_tokens: 23_937_147,
      total_cost: 312.45,
      total_actual_cost: 287.13,
      today_requests: 821,
      today_input_tokens: 543_210,
      today_output_tokens: 198_765,
      today_cache_creation_tokens: 12_345,
      today_cache_read_tokens: 321_098,
      today_tokens: 1_075_418,
      today_cost: 12.34,
      today_actual_cost: 11.45,
      average_duration_ms: 1234,
      uptime: 86400,
      rpm: 14,
      tpm: 23456,
      stats_updated_at: new Date().toISOString(),
      stats_stale: false,
    }),

  dashboardTrend: (p: TrendParams): Promise<TrendResponse> => {
    const points = p.granularity === 'hour' ? 24 * 7 : 30;
    const now = Date.now();
    const stepMs = p.granularity === 'hour' ? 3_600_000 : 86_400_000;
    const trend = Array.from({ length: points }, (_, i) => {
      const ts = new Date(now - (points - 1 - i) * stepMs).toISOString();
      const base = rand(50, 800);
      return {
        timestamp: ts,
        requests: base,
        input_tokens: base * rand(80, 200),
        output_tokens: base * rand(20, 80),
        cache_creation_tokens: base * rand(2, 12),
        cache_read_tokens: base * rand(30, 120),
        total_tokens: 0,
        total_cost: base * 0.012,
        actual_cost: base * 0.0095,
      };
    }).map((b) => {
      b.total_tokens =
        b.input_tokens + b.output_tokens + b.cache_creation_tokens + b.cache_read_tokens;
      return b;
    });
    return delay({
      trend,
      start_date: trend[0].timestamp.slice(0, 10),
      end_date: trend[trend.length - 1].timestamp.slice(0, 10),
      granularity: p.granularity || 'day',
    });
  },

  apiKeysUsage: (ids: number[]): Promise<ApiKeysUsageResponse> => {
    const stats: ApiKeysUsageResponse['stats'] = {};
    for (const id of ids) {
      const req = rand(50, 5000);
      const it = req * rand(80, 300);
      const ot = req * rand(20, 80);
      stats[String(id)] = {
        api_key_id: id,
        requests: req,
        input_tokens: it,
        output_tokens: ot,
        cache_creation_tokens: req * rand(1, 20),
        cache_read_tokens: req * rand(10, 200),
        total_tokens: it + ot,
        total_cost: req * 0.012,
        actual_cost: req * 0.0095,
        duration_ms_sum: req * rand(800, 3500),
      };
    }
    return delay({ stats });
  },

  dashboardModels: (_p: TrendParams): Promise<ModelsResponse> => {
    const models = MODELS.map((m) => ({
      model: m,
      requests: rand(100, 5000),
      total_tokens: rand(100_000, 5_000_000),
      total_cost: rand(1, 100) + Math.random(),
      actual_cost: rand(1, 80) + Math.random(),
    })).sort((a, b) => b.total_cost - a.total_cost);
    return delay({
      models,
      start_date: '2026-05-27',
      end_date: '2026-06-03',
    });
  },

  usersRanking: (
    p: TimeRangeParams & { limit?: number },
  ): Promise<UsersRankingResponse> => {
    const limit = p.limit ?? 12;
    const ranking = NAMES.slice(0, Math.min(limit, NAMES.length)).map((n, i) => ({
      user_id: i + 1,
      email: `${n}@team.local`,
      requests: rand(100, 3000),
      total_tokens: rand(100_000, 4_000_000),
      total_cost: rand(5, 80) + Math.random(),
      actual_cost: rand(5, 70) + Math.random(),
    }));
    return delay({
      ranking,
      total_actual_cost: 312.45,
      total_requests: 18432,
      total_tokens: 23_937_147,
      start_date: p.start_date || '2026-05-27',
      end_date: p.end_date || '2026-06-03',
    });
  },

  usageStatsByPeriod: (
    period: 'today' | 'week' | 'month',
    _extra?: Partial<UsageListParams>,
  ): Promise<UsageStats> => {
    const m = period === 'today' ? 1 : period === 'week' ? 7 : 30;
    return delay({
      total_requests: 800 * m,
      total_input_tokens: 600_000 * m,
      total_output_tokens: 200_000 * m,
      total_cache_creation_tokens: 12_000 * m,
      total_cache_read_tokens: 300_000 * m,
      total_tokens: 1_112_000 * m,
      total_cost: 12.5 * m,
      total_actual_cost: 11.2 * m,
      total_duration_ms: 1_500_000 * m,
      first_request_at: new Date(Date.now() - m * 86400_000).toISOString(),
      last_request_at: new Date().toISOString(),
    });
  },

  usageStatsByRange: (_p: TimeRangeParams): Promise<UsageStats> =>
    mockEndpoints.usageStatsByPeriod('week'),

  usageList: (p: UsageListParams): Promise<Paginated<UsageLog>> => {
    const pageSize = p.page_size || 50;
    const page = p.page || 1;
    const items: UsageLog[] = Array.from({ length: pageSize }, (_, i) => {
      const id = (page - 1) * pageSize + i + 1;
      const created = new Date(Date.now() - rand(0, 7 * 86400_000)).toISOString();
      const it = rand(500, 8000);
      const ot = rand(100, 2000);
      return {
        id,
        user_id: rand(1, NAMES.length),
        api_key_id: 100 + rand(0, 15),
        account_id: rand(1, 4),
        request_id: `req_${id}`,
        model: MODELS[rand(0, MODELS.length)],
        input_tokens: it,
        output_tokens: ot,
        cache_creation_tokens: rand(0, 200),
        cache_read_tokens: rand(0, 1500),
        input_cost: it * 0.000003,
        output_cost: ot * 0.000015,
        cache_creation_cost: 0,
        cache_read_cost: 0,
        total_cost: it * 0.000003 + ot * 0.000015,
        actual_cost: (it * 0.000003 + ot * 0.000015) * 0.9,
        stream: Math.random() < 0.6,
        duration_ms: rand(200, 8000),
        first_token_ms: rand(80, 1500),
        created_at: created,
        ip_address: MOCK_IPS[rand(0, MOCK_IPS.length)],
        user_agent: MOCK_UAS[rand(0, MOCK_UAS.length)],
      };
    });
    return delay({
      items,
      total: 1000,
      page,
      page_size: pageSize,
    });
  },

  groupsAll: (_platform?: string): Promise<AdminGroup[]> =>
    delay(
      NAMES.slice(0, 4).map((n, i) => ({
        id: i + 1,
        name: `${n}的池`,
        description: `${n} 专属上游池`,
        platform: 'anthropic',
        status: 'active',
        is_exclusive: false,
        sort_order: i,
        rate_multiplier: 1,
      })),
    ),

  groupApiKeys: (
    groupId: number,
    page: number,
    pageSize: number,
  ): Promise<Paginated<ApiKey>> => {
    const ids = mockApiKeyIds();
    const slice = ids.filter((_, i) => i % 4 === (groupId - 1) % 4);
    const items: ApiKey[] = slice.map((id) => ({
      id,
      user_id: 1,
      name: `${NAMES[(groupId - 1) % NAMES.length]}-${id}`,
      group_id: groupId,
      status: 'active',
      quota: 100,
      quota_used: Math.random() * 50,
      created_at: new Date(Date.now() - rand(1, 60) * 86400_000).toISOString(),
      last_used_at: new Date(Date.now() - rand(0, 7) * 86400_000).toISOString(),
    }));
    return delay({ items, total: items.length, page, page_size: pageSize });
  },

  searchApiKeys: (q: string, _user_id?: number): Promise<SearchApiKeyHit[]> =>
    delay(
      mockApiKeyIds()
        .filter((id) => String(id).includes(q))
        .slice(0, 30)
        .map((id) => ({
          id,
          name: `key-${id}`,
          user_id: 1,
        })),
    ),

  searchUsers: (q: string): Promise<SearchUserHit[]> =>
    delay(
      NAMES.filter((n) => n.includes(q) || `${n}@team.local`.includes(q)).map((n, i) => ({
        id: i + 1,
        email: `${n}@team.local`,
        deleted: false,
      })),
    ),
};
