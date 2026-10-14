<script setup lang="ts">
import { ref, computed, watch } from 'vue';
import { useConfigStore, type VirtualGroup } from '@/stores/config';
import { useRangeStore } from '@/stores/range';
import { endpoints } from '@/api/endpoints';
import { formatCost, formatNumber, formatTokens, formatDuration, formatTime } from '@/utils/format';
import { hourAndDow, localDate } from '@/utils/time';
import { pMapLimit } from '@/utils/reduce';
import type { UsageLog } from '@/api/types';

import VChart from 'vue-echarts';
import 'echarts';

const config = useConfigStore();
const range = useRangeStore();

// 仅统计在「设置页」配置过的虚拟分组，不再合并未分组 keys
const virtualGroupsWithUnassigned = computed<VirtualGroup[]>(() => config.groups);

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

const tabIndex = ref(0);
const loading = ref(false);
const error = ref<string | null>(null);

// Detailed fun metrics calculated per group
interface GroupFunStats {
  id: string;
  name: string;
  color: string;
  requests: number;
  tokens: number;
  cost: number;

  // Birds & Owls (seconds from midnight)
  avgMinSeconds: number | null;
  avgMaxSeconds: number | null;

  // Longest duration request
  maxDurationMs: number;
  longestRequestModel: string;
  longestRequestTime: string;

  // Costliest single request
  maxRequestCost: number;
  costliestRequestModel: string;
  costliestRequestTime: string;

  // Cache & input/output ratios
  cacheReadRatio: number;
  inputRatio: number;
  outputRatio: number;

  // Hourly token peak
  peakHourlyTokens: number;

  // Model stats
  modelsCount: number;
  modelsList: string[];

  // Work time sum
  totalDurationMs: number;

  // First token latency
  avgFirstTokenMs: number | null;

  // Growth rate
  growthRate: number | null;
}

const groupsStatsList = ref<GroupFunStats[]>([]);

// Heatmap filter: selected virtual group (or 'all' for overall)
const selectedHeatmapGroupId = ref<string>('all');
const heatmapHourlyTrendData = ref<Record<string, { timestamp: string; requests: number }[]>>({}); // Keyed by group ID
const loadingHeatmap = ref(false);

const cards = [
  { emoji: '🌅', title: '早起鸟榜', desc: '每天平均首次请求时间最早', unit: '时分秒', field: 'avgMinSeconds', order: 'asc', isTime: true },
  { emoji: '🦉', title: '夜猫子榜', desc: '每天平均末次请求时间最晚', unit: '时分秒', field: 'avgMaxSeconds', order: 'desc', isTime: true },
  { emoji: '⏱️', title: '持久作战王', desc: '单次请求执行耗时最长', unit: '毫秒', field: 'maxDurationMs', order: 'desc', isDuration: true },
  { emoji: '🔥', title: '当日峰值', desc: '单小时消耗 Token 峰值', unit: 'Token', field: 'peakHourlyTokens', order: 'desc', isTokens: true },
  { emoji: '💸', title: '烧钱大户', desc: '本期消耗累计费用最高', unit: '金额', field: 'cost', order: 'desc', isCost: true },
  { emoji: '📦', title: '缓存王', desc: '缓存命中的 Token 比例最高', unit: '比例', field: 'cacheReadRatio', order: 'desc', isPercent: true },
  { emoji: '🌪️', title: '提示词洪流', desc: '输入 Token 占总消耗比例最高', unit: '比例', field: 'inputRatio', order: 'desc', isPercent: true },
  { emoji: '📝', title: '长输出爱好者', desc: '输出 Token 占总消耗比例最高', unit: '比例', field: 'outputRatio', order: 'desc', isPercent: true },
  { emoji: '🎯', title: '模型专一户', desc: '只钟情于极少数模型的队伍', unit: '种', field: 'modelsCount', order: 'asc', isCount: true },
  { emoji: '🌈', title: '模型博物馆', desc: '使用模型类型最繁杂的队伍', unit: '种', field: 'modelsCount', order: 'desc', isCount: true },
  { emoji: '🕒', title: '工作时长榜', desc: '所有请求累计耗时最长', unit: '时长', field: 'totalDurationMs', order: 'desc', isDuration: true },
  { emoji: '🐎', title: '首字节最快', desc: '流式请求平均首字响应最快', unit: '毫秒', field: 'avgFirstTokenMs', order: 'asc', isMs: true },
  { emoji: '🐌', title: '首字节最慢', desc: '流式请求平均首字响应最慢', unit: '毫秒', field: 'avgFirstTokenMs', order: 'desc', isMs: true },
  { emoji: '🏆', title: '本周 MVP', desc: '最近 7 天总 Token 消耗最高', unit: 'Token', field: 'tokens', order: 'desc', isTokens: true },
  { emoji: '📈', title: '黑马奖', desc: '对比上个周期费用增幅最大', unit: '比例', field: 'growthRate', order: 'desc', isPercent: true },
  { emoji: '🛌', title: '隐居者', desc: '本期内静默无任何消耗的分组', unit: '次', field: 'requests', order: 'asc', isRequestsZero: true },
];

const selectedRule = computed(() => cards[tabIndex.value]);

// Calculate ranking for selected card
const leaderboard = computed(() => {
  const rule = selectedRule.value;
  let list = [...groupsStatsList.value];

  if (rule.isRequestsZero) {
    // Only show groups with 0 requests
    return list
      .filter(g => g.requests === 0)
      .map(g => ({
        group: g,
        displayValue: '0 次请求',
        tagline: '悄悄地我走了，正如我悄悄地来…'
      }));
  }

  // Filter out null values for fields
  list = list.filter(g => {
    const val = (g as any)[rule.field];
    return val !== null && val !== undefined;
  });

  // Sort
  list.sort((a, b) => {
    const valA = (a as any)[rule.field];
    const valB = (b as any)[rule.field];
    return rule.order === 'asc' ? valA - valB : valB - valA;
  });

  // Take top 3 and format values
  return list.slice(0, 3).map((g, idx) => {
    const val = (g as any)[rule.field];
    let displayValue = '';

    if (rule.isTime) {
      const h = Math.floor(val / 3600);
      const m = Math.floor((val % 3600) / 60);
      const s = val % 60;
      displayValue = `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
    } else if (rule.isDuration) {
      displayValue = formatDuration(val);
    } else if (rule.isPercent) {
      displayValue = (val * 100).toFixed(1) + '%';
    } else if (rule.isTokens) {
      displayValue = formatTokens(val);
    } else if (rule.isCost) {
      displayValue = formatCost(val, config.currency);
    } else if (rule.isMs) {
      displayValue = `${Math.round(val)} ms`;
    } else if (rule.isCount) {
      displayValue = `${val} 种模型`;
    }

    // Witty remarks based on rank and card
    const taglinesMap: Record<string, string[]> = {
      '早起鸟榜': ['早起的鸟儿有 token 吃！', '披星戴月，AI 先行。', '唤醒清晨的第一条 Prompt。'],
      '夜猫子榜': ['月亮不睡我不睡，我是 AI 小美味。', '深夜灵感爆发，算力狂飙！', '修仙玩家，恐怖如斯。'],
      '持久作战王': ['超长待机，稳如泰山。', '这是一个漫长而耐心的等待。', '服务器表示它尽力了！'],
      '当日峰值': ['算力海啸！那一瞬间地动山摇。', '闪电战，爆发力惊人。', '峰值狂热分子。'],
      '烧钱大户': ['家里有矿，神兽避让。', '钱包在哭泣，GPU 在微笑。', '为算力产业做出了突出贡献！'],
      '缓存王': ['上下文复用大师，省钱小能手。', '一字不落，记忆超群。', '缓存常客，老练！'],
      '提示词洪流': ['长篇大论，Prompt 狂魔。', '信息洪流涌入，脑洞大开。', '前戏十足，铺垫极长。'],
      '长输出爱好者': ['废话文学大师，输出滔滔不绝。', '洋洋洒洒，文采飞扬。', '给它一个支点，能写一部小说。'],
      '模型专一户': ['弱水三千，只取一瓢。', '忠贞不渝，真爱无疑。', '模型单一，但足够专业。'],
      '模型博物馆': ['模型集邮爱好者，博爱无双。', '海纳百川，无所不用其极。', '玩的就是新鲜感！'],
      '工作时长榜': ['AI 劳模，永不停歇。', '辛苦了，GPU 守护者！', '滴水穿石，水滴石穿。'],
      '首字节最快': ['天下武功，唯快不破。', '闪电响应，体验极佳。', '快如脱兔，首字秒回。'],
      '首字节最慢': ['大智若愚，深思熟虑。', '好酒不怕晚，容我思索片刻。', '老牛拉破车，沉稳异常。'],
      '本周 MVP': ['全场瞩目，本周战绩之王！', '领跑全队，荣耀加身。', 'MVP 闪耀，名副其实。'],
      '黑马奖': ['异军突起，势不可挡！', '增长狂飙，黑马逆袭。', '成长速度令人瞩目。'],
    };

    const taglines = taglinesMap[rule.title] || ['战绩斐然，继续保持！', '算力风向标。', '默默贡献力量。'];

    return {
      group: g,
      displayValue,
      tagline: taglines[idx] || taglines[0]
    };
  });
});

// 每张卡的首位分组（用于在卡片上直接显示赢家）
const cardWinners = computed(() => {
  return cards.map((rule) => {
    let list = [...groupsStatsList.value];

    if (rule.isRequestsZero) {
      list = list.filter((g) => g.requests === 0);
      if (!list.length) return null;
      return { group: list[0], displayValue: '0 次' };
    }

    list = list.filter((g) => {
      const val = (g as any)[rule.field];
      return val !== null && val !== undefined;
    });
    list.sort((a, b) => {
      const valA = (a as any)[rule.field];
      const valB = (b as any)[rule.field];
      return rule.order === 'asc' ? valA - valB : valB - valA;
    });
    if (!list.length) return null;

    const g = list[0];
    const val = (g as any)[rule.field];
    let displayValue = '';
    if (rule.isTime) {
      const h = Math.floor(val / 3600);
      const m = Math.floor((val % 3600) / 60);
      const s = val % 60;
      displayValue = `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
    } else if (rule.isDuration) {
      displayValue = formatDuration(val);
    } else if (rule.isPercent) {
      displayValue = (val * 100).toFixed(1) + '%';
    } else if (rule.isTokens) {
      displayValue = formatTokens(val);
    } else if (rule.isCost) {
      displayValue = formatCost(val, config.currency);
    } else if (rule.isMs) {
      displayValue = `${Math.round(val)} ms`;
    } else if (rule.isCount) {
      displayValue = `${val} 种`;
    }
    return { group: g, displayValue };
  });
});

// ---- 夜猫指数：近 7 天 0-6 点请求占比（复用热力图数据） ----
function nightRatioOf(groupId: string): number {
  const list = heatmapHourlyTrendData.value[groupId] || [];
  let night = 0;
  let total = 0;
  for (const item of list) {
    const { hour } = hourAndDow(item.timestamp, config.timezone);
    total += item.requests;
    if (hour < 6) night += item.requests;
  }
  return total > 0 ? night / total : 0;
}

function weekendRatioOf(groupId: string): number {
  const list = heatmapHourlyTrendData.value[groupId] || [];
  let weekend = 0;
  let total = 0;
  for (const item of list) {
    const { dow } = hourAndDow(item.timestamp, config.timezone);
    total += item.requests;
    if (dow === 0 || dow === 6) weekend += item.requests;
  }
  return total > 0 ? weekend / total : 0;
}

// ---- 分组 PK 雷达 ----
const pkGroupAId = ref<string>('');
const pkGroupBId = ref<string>('');

watch(groupsStatsList, (list) => {
  if (list.length >= 2) {
    if (!list.find(g => g.id === pkGroupAId.value)) pkGroupAId.value = list[0].id;
    if (!list.find(g => g.id === pkGroupBId.value) || pkGroupBId.value === pkGroupAId.value) {
      pkGroupBId.value = list.find(g => g.id !== pkGroupAId.value)?.id || '';
    }
  }
}, { immediate: true });

const PK_DIMS = [
  { key: 'requests', label: '请求量' },
  { key: 'cost', label: '烧钱力' },
  { key: 'modelsCount', label: '模型博学' },
  { key: 'cacheReadRatio', label: '缓存精通' },
  { key: 'nightRatio', label: '夜猫指数' },
  { key: 'totalDurationMs', label: '持久力' }
] as const;

const pkStats = computed(() =>
  groupsStatsList.value.map(g => ({
    ...g,
    nightRatio: nightRatioOf(g.id),
    weekendRatio: weekendRatioOf(g.id)
  }))
);

const pkA = computed(() => pkStats.value.find(g => g.id === pkGroupAId.value) || null);
const pkB = computed(() => pkStats.value.find(g => g.id === pkGroupBId.value) || null);

/** 每个维度用全体分组的最大值归一化，保证不同量纲可比 */
const pkDimMax = computed(() =>
  PK_DIMS.map(d => Math.max(...pkStats.value.map(g => (g as any)[d.key] || 0), 1e-9))
);

function pkScores(g: any): number[] {
  return PK_DIMS.map((d, i) => Math.round((((g?.[d.key] || 0) / pkDimMax.value[i]) * 100)));
}

const pkWinner = computed(() => {
  if (!pkA.value || !pkB.value) return null;
  const sa = pkScores(pkA.value).reduce((s, v) => s + v, 0);
  const sb = pkScores(pkB.value).reduce((s, v) => s + v, 0);
  if (sa === sb) return { name: '平手', text: '棋逢对手，将遇良才，握手言和！' };
  const w = sa > sb ? pkA.value : pkB.value;
  const l = sa > sb ? pkB.value : pkA.value;
  return { name: w.name, text: `「${w.name}」综合战力 ${Math.max(sa, sb)} vs ${Math.min(sa, sb)}，碾压「${l.name}」不接受反驳。` };
});

const pkRadarOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  if (!pkA.value || !pkB.value) return {};
  return {
    backgroundColor: 'transparent',
    tooltip: {
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' }
    },
    legend: {
      data: [pkA.value.name, pkB.value.name],
      bottom: 0,
      textStyle: { color: isDark ? '#8e8e93' : '#7a7a7a' }
    },
    radar: {
      indicator: PK_DIMS.map(d => ({ name: d.label, max: 100 })),
      radius: '62%',
      splitArea: { areaStyle: { color: 'transparent' } },
      splitLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e5e5e5' } },
      axisLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e5e5e5' } },
      axisName: { color: isDark ? '#8e8e93' : '#7a7a7a', fontSize: 11 }
    },
    series: [{
      type: 'radar',
      data: [
        {
          name: pkA.value.name,
          value: pkScores(pkA.value),
          itemStyle: { color: pkA.value.color },
          lineStyle: { color: pkA.value.color, width: 2.5 },
          areaStyle: { color: pkA.value.color, opacity: 0.15 }
        },
        {
          name: pkB.value.name,
          value: pkScores(pkB.value),
          itemStyle: { color: pkB.value.color },
          lineStyle: { color: pkB.value.color, width: 2.5 },
          areaStyle: { color: pkB.value.color, opacity: 0.15 }
        }
      ]
    }]
  };
});

// ---- 成就徽章墙 ----
interface BadgeDef {
  emoji: string;
  name: string;
  desc: string;
  earned: (g: (typeof pkStats)['value'][number]) => boolean;
}

const BADGES: BadgeDef[] = [
  { emoji: '💰', name: '百万 Token', desc: '本期消耗 ≥ 100 万 Token', earned: g => g.tokens >= 1_000_000 },
  { emoji: '🏭', name: '亿级玩家', desc: '本期消耗 ≥ 1 亿 Token', earned: g => g.tokens >= 100_000_000 },
  { emoji: '🔥', name: '千次斩', desc: '本期请求 ≥ 1000 次', earned: g => g.requests >= 1000 },
  { emoji: '📦', name: '缓存大师', desc: '缓存命中 Token 占比 ≥ 50%', earned: g => g.cacheReadRatio >= 0.5 },
  { emoji: '🌈', name: '模型收藏家', desc: '本期用过 ≥ 5 种模型', earned: g => g.modelsCount >= 5 },
  { emoji: '🎯', name: '从一而终', desc: '本期只用 1 种模型', earned: g => g.modelsCount === 1 && g.requests > 0 },
  { emoji: '🦉', name: '修仙老祖', desc: '近 7 天凌晨 0-6 点请求占比 ≥ 25%', earned: g => g.nightRatio >= 0.25 },
  { emoji: '🐔', name: '闻鸡起舞', desc: '日均开工早于 07:00', earned: g => g.avgMinSeconds !== null && g.avgMinSeconds <= 7 * 3600 },
  { emoji: '🏖️', name: '周末战士', desc: '近 7 天周末请求占比 ≥ 25%', earned: g => g.weekendRatio >= 0.25 },
  { emoji: '🏃', name: '马拉松', desc: '本期请求累计耗时 ≥ 24 小时', earned: g => g.totalDurationMs >= 24 * 3600_000 },
  { emoji: '⚡', name: '光速首字', desc: '流式平均首字 < 400ms', earned: g => g.avgFirstTokenMs !== null && g.avgFirstTokenMs < 400 },
  { emoji: '🤑', name: '一掷千金', desc: '出现过单次费用 ≥ $1 的请求', earned: g => g.maxRequestCost >= 1 },
  { emoji: '👻', name: '隐身人', desc: '本期 0 请求', earned: g => g.requests === 0 }
];

const badgeWall = computed(() =>
  pkStats.value.map(g => ({
    group: g,
    badges: BADGES.filter(b => b.earned(g))
  }))
);

// Load date range helper for last 7 days (Heatmap specific)
const heatmapDates = computed(() => {
  const now = new Date();
  const start = new Date(now.getTime() - 7 * 86400_000);
  const fmt = (d: Date) => {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
  };
  return { start_date: fmt(start), end_date: fmt(now) };
});

// Load hourly trend and calculate heatmap
async function loadHeatmapData() {
  if (!config.isConfigured()) return;
  loadingHeatmap.value = true;
  try {
    const allGroups = [...config.groups];
    const tempMap: Record<string, { timestamp: string; requests: number }[]> = {};

    await pMapLimit(
      allGroups,
      async (g) => {
        if (!g.apiKeyIds.length) {
          tempMap[g.id] = [];
          return;
        }

        // Load hourly trend for keys in group
        const trends = await Promise.all(
          g.apiKeyIds.map(id =>
            endpoints.dashboardTrend({
              ...heatmapDates.value,
              granularity: 'hour',
              timezone: config.timezone,
              api_key_id: id
            }).then(r => r.trend).catch(() => [])
          )
        );

        // Merge key hourly trend buckets
        const merged: Record<string, number> = {};
        for (const list of trends) {
          for (const b of list) {
            merged[b.timestamp] = (merged[b.timestamp] || 0) + (b.requests || 0);
          }
        }

        tempMap[g.id] = Object.entries(merged).map(([ts, req]) => ({
          timestamp: ts,
          requests: req
        }));
      },
      3
    );

    heatmapHourlyTrendData.value = tempMap;
  } catch (e) {
    console.error('Failed to load heatmap data', e);
  } finally {
    loadingHeatmap.value = false;
  }
}

// Format Heatmap ECharts Option
const heatmapChartOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';

  // Matrix grid dimensions: 7 rows (weekdays), 24 columns (hours)
  const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
  const hours = Array.from({ length: 24 }, (_, i) => `${String(i).padStart(2, '0')}:00`);

  // Initialize matrix values to 0
  const matrix: number[][] = [];
  for (let d = 0; d < 7; d++) {
    for (let h = 0; h < 24; h++) {
      matrix.push([h, d, 0]);
    }
  }

  // Filter and populate based on selection
  let activeTrendList: { timestamp: string; requests: number }[] = [];
  if (selectedHeatmapGroupId.value === 'all') {
    activeTrendList = Object.values(heatmapHourlyTrendData.value).flat();
  } else {
    activeTrendList = heatmapHourlyTrendData.value[selectedHeatmapGroupId.value] || [];
  }

  for (const item of activeTrendList) {
    const { hour, dow } = hourAndDow(item.timestamp, config.timezone);
    const index = dow * 24 + hour;
    if (matrix[index]) {
      matrix[index][2] += item.requests;
    }
  }

  const maxVal = Math.max(...matrix.map(m => m[2]), 1);

  return {
    backgroundColor: 'transparent',
    tooltip: {
      position: 'top',
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      formatter: (params: any) => {
        const h = hours[params.data[0]];
        const d = weekdays[params.data[1]];
        const val = params.data[2];
        return `<b>${d} ${h}</b><br/>请求数: <b>${formatNumber(val)} 次</b>`;
      }
    },
    grid: {
      left: '3%',
      right: '3%',
      bottom: '12%',
      top: '5%',
      containLabel: true
    },
    xAxis: {
      type: 'category',
      data: hours,
      splitArea: { show: true },
      axisLabel: { color: isDark ? '#8e8e93' : '#7a7a7a', fontSize: 10, rotate: 30 },
      axisLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e0e0e0' } }
    },
    yAxis: {
      type: 'category',
      data: weekdays,
      splitArea: { show: true },
      axisLabel: { color: isDark ? '#8e8e93' : '#7a7a7a', fontSize: 11 },
      axisLine: { lineStyle: { color: isDark ? '#3a3a3c' : '#e0e0e0' } }
    },
    visualMap: {
      min: 0,
      max: maxVal,
      calculable: true,
      orient: 'horizontal',
      left: 'center',
      bottom: '0%',
      textStyle: { color: isDark ? '#8e8e93' : '#7a7a7a' },
      inRange: {
        color: isDark
          ? ['rgba(41, 151, 255, 0.05)', '#2997ff']
          : ['rgba(0, 102, 204, 0.05)', '#0066cc']
      }
    },
    series: [
      {
        name: 'Hourly Requests',
        type: 'heatmap',
        data: matrix,
        label: {
          show: false
        },
        emphasis: {
          itemStyle: {
            shadowBlur: 10,
            shadowColor: 'rgba(0, 0, 0, 0.5)'
          }
        }
      }
    ]
  };
});

// Load details for all 16 Fun cards
async function calculateAllFunStats() {
  if (!config.isConfigured()) return;
  loading.value = true;
  error.value = null;
  try {
    const allGroups = [...config.groups];
    const tempStatsList: GroupFunStats[] = [];

    // Resolve date ranges
    const prevDates = {
      start_date: prevApiDates.value.start_date,
      end_date: prevApiDates.value.end_date
    };

    await pMapLimit(
      allGroups,
      async (g) => {
        if (!g.apiKeyIds.length) {
          tempStatsList.push({
            id: g.id,
            name: g.name,
            color: g.color,
            requests: 0, tokens: 0, cost: 0,
            avgMinSeconds: null, avgMaxSeconds: null,
            maxDurationMs: 0, longestRequestModel: '—', longestRequestTime: '—',
            maxRequestCost: 0, costliestRequestModel: '—', costliestRequestTime: '—',
            cacheReadRatio: 0, inputRatio: 0, outputRatio: 0,
            peakHourlyTokens: 0, modelsCount: 0, modelsList: [],
            totalDurationMs: 0, avgFirstTokenMs: null, growthRate: null
          });
          return;
        }

        // Fetch usage list logs (concurrency limit per key)
        const logsList = await Promise.all(
          g.apiKeyIds.map(id =>
            endpoints.usageList({
              ...range.apiDates,
              timezone: config.timezone,
              api_key_id: id,
              page_size: 100 // fetch top 100 for stats calculation
            }).then(r => r.items).catch(() => [])
          )
        );
        const allLogs = logsList.flat();

        // 本期 trend 用于求和（api-keys-usage 接口只有 today/total_actual_cost 两字段，不可用）
        const trends = await Promise.all(
          g.apiKeyIds.map(id =>
            endpoints.dashboardTrend({
              ...range.apiDates,
              granularity: range.resolved.granularity,
              timezone: config.timezone,
              api_key_id: id
            }).then(r => r.trend).catch(() => [])
          )
        );

        let req = 0, tok = 0, totalCost = 0, actualCost = 0, inputTok = 0, outputTok = 0, cacheRead = 0;
        for (const list of trends) {
          for (const b of list) {
            req += b.requests || 0;
            tok += b.total_tokens || 0;
            totalCost += b.total_cost || 0;
            actualCost += b.actual_cost || 0;
            inputTok += b.input_tokens || 0;
            outputTok += b.output_tokens || 0;
            cacheRead += b.cache_read_tokens || 0;
          }
        }
        const periodCost = config.costSource === 'actual' ? actualCost : totalCost;

        // duration 累计：用 allLogs 的样本（page_size=100/key）求和
        let durSum = 0;
        for (const l of allLogs) {
          durSum += l.duration_ms || 0;
        }

        // 1 & 2: Calculate daily min/max seconds of day
        const dailyLogs: Record<string, UsageLog[]> = {};
        for (const log of allLogs) {
          const dateStr = localDate(log.created_at, config.timezone);
          if (!dailyLogs[dateStr]) dailyLogs[dateStr] = [];
          dailyLogs[dateStr].push(log);
        }

        let minSecSum = 0;
        let maxSecSum = 0;
        let activeDaysCount = 0;

        for (const logs of Object.values(dailyLogs)) {
          const times = logs.map(l => {
            const date = new Date(l.created_at);
            // Translate to seconds from midnight in local timezone
            const fmt = new Intl.DateTimeFormat('en-GB', {
              timeZone: config.timezone,
              hour12: false,
              hour: '2-digit',
              minute: '2-digit',
              second: '2-digit'
            });
            const [h, m, s] = fmt.format(date).split(':').map(Number);
            return h * 3600 + m * 60 + s;
          });

          if (times.length > 0) {
            minSecSum += Math.min(...times);
            maxSecSum += Math.max(...times);
            activeDaysCount++;
          }
        }

        const avgMinSeconds = activeDaysCount > 0 ? Math.round(minSecSum / activeDaysCount) : null;
        const avgMaxSeconds = activeDaysCount > 0 ? Math.round(maxSecSum / activeDaysCount) : null;

        // 3: Longest request
        let maxDurationMs = 0;
        let longestRequestModel = '—';
        let longestRequestTime = '—';
        for (const l of allLogs) {
          if (l.duration_ms && l.duration_ms > maxDurationMs) {
            maxDurationMs = l.duration_ms;
            longestRequestModel = l.model;
            longestRequestTime = l.created_at;
          }
        }

        // Costliest request
        let maxRequestCost = 0;
        let costliestRequestModel = '—';
        let costliestRequestTime = '—';
        for (const l of allLogs) {
          const c = config.costSource === 'actual' ? l.actual_cost : l.total_cost;
          if (c && c > maxRequestCost) {
            maxRequestCost = c;
            costliestRequestModel = l.model;
            costliestRequestTime = l.created_at;
          }
        }

        // Peak tokens per hour（复用上面已经取到的 trends）
        const mergedTrend: Record<string, number> = {};
        for (const list of trends) {
          for (const b of list) {
            mergedTrend[b.timestamp] = (mergedTrend[b.timestamp] || 0) + (b.total_tokens || 0);
          }
        }
        const peakHourlyTokens = Object.values(mergedTrend).length > 0 ? Math.max(...Object.values(mergedTrend)) : 0;

        // Unique models
        const modelsRes = await Promise.all(
          g.apiKeyIds.map(id =>
            endpoints.dashboardModels({
              ...range.apiDates,
              api_key_id: id,
              timezone: config.timezone
            }).then(r => r.models).catch(() => [])
          )
        );
        const modelsSet = new Set(modelsRes.flat().map(m => m.model));

        // First token latency
        const streamLogs = allLogs.filter(l => l.stream && l.first_token_ms);
        const avgFirstTokenMs = streamLogs.length > 0
          ? streamLogs.reduce((sum, l) => sum + (l.first_token_ms || 0), 0) / streamLogs.length
          : null;

        // Calculate growth rate compared to previous period
        const prevTrends = await Promise.all(
          g.apiKeyIds.map(id =>
            endpoints.dashboardTrend({
              ...prevDates,
              granularity: range.resolved.granularity,
              timezone: config.timezone,
              api_key_id: id
            }).then(r => r.trend).catch(() => [])
          )
        );
        const prevCost = prevTrends.flat().reduce((sum, b) => {
          return sum + (config.costSource === 'actual' ? (b.actual_cost || 0) : (b.total_cost || 0));
        }, 0);
        const growthRate = prevCost > 0 ? (periodCost - prevCost) / prevCost : null;

        tempStatsList.push({
          id: g.id,
          name: g.name,
          color: g.color,
          requests: req,
          tokens: tok,
          cost: periodCost,
          avgMinSeconds,
          avgMaxSeconds,
          maxDurationMs,
          longestRequestModel,
          longestRequestTime,
          maxRequestCost,
          costliestRequestModel,
          costliestRequestTime,
          cacheReadRatio: tok > 0 ? cacheRead / tok : 0,
          inputRatio: tok > 0 ? inputTok / tok : 0,
          outputRatio: tok > 0 ? outputTok / tok : 0,
          peakHourlyTokens,
          modelsCount: modelsSet.size,
          modelsList: Array.from(modelsSet),
          totalDurationMs: durSum,
          avgFirstTokenMs,
          growthRate
        });
      },
      3
    );

    groupsStatsList.value = tempStatsList;
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}

// Watch range dates to recalculate
watch(() => range.apiDates, () => {
  void calculateAllFunStats();
  void loadHeatmapData();
}, { immediate: true, deep: true });
</script>

<template>
  <div class="space-y-8 max-w-[1440px] mx-auto pb-12">
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-bold tracking-tight text-text">趣味统计</h1>
        <p class="text-xs text-muted mt-1">分析各团队的消耗趣味战况与时段倾向</p>
      </div>
      <button class="btn-secondary py-1 px-3 text-xs h-7" @click="calculateAllFunStats">
        重新计算
      </button>
    </div>

    <!-- Error display -->
    <div v-if="error" class="flex items-center gap-3 rounded-xl border border-error/20 bg-error/10 px-5 py-4 text-error">
      <svg class="h-5 w-5 text-error" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
      </svg>
      <span class="text-sm font-medium">指标计算失败: {{ error }}</span>
    </div>

    <!-- 16 Fun Cards Grid -->
    <div class="grid grid-cols-2 gap-4 md:grid-cols-4 lg:grid-cols-8">
      <div
        v-for="(c, i) in cards"
        :key="c.title"
        class="apple-card p-4 cursor-pointer select-none transition-all flex flex-col justify-between hover:bg-surface-hover"
        :class="i === tabIndex ? 'ring-2 ring-accent border-transparent bg-surface shadow-md' : 'border-border bg-surface'"
        @click="tabIndex = i"
      >
        <div class="text-3xl">{{ c.emoji }}</div>
        <div class="mt-3">
          <div class="text-xs font-bold text-text leading-snug">{{ c.title }}</div>
          <div class="text-[10px] text-muted leading-tight mt-0.5 truncate">{{ c.desc }}</div>
        </div>
        <div class="mt-3 pt-2 border-t border-border/50">
          <div v-if="cardWinners[i]" class="flex items-center gap-1.5 min-w-0">
            <span class="dot h-2 w-2 flex-shrink-0" :style="{ backgroundColor: cardWinners[i]!.group.color }"></span>
            <span class="text-[11px] font-semibold text-text truncate">{{ cardWinners[i]!.group.name }}</span>
          </div>
          <div v-if="cardWinners[i]" class="text-[10px] text-muted font-mono mt-0.5 truncate">{{ cardWinners[i]!.displayValue }}</div>
          <div v-else class="text-[10px] text-muted italic">暂无数据</div>
        </div>
      </div>
    </div>

    <!-- Selection Detail Box -->
    <div class="grid grid-cols-12 gap-6">
      <!-- Leaderboard for selected Card (7-col) -->
      <div class="apple-card col-span-12 lg:col-span-7 flex flex-col justify-between">
        <div>
          <div class="flex items-center gap-2 mb-1">
            <span class="text-2xl">{{ selectedRule.emoji }}</span>
            <h2 class="text-base font-semibold text-text">{{ selectedRule.title }}</h2>
          </div>
          <p class="text-xs text-muted mb-4">{{ selectedRule.desc }} (当前排行)</p>

          <div v-if="loading" class="space-y-4 py-4">
            <div v-for="i in 3" :key="i" class="h-12 animate-pulse rounded-xl bg-bg w-full"></div>
          </div>
          <div v-else class="space-y-4">
            <!-- Rank items -->
            <div
              v-for="(item, idx) in leaderboard"
              :key="item.group.id"
              class="flex items-center justify-between p-3 rounded-xl border border-border bg-bg/50 hover:bg-bg transition-colors"
            >
              <div class="flex items-center gap-3 min-w-0">
                <!-- Trophy / Rank -->
                <span
                  class="flex h-6 w-6 items-center justify-center rounded-full text-xs font-bold"
                  :class="idx === 0 ? 'bg-google-yellow/10 text-warning border border-google-yellow/20'
                        : idx === 1 ? 'bg-google-blue/10 text-accent border border-google-blue/20'
                        : 'bg-google-red/10 text-error border border-google-red/20'"
                >
                  {{ idx + 1 }}
                </span>
                <span class="dot" :style="{ backgroundColor: item.group.color }"></span>
                <div class="min-w-0">
                  <div class="text-sm font-semibold text-text truncate">{{ item.group.name }}</div>
                  <div class="text-[10px] text-muted italic mt-0.5 truncate">" {{ item.tagline }} "</div>
                </div>
              </div>

              <div class="text-right flex-shrink-0 ml-4">
                <span class="text-sm font-bold text-text font-mono bg-surface border border-border px-2.5 py-1 rounded-lg shadow-sm">
                  {{ item.displayValue }}
                </span>
              </div>
            </div>

            <!-- Empty state -->
            <div v-if="!leaderboard.length" class="text-center text-sm text-muted py-12">
              暂无匹配数据，请检查各虚拟分组的 Token 消耗及请求日志。
            </div>
          </div>
        </div>

        <div class="mt-4 pt-4 border-t border-border flex items-center justify-between text-[11px] text-muted">
          <span>算法规则: {{ selectedRule.desc }}</span>
          <span>按 {{ selectedRule.order === 'asc' ? '数值递增' : '数值递减' }} 排序</span>
        </div>
      </div>

      <!-- Detail Card (5-col) -->
      <div class="apple-card col-span-12 lg:col-span-5 flex flex-col justify-between bg-surface-hover">
        <h2 class="text-sm font-semibold text-muted uppercase tracking-wider mb-4">战绩详情</h2>

        <div v-if="loading" class="space-y-4">
          <div class="h-6 animate-pulse rounded bg-border w-2/3"></div>
          <div class="h-20 animate-pulse rounded bg-border w-full"></div>
        </div>
        <div v-else-if="leaderboard.length" class="space-y-4 flex-1">
          <div class="flex items-center gap-3">
            <span class="dot h-3 w-3" :style="{ backgroundColor: leaderboard[0].group.color }"></span>
            <span class="text-lg font-bold text-text">{{ leaderboard[0].group.name }}</span>
            <span class="text-xs bg-accent/15 text-accent border border-accent/20 px-2 py-0.5 rounded-full font-semibold">首位军团</span>
          </div>

          <!-- 当前卡的核心数据（高亮） -->
          <div class="rounded-xl border-2 border-accent/40 bg-accent/5 p-4 shadow-sm">
            <div class="text-[10px] text-muted uppercase tracking-wider">{{ selectedRule.desc }}</div>
            <div class="text-2xl font-bold text-text font-mono mt-1">{{ leaderboard[0].displayValue }}</div>
          </div>

          <!-- 按卡片维度展示差异化辅助信息 -->
          <div class="rounded-xl border border-border bg-surface p-4 text-xs">
            <!-- 🌅 早起鸟 -->
            <div v-if="selectedRule.field === 'avgMinSeconds'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">每日平均最晚收工：</span><span class="font-mono text-text">{{ leaderboard[0].group.avgMaxSeconds !== null ? `${String(Math.floor(leaderboard[0].group.avgMaxSeconds/3600)).padStart(2,'0')}:${String(Math.floor((leaderboard[0].group.avgMaxSeconds%3600)/60)).padStart(2,'0')}` : '—' }}</span></div>
              <div class="flex justify-between"><span class="text-muted">日均跨度：</span><span class="font-mono text-text">{{ leaderboard[0].group.avgMaxSeconds !== null && leaderboard[0].group.avgMinSeconds !== null ? formatDuration((leaderboard[0].group.avgMaxSeconds - leaderboard[0].group.avgMinSeconds) * 1000) : '—' }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
            </div>

            <!-- 🦉 夜猫子 -->
            <div v-else-if="selectedRule.field === 'avgMaxSeconds'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">每日平均开工：</span><span class="font-mono text-text">{{ leaderboard[0].group.avgMinSeconds !== null ? `${String(Math.floor(leaderboard[0].group.avgMinSeconds/3600)).padStart(2,'0')}:${String(Math.floor((leaderboard[0].group.avgMinSeconds%3600)/60)).padStart(2,'0')}` : '—' }}</span></div>
              <div class="flex justify-between"><span class="text-muted">日均跨度：</span><span class="font-mono text-text">{{ leaderboard[0].group.avgMaxSeconds !== null && leaderboard[0].group.avgMinSeconds !== null ? formatDuration((leaderboard[0].group.avgMaxSeconds - leaderboard[0].group.avgMinSeconds) * 1000) : '—' }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
            </div>

            <!-- ⏱️ 持久作战王 -->
            <div v-else-if="selectedRule.field === 'maxDurationMs'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">使用模型：</span><span class="font-mono text-text truncate ml-2">{{ leaderboard[0].group.longestRequestModel }}</span></div>
              <div class="flex justify-between"><span class="text-muted">发生时间：</span><span class="font-mono text-text">{{ leaderboard[0].group.longestRequestTime !== '—' ? formatTime(leaderboard[0].group.longestRequestTime, config.timezone) : '—' }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期累计耗时：</span><span class="font-mono text-text">{{ formatDuration(leaderboard[0].group.totalDurationMs) }}</span></div>
            </div>

            <!-- 🔥 当日峰值 -->
            <div v-else-if="selectedRule.field === 'peakHourlyTokens'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">本期总 Token：</span><span class="font-mono text-text">{{ formatTokens(leaderboard[0].group.tokens) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">峰值占总比：</span><span class="font-mono text-text">{{ leaderboard[0].group.tokens ? ((leaderboard[0].group.peakHourlyTokens / leaderboard[0].group.tokens) * 100).toFixed(1) + '%' : '—' }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
            </div>

            <!-- 💸 烧钱大户 -->
            <div v-else-if="selectedRule.field === 'cost'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">单次最贵：</span><span class="font-mono text-text">{{ formatCost(leaderboard[0].group.maxRequestCost, config.currency) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">最贵单次模型：</span><span class="font-mono text-text truncate ml-2">{{ leaderboard[0].group.costliestRequestModel }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期 Token：</span><span class="font-mono text-text">{{ formatTokens(leaderboard[0].group.tokens) }}</span></div>
            </div>

            <!-- 📦 缓存王 -->
            <div v-else-if="selectedRule.field === 'cacheReadRatio'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">缓存命中 Token：</span><span class="font-mono text-text">{{ formatTokens(Math.round(leaderboard[0].group.tokens * leaderboard[0].group.cacheReadRatio)) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期总 Token：</span><span class="font-mono text-text">{{ formatTokens(leaderboard[0].group.tokens) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期成本：</span><span class="font-mono text-text">{{ formatCost(leaderboard[0].group.cost, config.currency) }}</span></div>
            </div>

            <!-- 🌪️ 提示词洪流 -->
            <div v-else-if="selectedRule.field === 'inputRatio'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">输入 Token：</span><span class="font-mono text-text">{{ formatTokens(Math.round(leaderboard[0].group.tokens * leaderboard[0].group.inputRatio)) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">输出 Token：</span><span class="font-mono text-text">{{ formatTokens(Math.round(leaderboard[0].group.tokens * leaderboard[0].group.outputRatio)) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
            </div>

            <!-- 📝 长输出爱好者 -->
            <div v-else-if="selectedRule.field === 'outputRatio'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">输出 Token：</span><span class="font-mono text-text">{{ formatTokens(Math.round(leaderboard[0].group.tokens * leaderboard[0].group.outputRatio)) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">输入 Token：</span><span class="font-mono text-text">{{ formatTokens(Math.round(leaderboard[0].group.tokens * leaderboard[0].group.inputRatio)) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
            </div>

            <!-- 🎯 模型专一户 / 🌈 模型博物馆 -->
            <div v-else-if="selectedRule.field === 'modelsCount'" class="space-y-2">
              <div class="text-muted">{{ selectedRule.order === 'asc' ? '专一使用的模型' : '集邮收藏' }}：</div>
              <div class="flex flex-wrap gap-1">
                <span v-for="m in leaderboard[0].group.modelsList" :key="m" class="text-[10px] bg-bg border border-border rounded-full px-2 py-0.5 font-mono">{{ m }}</span>
                <span v-if="!leaderboard[0].group.modelsList.length" class="text-muted italic">—</span>
              </div>
              <div class="flex justify-between pt-2"><span class="text-muted">本期请求：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
            </div>

            <!-- 🕒 工作时长榜 -->
            <div v-else-if="selectedRule.field === 'totalDurationMs'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">最长单次：</span><span class="font-mono text-text">{{ formatDuration(leaderboard[0].group.maxDurationMs) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">单次平均：</span><span class="font-mono text-text">{{ leaderboard[0].group.requests ? formatDuration(leaderboard[0].group.totalDurationMs / leaderboard[0].group.requests) : '—' }}</span></div>
            </div>

            <!-- 🐎 / 🐌 首字节 -->
            <div v-else-if="selectedRule.field === 'avgFirstTokenMs'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">本期流式请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">使用模型数：</span><span class="font-mono text-text">{{ leaderboard[0].group.modelsCount }}</span></div>
              <div class="text-muted pt-1 text-[11px]">仅统计 stream=true 且有 first_token_ms 字段的样本</div>
            </div>

            <!-- 🏆 本周 MVP -->
            <div v-else-if="selectedRule.field === 'tokens'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">本期成本：</span><span class="font-mono text-text">{{ formatCost(leaderboard[0].group.cost, config.currency) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期请求数：</span><span class="font-mono text-text">{{ formatNumber(leaderboard[0].group.requests) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">小时峰值：</span><span class="font-mono text-text">{{ formatTokens(leaderboard[0].group.peakHourlyTokens) }}</span></div>
            </div>

            <!-- 📈 黑马奖 -->
            <div v-else-if="selectedRule.field === 'growthRate'" class="space-y-2">
              <div class="flex justify-between"><span class="text-muted">本期成本：</span><span class="font-mono text-text">{{ formatCost(leaderboard[0].group.cost, config.currency) }}</span></div>
              <div class="flex justify-between"><span class="text-muted">本期 Token：</span><span class="font-mono text-text">{{ formatTokens(leaderboard[0].group.tokens) }}</span></div>
              <div class="text-muted pt-1 text-[11px]">对比的是与本期等长的上一周期 actual_cost</div>
            </div>

            <!-- 🛌 隐居者 -->
            <div v-else-if="selectedRule.isRequestsZero" class="text-muted italic py-2">
              本期内一次也没用过 — 也许是养精蓄锐，也许是早已淡出江湖。
            </div>
          </div>
        </div>
        <div v-else class="text-center py-12 text-muted text-sm flex-1 flex items-center justify-center">
          选择卡片查看首位详情
        </div>
      </div>
    </div>

    <!-- Group PK radar -->
    <div v-if="groupsStatsList.length >= 2" class="apple-card">
      <div class="mb-4 flex flex-col md:flex-row md:items-center justify-between gap-3">
        <div>
          <h2 class="text-base font-semibold text-text">⚔️ 分组对战</h2>
          <p class="text-xs text-muted mt-1">六维战力雷达，每一维按全体分组最大值归一化（0-100）</p>
        </div>
        <div class="flex items-center gap-2 text-xs">
          <select v-model="pkGroupAId" class="apple-select text-xs py-1 px-3 h-8">
            <option v-for="g in groupsStatsList" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
          <span class="font-bold text-error">VS</span>
          <select v-model="pkGroupBId" class="apple-select text-xs py-1 px-3 h-8">
            <option v-for="g in groupsStatsList" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
        </div>
      </div>

      <div v-if="pkA && pkB && pkA.id !== pkB.id" class="grid grid-cols-12 gap-6">
        <div class="col-span-12 lg:col-span-7 h-80">
          <v-chart class="h-full w-full" :option="pkRadarOption" autocall />
        </div>
        <div class="col-span-12 lg:col-span-5 flex flex-col justify-center gap-3">
          <div v-if="pkWinner" class="rounded-xl border-2 border-warning/40 bg-warning/5 p-4">
            <div class="text-[10px] text-muted uppercase tracking-wider">🏆 胜者</div>
            <div class="text-lg font-bold text-text mt-0.5">{{ pkWinner.name }}</div>
            <div class="text-xs text-muted mt-1 italic">{{ pkWinner.text }}</div>
          </div>
          <div class="rounded-xl border border-border bg-bg/50 p-4 text-xs space-y-1.5">
            <div v-for="(d, i) in PK_DIMS" :key="d.key" class="flex items-center justify-between">
              <span class="text-muted">{{ d.label }}</span>
              <span class="font-mono">
                <b :style="{ color: pkA.color }">{{ pkScores(pkA)[i] }}</b>
                <span class="text-muted mx-1">:</span>
                <b :style="{ color: pkB.color }">{{ pkScores(pkB)[i] }}</b>
              </span>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="py-8 text-center text-sm text-muted">选择两个不同的分组开战</div>
    </div>

    <!-- Achievement badge wall -->
    <div v-if="badgeWall.length" class="apple-card">
      <div class="mb-4">
        <h2 class="text-base font-semibold text-text">🏅 本期成就墙</h2>
        <p class="text-xs text-muted mt-1">根据本期数据自动颁发，共 {{ BADGES.length }} 种徽章可解锁</p>
      </div>
      <div class="space-y-3">
        <div
          v-for="row in badgeWall"
          :key="row.group.id"
          class="flex flex-col md:flex-row md:items-center gap-2 md:gap-4 rounded-xl border border-border bg-bg/50 px-4 py-3"
        >
          <div class="flex items-center gap-2.5 md:w-44 flex-shrink-0">
            <span class="dot h-2.5 w-2.5" :style="{ backgroundColor: row.group.color }"></span>
            <span class="text-sm font-semibold text-text truncate">{{ row.group.name }}</span>
            <span class="text-[10px] text-muted font-mono">{{ row.badges.length }}枚</span>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <span
              v-for="b in row.badges"
              :key="b.name"
              class="inline-flex items-center gap-1 rounded-full border border-border bg-surface px-2.5 py-1 text-[11px] font-medium text-text cursor-default"
              :title="b.desc"
            >
              <span>{{ b.emoji }}</span>{{ b.name }}
            </span>
            <span v-if="!row.badges.length" class="text-[11px] text-muted italic py-1">白板一块，本期无成就 =。=</span>
          </div>
        </div>
      </div>
    </div>

    <!-- 24h x 7d Request Activity Heatmap -->
    <div class="apple-card">
      <div class="mb-6 flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-semibold text-text">活跃时段热力图</h2>
          <p class="text-xs text-muted mt-1">展示最近 7 天的每小时请求频次（基于本地时区 {{ config.timezone }}）</p>
        </div>

        <!-- Group Filter Dropdown -->
        <div class="flex items-center gap-2">
          <span class="text-xs text-muted">筛选分组:</span>
          <select
            v-model="selectedHeatmapGroupId"
            class="apple-select text-xs py-1 px-3 h-8"
            :disabled="loadingHeatmap"
          >
            <option value="all">所有分组</option>
            <option
              v-for="g in virtualGroupsWithUnassigned"
              :key="g.id"
              :value="g.id"
            >
              {{ g.name }}
            </option>
          </select>
        </div>
      </div>

      <div class="h-[280px] w-full relative">
        <div v-if="loadingHeatmap || loading" class="absolute inset-0 flex items-center justify-center bg-surface/50 z-10 rounded-xl">
          <div class="h-8 w-8 animate-spin rounded-full border-4 border-accent border-t-transparent"></div>
        </div>
        <v-chart v-else class="h-full w-full" :option="heatmapChartOption" autocall />
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Fun stats styles */
</style>
