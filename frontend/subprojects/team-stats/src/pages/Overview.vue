<script setup lang="ts">
import { ref, watch, computed } from 'vue';
import { endpoints } from '@/api/endpoints';
import { useRangeStore } from '@/stores/range';
import { useConfigStore } from '@/stores/config';
import { aggregateAllGroups, type GroupAggregate } from '@/composables/useGroupStats';
import { useAutoRefresh } from '@/composables/useAutoRefresh';
import { formatCost, formatNumber, formatTokens } from '@/utils/format';
import { formatBucketLabel } from '@/utils/time';
import { resolveIpGeos, type IpGeo } from '@/utils/ipGeo';
import { ensureWorldMap } from '@/utils/worldMap';
import { spreadColors } from '@/utils/colors';
import type { DashboardStats, UsageStats, ModelsResponse } from '@/api/types';

import VChart from 'vue-echarts';
import * as echarts from 'echarts';

const range = useRangeStore();
const config = useConfigStore();

const stats = ref<DashboardStats | null>(null);
const currentRangeStats = ref<UsageStats | null>(null);
const previousRangeStats = ref<UsageStats | null>(null);
const modelsData = ref<ModelsResponse | null>(null);
const groupsAgg = ref<GroupAggregate[]>([]);

const loading = ref(false);
const err = ref<string | null>(null);

// Get previous date range as YYYY-MM-DD
const prevApiDates = computed(() => {
  const { prevStart, prevEnd } = range.resolved;
  const fmt = (d: Date) => {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
  };
  const lastDay = new Date(prevEnd.getTime() - 1);
  return { start_date: fmt(prevStart), end_date: fmt(lastDay) };
});

async function load() {
  if (!config.isConfigured()) return;
  loading.value = true;
  err.value = null;
  try {
    const [s, curStats, prevStats, md, g] = await Promise.all([
      endpoints.dashboardStats(),
      endpoints.usageStatsByRange({ ...range.apiDates, timezone: config.timezone }),
      endpoints.usageStatsByRange({ ...prevApiDates.value, timezone: config.timezone }),
      endpoints.dashboardModels({ ...range.apiDates, timezone: config.timezone }),
      aggregateAllGroups()
    ]);

    stats.value = s;
    currentRangeStats.value = curStats;
    previousRangeStats.value = prevStats;
    modelsData.value = md;
    groupsAgg.value = g;
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}

// ---- 全站请求 IP 分布（不按 key 过滤，直接采样全局明细） ----
const IP_SAMPLE_PAGES = 5;

interface IpStatRow {
  ip: string;
  requests: number;
  geo: IpGeo | null;
}

const ipStats = ref<IpStatRow[]>([]);
const ipSampleCount = ref(0);
const ipTotalCount = ref(0);
const ipNoIpCount = ref(0);
const loadingIp = ref(false);
const worldMapReady = ref(false);

async function loadIpStats() {
  if (!config.isConfigured()) return;
  loadingIp.value = true;
  try {
    worldMapReady.value = await ensureWorldMap();
    const counts = new Map<string, number>();
    let sampled = 0;
    let total = 0;
    let noIp = 0;

    for (let page = 1; page <= IP_SAMPLE_PAGES; page++) {
      const res = await endpoints.usageList({
        ...range.apiDates,
        timezone: config.timezone,
        page,
        page_size: 100,
        sort_by: 'created_at',
        sort_order: 'desc'
      }).catch(() => null);
      if (!res || !res.items.length) break;
      if (page === 1) total = res.total || 0;
      for (const l of res.items) {
        sampled++;
        const ip = (l.ip_address || '').trim();
        if (!ip) {
          noIp++;
          continue;
        }
        counts.set(ip, (counts.get(ip) || 0) + 1);
      }
      if (res.items.length < 100) break;
    }

    ipSampleCount.value = sampled;
    ipTotalCount.value = total;
    ipNoIpCount.value = noIp;

    const geos = await resolveIpGeos(Array.from(counts.keys()));
    ipStats.value = Array.from(counts.entries())
      .map(([ip, requests]) => ({ ip, requests, geo: geos.get(ip) || null }))
      .sort((a, b) => b.requests - a.requests);
  } catch (e) {
    console.error('Failed to load overview IP stats', e);
  } finally {
    loadingIp.value = false;
  }
}

const ipTopRows = computed(() => ipStats.value.slice(0, 12));
const ipSampleIsFull = computed(() => ipSampleCount.value >= ipTotalCount.value);

const ipMapOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  const accent = isDark ? '#2997ff' : '#0066cc';
  const points = ipStats.value.filter(
    (r) => r.geo && r.geo.longitude !== undefined && r.geo.latitude !== undefined
  );
  const maxReq = Math.max(...points.map((p) => p.requests), 1);

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      formatter: (params: any) => {
        const d = params.data;
        if (!d) return '';
        return `<b>${d.name}</b><br/>${d.geoLabel || '未知'}<br/>请求数: <b>${formatNumber(d.value[2])}</b>`;
      }
    },
    geo: {
      map: 'world',
      roam: true,
      zoom: 1.6,
      center: [105, 32],
      scaleLimit: { min: 0.8, max: 12 },
      itemStyle: {
        areaColor: isDark ? '#26262a' : '#eef1f5',
        borderColor: isDark ? '#3a3a3c' : '#d5d9e0'
      },
      emphasis: {
        label: { show: false },
        itemStyle: { areaColor: isDark ? '#303036' : '#e2e7ee' }
      },
      label: { show: false }
    },
    series: [
      {
        name: '请求来源',
        type: 'effectScatter',
        coordinateSystem: 'geo',
        data: points.map((p) => ({
          name: p.ip,
          geoLabel: p.geo!.label,
          value: [p.geo!.longitude, p.geo!.latitude, p.requests]
        })),
        symbolSize: (val: number[]) => 6 + 20 * Math.sqrt((val[2] || 0) / maxReq),
        rippleEffect: { brushType: 'stroke', scale: 2.6 },
        itemStyle: { color: accent, shadowBlur: 6, shadowColor: 'rgba(0,0,0,0.25)' },
        zlevel: 2
      }
    ]
  };
});

async function loadAll() {
  await Promise.all([load(), loadIpStats()]);
}

watch(
  [
    () => config.authed && config.hydrated,
    () => range.apiDates,
    () => config.timezone,
    () => config.groups.map((g) => `${g.id}:${g.apiKeyIds.join(',')}`).join('|'),
  ],
  ([ready]) => {
    if (ready) void loadAll();
  },
  { immediate: true, deep: true },
);
useAutoRefresh(loadAll);

// Comparison calculation helpers
function calcComparison(current: number | undefined, previous: number | undefined) {
  if (current === undefined || previous === undefined || previous === 0) return null;
  const change = ((current - previous) / previous) * 100;
  return {
    value: Math.abs(change).toFixed(1) + '%',
    isUp: change > 0,
    isDown: change < 0
  };
}

const reqComparison = computed(() => {
  return calcComparison(currentRangeStats.value?.total_requests, previousRangeStats.value?.total_requests);
});

const tokenComparison = computed(() => {
  return calcComparison(currentRangeStats.value?.total_tokens, previousRangeStats.value?.total_tokens);
});

const costComparison = computed(() => {
  const curCost = config.costSource === 'actual'
    ? currentRangeStats.value?.total_actual_cost
    : currentRangeStats.value?.total_cost;
  const prevCost = config.costSource === 'actual'
    ? previousRangeStats.value?.total_actual_cost
    : previousRangeStats.value?.total_cost;
  return calcComparison(curCost, prevCost);
});

// 展示层避让色：配置里的分组颜色若过于接近，这里自动拉开（不改配置原色）
const displayColors = computed<Record<string, string>>(() => {
  const list = groupsAgg.value;
  const spread = spreadColors(list.map((g) => g.group.color));
  const map: Record<string, string> = {};
  list.forEach((g, i) => {
    map[g.group.id] = spread[i];
  });
  return map;
});

function groupColor(id: string, fallback: string): string {
  return displayColors.value[id] || fallback;
}

// Dynamic Timestamps and ECharts options
const timestamps = computed(() => {
  return Array.from(
    new Set(groupsAgg.value.flatMap(g => g.trend.map(t => t.timestamp)))
  ).sort();
});

const trendXAxisData = computed(() => {
  const gran = range.resolved.granularity;
  return timestamps.value.map(ts => formatBucketLabel(ts, gran, config.timezone));
});

// 1. Group Token Trend Stacked Area Option
const groupTrendOption = computed(() => {
  const series = groupsAgg.value.map(g => {
    const dataMap = new Map(g.trend.map(t => [t.timestamp, t.total_tokens]));
    return {
      name: g.group.name,
      type: 'line',
      stack: 'Total',
      smooth: true,
      symbol: 'none',
      areaStyle: {
        opacity: 0.15
      },
      emphasis: { focus: 'series' },
      data: timestamps.value.map(ts => dataMap.get(ts) || 0),
      itemStyle: { color: groupColor(g.group.id, g.group.color) },
      lineStyle: { width: 2 }
    };
  });

  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      valueFormatter: (value: number) => formatTokens(value)
    },
    legend: {
      textStyle: { color: isDark ? '#8e8e93' : '#7a7a7a' },
      icon: 'circle'
    },
    grid: {
      left: '3%',
      right: '4%',
      bottom: '3%',
      top: '12%',
      containLabel: true
    },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: trendXAxisData.value,
      axisLabel: { color: isDark ? '#8e8e93' : '#7a7a7a' },
      axisLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e0e0e0' } }
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        color: isDark ? '#8e8e93' : '#7a7a7a',
        formatter: (val: number) => formatTokens(val)
      },
      splitLine: { lineStyle: { color: isDark ? '#272729' : '#f0f0f0' } }
    },
    series
  };
});

// 2. Cost Distribution Donut Option
const costDonutOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  const totalCost = groupsAgg.value.reduce((sum, g) => {
    return sum + (config.costSource === 'actual' ? g.usage.actual_cost : g.usage.total_cost);
  }, 0);

  const data = groupsAgg.value.map(g => ({
    name: g.group.name,
    value: config.costSource === 'actual' ? g.usage.actual_cost : g.usage.total_cost,
    itemStyle: { color: groupColor(g.group.id, g.group.color) }
  }));

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      formatter: (params: any) => {
        return `${params.marker} ${params.name}: <b>${formatCost(params.value, config.currency)}</b> (${params.percent}%)`;
      }
    },
    legend: {
      orient: 'vertical',
      right: '5%',
      top: 'center',
      textStyle: { color: isDark ? '#8e8e93' : '#7a7a7a' },
      icon: 'circle'
    },
    series: [
      {
        name: '成本占比',
        type: 'pie',
        radius: ['50%', '75%'],
        center: ['40%', '50%'],
        avoidLabelOverlap: false,
        label: {
          show: true,
          position: 'center',
          formatter: () => `${formatCost(totalCost, config.currency)}\n总成本`,
          fontSize: 14,
          fontWeight: '600',
          color: isDark ? '#ffffff' : '#1d1d1f',
          lineHeight: 20
        },
        emphasis: {
          label: {
            show: true,
            fontSize: 16,
            fontWeight: 'bold'
          }
        },
        labelLine: {
          show: false
        },
        data
      }
    ]
  };
});

// 3. Token Type Stacked Column Option
const tokenTypesOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  const inputData: number[] = [];
  const outputData: number[] = [];
  const creationData: number[] = [];
  const readData: number[] = [];

  for (const ts of timestamps.value) {
    let input = 0, output = 0, creation = 0, read = 0;
    for (const g of groupsAgg.value) {
      const bucket = g.trend.find(t => t.timestamp === ts);
      if (bucket) {
        input += bucket.input_tokens || 0;
        output += bucket.output_tokens || 0;
        creation += bucket.cache_creation_tokens || 0;
        read += bucket.cache_read_tokens || 0;
      }
    }
    inputData.push(input);
    outputData.push(output);
    creationData.push(creation);
    readData.push(read);
  }

  const colors = {
    input: '#1a73e8',        // Google Blue
    output: '#34a853',       // Google Green
    creation: '#fbbc05',     // Google Yellow
    read: '#ea4335'          // Google Red
  };

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      valueFormatter: (value: number) => formatTokens(value)
    },
    legend: {
      textStyle: { color: isDark ? '#8e8e93' : '#7a7a7a' },
      icon: 'circle'
    },
    grid: {
      left: '3%',
      right: '4%',
      bottom: '3%',
      top: '15%',
      containLabel: true
    },
    xAxis: {
      type: 'category',
      data: trendXAxisData.value,
      axisLabel: { color: isDark ? '#8e8e93' : '#7a7a7a' },
      axisLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e0e0e0' } }
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        color: isDark ? '#8e8e93' : '#7a7a7a',
        formatter: (val: number) => formatTokens(val)
      },
      splitLine: { lineStyle: { color: isDark ? '#272729' : '#f0f0f0' } }
    },
    series: [
      {
        name: 'Input Tokens',
        type: 'bar',
        stack: 'tokens',
        emphasis: { focus: 'series' },
        data: inputData,
        itemStyle: { color: colors.input }
      },
      {
        name: 'Output Tokens',
        type: 'bar',
        stack: 'tokens',
        emphasis: { focus: 'series' },
        data: outputData,
        itemStyle: { color: colors.output }
      },
      {
        name: 'Cache Creation',
        type: 'bar',
        stack: 'tokens',
        emphasis: { focus: 'series' },
        data: creationData,
        itemStyle: { color: colors.creation }
      },
      {
        name: 'Cache Read',
        type: 'bar',
        stack: 'tokens',
        emphasis: { focus: 'series' },
        data: readData,
        itemStyle: { color: colors.read }
      }
    ]
  };
});

// 4. Model Preference Horizontal Bar Option
const modelPreferenceOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  if (!modelsData.value || !modelsData.value.models.length) return {};

  // Sort and take top 8 models, reversed for bottom-to-top rendering in y-axis
  const topModels = [...modelsData.value.models]
    .slice(0, 8)
    .reverse();

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      formatter: (params: any[]) => {
        const item = params[0];
        const raw = topModels[item.dataIndex];
        return `<b>${item.name}</b><br/>
          请求数: ${formatNumber(raw.requests)}<br/>
          Token数: ${formatTokens(raw.total_tokens)}<br/>
          本期成本: ${formatCost(config.costSource === 'actual' ? raw.actual_cost : raw.total_cost, config.currency)}`;
      }
    },
    grid: {
      left: '3%',
      right: '6%',
      bottom: '3%',
      top: '5%',
      containLabel: true
    },
    xAxis: {
      type: 'value',
      axisLabel: {
        color: isDark ? '#8e8e93' : '#7a7a7a',
        formatter: (val: number) => formatTokens(val)
      },
      splitLine: { lineStyle: { color: isDark ? '#272729' : '#f0f0f0' } }
    },
    yAxis: {
      type: 'category',
      data: topModels.map(m => m.model),
      axisLabel: { color: isDark ? '#8e8e93' : '#7a7a7a', fontSize: 11 },
      axisLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e0e0e0' } }
    },
    series: [
      {
        name: 'Tokens',
        type: 'bar',
        data: topModels.map(m => m.total_tokens),
        itemStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 1, 0, [
            { offset: 0, color: 'rgba(26, 115, 232, 0.4)' },
            { offset: 1, color: '#1a73e8' }
          ]),
          borderRadius: [0, 8, 8, 0]
        },
        barWidth: 14
      }
    ]
  };
});
</script>

<template>
  <div class="space-y-8 max-w-[1440px] mx-auto pb-12">
    <!-- Error boundary alert -->
    <div v-if="err" class="flex items-center gap-3 rounded-xl border border-error/20 bg-error/10 px-5 py-4 text-error">
      <svg class="h-5 w-5 text-error" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
      </svg>
      <span class="text-sm font-medium">数据加载失败: {{ err }}</span>
    </div>

    <!-- 6 KPI Cards Grid -->
    <div class="grid grid-cols-2 gap-4 md:grid-cols-6">
      <!-- KPI 1: Requests -->
      <div class="apple-card flex flex-col justify-between">
        <div class="text-xs font-semibold uppercase tracking-wider text-muted">本期总请求</div>
        <div v-if="loading || !currentRangeStats" class="mt-4 h-8 animate-pulse rounded bg-border w-2/3"></div>
        <div v-else class="mt-2 flex flex-col">
          <span class="text-3xl font-bold tracking-tight text-text">{{ formatNumber(currentRangeStats.total_requests) }}</span>
          <span v-if="reqComparison" class="mt-1 text-[11px] font-medium" :class="reqComparison.isUp ? 'text-error' : 'text-success'">
            较上周期 {{ reqComparison.isUp ? '↑' : '↓' }} {{ reqComparison.value }}
          </span>
        </div>
      </div>

      <!-- KPI 2: Tokens -->
      <div class="apple-card flex flex-col justify-between">
        <div class="text-xs font-semibold uppercase tracking-wider text-muted">本期总 Token</div>
        <div v-if="loading || !currentRangeStats" class="mt-4 h-8 animate-pulse rounded bg-border w-2/3"></div>
        <div v-else class="mt-2 flex flex-col">
          <span class="text-3xl font-bold tracking-tight text-text">{{ formatTokens(currentRangeStats.total_tokens) }}</span>
          <span v-if="tokenComparison" class="mt-1 text-[11px] font-medium" :class="tokenComparison.isUp ? 'text-error' : 'text-success'">
            较上周期 {{ tokenComparison.isUp ? '↑' : '↓' }} {{ tokenComparison.value }}
          </span>
        </div>
      </div>

      <!-- KPI 3: Cost -->
      <div class="apple-card flex flex-col justify-between">
        <div class="text-xs font-semibold uppercase tracking-wider text-muted">本期成本 ({{ config.currency }})</div>
        <div v-if="loading || !currentRangeStats" class="mt-4 h-8 animate-pulse rounded bg-border w-2/3"></div>
        <div v-else class="mt-2 flex flex-col">
          <span class="text-3xl font-bold tracking-tight text-text">
            {{ formatCost(config.costSource === 'actual' ? currentRangeStats.total_actual_cost : currentRangeStats.total_cost, config.currency) }}
          </span>
          <span v-if="costComparison" class="mt-1 text-[11px] font-medium" :class="costComparison.isUp ? 'text-error' : 'text-success'">
            较上周期 {{ costComparison.isUp ? '↑' : '↓' }} {{ costComparison.value }}
          </span>
        </div>
      </div>

      <!-- KPI 4: Active Keys -->
      <div class="apple-card flex flex-col justify-between">
        <div class="text-xs font-semibold uppercase tracking-wider text-muted">活跃 Key 数</div>
        <div v-if="loading || !stats" class="mt-4 h-8 animate-pulse rounded bg-border w-2/3"></div>
        <div v-else class="mt-2 flex flex-col">
          <span class="text-3xl font-bold tracking-tight text-text">{{ formatNumber(stats.active_api_keys) }}</span>
          <span class="mt-1 text-[11px] text-muted font-medium">全平台 API 密钥</span>
        </div>
      </div>

      <!-- KPI 5: RPM -->
      <div class="apple-card flex flex-col justify-between">
        <div class="text-xs font-semibold uppercase tracking-wider text-muted">当前 RPM</div>
        <div v-if="loading || !stats" class="mt-4 h-8 animate-pulse rounded bg-border w-2/3"></div>
        <div v-else class="mt-2 flex flex-col">
          <span class="text-3xl font-bold tracking-tight text-text">{{ formatNumber(stats.rpm) }}</span>
          <span class="mt-1 text-[11px] text-muted font-medium">当前每分钟请求数</span>
        </div>
      </div>

      <!-- KPI 6: TPM -->
      <div class="apple-card flex flex-col justify-between">
        <div class="text-xs font-semibold uppercase tracking-wider text-muted">当前 TPM</div>
        <div v-if="loading || !stats" class="mt-4 h-8 animate-pulse rounded bg-border w-2/3"></div>
        <div v-else class="mt-2 flex flex-col">
          <span class="text-3xl font-bold tracking-tight text-text">{{ formatTokens(stats.tpm) }}</span>
          <span class="mt-1 text-[11px] text-muted font-medium">当前每分钟 Token</span>
        </div>
      </div>
    </div>

    <!-- Main Visuals (Area Trend + Rankings) -->
    <div class="grid grid-cols-12 gap-6">
      <!-- Stacked Area Line Chart (8-col) -->
      <div class="apple-card col-span-12 lg:col-span-8 flex flex-col">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="text-base font-semibold text-text">分组 Token 消耗趋势</h2>
          <span class="text-xs text-muted">数据粒度: {{ range.resolved.granularity === 'hour' ? '小时级' : '天级' }}</span>
        </div>
        <div class="h-80 w-full relative">
          <div v-if="loading || !groupsAgg.length" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
            <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
          </div>
          <v-chart v-else class="h-full w-full" :option="groupTrendOption" autocall />
        </div>
      </div>

      <!-- Top N rankings (4-col) -->
      <div class="apple-card col-span-12 lg:col-span-4 flex flex-col">
        <h2 class="mb-4 text-base font-semibold text-text">本期消耗排行</h2>
        <div class="flex-1 overflow-auto max-h-[320px]">
          <ul class="space-y-4">
            <li
              v-for="(ga, idx) in [...groupsAgg].sort((a, b) => {
                const aCost = config.costSource === 'actual' ? a.usage.actual_cost : a.usage.total_cost;
                const bCost = config.costSource === 'actual' ? b.usage.actual_cost : b.usage.total_cost;
                return bCost - aCost;
              })"
              :key="ga.group.id"
              class="flex items-center justify-between"
            >
              <div class="flex items-center gap-3">
                <span class="flex h-5 w-5 items-center justify-center rounded-full bg-bg text-xs font-semibold text-muted">
                  {{ idx + 1 }}
                </span>
                <span class="dot" :style="{ backgroundColor: groupColor(ga.group.id, ga.group.color) }"></span>
                <span class="text-sm font-medium text-text">{{ ga.group.name }}</span>
              </div>
              <div class="flex flex-col items-end">
                <span class="text-sm font-bold text-text">
                  {{ formatCost(config.costSource === 'actual' ? ga.usage.actual_cost : ga.usage.total_cost, config.currency) }}
                </span>
                <span class="text-[11px] text-muted">{{ formatTokens(ga.usage.total_tokens) }} tokens</span>
              </div>
            </li>
            <li v-if="!groupsAgg.length" class="text-center text-sm text-muted py-8">
              暂无分组数据，请在设置中配置虚拟分组。
            </li>
          </ul>
        </div>
      </div>
    </div>

    <!-- Center Strip (Cost Donut + Token Type Columns) -->
    <div class="grid grid-cols-12 gap-6">
      <!-- Cost Donut (6-col) -->
      <div class="apple-card col-span-12 md:col-span-6 flex flex-col">
        <h2 class="mb-4 text-base font-semibold text-text">分组成本占比</h2>
        <div class="h-64 w-full relative">
          <div v-if="loading || !groupsAgg.length" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
            <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
          </div>
          <v-chart v-else class="h-full w-full" :option="costDonutOption" autocall />
        </div>
      </div>

      <!-- Token Types Stacked Column (6-col) -->
      <div class="apple-card col-span-12 md:col-span-6 flex flex-col">
        <h2 class="mb-4 text-base font-semibold text-text">Token 构成分析</h2>
        <div class="h-64 w-full relative">
          <div v-if="loading || !groupsAgg.length" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
            <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
          </div>
          <v-chart v-else class="h-full w-full" :option="tokenTypesOption" autocall />
        </div>
      </div>
    </div>

    <!-- Bottom Row (Model Preferences Horizontal Bar Chart) -->
    <div class="apple-card">
      <h2 class="mb-4 text-base font-semibold text-text">模型偏好分析 (按总消耗)</h2>
      <div class="h-72 w-full relative">
        <div v-if="loading || !modelsData" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
          <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
        </div>
        <v-chart v-else class="h-full w-full" :option="modelPreferenceOption" autocall />
      </div>
    </div>

    <!-- Global request IP geo map -->
    <div class="apple-card">
      <div class="mb-4 flex flex-col md:flex-row md:items-center justify-between gap-2">
        <h2 class="text-base font-semibold text-text">全站请求 IP 分布</h2>
        <span class="text-[11px] text-muted">
          <template v-if="ipSampleCount">
            {{ ipSampleIsFull ? `已统计本期全部 ${formatNumber(ipSampleCount)} 条请求` : `基于最近 ${formatNumber(ipSampleCount)} 条请求采样（本期共 ${formatNumber(ipTotalCount)} 条）` }}
            · 归属地由 geojs.io 解析
          </template>
        </span>
      </div>

      <div v-if="loadingIp" class="grid grid-cols-12 gap-6">
        <div class="col-span-12 lg:col-span-8 h-96 animate-pulse rounded-xl bg-surface border border-border"></div>
        <div class="col-span-12 lg:col-span-4 h-96 animate-pulse rounded-xl bg-surface border border-border"></div>
      </div>

      <div v-else-if="ipStats.length" class="grid grid-cols-12 gap-6">
        <div class="col-span-12 lg:col-span-8">
          <div class="h-96 w-full relative rounded-xl overflow-hidden">
            <v-chart v-if="worldMapReady" class="h-full w-full" :option="ipMapOption" autocall />
            <div v-else class="absolute inset-0 flex items-center justify-center text-sm text-muted">
              地图底图加载失败，仅显示右侧列表
            </div>
          </div>
          <div class="mt-2 text-[10px] text-muted">滚轮缩放 / 拖拽平移；点大小代表请求数占比</div>
        </div>

        <div class="col-span-12 lg:col-span-4">
          <div class="text-xs font-semibold text-muted mb-2">Top IP（按请求数）</div>
          <div class="space-y-1.5 max-h-96 overflow-y-auto pr-1">
            <div
              v-for="row in ipTopRows"
              :key="row.ip"
              class="flex items-center justify-between rounded-lg border border-border bg-bg/50 px-3 py-2"
            >
              <div class="min-w-0">
                <div class="text-xs font-mono font-semibold text-text truncate">{{ row.ip }}</div>
                <div class="text-[10px] text-muted truncate mt-0.5">
                  {{ row.geo?.label || '解析中…' }}
                  <span v-if="row.geo?.organization" class="opacity-70"> · {{ row.geo.organization }}</span>
                </div>
              </div>
              <div class="text-right flex-shrink-0 ml-3">
                <div class="text-xs font-bold font-mono text-text">{{ formatNumber(row.requests) }}</div>
                <div class="text-[10px] text-muted">{{ ((row.requests / Math.max(ipSampleCount - ipNoIpCount, 1)) * 100).toFixed(1) }}%</div>
              </div>
            </div>
          </div>
          <div v-if="ipNoIpCount" class="mt-2 text-[10px] text-muted">
            另有 {{ formatNumber(ipNoIpCount) }} 条请求未记录 IP
          </div>
        </div>
      </div>

      <div v-else class="py-10 text-center text-sm text-muted">本期采样请求中没有 IP 记录</div>
    </div>
  </div>
</template>

<style scoped>
/* Custom style adaptations for ECharts */
</style>
