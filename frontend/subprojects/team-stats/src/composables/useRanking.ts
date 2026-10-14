import { endpoints } from '@/api/endpoints';
import type { ApiKeyUsageStats, TrendBucket } from '@/api/types';
import type { VirtualGroup } from '@/stores/config';
import { emptyUsage, mergeTrends, pMapLimit, summarizeTrend, sumUsage } from '@/utils/reduce';
import { ymdToday } from '@/utils/time';

/** 排行榜页的时间口径：某一天 / 某一周 / 某一月 */
export type RankMode = 'day' | 'week' | 'month';

/** 排行榜主指标（可在页内切换） */
export type RankMetric = 'tokens' | 'cost' | 'requests';

/** 解析后的取数区间（sub2api 接口口径，YYYY-MM-DD 含首尾） */
export interface RangeSpec {
  start_date: string;
  end_date: string;
  granularity: 'hour' | 'day';
  timezone: string;
  /** 人类可读的区间描述，用于标题展示 */
  label: string;
}

/** 一个分组在指定区间内的聚合结果 */
export interface GroupRank {
  group: VirtualGroup;
  /** 该组所有 key 的 trend 按时间桶合并 */
  trend: TrendBucket[];
  /** 该组本期总用量 */
  usage: Omit<ApiKeyUsageStats, 'api_key_id'>;
  /** 每个 key 的本期用量，用于下钻/展开 */
  perKey: Record<number, ApiKeyUsageStats>;
}

function pad(n: number): string {
  return String(n).padStart(2, '0');
}

function fmtYmd(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/**
 * 把「口径 + 锚点日期」解析成取数区间。
 * - day：当天 00:00~24:00，小时粒度（可看时段分布）
 * - week：锚点所在周（周一起 → 周日），天粒度
 * - month：锚点所在自然月，天粒度
 */
export function resolveRangeSpec(mode: RankMode, anchor: string, timezone: string): RangeSpec {
  const a = anchor || ymdToday(timezone);

  if (mode === 'day') {
    return { start_date: a, end_date: a, granularity: 'hour', timezone, label: a };
  }

  if (mode === 'week') {
    const d = new Date(`${a}T00:00:00`);
    const dow = (d.getDay() + 6) % 7; // 0=周一
    const monday = new Date(d);
    monday.setDate(d.getDate() - dow);
    const sunday = new Date(monday);
    sunday.setDate(monday.getDate() + 6);
    const start = fmtYmd(monday);
    const end = fmtYmd(sunday);
    return { start_date: start, end_date: end, granularity: 'day', timezone, label: `${start} ~ ${end}` };
  }

  // month
  const [y, m] = a.split('-').map(Number);
  const lastDay = new Date(y, m, 0).getDate(); // m 为 1-based，第 0 天即上月末→本月天数
  const start = `${y}-${pad(m)}-01`;
  const end = `${y}-${pad(m)}-${pad(lastDay)}`;
  return { start_date: start, end_date: end, granularity: 'day', timezone, label: `${y}-${pad(m)}` };
}

/** 从一个 usage/桶里按当前主指标取值（成本口径由调用方决定 actual/total） */
export function metricOfUsage(
  u: Pick<ApiKeyUsageStats, 'total_tokens' | 'requests' | 'actual_cost' | 'total_cost'>,
  metric: RankMetric,
  costActual: boolean,
): number {
  if (metric === 'tokens') return u.total_tokens || 0;
  if (metric === 'requests') return u.requests || 0;
  return (costActual ? u.actual_cost : u.total_cost) || 0;
}

/** 从一个 trend 桶里按当前主指标取值 */
export function metricOfBucket(b: TrendBucket, metric: RankMetric, costActual: boolean): number {
  if (metric === 'tokens') return b.total_tokens || 0;
  if (metric === 'requests') return b.requests || 0;
  return (costActual ? b.actual_cost : b.total_cost) || 0;
}

/**
 * 对给定分组在指定区间内并发聚合（跨分组排行用）。
 * 不依赖全局 range store，区间完全由入参决定，避免污染其他页。
 */
export async function aggregateGroupsForRange(
  groups: VirtualGroup[],
  spec: RangeSpec,
): Promise<GroupRank[]> {
  return pMapLimit(
    groups,
    async (g): Promise<GroupRank> => {
      if (!g.apiKeyIds.length) {
        return { group: g, trend: [], usage: emptyUsage(), perKey: {} };
      }
      const trends = await pMapLimit(
        g.apiKeyIds,
        async (id) => ({
          id,
          trend: (
            await endpoints.dashboardTrend({
              start_date: spec.start_date,
              end_date: spec.end_date,
              granularity: spec.granularity,
              timezone: spec.timezone,
              api_key_id: id,
            })
          ).trend,
        }),
        4,
      );
      const perKey: Record<number, ApiKeyUsageStats> = {};
      for (const { id, trend } of trends) perKey[id] = summarizeTrend(id, trend);
      return {
        group: g,
        trend: mergeTrends(trends.map((t) => t.trend)),
        usage: sumUsage(Object.values(perKey)),
        perKey,
      };
    },
    3,
  );
}
