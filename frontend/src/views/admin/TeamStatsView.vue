<script setup lang="ts">
import { onBeforeUnmount } from 'vue'
import { apiClient } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import AppLayout from '@/components/layout/AppLayout.vue'
import { isTeamStatsRequestAllowed, type TeamStatsBridge, type TeamStatsRequest } from '@/features/team-stats/bridge'

const auth = useAuthStore()
const bridge: TeamStatsBridge = {
  session: () => localStorage.getItem('auth_token') || '',
  async request<T>(request: TeamStatsRequest): Promise<T> {
    if (!auth.isAdmin || !isTeamStatsRequestAllowed(request)) {
      throw { status: 403, message: '管理员权限不足或请求超出看板范围' }
    }
    const sessionUser = localStorage.getItem('auth_user')
    const response = await apiClient.request<T>({
      url: request.path,
      method: request.method,
      params: request.query,
      data: request.body,
      signal: request.signal,
    })
    if (!auth.isAdmin || localStorage.getItem('auth_user') !== sessionUser) {
      throw { status: 401, message: '登录状态已变更，请刷新看板' }
    }
    return response.data
  },
}
window.__SUB2API_TEAM_STATS__ = bridge
onBeforeUnmount(() => {
  if (window.__SUB2API_TEAM_STATS__ === bridge) delete window.__SUB2API_TEAM_STATS__
})
</script>

<template>
  <AppLayout>
    <iframe
      v-if="auth.isAdmin"
      src="/team-stats/index.html"
      title="团队消耗看板"
      class="w-full rounded-xl border-0"
      style="height: calc(100dvh - 140px); min-height: 600px"
    />
  </AppLayout>
</template>
