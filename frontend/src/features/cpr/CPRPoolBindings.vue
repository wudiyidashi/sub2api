<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { apiClient } from '@/api/client'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'

interface Pool {
  id: number
  name: string
  group_id: number
  provider: 'openai' | 'xai'
  account_group_ids: string[]
  enabled: boolean
  concurrency: number
  revision: number
}
interface BusinessGroup { id: number; name: string; platform: string }
interface AccountGroup { id: string; name: string; enabled: boolean; providerCounts: Record<string, number> }
const pools = ref<Pool[]>([])
const businessGroups = ref<BusinessGroup[]>([])
const accountGroups = ref<AccountGroup[]>([])
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const showEditor = ref(false)
const lifetime = new AbortController()
const draft = ref({ id: 0, name: '', group_id: 0, provider: 'openai' as Pool['provider'], account_group_ids: [] as string[], enabled: false, concurrency: 0, expected_revision: 0 })
const businessOptions = computed(() => businessGroups.value.filter(group =>
  (group.platform === (draft.value.provider === 'xai' ? 'grok' : 'openai') || group.platform === 'composite') &&
  (draft.value.id || !pools.value.some(pool => pool.group_id === group.id && pool.provider === draft.value.provider)),
).map(group => ({ value: group.id, label: group.name })))
function eligible(group: AccountGroup) {
  return group.enabled && Object.entries(group.providerCounts).every(([provider, count]) => count === 0 || provider === draft.value.provider)
}
const accountOptions = computed(() => [
  ...accountGroups.value.filter(group => eligible(group) || draft.value.account_group_ids.includes(group.id)),
  ...draft.value.account_group_ids.filter(id => !accountGroups.value.some(group => group.id === id)).map(id => ({ id, name: id, enabled: false, providerCounts: {} })),
])
function errorMessage(value: unknown) {
  const failure = value as { response?: { data?: { message?: string } }; message?: string }
  return failure.response?.data?.message || failure.message || '账号池绑定加载失败'
}
async function load() {
  loading.value = true
  error.value = ''
  try {
    const [bindings, groups] = await Promise.all([
      apiClient.get<Pool[]>('/admin/cpr/pools', { signal: lifetime.signal }),
      apiClient.get<BusinessGroup[]>('/admin/groups/all', { params: { include_inactive: true }, signal: lifetime.signal }),
    ])
    const remote: AccountGroup[] = []
    for (let page = 1; page <= 1000; page++) {
      const { data } = await apiClient.get<{ items: AccountGroup[]; page: { totalPages: number } }>('/admin/cpr/account-groups', { params: { page, pageSize: 100 }, signal: lifetime.signal })
      remote.push(...data.items)
      if (page >= data.page.totalPages) break
    }
    pools.value = bindings.data
    businessGroups.value = groups.data
    accountGroups.value = remote
  } catch (value) { if (!lifetime.signal.aborted) error.value = errorMessage(value) }
  finally { loading.value = false }
}
function edit(pool?: Pool) {
  draft.value = pool ? { ...pool, account_group_ids: [...pool.account_group_ids], expected_revision: pool.revision } : { id: 0, name: '', group_id: 0, provider: 'openai', account_group_ids: [], enabled: false, concurrency: 0, expected_revision: 0 }
  error.value = ''
  showEditor.value = true
}
function changeProvider(value: string | number | boolean | null) {
  if (value !== 'openai' && value !== 'xai') return
  draft.value.provider = value
  draft.value.group_id = 0
  draft.value.account_group_ids = []
}
async function save() {
  saving.value = true
  error.value = ''
  try {
    await apiClient.post('/admin/cpr/pools/save', draft.value, { signal: lifetime.signal })
    showEditor.value = false
    await load()
  } catch (value) { if (!lifetime.signal.aborted) error.value = errorMessage(value) }
  finally { saving.value = false }
}
onMounted(load)
onBeforeUnmount(() => lifetime.abort())
</script>

<template>
  <section class="space-y-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div><h2 class="text-lg font-semibold text-gray-900 dark:text-white">业务分组绑定</h2>
        <p class="mt-1 text-sm text-gray-500">为套餐和 API Key 所属分组选择 CPR 账号组，用户权限和计费沿用当前分组配置。</p></div>
      <button class="btn btn-primary" :disabled="loading" @click="edit()">新增绑定</button>
    </div>
    <p v-if="error && !showEditor" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="loading" class="text-sm text-gray-500">正在加载账号池绑定…</p>
    <div v-else-if="pools.length === 0" class="card p-8 text-center text-sm text-gray-500">尚未绑定业务分组。先在账号池中创建账号组，再新增绑定。</div>
    <div v-else class="grid gap-4 md:grid-cols-2">
      <article v-for="pool in pools" :key="pool.id" class="card space-y-3 p-5">
        <div class="flex items-center justify-between gap-3"><h3 class="font-semibold text-gray-900 dark:text-white">{{ pool.name }}</h3><span class="text-sm" :class="pool.enabled ? 'text-green-600' : 'text-gray-500'">{{ pool.enabled ? '已启用' : '已停用' }}</span></div>
        <p class="text-sm text-gray-500">{{ businessGroups.find(group => group.id === pool.group_id)?.name || `分组 ${pool.group_id}` }} · {{ pool.provider === 'xai' ? 'Grok' : 'OpenAI / Codex' }}</p>
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ pool.account_group_ids.map(id => accountGroups.find(group => group.id === id)?.name || id).join('、') }}</p>
        <button class="btn btn-secondary" @click="edit(pool)">编辑绑定</button>
      </article>
    </div>
    <BaseDialog :show="showEditor" :title="draft.id ? '编辑账号池绑定' : '新增账号池绑定'" :close-on-escape="!saving" :close-on-click-outside="!saving" :show-close-button="!saving" @close="showEditor = false">
      <form id="cpr-pool-form" class="space-y-5" @submit.prevent="save">
        <label class="block"><span class="input-label">名称</span><input v-model="draft.name" required maxlength="128" class="input" /></label>
        <div><label for="cpr-pool-provider" class="input-label">平台</label><Select id="cpr-pool-provider" aria-label="平台" :model-value="draft.provider" :disabled="!!draft.id || saving" :options="[{value:'openai',label:'OpenAI / Codex'}, {value:'xai',label:'Grok'}]" @update:model-value="changeProvider" /></div>
        <div><label for="cpr-pool-business" class="input-label">业务分组</label><Select id="cpr-pool-business" aria-label="业务分组" v-model="draft.group_id" :disabled="!!draft.id || saving" :options="businessOptions" /></div>
        <fieldset class="space-y-2"><legend class="input-label">CPR 账号组</legend>
          <label v-for="group in accountOptions" :key="group.id" class="flex items-center gap-2 text-sm"><input v-model="draft.account_group_ids" type="checkbox" :value="group.id" :disabled="saving" />{{ group.name }}{{ eligible(group) ? '' : '（不可用，请移除或在账号池中恢复）' }}</label>
          <p v-if="accountOptions.length === 0" class="input-hint">暂无符合平台的账号组，请先在账号池创建账号组。</p>
        </fieldset>
        <label class="block"><span class="input-label">业务入口并发上限</span><input v-model.number="draft.concurrency" type="number" min="0" max="10000" required class="input" /><span class="input-hint">0 表示不限，账号级并发由 CPR 管理。</span></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="draft.enabled" type="checkbox" :disabled="saving" />启用该账号池绑定</label>
        <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      </form>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="showEditor = false">取消</button><button form="cpr-pool-form" type="submit" class="btn btn-primary" :disabled="saving || !draft.group_id || !draft.account_group_ids.length">{{ saving ? '保存中…' : '保存绑定' }}</button></template>
    </BaseDialog>
  </section>
</template>
