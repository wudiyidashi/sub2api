<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router'
const tabs = [
  { path: '/accounts', title: '账号' },
  { path: '/groups', title: '账号组' },
  { path: '/proxies', title: '出口代理' },
  { path: '/settings', title: '调度策略' },
  { path: '/settings/upstream', title: '身份与上游' },
  { path: '/usage', title: '执行记录' },
]
</script>
<template>
  <div class="flex h-dvh min-w-0 flex-col bg-cp-bg-layout text-cp-text-primary">
    <nav class="flex shrink-0 flex-wrap items-center gap-1 px-4 py-3" aria-label="账号池管理">
      <RouterLink v-for="tab in tabs" :key="tab.path" :to="tab.path"
        class="shrink-0 rounded-lg px-3 py-2 text-sm font-medium text-cp-text-secondary transition-colors hover:bg-cp-bg-container hover:text-cp-text-primary"
        exact-active-class="bg-cp-bg-container text-cp-text-primary">
        {{ tab.title }}
      </RouterLink>
    </nav>
    <main class="min-h-0 min-w-0 flex-1 overflow-auto px-4 pb-4">
      <RouterView v-slot="{ Component, route }">
        <component :is="Component" :class="route.name === 'accounts' ? 'min-h-full' : 'h-full min-h-0'" />
      </RouterView>
    </main>
  </div>
</template>
