<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { apiClient } from '@/api/client'
import { buildApiUrl } from '@/api/url'
import { useAuthStore } from '@/stores/auth'
import { useStepUp } from '@/composables/useStepUp'
import AppLayout from '@/components/layout/AppLayout.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import CPRPoolBindings from '@/features/cpr/CPRPoolBindings.vue'
import { resolveCPRConsoleRequest, type CPRConsoleBridge } from '@/features/cpr/bridge'

const auth = useAuthStore()
const stepUp = useStepUp()
const status = ref<{ enabled: boolean; connected: boolean } | null>(null)
const loading = ref(false)
const section = ref<'accounts' | 'bindings'>('accounts')
const error = ref('')
const lifetime = new AbortController()
function identity() {
  if (!auth.isAdmin) throw { status: 403, message: '需要管理员权限' }
  return localStorage.getItem('auth_user')
}
function current(expected: string | null) {
  if (!auth.isAdmin || localStorage.getItem('auth_user') !== expected)
    throw { status: 401, message: '登录状态已变更，请重新打开账号池' }
}
async function loadStatus() {
  loading.value = true
  error.value = ''
  try {
    const owner = identity()
    const response = await apiClient.get('/admin/cpr/status', { signal: lifetime.signal })
    current(owner)
    status.value = response.data
  } catch (value) {
    if (!lifetime.signal.aborted) error.value = (value as Error).message || '无法连接账号池服务'
  } finally { loading.value = false }
}
const bridge: CPRConsoleBridge = {
  async session() {
    const owner = identity()
    await apiClient.get('/admin/cpr/status', { signal: lifetime.signal })
    current(owner)
    // 这是内嵌页面的刷新时刻；每次业务请求仍由宿主与服务器验证真实登录状态。
    return { role: 'admin', expiresAt: new Date(Date.now() + 300_000).toISOString() }
  },
  async request(input) {
    const owner = identity()
    const target = resolveCPRConsoleRequest(input.path, input.method)
    if (!target) throw { status: 403, message: '该操作不在账号池管理范围内' }
    const query = { ...Object.fromEntries(target.query), ...(input.query as Record<string, unknown> | undefined) }
    const response = await stepUp.run(() => apiClient.request({
      url: target.path, method: input.method, params: query, data: input.body,
      responseType: input.responseType,
      signal: input.signal ? AbortSignal.any([lifetime.signal, input.signal]) : lifetime.signal,
    }))
    current(owner)
    return { status: response.status, headers: Object.fromEntries(Object.entries(response.headers).map(([name, value]) => [name, String(value)])), data: response.data }
  },
  async stream(path, signal) {
    const owner = identity()
    const target = resolveCPRConsoleRequest(path, 'GET', 'stream')
    if (!target) throw { status: 403, message: '该事件流不在账号池管理范围内' }
    // 使用统一请求层先校验/刷新宿主会话，事件流中的用户令牌仅在宿主闭包内使用。
    await apiClient.get('/admin/cpr/status', { signal: lifetime.signal })
    current(owner)
    const url = new URL(buildApiUrl(target.path), window.location.origin)
    target.query.forEach((value, name) => url.searchParams.append(name, value))
    const response = await fetch(url, {
      headers: { Authorization: 'Bearer ' + (localStorage.getItem('auth_token') || '') },
      credentials: 'same-origin', signal: AbortSignal.any([lifetime.signal, signal]),
    })
    try { current(owner) } catch (error) {
      await response.body?.cancel()
      throw error
    }
    if (!response.ok || !response.body) throw { status: response.status, message: '账号连接测试失败' }
    return response.body
  },
  theme: () => document.documentElement.classList.contains('dark') ? 'dark' : 'light',
}
window.__SUB2API_CPR_BRIDGE__ = bridge
onMounted(loadStatus)
onBeforeUnmount(() => {
  lifetime.abort()
  stepUp.onCancel()
  if (window.__SUB2API_CPR_BRIDGE__ === bridge) delete window.__SUB2API_CPR_BRIDGE__
})
</script>

<template>
  <AppLayout>
    <div v-if="auth.isAdmin && status?.connected" class="mb-4 flex gap-2" aria-label="账号池功能">
      <button class="btn" :class="section === 'accounts' ? 'btn-primary' : 'btn-secondary'" @click="section = 'accounts'">账号池管理</button>
      <button class="btn" :class="section === 'bindings' ? 'btn-primary' : 'btn-secondary'" @click="section = 'bindings'">业务分组绑定</button>
    </div>
    <CPRPoolBindings v-if="auth.isAdmin && status?.connected && section === 'bindings'" />
    <iframe v-else-if="auth.isAdmin && status?.enabled && status.connected" src="/cpr-console/index.html#/accounts"
      title="CPR 账号池管理" class="w-full rounded-xl border-0" style="height: calc(100dvh - 140px); min-height: 600px" />
    <section v-else class="card flex min-h-64 flex-col items-center justify-center gap-4 p-8 text-center">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">CPR 账号池</h2>
      <p class="text-sm text-gray-500">{{ error || (loading ? '正在连接账号池服务…' : status?.enabled ? '账号池服务暂不可用' : '账号池服务尚未启用') }}</p>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="loadStatus">重新检查</button>
    </section>
    <TotpStepUpDialog :controller="stepUp" />
  </AppLayout>
</template>
