<script setup lang="ts">
import { computed, ref, onMounted } from 'vue';
import { RouterLink, RouterView, useRoute } from 'vue-router';
import { useConfigStore } from '@/stores/config';
import { useRangeStore, type RangePreset } from '@/stores/range';
import { useTheme } from '@/composables/useTheme';
import { clearApiCache, ApiError } from '@/api/client';
import { isMockMode } from '@/api/mock';
import { endpoints, type MeInfo } from '@/api/endpoints';

useTheme();

const route = useRoute();
const config = useConfigStore();
const range = useRangeStore();

const navs: { to: string; label: string }[] = [
  { to: '/overview', label: '总览' },
  { to: '/groups', label: '分组' },
  { to: '/ranking', label: '排行榜' },
  { to: '/fun', label: '趣味统计' },
  { to: '/raw', label: '明细' },
  { to: '/settings', label: '设置' },
];

const presets: { value: RangePreset; label: string }[] = [
  { value: 'today', label: '今天' },
  { value: 'last_2d', label: '近 2 日' },
  { value: 'last_7d', label: '近 7d' },
  { value: 'last_30d', label: '近 30d' },
  { value: 'custom', label: '自定义' },
];

const showRangeBar = computed(() => route.name !== 'settings' && route.name !== 'ranking');

function forceRefresh() {
  clearApiCache();
  range.requestRefresh();
}

function retry() { window.location.reload(); }

function toggleTheme() {
  config.theme = config.theme === 'dark' ? 'light' : 'dark';
}

const me = ref<MeInfo | null>(null);
const authError = ref<string>('');

onMounted(async () => {
  // 1) 校验 sub2api 登录态（必须 admin）
  try {
    me.value = await endpoints.me();
    config.setAuthed(true);
  } catch (e) {
    config.setAuthed(false);
    const err = e as ApiError;
    if (err.status === 401) {
      authError.value = '请先在 sub2api 主后台登录后再访问本页';
    } else if (err.status === 403) {
      authError.value = '当前账户不是管理员，无权访问';
    } else {
      authError.value = `身份校验失败：${err.message}`;
    }
    return;
  }
  // 2) 拉服务器端持久化的配置
  try { await config.loadFromBackend(); }
  catch (e) { authError.value = `配置读取失败：${(e as Error).message}。请刷新重试。`; return; }
  // 3) 预加载 api keys 用于配置面板下拉
  void config.loadAllApiKeys();
});
</script>

<template>
  <div class="flex h-full flex-col bg-bg text-text selection:bg-accent-dim selection:text-accent">
    <!-- Google Style Header (64px) -->
    <header class="sticky top-0 z-50 flex min-h-16 flex-wrap gap-3 py-3 items-center justify-between border-b border-border bg-surface px-6 select-none shadow-sm">
      <div class="flex items-center gap-3 h-full flex-wrap">
        <h1 class="text-base font-bold tracking-tight text-text select-none">团队消耗看板</h1>

        <!-- Navigation Tabs -->
        <nav class="flex items-center h-full overflow-x-auto">
          <RouterLink
            v-for="n in navs"
            :key="n.to"
            :to="n.to"
            class="nav-tab"
            :class="{ active: route.path === n.to }"
          >
            {{ n.label }}
          </RouterLink>
        </nav>
      </div>

      <!-- Right actions -->
      <div class="flex items-center gap-3 flex-wrap">
        <!-- Time Presets in Header -->
        <div v-if="showRangeBar" class="flex items-center gap-2">
          <!-- Preset chips -->
          <div class="flex flex-wrap rounded-full bg-bg p-0.5 border border-border">
            <button
              v-for="p in presets"
              :key="p.value"
              class="px-3.5 py-1 text-xs rounded-full transition-all duration-200"
              :class="range.preset === p.value
                ? 'bg-surface text-accent shadow-sm font-semibold border border-border/10'
                : 'text-muted hover:text-text'"
              @click="range.setPreset(p.value)"
            >
              {{ p.label }}
            </button>
          </div>

          <!-- Custom Dates -->
          <div v-if="range.preset === 'custom'" class="flex items-center gap-1 text-xs">
            <input
              type="date"
              v-model="range.customStart"
              class="apple-input text-xs py-1 px-2 h-7 focus:border-accent outline-none"
            />
            <span class="text-muted">-</span>
            <input
              type="date"
              v-model="range.customEnd"
              class="apple-input text-xs py-1 px-2 h-7 focus:border-accent outline-none"
            />
          </div>

          <button class="btn-secondary py-1 px-3 text-xs h-7" @click="forceRefresh">
            刷新
          </button>
        </div>

        <span v-if="showRangeBar" class="h-4 w-px bg-border"></span>

        <!-- Indicators and Theme -->
        <div class="flex items-center gap-3">
          <div v-if="isMockMode()" class="flex items-center gap-1.5 text-xs text-sky-400 bg-sky-400/10 border border-sky-400/20 px-2 py-0.5 rounded-full font-medium">
            <span class="h-1.5 w-1.5 rounded-full bg-sky-400 animate-pulse"></span>
            <span>Mocking</span>
          </div>

          <button class="text-xs text-muted hover:text-text border border-border rounded-full p-1.5 hover:bg-surface-hover transition-colors" @click="toggleTheme" title="切换主题">
            <!-- Light/Dark icon -->
            <svg v-if="config.theme === 'dark'" class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 9H3m15.364-6.364l-.707.707M6.343 17.657l-.707.707m12.728 0l-.707-.707M6.343 6.343l-.707-.707M14.25 12a2.25 2.25 0 11-4.5 0 2.25 2.25 0 014.5 0z" />
            </svg>
            <svg v-else class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z" />
            </svg>
          </button>
        </div>
      </div>
    </header>

    <!-- Main Content Container -->
    <main class="flex-1 overflow-auto p-6 md:p-8">
      <div
        v-if="authError"
        class="mb-6 flex items-center justify-between rounded-2xl border border-amber-500/20 dark:border-amber-500/30 bg-amber-500/10 dark:bg-amber-500/20 px-5 py-4 text-amber-800 dark:text-amber-200 shadow-sm"
      >
        <div class="flex items-center gap-3">
          <svg class="h-5 w-5 text-amber-600 dark:text-amber-400" viewBox="0 0 20 20" fill="currentColor">
            <path fill-rule="evenodd" d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
          </svg>
          <span class="text-sm font-medium">{{ authError }}</span>
        </div>
        <a v-if="authError.includes('登录')" href="/" target="_top" class="btn-primary py-1 px-4 text-xs font-semibold h-8 no-underline">去登录</a>
        <button v-else class="btn-primary py-1 px-4 text-xs font-semibold h-8" @click="retry">重试</button>
      </div>

      <RouterView v-if="config.isConfigured()" />
      <p v-else-if="!authError" class="text-muted">正在读取团队配置…</p>
    </main>
  </div>
</template>

<style scoped>
.nav-tab {
  height: 100%;
  display: flex;
  align-items: center;
  padding: 0 1.2rem;
  font-size: 0.85rem;
  color: var(--muted);
  border-bottom: 3px solid transparent;
  text-decoration: none;
  font-weight: 500;
  transition: all 0.15s ease;
  margin-bottom: -1px; /* Align with bottom border of header */
}
.nav-tab:hover {
  color: var(--text);
  background-color: var(--surface-hover);
}
.nav-tab.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
  font-weight: 600;
}
</style>
