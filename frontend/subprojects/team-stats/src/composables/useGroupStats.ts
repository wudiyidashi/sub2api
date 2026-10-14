import { computed, ref, watch } from 'vue';
import { endpoints } from '@/api/endpoints';
import type { ApiKeyUsageStats, TrendBucket } from '@/api/types';
import { useConfigStore, type VirtualGroup } from '@/stores/config';
import { useRangeStore } from '@/stores/range';
import { mergeTrends, pMapLimit, sumUsage, summarizeTrend } from '@/utils/reduce';

export interface GroupAggregate {
  group: VirtualGroup;
  usage: Omit<ApiKeyUsageStats, 'api_key_id'>;
  trend: TrendBucket[];
  perKey: Record<number, ApiKeyUsageStats>;
}

/**
 * 聚合一个虚拟分组的"本期"统计。
 * 因为 sub2api 的 api-keys-usage 接口只返回 today/total_actual_cost 两个字段，
 * 所有"按 key 求和"的指标都改成从 dashboard/trend 桶累加得出。
 */
export function useGroupAggregate(group: () => VirtualGroup | null | undefined) {
  const range = useRangeStore();
  const config = useConfigStore();

  const data = ref<GroupAggregate | null>(null);
  const loading = ref(false);
  const error = ref<string | null>(null);

  const key = computed(() => {
    const g = group();
    if (!g) return null;
    return [
      g.id,
      g.apiKeyIds.join(','),
      range.apiDates.start_date,
      range.apiDates.end_date,
      range.resolved.granularity,
      config.timezone,
    ].join('|');
  });

  async function fetchOnce(g: VirtualGroup) {
    loading.value = true;
    error.value = null;
    try {
      if (!g.apiKeyIds.length) {
        data.value = {
          group: g,
          usage: emptyUsageBody(),
          trend: [],
          perKey: {},
        };
        return;
      }

      const trends = await pMapLimit(
        g.apiKeyIds,
        async (id) => {
          const r = await endpoints.dashboardTrend({
            ...range.apiDates,
            granularity: range.resolved.granularity,
            timezone: config.timezone,
            api_key_id: id,
          });
          return { id, trend: r.trend };
        },
        6,
      );

      const perKey: Record<number, ApiKeyUsageStats> = {};
      for (const { id, trend } of trends) {
        perKey[id] = summarizeTrend(id, trend);
      }

      data.value = {
        group: g,
        usage: sumUsage(Object.values(perKey)),
        trend: mergeTrends(trends.map((t) => t.trend)),
        perKey,
      };
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  watch(
    key,
    () => {
      const g = group();
      if (g) void fetchOnce(g);
      else data.value = null;
    },
    { immediate: true },
  );

  return {
    data,
    loading,
    error,
    refresh: () => {
      const g = group();
      if (g) return fetchOnce(g);
      return Promise.resolve();
    },
  };
}

/** 对配置里的所有分组并发聚合（仅用于"总览 / 排行"）。 */
export async function aggregateAllGroups(): Promise<GroupAggregate[]> {
  const config = useConfigStore();
  const range = useRangeStore();
  return pMapLimit(
    config.groups,
    async (g) => {
      if (!g.apiKeyIds.length) {
        return { group: g, usage: emptyUsageBody(), trend: [], perKey: {} };
      }
      const trends = await pMapLimit(
        g.apiKeyIds,
        async (id) => ({
          id,
          trend: (
            await endpoints.dashboardTrend({
              ...range.apiDates,
              granularity: range.resolved.granularity,
              timezone: config.timezone,
              api_key_id: id,
            })
          ).trend,
        }),
        4,
      );
      const perKey: Record<number, ApiKeyUsageStats> = {};
      for (const { id, trend } of trends) {
        perKey[id] = summarizeTrend(id, trend);
      }
      return {
        group: g,
        usage: sumUsage(Object.values(perKey)),
        trend: mergeTrends(trends.map((t) => t.trend)),
        perKey,
      };
    },
    3,
  );
}

function emptyUsageBody(): Omit<ApiKeyUsageStats, 'api_key_id'> {
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
