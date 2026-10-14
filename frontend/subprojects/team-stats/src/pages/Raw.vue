<script setup lang="ts">
import { ref, watch, onMounted, computed } from 'vue';
import { useRoute } from 'vue-router';
import { endpoints } from '@/api/endpoints';
import { useRangeStore } from '@/stores/range';
import { useConfigStore } from '@/stores/config';
import { formatCost, formatNumber, formatTime } from '@/utils/format';
import type { UsageLog } from '@/api/types';

const route = useRoute();
const range = useRangeStore();
const config = useConfigStore();

const items = ref<UsageLog[]>([]);
const total = ref(0);
const page = ref(1);
const pageSize = ref(50);
const loading = ref(false);
const err = ref<string | null>(null);

// Filters
const selectedGroupId = ref<string>('all');
const apiKeyId = ref<number | undefined>(undefined);
const model = ref<string>('');
const streamFilter = ref<'all' | 'stream' | 'non_stream'>('all');
const minCost = ref<number>(0);
const minDuration = ref<number>(0);

const sortBy = ref<'created_at' | 'duration_ms' | 'total_cost' | 'total_tokens'>('created_at');
const sortOrder = ref<'asc' | 'desc'>('desc');

const selectedGroup = computed(() => {
  if (selectedGroupId.value === 'all') return null;
  return config.groups.find(g => g.id === selectedGroupId.value) || null;
});

// Group details warning
const groupTooLarge = computed(() => {
  return selectedGroup.value && selectedGroup.value.apiKeyIds.length > 20;
});

async function load() {
  loading.value = true;
  err.value = null;
  try {
    // If filtering by virtual group
    if (selectedGroup.value) {
      const g = selectedGroup.value;
      if (g.apiKeyIds.length === 0) {
        items.value = [];
        total.value = 0;
        return;
      }
      if (g.apiKeyIds.length > 20) {
        // Warning: too many keys, do not load
        items.value = [];
        total.value = 0;
        return;
      }

      // Concurrently fetch logs for each key in group (up to 200 logs per key)
      const logsList = await Promise.all(
        g.apiKeyIds.map(async (id) => {
          try {
            const res = await endpoints.usageList({
              ...range.apiDates,
              timezone: config.timezone,
              api_key_id: id,
              model: model.value || undefined,
              stream: streamFilter.value !== 'all' ? streamFilter.value === 'stream' : undefined,
              page: 1,
              page_size: 200
            });
            return res.items;
          } catch {
            return [];
          }
        })
      );

      let merged = logsList.flat();

      // Client-side cost and duration thresholds
      if (minCost.value > 0) {
        merged = merged.filter(l => (config.costSource === 'actual' ? l.actual_cost : l.total_cost) >= minCost.value);
      }
      if (minDuration.value > 0) {
        merged = merged.filter(l => (l.duration_ms || 0) >= minDuration.value);
      }

      // Client-side sorting
      merged.sort((a, b) => {
        let valA = 0;
        let valB = 0;
        if (sortBy.value === 'created_at') {
          valA = new Date(a.created_at).getTime();
          valB = new Date(b.created_at).getTime();
        } else if (sortBy.value === 'total_cost') {
          valA = config.costSource === 'actual' ? a.actual_cost : a.total_cost;
          valB = config.costSource === 'actual' ? b.actual_cost : b.total_cost;
        } else if (sortBy.value === 'duration_ms') {
          valA = a.duration_ms || 0;
          valB = b.duration_ms || 0;
        } else if (sortBy.value === 'total_tokens') {
          valA = a.input_tokens + a.output_tokens + (a.cache_creation_tokens || 0) + (a.cache_read_tokens || 0);
          valB = b.input_tokens + b.output_tokens + (b.cache_creation_tokens || 0) + (b.cache_read_tokens || 0);
        }
        return sortOrder.value === 'asc' ? valA - valB : valB - valA;
      });

      // Client-side pagination
      total.value = merged.length;
      items.value = merged.slice((page.value - 1) * pageSize.value, page.value * pageSize.value);
    } else {
      // Server-side fetching (normal)
      const res = await endpoints.usageList({
        ...range.apiDates,
        timezone: config.timezone,
        page: page.value,
        page_size: pageSize.value,
        sort_by: sortBy.value,
        sort_order: sortOrder.value,
        api_key_id: apiKeyId.value || undefined,
        model: model.value || undefined,
        stream: streamFilter.value !== 'all' ? streamFilter.value === 'stream' : undefined,
      });

      let filtered = res.items;

      // Client-side cost and duration filters for server response
      if (minCost.value > 0) {
        filtered = filtered.filter(l => (config.costSource === 'actual' ? l.actual_cost : l.total_cost) >= minCost.value);
      }
      if (minDuration.value > 0) {
        filtered = filtered.filter(l => (l.duration_ms || 0) >= minDuration.value);
      }

      items.value = filtered;
      total.value = res.total;
    }
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}

// Map key ID to virtual group name
function getGroupName(keyId: number): string {
  const g = config.groups.find(x => x.apiKeyIds.includes(keyId));
  return g ? g.name : '未分组';
}

function getKeyName(keyId: number): string {
  const k = config.allApiKeys.find(x => x.id === keyId);
  return k ? k.name : `key-${keyId}`;
}

// Watch query parameters for redirections
onMounted(() => {
  if (route.query.key_id) {
    apiKeyId.value = Number(route.query.key_id);
    selectedGroupId.value = 'all';
  }
});

// Watch parameters to reload logs
watch(
  [() => range.apiDates, page, pageSize, sortBy, sortOrder, selectedGroupId, apiKeyId, model, streamFilter, minCost, minDuration],
  () => {
    void load();
  },
  { immediate: true, deep: true }
);

// Watch group change to reset page
watch(selectedGroupId, () => {
  page.value = 1;
});
</script>

<template>
  <div class="space-y-6 max-w-[1440px] mx-auto pb-12">
    <div>
      <h1 class="text-xl font-bold tracking-tight text-text">原始用量明细</h1>
      <p class="text-xs text-muted mt-1">查看及过滤团队 API Key 的单次请求日志明细</p>
    </div>

    <!-- Advanced Filter Bar Card -->
    <div class="apple-card p-5 space-y-4">
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-4 lg:grid-cols-6">
        <!-- Virtual Group Filter -->
        <label class="block">
          <span class="text-xs font-semibold text-muted uppercase tracking-wider">虚拟分组</span>
          <select v-model="selectedGroupId" class="apple-select mt-1 w-full text-xs h-9">
            <option value="all">所有分组</option>
            <option v-for="g in config.groups" :key="g.id" :value="g.id">{{ g.name }}</option>
          </select>
        </label>

        <!-- API Key ID filter (disabled if virtual group is selected) -->
        <label class="block">
          <span class="text-xs font-semibold text-muted uppercase tracking-wider">API Key ID</span>
          <input
            v-model.number="apiKeyId"
            type="number"
            placeholder="输入密钥 ID"
            class="apple-input mt-1 w-full text-xs h-9 disabled:opacity-50"
            :disabled="selectedGroupId !== 'all'"
          />
        </label>

        <!-- Model search filter -->
        <label class="block">
          <span class="text-xs font-semibold text-muted uppercase tracking-wider">模型名称</span>
          <input
            v-model="model"
            placeholder="搜索模型, 如 gpt-4o"
            class="apple-input mt-1 w-full text-xs h-9"
          />
        </label>

        <!-- Stream state filter -->
        <label class="block">
          <span class="text-xs font-semibold text-muted uppercase tracking-wider">流式传输</span>
          <select v-model="streamFilter" class="apple-select mt-1 w-full text-xs h-9">
            <option value="all">全部</option>
            <option value="stream">流式 (Stream)</option>
            <option value="non_stream">普通 (Non-Stream)</option>
          </select>
        </label>

        <!-- Minimum cost filter -->
        <label class="block">
          <span class="text-xs font-semibold text-muted uppercase tracking-wider">最低单次费用</span>
          <input
            v-model.number="minCost"
            type="number"
            step="0.001"
            placeholder="0.00"
            class="apple-input mt-1 w-full text-xs h-9"
          />
        </label>

        <!-- Minimum duration filter -->
        <label class="block">
          <span class="text-xs font-semibold text-muted uppercase tracking-wider">最低请求耗时 (ms)</span>
          <input
            v-model.number="minDuration"
            type="number"
            placeholder="0"
            class="apple-input mt-1 w-full text-xs h-9"
          />
        </label>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-4 pt-2 border-t border-border/60">
        <!-- Sort tools -->
        <div class="flex items-center gap-4 text-xs">
          <div class="flex items-center gap-2">
            <span class="text-muted">排序维度:</span>
            <select v-model="sortBy" class="apple-select py-1 px-2.5 h-7">
              <option value="created_at">请求时刻</option>
              <option value="duration_ms">请求耗时</option>
              <option value="total_cost">单次费用</option>
              <option value="total_tokens">单次 Token</option>
            </select>
          </div>

          <div class="flex items-center gap-2">
            <span class="text-muted">排序顺序:</span>
            <select v-model="sortOrder" class="apple-select py-1 px-2.5 h-7">
              <option value="desc">降序</option>
              <option value="asc">升序</option>
            </select>
          </div>
        </div>

        <div class="text-xs text-muted font-medium">
          本期符合条件共 <span class="text-text font-bold">{{ formatNumber(total) }}</span> 条记录
        </div>
      </div>
    </div>

    <!-- Warning if virtual group has too many keys -->
    <p v-if="selectedGroup && !groupTooLarge" class="text-xs text-muted">
      虚拟分组明细为样本：每个密钥最多读取 200 条。完整分页明细请按单个密钥筛选，或使用主后台“使用记录”。
    </p>
    <p v-if="minCost > 0 || minDuration > 0" class="text-xs text-muted">
      成本与耗时筛选仅作用于当前返回页或分组样本；总记录数以服务器统计为准。
    </p>
    <div v-if="groupTooLarge" class="flex items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-5 py-4 text-warning">
      <svg class="h-5 w-5 text-warning" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
      </svg>
      <span class="text-sm font-medium">当前所选虚拟分组 Key 数量超过 20，多路并发拉取受限，请改用单个 API Key ID 或模型进行过滤。</span>
    </div>

    <div v-else-if="err" class="flex items-center gap-3 rounded-xl border border-error/20 bg-error/10 px-5 py-4 text-error">
      <svg class="h-5 w-5 text-error" viewBox="0 0 20 20" fill="currentColor">
        <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
      </svg>
      <span class="text-sm font-medium">加载明细失败: {{ err }}</span>
    </div>

    <!-- Logs Table Card -->
    <div class="apple-card overflow-hidden">
      <div v-if="loading" class="space-y-4 py-8">
        <div v-for="i in 5" :key="i" class="h-10 animate-pulse bg-surface-hover rounded-lg w-full"></div>
      </div>
      <div v-else class="overflow-x-auto">
        <table class="apple-table">
          <thead>
            <tr>
              <th class="text-left">时间</th>
              <th class="text-left">所属分组</th>
              <th class="text-left">密钥 (Key) 名称 / ID</th>
              <th class="text-left">请求模型</th>
              <th class="text-right">Input</th>
              <th class="text-right">Output</th>
              <th class="text-right">Cache Read</th>
              <th class="text-right">单次费用</th>
              <th class="text-right">首字节 (ms)</th>
              <th class="text-right">耗时 (ms)</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="l in items" :key="l.id">
              <td class="text-left text-xs font-mono text-muted">
                {{ formatTime(l.created_at, config.timezone) }}
              </td>
              <td class="text-left">
                <span class="text-xs font-semibold text-text bg-bg border border-border px-2 py-0.5 rounded-full">
                  {{ getGroupName(l.api_key_id) }}
                </span>
              </td>
              <td class="text-left font-mono text-xs">
                <div class="text-text font-semibold">{{ getKeyName(l.api_key_id) }}</div>
                <div class="text-[10px] text-muted">ID: {{ l.api_key_id }}</div>
              </td>
              <td class="text-left text-xs font-mono font-medium">
                {{ l.model }}
                <span v-if="l.stream" class="ml-1 text-[9px] bg-accent-dim text-accent border border-accent/20 px-1.5 py-0.5 rounded">流</span>
              </td>
              <td class="text-right font-mono text-xs text-muted">{{ formatNumber(l.input_tokens) }}</td>
              <td class="text-right font-mono text-xs text-muted">{{ formatNumber(l.output_tokens) }}</td>
              <td class="text-right font-mono text-xs text-muted">{{ formatNumber(l.cache_read_tokens || 0) }}</td>
              <td class="text-right font-mono text-xs font-bold text-text">
                {{ formatCost(config.costSource === 'actual' ? l.actual_cost : l.total_cost, config.currency) }}
              </td>
              <td class="text-right font-mono text-xs text-muted">
                {{ l.first_token_ms ? `${l.first_token_ms} ms` : '—' }}
              </td>
              <td class="text-right font-mono text-xs font-medium text-text">
                {{ l.duration_ms ? `${l.duration_ms} ms` : '—' }}
              </td>
            </tr>
            <tr v-if="!items.length">
              <td colspan="10" class="text-center py-12 text-muted text-sm">
                未找到匹配的请求日志记录。
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Pagination Footer -->
      <div v-if="total > 0" class="flex items-center justify-between border-t border-border/80 pt-4 mt-4 text-xs">
        <div class="text-muted">
          显示第 {{ (page - 1) * pageSize + 1 }} - {{ Math.min(page * pageSize, total) }} 条，共 {{ formatNumber(total) }} 条
        </div>

        <div class="flex items-center gap-3">
          <!-- Page size picker -->
          <div class="flex items-center gap-1.5">
            <span class="text-muted">每页行数:</span>
            <select v-model.number="pageSize" class="apple-select py-0.5 px-2 h-7">
              <option :value="10">10</option>
              <option :value="20">20</option>
              <option :value="50">50</option>
              <option :value="100">100</option>
            </select>
          </div>

          <div class="flex items-center gap-1">
            <button
              class="btn-secondary py-1 px-3 text-xs h-7 disabled:opacity-50"
              :disabled="page <= 1"
              @click="page = 1"
            >
              首页
            </button>
            <button
              class="btn-secondary py-1 px-3 text-xs h-7 disabled:opacity-50"
              :disabled="page <= 1"
              @click="page--"
            >
              上一页
            </button>
            <span class="px-2 font-semibold text-text">第 {{ page }} 页 / 共 {{ Math.ceil(total / pageSize) }} 页</span>
            <button
              class="btn-secondary py-1 px-3 text-xs h-7 disabled:opacity-50"
              :disabled="page * pageSize >= total"
              @click="page++"
            >
              下一页
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Raw page styles */
</style>
