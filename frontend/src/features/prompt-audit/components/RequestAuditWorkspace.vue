<template>
  <section class="py-6">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div><h2 class="text-base font-semibold text-gray-950 dark:text-white">{{ t('admin.promptAudit.requests.title') }}</h2><p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.promptAudit.requests.description') }}</p></div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <form class="mt-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-6" @submit.prevent="applyFilters">
      <label class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.promptAudit.requests.timeRange') }}</span><select v-model="filters.time_range" class="input mt-1 w-full" @change="clearCustomRange"><option value="5m">5m</option><option value="30m">30m</option><option value="1h">1h</option><option value="6h">6h</option><option value="24h">24h</option></select></label>
      <div class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.dashboard.timeRange') }}</span><DateRangePicker v-model:start-date="startDate" v-model:end-date="endDate" class="mt-1" @change="onDateRangeChange" /></div>
      <label class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.promptAudit.requests.kind') }}</span><select v-model="filters.kind" class="input mt-1 w-full"><option value="all">{{ t('common.all') }}</option><option value="success">{{ t('admin.promptAudit.requests.success') }}</option><option value="error">{{ t('admin.promptAudit.requests.error') }}</option></select></label>
      <label class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.promptAudit.requests.platform') }}</span><input v-model.trim="filters.platform" class="input mt-1 w-full" /></label>
      <label class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.promptAudit.requests.requestId') }}</span><input v-model.trim="filters.request_id" class="input mt-1 w-full font-mono" /></label>
      <label class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.promptAudit.requests.keyword') }}</span><input v-model.trim="filters.q" class="input mt-1 w-full" /></label>
      <label class="text-xs text-gray-600 dark:text-dark-200"><span>{{ t('admin.promptAudit.requests.sort') }}</span><select v-model="filters.sort" class="input mt-1 w-full"><option value="created_at_desc">{{ t('admin.promptAudit.requests.newest') }}</option><option value="duration_desc">{{ t('admin.promptAudit.requests.slowest') }}</option><option value="ttft_desc">{{ t('admin.promptAudit.requests.slowestTtft') }}</option></select></label>
      <div class="flex items-end gap-2 sm:col-span-2"><button type="submit" class="btn btn-primary btn-sm">{{ t('common.search') }}</button><button type="button" class="btn btn-ghost btn-sm" @click="resetFilters">{{ t('common.reset') }}</button></div>
    </form>
    <UsageFilters
      v-model="filters"
      flat
      mode="audit"
      :start-date="startDate"
      :end-date="endDate"
      :show-actions="false"
      :exporting="false"
      @change="applyFilters"
      @reset="resetFilters"
    />
    <p v-if="error" class="mt-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300" role="alert">{{ error }}</p>
    <div class="mt-5 overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-700">
      <table class="min-w-[1100px] w-full text-left text-sm">
        <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900/70 dark:text-dark-400"><tr><th class="px-3 py-3">{{ t('admin.promptAudit.requests.time') }}</th><th class="px-3 py-3">{{ t('admin.promptAudit.requests.requestId') }}</th><th class="px-3 py-3">{{ t('admin.promptAudit.requests.kind') }}</th><th class="px-3 py-3">{{ t('admin.promptAudit.requests.route') }}</th><th class="px-3 py-3">{{ t('admin.promptAudit.requests.latency') }}</th><th class="px-3 py-3">{{ t('admin.promptAudit.requests.identity') }}</th><th class="px-3 py-3">{{ t('admin.promptAudit.requests.risk') }}</th><th class="px-3 py-3 text-right">{{ t('admin.promptAudit.common.actions') }}</th></tr></thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-if="loading"><td colspan="8" class="px-4 py-12 text-center text-gray-500">{{ t('common.loading') }}</td></tr>
          <tr v-else-if="items.length === 0"><td colspan="8" class="px-4 py-12 text-center text-gray-500">{{ t('admin.promptAudit.requests.empty') }}</td></tr>
          <tr v-for="row in items" v-else :key="`${row.request_id}-${row.created_at}`" class="align-top hover:bg-gray-50/70 dark:hover:bg-dark-800/70">
            <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-dark-300">{{ formatDate(row.created_at) }}</td>
            <td class="max-w-[220px] px-3 py-3"><button type="button" class="max-w-full truncate font-mono text-xs text-primary-600 hover:underline dark:text-primary-400" :title="row.request_id" @click="openRequest(row)">{{ row.request_id || '—' }}</button></td>
            <td class="px-3 py-3"><span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="row.kind === 'error' ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300' : 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'">{{ row.kind === 'error' ? t('admin.promptAudit.requests.error') : t('admin.promptAudit.requests.success') }}</span><div class="mt-1 text-xs text-gray-500">{{ row.status_code ?? '—' }}</div></td>
            <td class="max-w-[260px] px-3 py-3"><div class="font-medium text-gray-900 dark:text-white">{{ row.platform || '—' }}</div><div class="truncate text-xs text-gray-500" :title="row.model">{{ row.model || '—' }}</div></td>
            <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-dark-300">{{ row.duration_ms ?? '—' }} ms<div v-if="row.first_token_ms != null" class="text-gray-400">TTFT {{ row.first_token_ms }} ms</div></td>
            <td class="px-3 py-3 text-xs text-gray-600 dark:text-dark-300">{{ row.user_id ?? '—' }} · {{ row.api_key_id ?? '—' }}<div class="text-gray-400">{{ row.account_id ?? '—' }} / {{ row.group_id ?? '—' }}</div></td>
            <td class="max-w-[200px] px-3 py-3 text-xs"><span v-if="row.severity || row.phase" class="rounded bg-amber-100 px-2 py-0.5 text-amber-800 dark:bg-amber-900/30 dark:text-amber-200">{{ row.severity || row.phase }}</span><div v-if="row.message" class="mt-1 truncate text-gray-500" :title="row.message">{{ row.message }}</div><span v-if="!row.severity && !row.phase" class="text-gray-400">—</span></td>
            <td class="whitespace-nowrap px-3 py-3 text-right"><button type="button" class="btn btn-ghost btn-sm" @click="openRequest(row)">{{ t('common.view') }}</button><button v-if="row.error_id" type="button" class="btn btn-ghost btn-sm text-red-600" @click="openError(row.error_id)">{{ t('admin.promptAudit.requests.viewRisk') }}</button></td>
          </tr>
        </tbody>
      </table>
      <Pagination :total="total" :page="page" :page-size="pageSize" @update:page="changePage" @update:page-size="changePageSize" />
    </div>
    <ContentAuditRequestDetailModal :show="showDetail" :request-id="selectedRequestId" @close="showDetail = false" />
    <OpsErrorDetailModal :show="showErrorDetail" :error-id="errorId" error-type="request" @update:show="showErrorDetail = $event" />
  </section>
</template>

<script setup lang="ts">
import { ref, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { type OpsRequestDetail, type OpsRequestDetailsParams } from '@/api/admin/ops'
import contentAuditAPI from '@/api/admin/contentAudit'
import { useAppStore } from '@/stores'
import Pagination from '@/components/common/Pagination.vue'
import OpsErrorDetailModal from '@/views/admin/ops/components/OpsErrorDetailModal.vue'
import ContentAuditRequestDetailModal from './ContentAuditRequestDetailModal.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import UsageFilters from '@/components/admin/usage/UsageFilters.vue'

const props = defineProps<{ active: boolean }>()
const { t, locale } = useI18n()
const appStore = useAppStore()
const items = ref<OpsRequestDetail[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const error = ref('')
const showDetail = ref(false)
const showErrorDetail = ref(false)
const errorId = ref<number | null>(null)
const selectedRequestId = ref('')
const dateString = (value: Date) => {
  const year = value.getFullYear()
  const month = String(value.getMonth() + 1).padStart(2, '0')
  const day = String(value.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
const endDate = ref(dateString(new Date()))
const startDate = ref(dateString(new Date(Date.now() - 24 * 60 * 60 * 1000)))
const filters = reactive<OpsRequestDetailsParams>({ time_range: '24h', kind: 'all', sort: 'created_at_desc', page: 1, page_size: 20 })
function formatDate(value: string) { return new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value)) }
async function load() {
  if (!props.active) return
  loading.value = true; error.value = ''
  try { const result = await contentAuditAPI.listRequests({ ...filters, page: page.value, page_size: pageSize.value }); items.value = result.items || []; total.value = result.total || 0 }
  catch (err: any) { error.value = err?.response?.data?.detail || err?.message || t('admin.promptAudit.requests.loadFailed'); appStore.showError(error.value) }
  finally { loading.value = false }
}
function applyFilters() { page.value = 1; void load() }
function clearCustomRange() { filters.start_time = undefined; filters.end_time = undefined }
function onDateRangeChange(range: { startDate: string; endDate: string; preset: string | null }) {
  const presetMap: Record<string, OpsRequestDetailsParams['time_range']> = { last24Hours: '24h' }
  if (range.preset && presetMap[range.preset]) {
    filters.time_range = presetMap[range.preset]
    clearCustomRange()
  } else {
    filters.time_range = undefined
    filters.start_time = range.startDate
    filters.end_time = range.endDate
  }
  applyFilters()
}
function resetFilters() {
  Object.assign(filters, { time_range: '24h', start_time: undefined, end_time: undefined, kind: 'all', platform: undefined, model: undefined, request_id: undefined, q: undefined, user_id: undefined, api_key_id: undefined, account_id: undefined, group_id: undefined, sort: 'created_at_desc' })
  endDate.value = dateString(new Date())
  startDate.value = dateString(new Date(Date.now() - 24 * 60 * 60 * 1000))
  applyFilters()
}
function changePage(value: number) { page.value = value; void load() }
function changePageSize(value: number) { pageSize.value = value; page.value = 1; void load() }
function openRequest(row: OpsRequestDetail) { selectedRequestId.value = row.request_id; showDetail.value = true }
function openError(id: number) { errorId.value = id; showErrorDetail.value = true }
watch(() => props.active, active => { if (active) void load() }, { immediate: true })
</script>
