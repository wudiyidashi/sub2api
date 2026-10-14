<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { useConfigStore, type VirtualGroup } from '@/stores/config';
import { useRangeStore } from '@/stores/range';
import { useGroupAggregate } from '@/composables/useGroupStats';
import { useAutoRefresh } from '@/composables/useAutoRefresh';
import { formatCost, formatNumber, formatTokens, formatDuration, formatTime, formatHHmm } from '@/utils/format';
import { formatBucketLabel, hourAndDow, localDate, ymdToday } from '@/utils/time';
import { classifyUserAgent } from '@/utils/userAgent';
import { mergeTrends } from '@/utils/reduce';
import { resolveIpGeos, type IpGeo } from '@/utils/ipGeo';
import { ensureWorldMap } from '@/utils/worldMap';
import { colorDistance, evenPalette } from '@/utils/colors';
import { endpoints } from '@/api/endpoints';
import type { TrendBucket, UsageLog } from '@/api/types';

import VChart from 'vue-echarts';
import * as echarts from 'echarts';

const config = useConfigStore();
const range = useRangeStore();
const router = useRouter();
const selectedId = ref<string | null>(null);

// Join virtual groups with unassigned keys bucket dynamically
const virtualGroupsWithUnassigned = computed<VirtualGroup[]>(() => {
  const assignedIds = new Set(config.groups.flatMap(g => g.apiKeyIds));
  const unassignedKeys = config.allApiKeys.filter(k => !assignedIds.has(k.id));

  const unassignedGroup: VirtualGroup = {
    id: 'unassigned',
    name: '未分组 keys',
    color: '#8e8e93', // Gray bullet
    apiKeyIds: unassignedKeys.map(k => k.id),
    note: '包含所有未被划分至任何虚拟分组的 API 密钥'
  };

  const list = [...config.groups];
  if (unassignedGroup.apiKeyIds.length > 0 || list.length === 0) {
    list.push(unassignedGroup);
  }
  return list;
});

const selectedGroup = computed(() =>
  virtualGroupsWithUnassigned.value.find((g) => g.id === selectedId.value) ?? null,
);

const { data, loading, error, refresh } = useGroupAggregate(() => selectedGroup.value);

// Auto select first group on startup
watch(
  virtualGroupsWithUnassigned,
  (list) => {
    if (list.length && (!selectedId.value || !list.find(g => g.id === selectedId.value))) {
      selectedId.value = list[0].id;
    }
  },
  { immediate: true }
);

// Trigger refresh
async function handleRefresh() {
  await Promise.all([
    refresh(),
    loadGroupModels(selectedGroup.value),
    loadGroupFunStats(selectedGroup.value),
    loadHourlyTrend(selectedGroup.value),
    loadPrevUsage(selectedGroup.value),
    loadIpStats(selectedGroup.value),
    loadYearHeatmap(selectedGroup.value)
  ]);
}
useAutoRefresh(handleRefresh);

// ---- 上一周期环比 ----
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

const prevUsage = ref<{ requests: number; total_tokens: number; total_cost: number; actual_cost: number } | null>(null);

async function loadPrevUsage(g: VirtualGroup | null) {
  if (!g || !g.apiKeyIds.length) {
    prevUsage.value = null;
    return;
  }
  try {
    const trends = await Promise.all(
      g.apiKeyIds.map(id =>
        endpoints.dashboardTrend({
          ...prevApiDates.value,
          granularity: range.resolved.granularity,
          timezone: config.timezone,
          api_key_id: id
        }).then(r => r.trend).catch(() => [])
      )
    );
    let requests = 0, total_tokens = 0, total_cost = 0, actual_cost = 0;
    for (const list of trends) {
      for (const b of list) {
        requests += b.requests || 0;
        total_tokens += b.total_tokens || 0;
        total_cost += b.total_cost || 0;
        actual_cost += b.actual_cost || 0;
      }
    }
    prevUsage.value = { requests, total_tokens, total_cost, actual_cost };
  } catch (e) {
    console.error('Failed to load previous period usage', e);
    prevUsage.value = null;
  }
}

function calcComparison(current: number | undefined, previous: number | undefined) {
  if (current === undefined || previous === undefined || previous === 0) return null;
  const change = ((current - previous) / previous) * 100;
  return {
    value: Math.abs(change).toFixed(1) + '%',
    isUp: change > 0,
    isDown: change < 0
  };
}

const reqComparison = computed(() => calcComparison(data.value?.usage.requests, prevUsage.value?.requests));
const tokenComparison = computed(() => calcComparison(data.value?.usage.total_tokens, prevUsage.value?.total_tokens));
const costComparison = computed(() => {
  const cur = config.costSource === 'actual' ? data.value?.usage.actual_cost : data.value?.usage.total_cost;
  const prev = config.costSource === 'actual' ? prevUsage.value?.actual_cost : prevUsage.value?.total_cost;
  return calcComparison(cur, prev);
});

// ---- 小时级 trend：时段分布 / 每日频率 / 工作节奏（统一按 config.timezone 折算） ----
const hourlyTrend = ref<TrendBucket[]>([]);
const loadingHourly = ref(false);

async function loadHourlyTrend(g: VirtualGroup | null) {
  if (!g || !g.apiKeyIds.length) {
    hourlyTrend.value = [];
    return;
  }
  loadingHourly.value = true;
  try {
    const trends = await Promise.all(
      g.apiKeyIds.map(id =>
        endpoints.dashboardTrend({
          ...range.apiDates,
          granularity: 'hour',
          timezone: config.timezone,
          api_key_id: id
        }).then(r => r.trend).catch(() => [])
      )
    );
    hourlyTrend.value = mergeTrends(trends);
  } catch (e) {
    console.error('Failed to load hourly trend', e);
  } finally {
    loadingHourly.value = false;
  }
}

/** 24 小时时段分布：本期各小时（按时区）的请求数合计 */
const hourOfDayDist = computed(() => {
  const buckets = new Array(24).fill(0) as number[];
  for (const b of hourlyTrend.value) {
    if (!b.requests) continue;
    const { hour } = hourAndDow(b.timestamp, config.timezone);
    buckets[hour] += b.requests;
  }
  return buckets;
});

/** 每日请求频率：按时区折算的自然日 → 请求数 */
const dailyFreq = computed(() => {
  const map = new Map<string, number>();
  for (const b of hourlyTrend.value) {
    const day = localDate(b.timestamp, config.timezone);
    map.set(day, (map.get(day) || 0) + (b.requests || 0));
  }
  return Array.from(map.entries()).sort((a, b) => (a[0] < b[0] ? -1 : 1));
});

/** 工作节奏：各活跃日的首个 / 末个活跃小时（小时粒度） */
const workRhythm = computed(() => {
  const perDay = new Map<string, { first: number; last: number }>();
  for (const b of hourlyTrend.value) {
    if (!(b.requests > 0)) continue;
    const day = localDate(b.timestamp, config.timezone);
    const { hour } = hourAndDow(b.timestamp, config.timezone);
    const cur = perDay.get(day);
    if (!cur) perDay.set(day, { first: hour, last: hour });
    else {
      cur.first = Math.min(cur.first, hour);
      cur.last = Math.max(cur.last, hour);
    }
  }
  const days = Array.from(perDay.values());
  if (!days.length) return null;
  const avgFirst = days.reduce((s, d) => s + d.first, 0) / days.length;
  const avgLast = days.reduce((s, d) => s + d.last, 0) / days.length;
  const dist = hourOfDayDist.value;
  const peakHour = dist.indexOf(Math.max(...dist));
  const totalDays = dailyFreq.value.length || days.length;
  return {
    activeDays: days.length,
    totalDays,
    avgFirst,
    avgLast,
    peakHour,
    earliestFirst: Math.min(...days.map(d => d.first)),
    latestLast: Math.max(...days.map(d => d.last))
  };
});

function hourToHHmm(h: number): string {
  const hh = Math.floor(h);
  const mm = Math.round((h - hh) * 60);
  return `${String(hh).padStart(2, '0')}:${String(mm).padStart(2, '0')}`;
}

// ---- 明细采样（IP 地图 / 工具偏好 / 缓存省钱共用同一份样本） ----
/** 每个 key 最多采样的明细页数（page_size=100），控制请求量 */
const IP_SAMPLE_PAGES = 3;

interface IpStatRow {
  ip: string;
  requests: number;
  geo: IpGeo | null;
}

const sampleLogs = ref<UsageLog[]>([]);
const ipStats = ref<IpStatRow[]>([]);
const ipSampleCount = ref(0);
const ipTotalCount = ref(0);
const ipNoIpCount = ref(0);
const loadingIp = ref(false);
const worldMapReady = ref(false);

async function loadIpStats(g: VirtualGroup | null) {
  if (!g || !g.apiKeyIds.length) {
    sampleLogs.value = [];
    ipStats.value = [];
    ipSampleCount.value = 0;
    ipTotalCount.value = 0;
    ipNoIpCount.value = 0;
    return;
  }
  loadingIp.value = true;
  try {
    worldMapReady.value = await ensureWorldMap();
    const counts = new Map<string, number>();
    const logs: UsageLog[] = [];
    let total = 0;
    let noIp = 0;

    await Promise.all(
      g.apiKeyIds.map(async (id) => {
        for (let page = 1; page <= IP_SAMPLE_PAGES; page++) {
          const res = await endpoints.usageList({
            ...range.apiDates,
            timezone: config.timezone,
            api_key_id: id,
            page,
            page_size: 100,
            sort_by: 'created_at',
            sort_order: 'desc'
          }).catch(() => null);
          if (!res || !res.items.length) break;
          if (page === 1) total += res.total || 0;
          for (const l of res.items) {
            logs.push(l);
            const ip = (l.ip_address || '').trim();
            if (!ip) {
              noIp++;
              continue;
            }
            counts.set(ip, (counts.get(ip) || 0) + 1);
          }
          if (res.items.length < 100) break;
        }
      })
    );

    sampleLogs.value = logs;
    ipSampleCount.value = logs.length;
    ipTotalCount.value = total;
    ipNoIpCount.value = noIp;

    const geos = await resolveIpGeos(Array.from(counts.keys()));
    ipStats.value = Array.from(counts.entries())
      .map(([ip, requests]) => ({ ip, requests, geo: geos.get(ip) || null }))
      .sort((a, b) => b.requests - a.requests);
  } catch (e) {
    console.error('Failed to load IP stats', e);
  } finally {
    loadingIp.value = false;
  }
}

const ipMapOption = computed(() => {
  const c = chartBaseColors();
  const color = selectedGroup.value?.color || '#0066cc';
  const points = ipStats.value.filter(
    (r) => r.geo && r.geo.longitude !== undefined && r.geo.latitude !== undefined
  );
  const maxReq = Math.max(...points.map((p) => p.requests), 1);

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
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
        areaColor: c.isDark ? '#26262a' : '#eef1f5',
        borderColor: c.isDark ? '#3a3a3c' : '#d5d9e0'
      },
      emphasis: {
        label: { show: false },
        itemStyle: { areaColor: c.isDark ? '#303036' : '#e2e7ee' }
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
        itemStyle: { color, shadowBlur: 6, shadowColor: 'rgba(0,0,0,0.25)' },
        zlevel: 2
      }
    ]
  };
});

/** 地图上画不出的行（内网 / 解析失败），在列表里单独标注 */
const ipTopRows = computed(() => ipStats.value.slice(0, 12));
const ipSampleIsFull = computed(() => ipSampleCount.value >= ipTotalCount.value);

// ---- 工具偏好（user_agent 归类，与 IP 地图同一份采样） ----
const uaStats = computed(() => {
  const map = new Map<string, number>();
  for (const l of sampleLogs.value) {
    const tool = classifyUserAgent(l.user_agent);
    map.set(tool, (map.get(tool) || 0) + 1);
  }
  return Array.from(map.entries())
    .map(([name, requests]) => ({ name, requests }))
    .sort((a, b) => b.requests - a.requests);
});

const uaPieOption = computed(() => {
  const c = chartBaseColors();
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      formatter: '{b}: {c} 次 ({d}%)'
    },
    series: [
      {
        name: '工具偏好',
        type: 'pie',
        radius: ['48%', '74%'],
        center: ['50%', '50%'],
        avoidLabelOverlap: true,
        data: uaStats.value.map((u) => ({ name: u.name, value: u.requests })),
        label: { color: c.axisLabel, fontSize: 10 },
        labelLine: { lineStyle: { color: c.axisLine } },
        itemStyle: { borderColor: c.isDark ? '#1c1c1e' : '#ffffff', borderWidth: 2 }
      }
    ]
  };
});

// ---- 缓存省钱彩蛋 ----
// 单价从采样日志反推（input_cost / input_tokens），省下的钱 = 缓存读 token 按输入价计费 - 实际缓存读费用
const cacheSavings = computed(() => {
  const usage = data.value?.usage;
  if (!usage || !usage.cache_read_tokens) return null;
  let sampleSave = 0;
  let sampleCrt = 0;
  for (const l of sampleLogs.value) {
    if (!l.cache_read_tokens || !l.input_tokens || !l.input_cost) continue;
    const unitInput = l.input_cost / l.input_tokens;
    const save = l.cache_read_tokens * unitInput - (l.cache_read_cost || 0);
    if (save > 0) {
      sampleSave += save;
      sampleCrt += l.cache_read_tokens;
    }
  }
  if (!sampleCrt) return null;
  const saved = (sampleSave / sampleCrt) * usage.cache_read_tokens;
  const cny = saved * 7.2;
  return {
    saved,
    hitRate: usage.total_tokens ? usage.cache_read_tokens / usage.total_tokens : 0,
    coffee: cny / 9.9,
    hotpot: cny / 110
  };
});

// ---- 活跃日历（近 12 个月，GitHub contribution 风格） ----
function addDaysYmd(ymd: string, n: number): string {
  const d = new Date(`${ymd}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

const calRange = computed(() => {
  const end = ymdToday(config.timezone);
  return [addDaysYmd(end, -364), end] as [string, string];
});

const yearDaily = ref<[string, number][]>([]);
const loadingYear = ref(false);

async function loadYearHeatmap(g: VirtualGroup | null) {
  if (!g || !g.apiKeyIds.length) {
    yearDaily.value = [];
    return;
  }
  loadingYear.value = true;
  try {
    const [start, end] = calRange.value;
    const trends = await Promise.all(
      g.apiKeyIds.map(id =>
        endpoints.dashboardTrend({
          start_date: start,
          end_date: end,
          granularity: 'day',
          timezone: config.timezone,
          api_key_id: id
        }).then(r => r.trend).catch(() => [])
      )
    );
    const byDay = new Map<string, number>();
    for (const b of mergeTrends(trends)) {
      const day = localDate(b.timestamp, config.timezone);
      byDay.set(day, (byDay.get(day) || 0) + (b.requests || 0));
    }
    // 补全区间内的空白天，保证日历格子完整
    const out: [string, number][] = [];
    for (let d = start; d <= end; d = addDaysYmd(d, 1)) {
      out.push([d, byDay.get(d) || 0]);
    }
    yearDaily.value = out;
  } catch (e) {
    console.error('Failed to load year heatmap', e);
  } finally {
    loadingYear.value = false;
  }
}

const yearActiveDays = computed(() => yearDaily.value.filter(([, n]) => n > 0).length);

// ---- 战报海报导出 ----
const posterRef = ref<HTMLElement | null>(null);
const exporting = ref(false);

const posterTagline = computed(() => {
  const r = workRhythm.value;
  if (!r) return 'AI 是我最好的同事。';
  if (r.avgLast >= 22 || r.latestLast >= 23) return '月亮不睡我不睡，token 烧起来是真的香。';
  if (r.avgFirst <= 7) return '早起的鸟儿有 token 吃。';
  if (r.activeDays === r.totalDays && r.totalDays > 1) return '全勤战士，一天不落。';
  return '打工人的算力日记。';
});

const posterTopModels = computed(() => {
  const total = groupModels.value.reduce((s, m) => s + m.total_tokens, 0) || 1;
  return groupModels.value.slice(0, 3).map(m => ({
    model: m.model,
    tokens: m.total_tokens,
    pct: (m.total_tokens / total) * 100
  }));
});

async function exportPoster() {
  if (!posterRef.value || !selectedGroup.value) return;
  exporting.value = true;
  try {
    const { default: html2canvas } = await import('html2canvas');
    const canvas = await html2canvas(posterRef.value, {
      scale: 2,
      backgroundColor: null,
      useCORS: true,
      logging: false
    });
    const a = document.createElement('a');
    a.href = canvas.toDataURL('image/png');
    a.download = `${selectedGroup.value.name}-战报-${range.apiDates.start_date}~${range.apiDates.end_date}.png`;
    a.click();
  } catch (e) {
    console.error('Failed to export poster', e);
    alert('导出失败：' + (e as Error).message);
  } finally {
    exporting.value = false;
  }
}

const calendarOption = computed(() => {
  const c = chartBaseColors();
  const color = selectedGroup.value?.color || '#0066cc';
  const maxVal = Math.max(...yearDaily.value.map(([, n]) => n), 1);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      formatter: (params: any) =>
        `<b>${params.data[0]}</b><br/>请求数: <b>${formatNumber(params.data[1])}</b>`
    },
    visualMap: {
      min: 0,
      max: maxVal,
      show: false,
      inRange: {
        color: [c.isDark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.04)', color]
      }
    },
    calendar: {
      range: calRange.value,
      cellSize: ['auto', 13],
      left: 30,
      right: 10,
      top: 26,
      bottom: 6,
      splitLine: { show: false },
      itemStyle: {
        borderWidth: 2.5,
        borderColor: c.isDark ? '#1c1c1e' : '#ffffff'
      },
      dayLabel: {
        color: c.axisLabel,
        fontSize: 9,
        firstDay: 1,
        nameMap: ['日', '一', '二', '三', '四', '五', '六']
      },
      monthLabel: {
        color: c.axisLabel,
        fontSize: 10,
        nameMap: ['1月', '2月', '3月', '4月', '5月', '6月', '7月', '8月', '9月', '10月', '11月', '12月']
      },
      yearLabel: { show: false }
    },
    series: [
      {
        type: 'heatmap',
        coordinateSystem: 'calendar',
        data: yearDaily.value
      }
    ]
  };
});

// Join usage statistics with key metadata
const keyDetailsList = computed(() => {
  if (!data.value || !selectedGroup.value) return [];
  return selectedGroup.value.apiKeyIds.map(id => {
    const meta = config.allApiKeys.find(k => k.id === id);
    const usage = data.value?.perKey[id];
    return {
      id,
      name: meta?.name || `key-${id}`,
      status: meta?.status || 'active',
      quota: meta?.quota || 0,
      quota_used: meta?.quota_used || 0,
      last_used_at: meta?.last_used_at || null,
      requests: usage?.requests || 0,
      total_tokens: usage?.total_tokens || 0,
      actual_cost: usage?.actual_cost || 0,
      total_cost: usage?.total_cost || 0
    };
  });
});

// Load and aggregate model preference for selected group
const groupModels = ref<{ model: string; requests: number; total_tokens: number; total_cost: number; actual_cost: number }[]>([]);
const loadingModels = ref(false);

async function loadGroupModels(g: VirtualGroup | null) {
  if (!g || !g.apiKeyIds.length) {
    groupModels.value = [];
    return;
  }
  loadingModels.value = true;
  try {
    const modelsList = await Promise.all(
      g.apiKeyIds.map(id =>
        endpoints.dashboardModels({
          ...range.apiDates,
          api_key_id: id,
          timezone: config.timezone
        }).then(res => res.models).catch(() => [])
      )
    );

    const mergedMap = new Map<string, typeof groupModels.value[0]>();
    for (const list of modelsList) {
      for (const m of list) {
        const cur = mergedMap.get(m.model);
        if (!cur) {
          mergedMap.set(m.model, { ...m });
        } else {
          cur.requests += m.requests;
          cur.total_tokens += m.total_tokens;
          cur.total_cost += m.total_cost;
          cur.actual_cost += m.actual_cost;
        }
      }
    }
    groupModels.value = Array.from(mergedMap.values()).sort((a, b) => b.total_cost - a.total_cost);
  } catch (e) {
    console.error('Failed to load group models', e);
  } finally {
    loadingModels.value = false;
  }
}

// 4 Fun Cards loaders
const groupFunStats = ref<{
  earliest: UsageLog | null;
  latest: UsageLog | null;
  longest: UsageLog | null;
  costliest: UsageLog | null;
}>({ earliest: null, latest: null, longest: null, costliest: null });
const loadingFun = ref(false);

async function loadGroupFunStats(g: VirtualGroup | null) {
  if (!g || !g.apiKeyIds.length) {
    groupFunStats.value = { earliest: null, latest: null, longest: null, costliest: null };
    return;
  }
  loadingFun.value = true;
  try {
    const promises = g.apiKeyIds.map(async (id) => {
      const baseParams = {
        ...range.apiDates,
        timezone: config.timezone,
        api_key_id: id,
        page_size: 1
      };

      const [earliestRes, latestRes, longestRes, costliestRes] = await Promise.all([
        endpoints.usageList({ ...baseParams, sort_by: 'created_at', sort_order: 'asc' }).catch(() => null),
        endpoints.usageList({ ...baseParams, sort_by: 'created_at', sort_order: 'desc' }).catch(() => null),
        endpoints.usageList({ ...baseParams, sort_by: 'duration_ms', sort_order: 'desc' }).catch(() => null),
        endpoints.usageList({ ...baseParams, sort_by: 'total_cost', sort_order: 'desc' }).catch(() => null)
      ]);

      return {
        earliest: earliestRes?.items?.[0] || null,
        latest: latestRes?.items?.[0] || null,
        longest: longestRes?.items?.[0] || null,
        costliest: costliestRes?.items?.[0] || null
      };
    });

    const results = await Promise.all(promises);

    let earliest: UsageLog | null = null;
    let latest: UsageLog | null = null;
    let longest: UsageLog | null = null;
    let costliest: UsageLog | null = null;

    for (const r of results) {
      if (r.earliest) {
        if (!earliest || new Date(r.earliest.created_at) < new Date(earliest.created_at)) {
          earliest = r.earliest;
        }
      }
      if (r.latest) {
        if (!latest || new Date(r.latest.created_at) > new Date(latest.created_at)) {
          latest = r.latest;
        }
      }
      if (r.longest) {
        if (!longest || (r.longest.duration_ms || 0) > (longest.duration_ms || 0)) {
          longest = r.longest;
        }
      }
      if (r.costliest) {
        if (!costliest || (r.costliest.total_cost || 0) > (costliest.total_cost || 0)) {
          costliest = r.costliest;
        }
      }
    }

    groupFunStats.value = { earliest, latest, longest, costliest };
  } catch (e) {
    console.error('Failed to load group fun stats', e);
  } finally {
    loadingFun.value = false;
  }
}

// Watch group selection
watch(selectedGroup, (g) => {
  if (g) {
    void loadGroupModels(g);
    void loadGroupFunStats(g);
    void loadHourlyTrend(g);
    void loadPrevUsage(g);
    void loadIpStats(g);
    void loadYearHeatmap(g);
  }
}, { immediate: true });

// Watch global time range: reload all range-dependent panels
watch(() => range.apiDates, () => {
  const g = selectedGroup.value;
  if (g) {
    void loadGroupModels(g);
    void loadGroupFunStats(g);
    void loadHourlyTrend(g);
    void loadPrevUsage(g);
    void loadIpStats(g);
  }
}, { deep: true });

// Jump to raw page with key filters
function filterByKey(keyId: number) {
  router.push({
    path: '/raw',
    query: { key_id: String(keyId) }
  });
}

// Dynamic ECharts x-axis
const timestamps = computed(() => {
  if (!data.value) return [];
  return data.value.trend.map(t => t.timestamp);
});

const trendXAxisData = computed(() => {
  const gran = range.resolved.granularity;
  return timestamps.value.map(ts => formatBucketLabel(ts, gran, config.timezone));
});

// Single Group Token Area Trend Option
const selectedGroupTrendOption = computed(() => {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  if (!data.value || !data.value.trend.length) return {};

  const color = selectedGroup.value?.color || '#0066cc';

  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: isDark ? '#1f1f23' : '#ffffff',
      borderColor: isDark ? '#3a3a3c' : '#e0e0e0',
      textStyle: { color: isDark ? '#ffffff' : '#1d1d1f' },
      valueFormatter: (value: number) => formatTokens(value)
    },
    grid: {
      left: '3%',
      right: '4%',
      bottom: '3%',
      top: '10%',
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
    series: [{
      name: selectedGroup.value?.name || 'Token 消耗',
      type: 'line',
      smooth: true,
      symbol: 'none',
      areaStyle: {
        color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
          { offset: 0, color: color },
          { offset: 1, color: 'rgba(0, 0, 0, 0)' }
        ]),
        opacity: 0.2
      },
      data: timestamps.value.map(ts => {
        const b = data.value?.trend.find(t => t.timestamp === ts);
        return b?.total_tokens || 0;
      }),
      itemStyle: { color: color },
      lineStyle: { width: 3 }
    }]
  };
});

const isEditMode = ref(false);
const isEditingNote = ref(false);

const editingGroup = ref<VirtualGroup | null>(null);
const keySearchQuery = ref('');
const showKeySelector = ref(false);

function addGroup() {
  const id = `g-${Date.now().toString(36)}`;
  const color = randColor();
  config.groups.push({ id, name: '新分组', color, apiKeyIds: [], note: '自定义人名分组' });
  selectedId.value = id;
}

function removeGroup(id: string) {
  const idx = config.groups.findIndex((g) => g.id === id);
  if (idx >= 0) {
    config.groups.splice(idx, 1);
  }
}

function randColor(): string {
  const palette = ['#1a73e8', '#ea4335', '#fbbc05', '#34a853', '#af52de', '#5856d6', '#ff2d55', '#30b0c7'];
  const used = config.groups.map((g) => g.color);
  if (!used.length) return palette[Math.floor(Math.random() * palette.length)];
  // 挑与现有分组色距离最远的候选色，减少新建即撞色
  let best = palette[0];
  let bestD = -1;
  for (const c of palette) {
    const d = Math.min(...used.map((u) => colorDistance(c, u)));
    if (d > bestD) {
      bestD = d;
      best = c;
    }
  }
  return best;
}

/** 一键配色：按分组数量在色环上均匀取色并写回配置（全站生效） */
function autoRecolor() {
  const palette = evenPalette(config.groups.length);
  config.groups.forEach((g, i) => {
    g.color = palette[i];
  });
}

function openKeySelector(g: VirtualGroup) {
  editingGroup.value = g;
  keySearchQuery.value = '';
  showKeySelector.value = true;
}

const filteredApiKeys = computed(() => {
  const q = keySearchQuery.value.trim().toLowerCase();
  if (!q) return config.allApiKeys;
  return config.allApiKeys.filter(k =>
    String(k.id).includes(q) ||
    (k.name && k.name.toLowerCase().includes(q))
  );
});

function isKeySelected(keyId: number): boolean {
  if (!editingGroup.value) return false;
  return editingGroup.value.apiKeyIds.includes(keyId);
}

function toggleKeySelection(keyId: number) {
  if (!editingGroup.value) return;
  const ids = editingGroup.value.apiKeyIds;
  const idx = ids.indexOf(keyId);
  if (idx >= 0) {
    ids.splice(idx, 1);
  } else {
    config.groups.forEach(g => {
      const gIdx = g.apiKeyIds.indexOf(keyId);
      if (gIdx >= 0) g.apiKeyIds.splice(gIdx, 1);
    });
    ids.push(keyId);
  }
}

watch(
  () => selectedGroup.value?.apiKeyIds,
  () => {
    if (selectedGroup.value) {
      void loadGroupModels(selectedGroup.value);
      void loadGroupFunStats(selectedGroup.value);
      void loadHourlyTrend(selectedGroup.value);
      void loadPrevUsage(selectedGroup.value);
      void loadIpStats(selectedGroup.value);
      void loadYearHeatmap(selectedGroup.value);
      void refresh();
    }
  },
  { deep: true }
);

// ---- 时段 / 每日频率图表 ----
function chartBaseColors() {
  const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
  return {
    isDark,
    tooltipBg: isDark ? '#1f1f23' : '#ffffff',
    tooltipBorder: isDark ? '#3a3a3c' : '#e0e0e0',
    tooltipText: isDark ? '#ffffff' : '#1d1d1f',
    axisLabel: isDark ? '#8e8e93' : '#7a7a7a',
    axisLine: isDark ? '#3a3a3c' : '#e0e0e0',
    splitLine: isDark ? '#272729' : '#f0f0f0'
  };
}

const hourlyDistOption = computed(() => {
  const c = chartBaseColors();
  const color = selectedGroup.value?.color || '#0066cc';
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      valueFormatter: (v: number) => `${formatNumber(v)} 次`
    },
    grid: { left: '3%', right: '3%', bottom: '3%', top: '12%', containLabel: true },
    xAxis: {
      type: 'category',
      data: Array.from({ length: 24 }, (_, i) => `${String(i).padStart(2, '0')}`),
      axisLabel: { color: c.axisLabel, fontSize: 10, interval: 1 },
      axisLine: { lineStyle: { color: c.axisLine } }
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: c.axisLabel, formatter: (v: number) => formatNumber(v, { short: true }) },
      splitLine: { lineStyle: { color: c.splitLine } }
    },
    series: [{
      name: '请求数',
      type: 'bar',
      data: hourOfDayDist.value,
      itemStyle: { color, borderRadius: [3, 3, 0, 0] },
      barMaxWidth: 18
    }]
  };
});

const dailyFreqOption = computed(() => {
  const c = chartBaseColors();
  const color = selectedGroup.value?.color || '#0066cc';
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText },
      valueFormatter: (v: number) => `${formatNumber(v)} 次`
    },
    grid: { left: '3%', right: '3%', bottom: '3%', top: '12%', containLabel: true },
    xAxis: {
      type: 'category',
      data: dailyFreq.value.map(([day]) => day.slice(5)),
      axisLabel: { color: c.axisLabel, fontSize: 10 },
      axisLine: { lineStyle: { color: c.axisLine } }
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: c.axisLabel, formatter: (v: number) => formatNumber(v, { short: true }) },
      splitLine: { lineStyle: { color: c.splitLine } }
    },
    series: [{
      name: '请求数',
      type: 'bar',
      data: dailyFreq.value.map(([, req]) => req),
      itemStyle: { color, borderRadius: [3, 3, 0, 0] },
      barMaxWidth: 22
    }]
  };
});
</script>

<template>
  <div class="grid grid-cols-12 gap-6 max-w-[1440px] mx-auto pb-12">
    <!-- Left panel: list of virtual groups (3-col) -->
    <aside class="col-span-12 lg:col-span-3 flex flex-col gap-4">
      <div class="apple-card p-4">
        <div class="flex items-center justify-between mb-3 border-b border-border/40 pb-2">
          <h2 class="text-sm font-semibold text-muted uppercase tracking-wider">虚拟分组</h2>
          <button
            class="text-xs text-accent hover:text-accent-focus font-medium flex items-center gap-1 focus:outline-none transition-colors"
            @click="isEditMode = !isEditMode"
          >
            {{ isEditMode ? '完成' : '管理' }}
          </button>
        </div>

        <ul class="space-y-1.5 max-h-[500px] overflow-y-auto pr-1">
          <li
            v-for="g in virtualGroupsWithUnassigned"
            :key="g.id"
            class="flex items-center justify-between cursor-pointer rounded-xl px-3 py-2.5 transition-all select-none hover:bg-surface-hover"
            :class="selectedId === g.id ? 'bg-surface border border-border shadow-sm font-semibold' : 'border border-transparent'"
            @click="selectedId = g.id"
          >
            <div class="flex items-center gap-2.5 min-w-0 flex-1">
              <!-- Read Mode Avatar -->
              <div
                v-if="!isEditMode || g.id === 'unassigned'"
                class="w-8 h-8 rounded-full flex items-center justify-center text-white text-xs font-bold flex-shrink-0 select-none"
                :style="{ backgroundColor: g.color }"
              >
                {{ g.name ? g.name.charAt(0).toUpperCase() : 'G' }}
              </div>

              <!-- Edit Mode Color Picker -->
              <div
                v-else
                class="relative w-8 h-8 rounded-full flex items-center justify-center overflow-hidden flex-shrink-0 border border-border bg-bg cursor-pointer"
                @click.stop
              >
                <input type="color" v-model="g.color" class="absolute inset-0 opacity-0 cursor-pointer w-full h-full" />
                <div class="w-6 h-6 rounded-full" :style="{ backgroundColor: g.color }" />
              </div>

              <!-- Read Mode Name -->
              <span v-if="!isEditMode || g.id === 'unassigned'" class="text-sm font-medium text-text truncate">
                {{ g.name }}
              </span>

              <!-- Edit Mode Name Input -->
              <input
                v-else
                v-model="g.name"
                class="apple-input py-0.5 px-2 text-xs h-7 w-full rounded-md font-medium"
                @click.stop
                placeholder="名称"
              />
            </div>

            <!-- Actions -->
            <div class="flex items-center gap-1.5 flex-shrink-0 ml-2">
              <span v-if="!isEditMode || g.id === 'unassigned'" class="text-[10px] bg-bg px-1.5 py-0.5 rounded text-muted font-mono">
                {{ g.apiKeyIds.length }} key
              </span>

              <button
                v-else
                class="text-error hover:bg-error/10 p-1 rounded-full transition-colors focus:outline-none"
                @click.stop="removeGroup(g.id)"
                title="删除分组"
              >
                <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                </svg>
              </button>
            </div>
          </li>

          <li v-if="!virtualGroupsWithUnassigned.length" class="text-muted text-sm py-8 text-center">
            无虚拟分组
          </li>
        </ul>

        <!-- Add Group button in Edit Mode -->
        <button
          v-if="isEditMode"
          class="w-full flex items-center justify-center gap-1 py-1.5 mt-3 border border-dashed border-border rounded-xl text-xs text-muted hover:text-text hover:bg-surface-hover transition-all font-semibold animate-fade-in"
          @click="addGroup"
        >
          + 新增虚拟分组
        </button>
        <button
          v-if="isEditMode && config.groups.length >= 2"
          class="w-full flex items-center justify-center gap-1 py-1.5 mt-2 border border-dashed border-border rounded-xl text-xs text-muted hover:text-text hover:bg-surface-hover transition-all font-semibold animate-fade-in"
          title="按分组数量在色环上均匀取色，覆盖现有颜色"
          @click="autoRecolor"
        >
          🎨 一键配色（拉开色差）
        </button>
      </div>

      <!-- Description / Note Editing Card -->
      <div v-if="selectedGroup" class="apple-card p-4 text-xs text-muted leading-relaxed">
        <div class="flex items-center justify-between mb-1">
          <span class="font-semibold text-text">分组说明</span>
          <button
            v-if="selectedGroup.id !== 'unassigned'"
            class="text-[10px] text-accent hover:text-accent-focus font-medium focus:outline-none"
            @click="isEditingNote = !isEditingNote"
          >
            {{ isEditingNote ? '保存' : '修改说明' }}
          </button>
        </div>
        <div v-if="isEditingNote && selectedGroup.id !== 'unassigned'" class="mt-1.5">
          <textarea
            v-model="selectedGroup.note"
            class="apple-input w-full text-xs p-2 min-h-[60px] rounded-lg focus:ring-1 focus:ring-accent-dim bg-bg border border-border"
            placeholder="备注用途，例如: 自定义人名分组"
          />
        </div>
        <div v-else class="mt-1 text-xs">
          {{ selectedGroup.note || '暂无说明' }}
        </div>
      </div>
    </aside>

    <!-- Right panel: detailed statistical breakdown (9-col) -->
    <section class="col-span-12 lg:col-span-9 space-y-6">
      <div v-if="error" class="flex items-center gap-3 rounded-xl border border-error/20 bg-error/10 px-5 py-4 text-error">
        <svg class="h-5 w-5 text-error" viewBox="0 0 20 20" fill="currentColor">
          <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
        </svg>
        <span class="text-sm font-medium">数据加载错误: {{ error }}</span>
      </div>

      <!-- Detail skeleton loader -->
      <div v-if="loading || !data" class="space-y-6">
        <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
          <div v-for="i in 4" :key="i" class="h-24 animate-pulse rounded-2xl bg-surface border border-border"></div>
        </div>
        <div class="h-80 animate-pulse rounded-2xl bg-surface border border-border"></div>
      </div>

      <!-- Loaded Details view -->
      <div v-else class="space-y-6">
        <!-- Group header + poster export -->
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2.5">
            <div
              class="w-7 h-7 rounded-full flex items-center justify-center text-white text-xs font-bold select-none"
              :style="{ backgroundColor: selectedGroup?.color }"
            >
              {{ selectedGroup?.name ? selectedGroup.name.charAt(0).toUpperCase() : 'G' }}
            </div>
            <h2 class="text-lg font-bold text-text">{{ selectedGroup?.name }} 的战报</h2>
            <span class="text-[11px] text-muted font-mono">{{ range.apiDates.start_date }} ~ {{ range.apiDates.end_date }}</span>
          </div>
          <button class="btn-secondary text-xs px-4 h-8 font-semibold" :disabled="exporting" @click="exportPoster">
            {{ exporting ? '生成中…' : '📸 导出战报图' }}
          </button>
        </div>

        <!-- 4 KPI Cards -->
        <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
          <div class="apple-card flex flex-col justify-between">
            <div class="text-xs font-semibold uppercase tracking-wider text-muted">本期请求数</div>
            <div class="mt-2 text-2xl font-bold tracking-tight text-text">{{ formatNumber(data.usage.requests) }}</div>
            <div v-if="reqComparison" class="mt-1 text-[11px] font-medium" :class="reqComparison.isUp ? 'text-error' : 'text-success'">
              较上周期 {{ reqComparison.isUp ? '↑' : '↓' }} {{ reqComparison.value }}
            </div>
          </div>
          <div class="apple-card flex flex-col justify-between">
            <div class="text-xs font-semibold uppercase tracking-wider text-muted">消耗 Token</div>
            <div class="mt-2 text-2xl font-bold tracking-tight text-text">{{ formatTokens(data.usage.total_tokens) }}</div>
            <div v-if="tokenComparison" class="mt-1 text-[11px] font-medium" :class="tokenComparison.isUp ? 'text-error' : 'text-success'">
              较上周期 {{ tokenComparison.isUp ? '↑' : '↓' }} {{ tokenComparison.value }}
            </div>
          </div>
          <div class="apple-card flex flex-col justify-between">
            <div class="text-xs font-semibold uppercase tracking-wider text-muted">产生费用</div>
            <div class="mt-2 text-2xl font-bold tracking-tight text-text">
              {{ formatCost(config.costSource === 'actual' ? data.usage.actual_cost : data.usage.total_cost, config.currency) }}
            </div>
            <div v-if="costComparison" class="mt-1 text-[11px] font-medium" :class="costComparison.isUp ? 'text-error' : 'text-success'">
              较上周期 {{ costComparison.isUp ? '↑' : '↓' }} {{ costComparison.value }}
            </div>
          </div>
          <div class="apple-card flex flex-col justify-between">
            <div class="text-xs font-semibold uppercase tracking-wider text-muted">涉及模型数</div>
            <div class="mt-2 text-2xl font-bold tracking-tight text-text">{{ groupModels.length }}</div>
          </div>
        </div>

        <!-- Selected Group Trend Area Chart -->
        <div class="apple-card">
          <h2 class="mb-4 text-base font-semibold text-text">团队消耗趋势 ({{ selectedGroup?.name }})</h2>
          <div class="h-80 w-full relative">
            <v-chart class="h-full w-full" :option="selectedGroupTrendOption" autocall />
          </div>
        </div>

        <!-- Work rhythm summary + activity distribution -->
        <div class="apple-card">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="text-base font-semibold text-text">工作节奏与时段分布</h2>
            <span class="text-[11px] text-muted">统计时区：{{ config.timezone }}</span>
          </div>

          <div v-if="loadingHourly" class="space-y-4">
            <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
              <div v-for="i in 4" :key="i" class="h-16 animate-pulse rounded-xl bg-bg border border-border"></div>
            </div>
            <div class="h-56 animate-pulse rounded-xl bg-bg border border-border"></div>
          </div>

          <div v-else-if="workRhythm" class="space-y-6">
            <!-- Rhythm summary chips -->
            <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
              <div class="rounded-xl border border-border bg-bg/50 p-3">
                <div class="text-[10px] text-muted uppercase tracking-wider">日均开工时段</div>
                <div class="mt-1 text-lg font-bold text-text font-mono">{{ hourToHHmm(workRhythm.avgFirst) }}</div>
                <div class="text-[10px] text-muted mt-0.5">最早 {{ String(workRhythm.earliestFirst).padStart(2, '0') }}:00 时段</div>
              </div>
              <div class="rounded-xl border border-border bg-bg/50 p-3">
                <div class="text-[10px] text-muted uppercase tracking-wider">日均收工时段</div>
                <div class="mt-1 text-lg font-bold text-text font-mono">{{ hourToHHmm(workRhythm.avgLast) }}</div>
                <div class="text-[10px] text-muted mt-0.5">最晚 {{ String(workRhythm.latestLast).padStart(2, '0') }}:00 时段</div>
              </div>
              <div class="rounded-xl border border-border bg-bg/50 p-3">
                <div class="text-[10px] text-muted uppercase tracking-wider">最活跃时段</div>
                <div class="mt-1 text-lg font-bold text-text font-mono">{{ String(workRhythm.peakHour).padStart(2, '0') }}:00 - {{ String((workRhythm.peakHour + 1) % 24).padStart(2, '0') }}:00</div>
                <div class="text-[10px] text-muted mt-0.5">本期请求最集中</div>
              </div>
              <div class="rounded-xl border border-border bg-bg/50 p-3">
                <div class="text-[10px] text-muted uppercase tracking-wider">活跃天数</div>
                <div class="mt-1 text-lg font-bold text-text font-mono">{{ workRhythm.activeDays }} / {{ workRhythm.totalDays }} 天</div>
                <div class="text-[10px] text-muted mt-0.5">本期有请求的天数</div>
              </div>
            </div>

            <!-- Two distribution charts -->
            <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
              <div>
                <div class="mb-2 text-xs font-semibold text-muted">每日请求频率</div>
                <div class="h-56 w-full">
                  <v-chart class="h-full w-full" :option="dailyFreqOption" autocall />
                </div>
              </div>
              <div>
                <div class="mb-2 text-xs font-semibold text-muted">24 小时时段分布（几点在干活）</div>
                <div class="h-56 w-full">
                  <v-chart class="h-full w-full" :option="hourlyDistOption" autocall />
                </div>
              </div>
            </div>
          </div>

          <div v-else class="py-10 text-center text-sm text-muted">本期无请求数据</div>
        </div>

        <!-- Year activity calendar (GitHub contribution style) -->
        <div class="apple-card">
          <div class="mb-2 flex items-center justify-between">
            <h2 class="text-base font-semibold text-text">活跃日历（近 12 个月）</h2>
            <span class="text-[11px] text-muted">
              {{ yearActiveDays ? `共 ${yearActiveDays} 个活跃日` : '' }} · 颜色深浅 = 当日请求数
            </span>
          </div>
          <div v-if="loadingYear" class="h-[190px] animate-pulse rounded-xl bg-bg border border-border"></div>
          <div v-else-if="yearDaily.length" class="h-[190px] w-full overflow-x-auto">
            <div class="h-full min-w-[760px]">
              <v-chart class="h-full w-full" :option="calendarOption" autocall />
            </div>
          </div>
          <div v-else class="py-10 text-center text-sm text-muted">暂无历史数据</div>
        </div>

        <!-- Tool preference + cache savings -->
        <div class="grid grid-cols-12 gap-6">
          <!-- UA tool pie -->
          <div class="apple-card col-span-12 lg:col-span-7">
            <div class="mb-2 flex items-center justify-between">
              <h2 class="text-base font-semibold text-text">工具偏好（这人用什么干活）</h2>
              <span class="text-[11px] text-muted" v-if="ipSampleCount">基于 {{ formatNumber(ipSampleCount) }} 条采样</span>
            </div>
            <div v-if="loadingIp" class="h-64 animate-pulse rounded-xl bg-bg border border-border"></div>
            <div v-else-if="uaStats.length" class="flex flex-col md:flex-row items-center gap-4">
              <div class="h-64 w-full md:w-1/2">
                <v-chart class="h-full w-full" :option="uaPieOption" autocall />
              </div>
              <div class="w-full md:w-1/2 space-y-1.5 max-h-64 overflow-y-auto pr-1">
                <div
                  v-for="(u, idx) in uaStats.slice(0, 8)"
                  :key="u.name"
                  class="flex items-center justify-between rounded-lg border border-border bg-bg/50 px-3 py-2"
                >
                  <div class="flex items-center gap-2 min-w-0">
                    <span class="text-[10px] font-bold text-muted w-4 flex-shrink-0">{{ idx + 1 }}</span>
                    <span class="text-xs font-semibold text-text truncate">{{ u.name }}</span>
                  </div>
                  <div class="text-right flex-shrink-0 ml-3">
                    <span class="text-xs font-bold font-mono text-text">{{ formatNumber(u.requests) }}</span>
                    <span class="text-[10px] text-muted ml-1.5">{{ ((u.requests / Math.max(ipSampleCount, 1)) * 100).toFixed(1) }}%</span>
                  </div>
                </div>
              </div>
            </div>
            <div v-else class="py-10 text-center text-sm text-muted">本期采样中没有 user_agent 记录</div>
          </div>

          <!-- Cache savings easter egg -->
          <div class="apple-card col-span-12 lg:col-span-5 flex flex-col justify-between">
            <div class="flex items-center justify-between">
              <h2 class="text-base font-semibold text-text">📦 缓存省钱彩蛋</h2>
              <span class="text-[11px] text-muted">命中率 {{ cacheSavings ? (cacheSavings.hitRate * 100).toFixed(1) + '%' : '—' }}</span>
            </div>
            <div v-if="loadingIp" class="h-24 animate-pulse rounded-xl bg-bg border border-border my-4"></div>
            <template v-else-if="cacheSavings">
              <div class="my-4 rounded-2xl border-2 border-success/30 bg-success/5 p-5 text-center">
                <div class="text-[11px] text-muted uppercase tracking-wider">本期缓存为你省下约</div>
                <div class="mt-1 text-3xl font-bold tracking-tight text-success">
                  {{ formatCost(cacheSavings.saved, config.currency) }}
                </div>
                <div class="mt-2 text-xs text-muted">
                  ≈ <b class="text-text">{{ Math.max(1, Math.round(cacheSavings.coffee)) }}</b> 杯瑞幸
                  <template v-if="cacheSavings.hotpot >= 1">
                    ，或 <b class="text-text">{{ Math.round(cacheSavings.hotpot) }}</b> 顿海底捞
                  </template>
                </div>
              </div>
              <div class="text-[10px] text-muted leading-relaxed">
                口径：缓存读 token 若按输入价计费的差额，单价由本期采样日志反推{{ '；换算按 1 USD ≈ ¥7.2' }}，瑞幸 ¥9.9/杯、海底捞 ¥110/顿，仅供一乐。
              </div>
            </template>
            <div v-else class="py-10 text-center text-sm text-muted flex-1 flex items-center justify-center">
              本期没有缓存命中，省钱从开启 prompt cache 开始
            </div>
          </div>
        </div>

        <!-- Request IP geo map -->
        <div class="apple-card">
          <div class="mb-4 flex flex-col md:flex-row md:items-center justify-between gap-2">
            <h2 class="text-base font-semibold text-text">请求 IP 分布地图</h2>
            <span class="text-[11px] text-muted">
              <template v-if="ipSampleCount">
                {{ ipSampleIsFull ? `已统计本期全部 ${formatNumber(ipSampleCount)} 条请求` : `基于最近 ${formatNumber(ipSampleCount)} 条请求采样（本期共 ${formatNumber(ipTotalCount)} 条）` }}
                · 归属地由 geojs.io 解析
              </template>
            </span>
          </div>

          <div v-if="loadingIp" class="grid grid-cols-12 gap-6">
            <div class="col-span-12 lg:col-span-8 h-96 animate-pulse rounded-xl bg-bg border border-border"></div>
            <div class="col-span-12 lg:col-span-4 h-96 animate-pulse rounded-xl bg-bg border border-border"></div>
          </div>

          <div v-else-if="ipStats.length" class="grid grid-cols-12 gap-6">
            <!-- Map -->
            <div class="col-span-12 lg:col-span-8">
              <div class="h-96 w-full relative rounded-xl overflow-hidden">
                <v-chart v-if="worldMapReady" class="h-full w-full" :option="ipMapOption" autocall />
                <div v-else class="absolute inset-0 flex items-center justify-center text-sm text-muted">
                  地图底图加载失败，仅显示右侧列表
                </div>
              </div>
              <div class="mt-2 text-[10px] text-muted">滚轮缩放 / 拖拽平移；点大小代表请求数占比</div>
            </div>

            <!-- Top IP list -->
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

          <div v-else class="py-10 text-center text-sm text-muted">
            本期采样请求中没有 IP 记录
          </div>
        </div>

        <!-- 4 Fun Cards -->
        <div class="grid grid-cols-2 gap-4 md:grid-cols-4">
          <!-- Fun 1: Earliest -->
          <div class="apple-card flex flex-col justify-between min-h-[110px]">
            <div class="flex items-center justify-between">
              <span class="text-xs font-semibold uppercase tracking-wider text-muted">🌅 最早开工</span>
              <span class="text-xs text-success">晨光</span>
            </div>
            <div v-if="loadingFun" class="h-6 animate-pulse rounded bg-border w-2/3 my-2"></div>
            <div v-else-if="groupFunStats.earliest" class="mt-2">
              <div class="text-lg font-bold text-text">{{ formatHHmm(groupFunStats.earliest.created_at, config.timezone) }}</div>
              <div class="text-[10px] text-muted mt-1 truncate">{{ localDate(groupFunStats.earliest.created_at, config.timezone).slice(5) }} · 密钥: {{ groupFunStats.earliest.api_key_id }}</div>
            </div>
            <div v-else class="text-sm text-muted mt-2">暂无数据</div>
          </div>

          <!-- Fun 2: Latest -->
          <div class="apple-card flex flex-col justify-between min-h-[110px]">
            <div class="flex items-center justify-between">
              <span class="text-xs font-semibold uppercase tracking-wider text-muted">🦉 最晚收工</span>
              <span class="text-xs text-warning">暗夜</span>
            </div>
            <div v-if="loadingFun" class="h-6 animate-pulse rounded bg-border w-2/3 my-2"></div>
            <div v-else-if="groupFunStats.latest" class="mt-2">
              <div class="text-lg font-bold text-text">{{ formatHHmm(groupFunStats.latest.created_at, config.timezone) }}</div>
              <div class="text-[10px] text-muted mt-1 truncate">{{ localDate(groupFunStats.latest.created_at, config.timezone).slice(5) }} · 密钥: {{ groupFunStats.latest.api_key_id }}</div>
            </div>
            <div v-else class="text-sm text-muted mt-2">暂无数据</div>
          </div>

          <!-- Fun 3: Longest Duration -->
          <div class="apple-card flex flex-col justify-between min-h-[110px]">
            <div class="flex items-center justify-between">
              <span class="text-xs font-semibold uppercase tracking-wider text-muted">⏱️ 最长耗时</span>
              <span class="text-xs text-purple-400">持久</span>
            </div>
            <div v-if="loadingFun" class="h-6 animate-pulse rounded bg-border w-2/3 my-2"></div>
            <div v-else-if="groupFunStats.longest" class="mt-2">
              <div class="text-lg font-bold text-text">{{ formatDuration(groupFunStats.longest.duration_ms) }}</div>
              <div class="text-[10px] text-muted mt-1 truncate">{{ groupFunStats.longest.model }}</div>
            </div>
            <div v-else class="text-sm text-muted mt-2">暂无数据</div>
          </div>

          <!-- Fun 4: Costliest -->
          <div class="apple-card flex flex-col justify-between min-h-[110px]">
            <div class="flex items-center justify-between">
              <span class="text-xs font-semibold uppercase tracking-wider text-muted">💸 最贵请求</span>
              <span class="text-xs text-error">暴敛</span>
            </div>
            <div v-if="loadingFun" class="h-6 animate-pulse rounded bg-border w-2/3 my-2"></div>
            <div v-else-if="groupFunStats.costliest" class="mt-2">
              <div class="text-lg font-bold text-text">
                {{ formatCost(config.costSource === 'actual' ? groupFunStats.costliest.actual_cost : groupFunStats.costliest.total_cost, config.currency) }}
              </div>
              <div class="text-[10px] text-muted mt-1 truncate">{{ groupFunStats.costliest.model }}</div>
            </div>
            <div v-else class="text-sm text-muted mt-2">暂无数据</div>
          </div>
        </div>

        <!-- Key detail table -->
        <div class="apple-card overflow-hidden">
          <div class="flex items-center justify-between mb-4 pb-2 border-b border-border/40">
            <h2 class="text-base font-semibold text-text">密钥消耗明细</h2>
            <button
              v-if="selectedGroup && selectedGroup.id !== 'unassigned'"
              class="btn-secondary text-xs px-4 h-8 font-semibold"
              @click="openKeySelector(selectedGroup)"
            >
              绑定密钥 (已选 {{ selectedGroup.apiKeyIds.length }} 个)
            </button>
          </div>
          <div class="overflow-x-auto">
            <table class="apple-table">
              <thead>
                <tr>
                  <th class="text-left">密钥 ID / 备注名</th>
                  <th>状态</th>
                  <th class="text-right">额度用量</th>
                  <th class="text-right">本期请求</th>
                  <th class="text-right">本期 Token</th>
                  <th class="text-right">本期费用</th>
                  <th class="text-right">最后使用时间</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="k in keyDetailsList" :key="k.id">
                  <td class="text-left font-medium min-w-[150px]">
                    <div class="text-text font-semibold">{{ k.name }}</div>
                    <div class="text-[11px] text-muted font-mono">ID: {{ k.id }}</div>
                  </td>
                  <td class="text-center">
                    <span
                      class="text-[11px] font-semibold px-2 py-0.5 rounded-full inline-block"
                      :class="k.status === 'active' ? 'bg-success/10 text-success' : 'bg-error/10 text-error'"
                    >
                      {{ k.status === 'active' ? '活跃' : '禁用' }}
                    </span>
                  </td>
                  <td class="text-right min-w-[150px]">
                    <div class="flex flex-col items-end gap-1">
                      <span class="text-xs text-muted">{{ formatTokens(k.quota_used) }} / {{ formatTokens(k.quota) }}</span>
                      <div class="h-1.5 w-24 bg-bg border border-border rounded-full overflow-hidden">
                        <div
                          class="h-full bg-accent transition-all duration-300"
                          :style="{ width: `${Math.min(100, k.quota > 0 ? (k.quota_used / k.quota) * 100 : 0)}%` }"
                        />
                      </div>
                    </div>
                  </td>
                  <td class="text-right font-mono">{{ formatNumber(k.requests) }}</td>
                  <td class="text-right font-mono">{{ formatTokens(k.total_tokens) }}</td>
                  <td class="text-right font-bold font-mono">
                    {{ formatCost(config.costSource === 'actual' ? k.actual_cost : k.total_cost, config.currency) }}
                  </td>
                  <td class="text-right text-xs text-muted">
                    {{ k.last_used_at ? formatTime(k.last_used_at, config.timezone).slice(5, 16) : '—' }}
                  </td>
                  <td class="text-center">
                    <button class="btn-secondary py-1 px-2.5 text-xs h-7" @click="filterByKey(k.id)">
                      查看日志
                    </button>
                  </td>
                </tr>
                <tr v-if="!keyDetailsList.length">
                  <td colspan="8" class="text-center py-8 text-muted text-sm">
                    此分组目前不含任何 API 密钥。
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </section>

    <!-- Off-screen poster node for html2canvas export（配色内联，独立于主题） -->
    <div class="fixed" style="left: -9999px; top: 0" aria-hidden="true">
      <div
        ref="posterRef"
        style="width: 420px; background: linear-gradient(165deg, #191929 0%, #0d0d17 100%); color: #ffffff; padding: 30px 28px; font-family: inherit; border-radius: 0"
      >
        <!-- Header -->
        <div style="display: flex; align-items: center; gap: 12px">
          <div
            :style="{
              width: '44px', height: '44px', borderRadius: '9999px', flexShrink: 0,
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              fontSize: '18px', fontWeight: 700, color: '#fff',
              backgroundColor: selectedGroup?.color || '#0066cc'
            }"
          >
            {{ selectedGroup?.name ? selectedGroup.name.charAt(0).toUpperCase() : 'G' }}
          </div>
          <div>
            <div style="font-size: 20px; font-weight: 800; line-height: 1.2">{{ selectedGroup?.name }} 的消耗战报</div>
            <div style="font-size: 11px; color: rgba(255,255,255,0.5); margin-top: 3px; font-family: monospace">
              {{ range.apiDates.start_date }} ~ {{ range.apiDates.end_date }}
            </div>
          </div>
        </div>

        <div style="font-size: 12px; color: rgba(255,255,255,0.65); font-style: italic; margin: 14px 0 18px">
          “{{ posterTagline }}”
        </div>

        <!-- KPI row -->
        <div style="display: flex; gap: 10px">
          <div style="flex: 1; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 12px">
            <div style="font-size: 10px; color: rgba(255,255,255,0.5)">请求数</div>
            <div style="font-size: 18px; font-weight: 800; margin-top: 3px">{{ formatNumber(data?.usage.requests) }}</div>
            <div v-if="reqComparison" :style="{ fontSize: '10px', marginTop: '2px', color: reqComparison.isUp ? '#ff6b6b' : '#51cf66' }">
              {{ reqComparison.isUp ? '↑' : '↓' }} {{ reqComparison.value }}
            </div>
          </div>
          <div style="flex: 1; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 12px">
            <div style="font-size: 10px; color: rgba(255,255,255,0.5)">Token</div>
            <div style="font-size: 18px; font-weight: 800; margin-top: 3px">{{ formatTokens(data?.usage.total_tokens) }}</div>
            <div v-if="tokenComparison" :style="{ fontSize: '10px', marginTop: '2px', color: tokenComparison.isUp ? '#ff6b6b' : '#51cf66' }">
              {{ tokenComparison.isUp ? '↑' : '↓' }} {{ tokenComparison.value }}
            </div>
          </div>
          <div style="flex: 1; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 12px">
            <div style="font-size: 10px; color: rgba(255,255,255,0.5)">费用</div>
            <div style="font-size: 18px; font-weight: 800; margin-top: 3px">
              {{ formatCost(config.costSource === 'actual' ? data?.usage.actual_cost : data?.usage.total_cost, config.currency) }}
            </div>
            <div v-if="costComparison" :style="{ fontSize: '10px', marginTop: '2px', color: costComparison.isUp ? '#ff6b6b' : '#51cf66' }">
              {{ costComparison.isUp ? '↑' : '↓' }} {{ costComparison.value }}
            </div>
          </div>
        </div>

        <!-- Rhythm -->
        <div v-if="workRhythm" style="display: flex; gap: 10px; margin-top: 10px">
          <div style="flex: 1; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 10px 12px; text-align: center">
            <div style="font-size: 10px; color: rgba(255,255,255,0.5)">🌅 日均开工</div>
            <div style="font-size: 15px; font-weight: 700; margin-top: 2px; font-family: monospace">{{ hourToHHmm(workRhythm.avgFirst) }}</div>
          </div>
          <div style="flex: 1; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 10px 12px; text-align: center">
            <div style="font-size: 10px; color: rgba(255,255,255,0.5)">🦉 日均收工</div>
            <div style="font-size: 15px; font-weight: 700; margin-top: 2px; font-family: monospace">{{ hourToHHmm(workRhythm.avgLast) }}</div>
          </div>
          <div style="flex: 1; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 10px 12px; text-align: center">
            <div style="font-size: 10px; color: rgba(255,255,255,0.5)">🔥 最活跃</div>
            <div style="font-size: 15px; font-weight: 700; margin-top: 2px; font-family: monospace">{{ String(workRhythm.peakHour).padStart(2, '0') }}:00</div>
          </div>
        </div>

        <!-- Top models -->
        <div v-if="posterTopModels.length" style="margin-top: 18px">
          <div style="font-size: 11px; color: rgba(255,255,255,0.5); margin-bottom: 8px">本期最爱模型</div>
          <div v-for="m in posterTopModels" :key="m.model" style="margin-bottom: 7px">
            <div style="display: flex; justify-content: space-between; font-size: 11px; margin-bottom: 3px">
              <span style="font-family: monospace">{{ m.model }}</span>
              <span style="color: rgba(255,255,255,0.55)">{{ formatTokens(m.tokens) }} · {{ m.pct.toFixed(0) }}%</span>
            </div>
            <div style="height: 5px; background: rgba(255,255,255,0.08); border-radius: 999px; overflow: hidden">
              <div :style="{ height: '100%', width: m.pct + '%', backgroundColor: selectedGroup?.color || '#0066cc', borderRadius: '999px' }"></div>
            </div>
          </div>
        </div>

        <!-- Misc facts -->
        <div style="margin-top: 16px; background: rgba(255,255,255,0.06); border-radius: 14px; padding: 12px; font-size: 11px; line-height: 1.9">
          <div v-if="uaStats.length">🛠️ 惯用兵器：<b>{{ uaStats[0].name }}</b>（{{ ((uaStats[0].requests / Math.max(ipSampleCount, 1)) * 100).toFixed(0) }}% 请求）</div>
          <div v-if="ipTopRows.length && ipTopRows[0].geo && !ipTopRows[0].geo.isPrivate">📍 常驻据点：<b>{{ ipTopRows[0].geo.label }}</b></div>
          <div v-if="cacheSavings">📦 缓存省下 <b style="color: #51cf66">{{ formatCost(cacheSavings.saved, config.currency) }}</b> ≈ {{ Math.max(1, Math.round(cacheSavings.coffee)) }} 杯瑞幸</div>
          <div v-if="workRhythm">📅 本期活跃 <b>{{ workRhythm.activeDays }}</b> / {{ workRhythm.totalDays }} 天</div>
        </div>

        <!-- Footer -->
        <div style="margin-top: 18px; display: flex; justify-content: space-between; align-items: center; font-size: 10px; color: rgba(255,255,255,0.35)">
          <span>sub2api 团队消耗看板</span>
          <span style="font-family: monospace">{{ range.apiDates.end_date }} 生成</span>
        </div>
      </div>
    </div>

    <!-- Custom Popover Key Selector Modal -->
    <div v-if="showKeySelector && editingGroup" class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4">
      <div class="bg-surface rounded-3xl w-full max-w-[500px] flex flex-col justify-between max-h-[85vh] p-6 shadow-2xl border border-border">
        <div>
          <div class="flex items-center justify-between border-b border-border pb-3 mb-4">
            <h3 class="text-base font-bold text-text">选择 API 密钥 - {{ editingGroup.name }}</h3>
            <button
              class="h-8 w-8 rounded-full flex items-center justify-center text-muted hover:text-text hover:bg-surface-hover transition-all text-xl cursor-pointer"
              @click="showKeySelector = false"
            >
              ×
            </button>
          </div>

          <!-- Search key bar -->
          <div class="mb-4">
            <input
              v-model="keySearchQuery"
              placeholder="搜索密钥名称或 ID..."
              class="apple-input w-full text-xs h-9 rounded-full px-4"
            />
          </div>

          <!-- Keys list checkboxes -->
          <div class="overflow-y-auto max-h-[40vh] space-y-2 pr-1 text-xs">
            <div
              v-for="k in filteredApiKeys"
              :key="k.id"
              class="flex items-center justify-between p-3 rounded-xl border border-border bg-bg/40 hover:bg-surface-hover cursor-pointer transition-all"
              @click="toggleKeySelection(k.id)"
            >
              <div class="flex items-center gap-3">
                <input
                  type="checkbox"
                  :checked="isKeySelected(k.id)"
                  class="h-4 w-4 rounded border-border text-accent focus:ring-accent"
                  @click.stop="toggleKeySelection(k.id)"
                />
                <div>
                  <span class="font-semibold text-text">{{ k.name }}</span>
                  <span class="text-[10px] text-muted font-mono ml-2 bg-bg px-1.5 py-0.5 rounded border border-border">ID: {{ k.id }}</span>
                </div>
              </div>
              <span
                class="text-[10px] font-semibold px-2 py-0.5 rounded-full inline-block"
                :class="k.status === 'active' ? 'bg-success/15 text-success' : 'bg-error/15 text-error'"
              >
                {{ k.status === 'active' ? '活跃' : '禁用' }}
              </span>
            </div>

            <div v-if="!filteredApiKeys.length" class="text-center text-muted py-8">
              未搜寻到可用 API 密钥，请检查连接状态或过滤词。
            </div>
          </div>
        </div>

        <div class="flex items-center justify-between border-t border-border pt-4 mt-6">
          <span class="text-[10px] text-muted font-medium">已选：{{ editingGroup.apiKeyIds.length }} 个密钥</span>
          <button class="btn-primary text-xs px-5 h-9 font-medium" @click="showKeySelector = false">
            确认
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Key page custom style adjustments */
</style>
