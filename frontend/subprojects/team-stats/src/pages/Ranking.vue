<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useConfigStore } from '@/stores/config';
import { useAutoRefresh } from '@/composables/useAutoRefresh';
import {
  aggregateGroupsForRange,
  metricOfBucket,
  metricOfUsage,
  resolveRangeSpec,
  type GroupRank,
  type RankMetric,
  type RankMode,
} from '@/composables/useRanking';
import { endpoints } from '@/api/endpoints';
import { mergeTrends, pMapLimit } from '@/utils/reduce';
import { formatCost, formatNumber, formatTokens } from '@/utils/format';
import { formatBucketLabel, hourAndDow, ymdToday } from '@/utils/time';
import { spreadColors } from '@/utils/colors';
import type { ModelUsage } from '@/api/types';

import VChart from 'vue-echarts';
import 'echarts';

const config = useConfigStore();

// ---- 页内独立时间口径（不动全局 range store） ----
const mode = ref<RankMode>('day');
const anchor = ref<string>(ymdToday(config.timezone)); // YYYY-MM-DD
const metric = ref<RankMetric>('tokens');

const modeTabs: { value: RankMode; label: string }[] = [
  { value: 'day', label: '日' },
  { value: 'week', label: '周' },
  { value: 'month', label: '月' },
];
const metricTabs: { value: RankMetric; label: string }[] = [
  { value: 'tokens', label: 'Token' },
  { value: 'cost', label: '成本' },
  { value: 'requests', label: '请求' },
];

const spec = computed(() => resolveRangeSpec(mode.value, anchor.value, config.timezone));
const costActual = computed(() => config.costSource === 'actual');

// month 选择器绑定 YYYY-MM
const anchorMonth = computed({
  get: () => anchor.value.slice(0, 7),
  set: (v: string) => {
    if (v) anchor.value = `${v}-01`;
  },
});

function pad(n: number): string {
  return String(n).padStart(2, '0');
}
function fmtYmd(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
/** 上一 / 下一 天·周·月 */
function stepAnchor(dir: number) {
  const d = new Date(`${anchor.value}T00:00:00`);
  if (mode.value === 'day') d.setDate(d.getDate() + dir);
  else if (mode.value === 'week') d.setDate(d.getDate() + 7 * dir);
  else d.setMonth(d.getMonth() + dir);
  anchor.value = fmtYmd(d);
}

// ---- 取数 ----
const ranks = ref<GroupRank[]>([]);
const loading = ref(false);
const err = ref<string | null>(null);

async function loadAll() {
  if (!config.isConfigured()) return;
  loading.value = true;
  err.value = null;
  try {
    ranks.value = await aggregateGroupsForRange(config.groups, spec.value);
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}

const groupsKey = computed(() =>
  config.groups.map((g) => `${g.id}:${g.apiKeyIds.join(',')}`).join('|'),
);

watch(
  [
    () => config.authed && config.hydrated,
    () => spec.value.start_date,
    () => spec.value.end_date,
    () => spec.value.granularity,
    () => config.timezone,
    groupsKey,
  ],
  ([ready]) => {
    if (ready) void loadAll();
  },
  { immediate: true },
);
useAutoRefresh(loadAll);

// ---- 展示层配色避让（不改配置原色） ----
const displayColors = computed<Record<string, string>>(() => {
  const spread = spreadColors(ranks.value.map((r) => r.group.color));
  const map: Record<string, string> = {};
  ranks.value.forEach((r, i) => {
    map[r.group.id] = spread[i];
  });
  return map;
});
function groupColor(r: GroupRank): string {
  return displayColors.value[r.group.id] || r.group.color;
}

// ---- 排行表 ----
const sortedRanks = computed(() =>
  [...ranks.value].sort(
    (a, b) =>
      metricOfUsage(b.usage, metric.value, costActual.value) -
      metricOfUsage(a.usage, metric.value, costActual.value),
  ),
);

const totalMetric = computed(() =>
  ranks.value.reduce((s, r) => s + metricOfUsage(r.usage, metric.value, costActual.value), 0),
);

function fmtMetric(v: number): string {
  if (metric.value === 'cost') return formatCost(v, config.currency);
  if (metric.value === 'requests') return formatNumber(v);
  return formatTokens(v);
}

const expanded = ref<Set<string>>(new Set());
function toggleExpand(id: string) {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
}

interface KeyRow {
  id: number;
  name: string;
  requests: number;
  total_tokens: number;
  actual_cost: number;
  total_cost: number;
  metric: number;
}
function keyRows(r: GroupRank): KeyRow[] {
  return r.group.apiKeyIds
    .map((id) => {
      const meta = config.allApiKeys.find((k) => k.id === id);
      const u = r.perKey[id];
      const row = {
        id,
        name: meta?.name || `key-${id}`,
        requests: u?.requests || 0,
        total_tokens: u?.total_tokens || 0,
        actual_cost: u?.actual_cost || 0,
        total_cost: u?.total_cost || 0,
      };
      return { ...row, metric: metricOfUsage(row, metric.value, costActual.value) };
    })
    .sort((a, b) => b.metric - a.metric);
}

// ---- 分组并排柱状图（X=时间桶，每组一色） ----
const timestamps = computed(() =>
  Array.from(new Set(ranks.value.flatMap((r) => r.trend.map((t) => t.timestamp)))).sort(),
);
const xAxisLabels = computed(() =>
  timestamps.value.map((ts) => formatBucketLabel(ts, spec.value.granularity, config.timezone)),
);

function chartColors() {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  return {
    isDark,
    tooltipBg: isDark ? '#1f1f23' : '#ffffff',
    tooltipBorder: isDark ? '#3a3a3c' : '#e0e0e0',
    tooltipText: isDark ? '#ffffff' : '#1d1d1f',
    axisLabel: isDark ? '#8e8e93' : '#7a7a7a',
    axisLine: isDark ? '#3a3a3c' : '#e0e0e0',
    splitLine: isDark ? '#272729' : '#f0f0f0',
  };
}

const groupedBarOption = computed(() => {
  const c = chartColors();
  const series = ranks.value
    .filter((r) => r.trend.some((t) => metricOfBucket(t, metric.value, costActual.value) > 0))
    .map((r) => {
      const dataMap = new Map(
        r.trend.map((t) => [t.timestamp, metricOfBucket(t, metric.value, costActual.value)]),
      );
      return {
        name: r.group.name,
        type: 'bar' as const,
        data: timestamps.value.map((ts) => dataMap.get(ts) || 0),
        itemStyle: { color: groupColor(r), borderRadius: [3, 3, 0, 0] as [number, number, number, number] },
        barMaxWidth: 26,
        emphasis: { focus: 'series' as const },
      };
    });

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      valueFormatter: (v: number) => fmtMetric(v),
    },
    legend: { textStyle: { color: c.axisLabel }, icon: 'circle', type: 'scroll' },
    grid: { left: '3%', right: '4%', bottom: '3%', top: '14%', containLabel: true },
    xAxis: {
      type: 'category',
      data: xAxisLabels.value,
      axisLabel: { color: c.axisLabel, fontSize: 10 },
      axisLine: { lineStyle: { color: c.axisLine } },
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        color: c.axisLabel,
        formatter: (v: number) =>
          metric.value === 'cost' ? formatCost(v, config.currency) : formatNumber(v, { short: true }),
      },
      splitLine: { lineStyle: { color: c.splitLine } },
    },
    series,
  };
});

const hasChartData = computed(() => groupedBarOption.value.series.length > 0);

// ---- 下钻：选中分组的模型分布 + 时段分布 ----
const selectedGroupId = ref<string | null>(null);
const selectedRank = computed(() => ranks.value.find((r) => r.group.id === selectedGroupId.value) || null);

const groupModels = ref<ModelUsage[]>([]);
const hourlyDist = ref<number[]>(new Array(24).fill(0));
const loadingDrill = ref(false);

async function loadDrill() {
  const r = selectedRank.value;
  if (!r || !r.group.apiKeyIds.length) {
    groupModels.value = [];
    hourlyDist.value = new Array(24).fill(0);
    return;
  }
  loadingDrill.value = true;
  try {
    const s = spec.value;
    // 模型分布：并发拉每个 key 的 models 再合并
    const modelsList = await pMapLimit(
      r.group.apiKeyIds,
      (id) =>
        endpoints
          .dashboardModels({ start_date: s.start_date, end_date: s.end_date, timezone: s.timezone, api_key_id: id })
          .then((res) => res.models)
          .catch(() => [] as ModelUsage[]),
      4,
    );
    const merged = new Map<string, ModelUsage>();
    for (const list of modelsList) {
      for (const m of list) {
        const cur = merged.get(m.model);
        if (!cur) merged.set(m.model, { ...m });
        else {
          cur.requests += m.requests;
          cur.total_tokens += m.total_tokens;
          cur.total_cost += m.total_cost;
          cur.actual_cost += m.actual_cost;
        }
      }
    }
    groupModels.value = Array.from(merged.values()).sort((a, b) => b.total_tokens - a.total_tokens);

    // 时段分布（24h）：day 口径已是小时粒度可直接复用，否则单独按小时拉
    let hourBuckets = r.trend;
    if (s.granularity !== 'hour') {
      const hTrends = await pMapLimit(
        r.group.apiKeyIds,
        (id) =>
          endpoints
            .dashboardTrend({
              start_date: s.start_date,
              end_date: s.end_date,
              granularity: 'hour',
              timezone: s.timezone,
              api_key_id: id,
            })
            .then((res) => res.trend)
            .catch(() => []),
        4,
      );
      hourBuckets = mergeTrends(hTrends);
    }
    const buckets = new Array(24).fill(0) as number[];
    for (const b of hourBuckets) {
      if (!b.requests) continue;
      const { hour } = hourAndDow(b.timestamp, config.timezone);
      buckets[hour] += b.requests;
    }
    hourlyDist.value = buckets;
  } catch (e) {
    console.error('Failed to load ranking drill-down', e);
  } finally {
    loadingDrill.value = false;
  }
}

watch(
  [selectedGroupId, () => spec.value.start_date, () => spec.value.end_date, groupsKey],
  () => {
    if (selectedGroupId.value) void loadDrill();
  },
);

function selectGroup(id: string) {
  selectedGroupId.value = selectedGroupId.value === id ? null : id;
}

const modelBarOption = computed(() => {
  const c = chartColors();
  const color = selectedRank.value ? groupColor(selectedRank.value) : '#0066cc';
  const top = [...groupModels.value].slice(0, 8).reverse();
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      formatter: (params: any[]) => {
        const raw = top[params[0].dataIndex];
        if (!raw) return '';
        return `<b>${raw.model}</b><br/>请求数: ${formatNumber(raw.requests)}<br/>Token: ${formatTokens(
          raw.total_tokens,
        )}<br/>成本: ${formatCost(costActual.value ? raw.actual_cost : raw.total_cost, config.currency)}`;
      },
    },
    grid: { left: '3%', right: '6%', bottom: '3%', top: '5%', containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { color: c.axisLabel, formatter: (v: number) => formatNumber(v, { short: true }) },
      splitLine: { lineStyle: { color: c.splitLine } },
    },
    yAxis: {
      type: 'category',
      data: top.map((m) => m.model),
      axisLabel: { color: c.axisLabel, fontSize: 11 },
      axisLine: { lineStyle: { color: c.axisLine } },
    },
    series: [
      {
        name: 'Tokens',
        type: 'bar',
        data: top.map((m) => m.total_tokens),
        itemStyle: { color, borderRadius: [0, 6, 6, 0] as [number, number, number, number] },
        barMaxWidth: 16,
      },
    ],
  };
});

const hourlyOption = computed(() => {
  const c = chartColors();
  const color = selectedRank.value ? groupColor(selectedRank.value) : '#0066cc';
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      valueFormatter: (v: number) => `${formatNumber(v)} 次`,
    },
    grid: { left: '3%', right: '3%', bottom: '3%', top: '12%', containLabel: true },
    xAxis: {
      type: 'category',
      data: Array.from({ length: 24 }, (_, i) => String(i).padStart(2, '0')),
      axisLabel: { color: c.axisLabel, fontSize: 10, interval: 1 },
      axisLine: { lineStyle: { color: c.axisLine } },
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: c.axisLabel, formatter: (v: number) => formatNumber(v, { short: true }) },
      splitLine: { lineStyle: { color: c.splitLine } },
    },
    series: [
      {
        name: '请求数',
        type: 'bar',
        data: hourlyDist.value,
        itemStyle: { color, borderRadius: [3, 3, 0, 0] as [number, number, number, number] },
        barMaxWidth: 16,
      },
    ],
  };
});
</script>

<template>
  <div class="space-y-6 max-w-[1440px] mx-auto pb-12">
    <!-- 控制条：口径 + 日期 + 指标 -->
    <div class="apple-card flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
      <div class="flex flex-wrap items-center gap-3">
        <!-- 日/周/月 -->
        <div class="flex rounded-full bg-bg p-0.5 border border-border">
          <button
            v-for="t in modeTabs"
            :key="t.value"
            class="px-4 py-1 text-xs rounded-full transition-all"
            :class="mode === t.value ? 'bg-surface text-accent shadow-sm font-semibold' : 'text-muted hover:text-text'"
            @click="mode = t.value"
          >
            {{ t.label }}
          </button>
        </div>

        <!-- 日期选择 + 前后翻页 -->
        <div class="flex items-center gap-1">
          <button class="btn-secondary px-2 h-7 text-xs" title="上一段" @click="stepAnchor(-1)">‹</button>
          <input
            v-if="mode === 'month'"
            type="month"
            v-model="anchorMonth"
            class="apple-input text-xs py-1 px-2 h-7 outline-none"
          />
          <input
            v-else
            type="date"
            v-model="anchor"
            class="apple-input text-xs py-1 px-2 h-7 outline-none"
          />
          <button class="btn-secondary px-2 h-7 text-xs" title="下一段" @click="stepAnchor(1)">›</button>
        </div>

        <span class="text-[11px] text-muted font-mono">{{ spec.label }} · {{ config.timezone }}</span>
      </div>

      <!-- 指标切换 -->
      <div class="flex items-center gap-2">
        <span class="text-xs text-muted">排行指标</span>
        <div class="flex rounded-full bg-bg p-0.5 border border-border">
          <button
            v-for="t in metricTabs"
            :key="t.value"
            class="px-3.5 py-1 text-xs rounded-full transition-all"
            :class="metric === t.value ? 'bg-surface text-accent shadow-sm font-semibold' : 'text-muted hover:text-text'"
            @click="metric = t.value"
          >
            {{ t.label }}
          </button>
        </div>
      </div>
    </div>

    <!-- 错误 -->
    <div v-if="err" class="flex items-center gap-3 rounded-xl border border-error/20 bg-error/10 px-5 py-4 text-error">
      <span class="text-sm font-medium">数据加载失败: {{ err }}</span>
    </div>

    <!-- 无分组提示 -->
    <div v-else-if="!config.groups.length" class="apple-card py-12 text-center text-sm text-muted">
      暂无虚拟分组，请先在「分组」页配置人名分组与其 API key。
    </div>

    <template v-else>
      <!-- 分组并排柱状图 -->
      <div class="apple-card flex flex-col">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="text-base font-semibold text-text">分组用量对比（{{ metricTabs.find((t) => t.value === metric)?.label }}）</h2>
          <span class="text-xs text-muted">粒度: {{ spec.granularity === 'hour' ? '小时级' : '天级' }}</span>
        </div>
        <div class="h-80 w-full relative">
          <div v-if="loading" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
            <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
          </div>
          <v-chart v-else-if="hasChartData" class="h-full w-full" :option="groupedBarOption" autocall />
          <div v-else class="absolute inset-0 flex items-center justify-center text-sm text-muted">本期各分组无用量数据</div>
        </div>
      </div>

      <!-- 排行榜表格 -->
      <div class="apple-card">
        <h2 class="mb-4 text-base font-semibold text-text">分组排行榜</h2>
        <div v-if="loading" class="space-y-2">
          <div v-for="i in 5" :key="i" class="h-12 animate-pulse rounded-xl bg-surface border border-border"></div>
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-left text-xs text-muted border-b border-border">
                <th class="py-2 pr-2 font-semibold w-12">#</th>
                <th class="py-2 pr-2 font-semibold w-10">颜色</th>
                <th class="py-2 pr-4 font-semibold">分组</th>
                <th class="py-2 pr-4 font-semibold text-right">Keys</th>
                <th class="py-2 pr-4 font-semibold text-right">请求</th>
                <th class="py-2 pr-4 font-semibold text-right">Token</th>
                <th class="py-2 pr-4 font-semibold text-right">成本</th>
                <th class="py-2 pr-2 font-semibold text-right">占比</th>
                <th class="py-2 pr-2 font-semibold text-right w-20"></th>
              </tr>
            </thead>
            <tbody>
              <template v-for="(r, idx) in sortedRanks" :key="r.group.id">
                <tr
                  class="border-b border-border/50 hover:bg-surface-hover transition-colors cursor-pointer"
                  :class="{ 'bg-surface-hover': selectedGroupId === r.group.id }"
                  @click="toggleExpand(r.group.id)"
                >
                  <td class="py-2.5 pr-2">
                    <span class="flex h-5 w-5 items-center justify-center rounded-full bg-bg text-xs font-semibold text-muted">{{ idx + 1 }}</span>
                  </td>
                  <td class="py-2.5 pr-2">
                    <span class="inline-block h-3.5 w-3.5 rounded-full" :style="{ backgroundColor: groupColor(r) }"></span>
                  </td>
                  <td class="py-2.5 pr-4 font-medium text-text">{{ r.group.name }}</td>
                  <td class="py-2.5 pr-4 text-right text-muted font-mono">{{ r.group.apiKeyIds.length }}</td>
                  <td class="py-2.5 pr-4 text-right font-mono">{{ formatNumber(r.usage.requests) }}</td>
                  <td class="py-2.5 pr-4 text-right font-mono">{{ formatTokens(r.usage.total_tokens) }}</td>
                  <td class="py-2.5 pr-4 text-right font-mono">{{ formatCost(costActual ? r.usage.actual_cost : r.usage.total_cost, config.currency) }}</td>
                  <td class="py-2.5 pr-2 text-right text-muted font-mono">
                    {{ totalMetric > 0 ? ((metricOfUsage(r.usage, metric, costActual) / totalMetric) * 100).toFixed(1) + '%' : '—' }}
                  </td>
                  <td class="py-2.5 pr-2 text-right">
                    <button
                      class="text-[11px] text-accent hover:text-accent-focus font-medium"
                      @click.stop="selectGroup(r.group.id)"
                    >
                      {{ selectedGroupId === r.group.id ? '收起详情' : '模型/时段' }}
                    </button>
                  </td>
                </tr>
                <!-- 展开：该组每个 key 的用量 -->
                <tr v-if="expanded.has(r.group.id)" :key="r.group.id + '-keys'">
                  <td colspan="9" class="bg-bg/40 px-4 py-3">
                    <div v-if="!r.group.apiKeyIds.length" class="text-xs text-muted">该分组未绑定任何 API key</div>
                    <table v-else class="w-full text-xs">
                      <thead>
                        <tr class="text-left text-muted">
                          <th class="py-1 pr-4 font-medium">Key</th>
                          <th class="py-1 pr-4 font-medium text-right">请求</th>
                          <th class="py-1 pr-4 font-medium text-right">Token</th>
                          <th class="py-1 pr-4 font-medium text-right">成本</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr v-for="k in keyRows(r)" :key="k.id" class="border-t border-border/40">
                          <td class="py-1.5 pr-4 font-mono text-text truncate max-w-[220px]">
                            {{ k.name }} <span class="text-muted">#{{ k.id }}</span>
                          </td>
                          <td class="py-1.5 pr-4 text-right font-mono">{{ formatNumber(k.requests) }}</td>
                          <td class="py-1.5 pr-4 text-right font-mono">{{ formatTokens(k.total_tokens) }}</td>
                          <td class="py-1.5 pr-4 text-right font-mono">{{ formatCost(costActual ? k.actual_cost : k.total_cost, config.currency) }}</td>
                        </tr>
                      </tbody>
                    </table>
                  </td>
                </tr>
              </template>
              <tr v-if="!sortedRanks.length">
                <td colspan="9" class="py-8 text-center text-sm text-muted">暂无数据</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 下钻：模型分布 + 时段分布 -->
      <div v-if="selectedRank" class="grid grid-cols-12 gap-6">
        <div class="apple-card col-span-12 lg:col-span-6 flex flex-col">
          <div class="mb-4 flex items-center gap-2">
            <span class="inline-block h-3 w-3 rounded-full" :style="{ backgroundColor: groupColor(selectedRank) }"></span>
            <h2 class="text-base font-semibold text-text">{{ selectedRank.group.name }} · 模型分布</h2>
          </div>
          <div class="h-72 w-full relative">
            <div v-if="loadingDrill" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
              <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
            </div>
            <v-chart v-else-if="groupModels.length" class="h-full w-full" :option="modelBarOption" autocall />
            <div v-else class="absolute inset-0 flex items-center justify-center text-sm text-muted">本期无模型调用记录</div>
          </div>
        </div>

        <div class="apple-card col-span-12 lg:col-span-6 flex flex-col">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="text-base font-semibold text-text">{{ selectedRank.group.name }} · 时段分布（24h）</h2>
            <span class="text-[11px] text-muted">按请求数 · {{ config.timezone }}</span>
          </div>
          <div class="h-72 w-full relative">
            <div v-if="loadingDrill" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
              <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
            </div>
            <v-chart v-else class="h-full w-full" :option="hourlyOption" autocall />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped></style>
