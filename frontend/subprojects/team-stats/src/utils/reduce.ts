import type { ApiKeyUsageStats, TrendBucket } from '@/api/types';

export function emptyUsage(): Omit<ApiKeyUsageStats, 'api_key_id'> {
  return {
    requests: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_creation_tokens: 0,
    cache_read_tokens: 0,
    total_tokens: 0,
    total_cost: 0,
    actual_cost: 0,
    duration_ms_sum: 0,
  };
}

/** 把多个 key 的统计求和。 */
export function sumUsage(stats: ApiKeyUsageStats[]): Omit<ApiKeyUsageStats, 'api_key_id'> {
  if (!Array.isArray(stats)) return emptyUsage();
  return stats.reduce(
    (acc, s) => {
      acc.requests += s.requests || 0;
      acc.input_tokens += s.input_tokens || 0;
      acc.output_tokens += s.output_tokens || 0;
      acc.cache_creation_tokens += s.cache_creation_tokens || 0;
      acc.cache_read_tokens += s.cache_read_tokens || 0;
      acc.total_tokens += s.total_tokens || 0;
      acc.total_cost += s.total_cost || 0;
      acc.actual_cost += s.actual_cost || 0;
      acc.duration_ms_sum = (acc.duration_ms_sum || 0) + (s.duration_ms_sum || 0);
      return acc;
    },
    emptyUsage(),
  );
}

/** 把多条 trend（不同 key 同一时间范围）按 timestamp 桶相加。 */
export function mergeTrends(series: TrendBucket[][]): TrendBucket[] {
  if (!Array.isArray(series) || series.length === 0) return [];
  const idx = new Map<string, TrendBucket>();
  for (const arr of series) {
    if (!Array.isArray(arr)) continue;
    for (const b of arr) {
      const cur = idx.get(b.timestamp);
      if (!cur) {
        idx.set(b.timestamp, { ...b });
      } else {
        cur.requests += b.requests || 0;
        cur.input_tokens += b.input_tokens || 0;
        cur.output_tokens += b.output_tokens || 0;
        cur.cache_creation_tokens += b.cache_creation_tokens || 0;
        cur.cache_read_tokens += b.cache_read_tokens || 0;
        cur.total_tokens += b.total_tokens || 0;
        cur.total_cost += b.total_cost || 0;
        cur.actual_cost += b.actual_cost || 0;
      }
    }
  }
  return Array.from(idx.values()).sort((a, b) =>
    a.timestamp < b.timestamp ? -1 : 1,
  );
}

/** 把一段 trend 桶累加成一个 usage 形状（api-keys-usage 字段不全，我们改从 trend 求和）。 */
export function summarizeTrend(apiKeyId: number, trend: TrendBucket[]): ApiKeyUsageStats {
  if (!Array.isArray(trend)) trend = [];
  let requests = 0,
    input_tokens = 0,
    output_tokens = 0,
    cache_creation_tokens = 0,
    cache_read_tokens = 0,
    total_tokens = 0,
    total_cost = 0,
    actual_cost = 0;
  for (const b of trend) {
    requests += b.requests || 0;
    input_tokens += b.input_tokens || 0;
    output_tokens += b.output_tokens || 0;
    cache_creation_tokens += b.cache_creation_tokens || 0;
    cache_read_tokens += b.cache_read_tokens || 0;
    total_tokens += b.total_tokens || 0;
    total_cost += b.total_cost || 0;
    actual_cost += b.actual_cost || 0;
  }
  return {
    api_key_id: apiKeyId,
    requests,
    input_tokens,
    output_tokens,
    cache_creation_tokens,
    cache_read_tokens,
    total_tokens,
    total_cost,
    actual_cost,
  };
}

/** 顺序受限的并发执行器（每批 N 个）。 */
export async function pMapLimit<I, O>(
  items: I[],
  fn: (item: I, idx: number) => Promise<O>,
  limit = 6,
): Promise<O[]> {
  const out: O[] = new Array(items.length);
  let i = 0;
  async function worker() {
    while (true) {
      const cur = i++;
      if (cur >= items.length) return;
      out[cur] = await fn(items[cur], cur);
    }
  }
  const workers = Array.from({ length: Math.min(limit, items.length) }, worker);
  await Promise.all(workers);
  return out;
}
