<template>
  <BaseDialog :show="show" :title="t('admin.promptAudit.requests.detailTitle')" width="full" @close="$emit('close')">
    <div v-if="loading" class="flex items-center justify-center py-16"><div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600" /></div>
    <div v-else-if="error" class="p-6 text-sm text-red-600 dark:text-red-300" role="alert">{{ error }}</div>
    <div v-else-if="detail" class="space-y-5 p-2">
      <div class="grid grid-cols-2 gap-3 rounded-xl bg-gray-50 p-4 text-sm dark:bg-dark-900 sm:grid-cols-4">
        <div><div class="text-xs text-gray-500">{{ t('admin.promptAudit.requests.requestId') }}</div><div class="mt-1 break-all font-mono">{{ detail.request?.request_id || requestId }}</div></div>
        <div><div class="text-xs text-gray-500">{{ t('admin.promptAudit.requests.route') }}</div><div class="mt-1">{{ detail.request?.platform || '—' }} / {{ detail.request?.model || '—' }}</div></div>
        <div><div class="text-xs text-gray-500">{{ t('admin.promptAudit.requests.latency') }}</div><div class="mt-1">{{ detail.request?.duration_ms ?? '—' }} ms</div></div>
        <div><div class="text-xs text-gray-500">{{ t('admin.promptAudit.requests.risk') }}</div><div class="mt-1">{{ riskCount }}</div></div>
      </div>
      <div class="grid gap-4 xl:grid-cols-2">
        <ContentAuditPayloadSection v-for="section in payloadSections" :key="section.id" :title="section.title" :payload="section.payload" />
      </div>
      <section v-if="errorPayloads.length" class="rounded-xl border border-gray-200 dark:border-dark-700">
        <div class="border-b border-gray-200 px-4 py-3 text-sm font-semibold dark:border-dark-700">{{ t('admin.promptAudit.requests.sanitizedPayloads') }}</div>
        <div class="divide-y divide-gray-100 dark:divide-dark-700"><div v-for="payload in errorPayloads" :key="payload.error_id" class="p-4"><div class="text-xs font-semibold text-gray-500">#{{ payload.error_id }} · {{ payload.upstream_status_code || '—' }}</div><pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded bg-gray-50 p-3 text-xs dark:bg-dark-900">{{ pretty(payload.sanitized_error_body || payload.upstream_error_detail || payload.upstream_error_message || '') }}</pre></div></div>
      </section>
      <section class="rounded-xl border border-gray-200 dark:border-dark-700">
        <div class="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-dark-700"><h3 class="text-sm font-semibold">{{ t('admin.promptAudit.requests.upstreamAttempts') }}</h3><span class="text-xs text-gray-500">{{ attempts.length }}</span></div>
        <div v-if="!attempts.length" class="p-4 text-sm text-gray-500">{{ t('admin.promptAudit.requests.noAttempts') }}</div>
        <div v-else class="divide-y divide-gray-100 dark:divide-dark-700"><div v-for="(attempt, index) in attempts" :key="String(attempt.id || attempt.error_id || index)" class="p-4 text-sm"><div class="flex flex-wrap gap-3"><span class="font-medium">#{{ index + 1 }}</span><span>{{ attempt.status_code || attempt.upstream_status_code || '—' }}</span><span class="text-gray-500">{{ attempt.kind || attempt.type || 'upstream' }}</span><span v-if="attempt.request_id" class="font-mono text-xs text-gray-500">{{ attempt.request_id }}</span></div><p v-if="attempt.message" class="mt-2 text-gray-600 dark:text-gray-300">{{ attempt.message }}</p><pre v-if="attempt.raw_body || attempt.body" class="mt-2 max-h-48 overflow-auto rounded bg-gray-50 p-3 text-xs dark:bg-dark-900">{{ pretty(attempt.raw_body || attempt.body) }}</pre></div></div>
      </section>
      <section v-if="detail.prompt_events?.length || detail.risk_flags?.length" class="rounded-xl border border-amber-200 bg-amber-50/50 p-4 dark:border-amber-900/60 dark:bg-amber-950/20"><h3 class="text-sm font-semibold text-amber-900 dark:text-amber-200">{{ t('admin.promptAudit.requests.riskEvents') }}</h3><div class="mt-3 space-y-2 text-sm"><div v-for="(event, index) in [...(detail.risk_flags || []), ...(detail.prompt_events || [])]" :key="String(event.id || index)" class="rounded bg-white/70 p-3 dark:bg-dark-900/50"><span class="font-medium">{{ event.decision || event.risk_level || event.action || t('admin.promptAudit.requests.risk') }}</span><span v-if="event.message || event.description" class="ml-2 text-gray-600 dark:text-gray-300">{{ event.message || event.description }}</span></div></div></section>
      <p v-if="!hasPersistedPayload" class="text-xs text-gray-500">{{ t('admin.promptAudit.requests.noRawPayload') }}</p>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import contentAuditAPI, { type ContentAuditRequestDetail } from '@/api/admin/contentAudit'
import ContentAuditPayloadSection from './ContentAuditPayloadSection.vue'

const props = defineProps<{ show: boolean; requestId: string }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
const loading = ref(false)
const error = ref('')
const detail = ref<ContentAuditRequestDetail | null>(null)
const attempts = ref<Array<Record<string, unknown>>>([])
const errorPayloads = ref<Array<{ error_id: number; sanitized_error_body?: string; upstream_status_code?: number; upstream_error_detail?: string; upstream_error_message?: string }>>([])
const payloadSections = computed(() => [
  { id: 'client_request', title: t('admin.promptAudit.requests.clientRequest'), payload: detail.value?.client_request || undefined },
  { id: 'upstream_request', title: t('admin.promptAudit.requests.upstreamRequest'), payload: detail.value?.upstream_request || undefined },
  { id: 'upstream_response', title: t('admin.promptAudit.requests.upstreamResponse'), payload: detail.value?.upstream_response || undefined },
  { id: 'client_response', title: t('admin.promptAudit.requests.clientResponse'), payload: detail.value?.client_response || undefined },
])
const riskCount = computed(() => (detail.value?.risk_flags?.length || 0) + (detail.value?.prompt_events?.length || 0) + (detail.value?.errors?.length || 0))
const hasPersistedPayload = computed(() => payloadSections.value.some(section => !!section.payload) || !!detail.value?.sanitized_error_payload_available)
function decodeRawBody(value?: string): string {
  if (!value) return ''
  try {
    const bytes = Uint8Array.from(atob(value), char => char.charCodeAt(0))
    return new TextDecoder().decode(bytes)
  } catch { return value }
}
function applyRawPayloads(next: ContentAuditRequestDetail) {
  const payloads = next.raw_payloads || []
  for (const direction of ['client_request', 'upstream_request', 'upstream_response', 'client_response']) {
    const chunks = payloads.filter(item => item.direction === direction).sort((a, b) => (a.chunk_index || 0) - (b.chunk_index || 0))
    if (!chunks.length) continue
    const first = chunks[0]
    const format = first.content_type?.includes('event-stream')
      ? 'sse'
      : first.content_type?.includes('websocket')
        ? 'websocket'
        : 'text'
    ;(next as Record<string, unknown>)[direction] = {
      headers: first.headers,
      raw: chunks.map(item => decodeRawBody(item.body)).join(format === 'websocket' ? '\n' : ''),
      format,
      bytes: chunks.reduce((sum, item) => sum + Number(item.original_bytes || 0), 0),
      truncated: chunks.some(item => item.truncated),
    }
  }
}
function pretty(value: unknown) { if (typeof value === 'string') { try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value } } try { return JSON.stringify(value, null, 2) } catch { return String(value) } }
async function load() {
  if (!props.show || !props.requestId) return
  loading.value = true; error.value = ''
  try {
    detail.value = await contentAuditAPI.getRequest(props.requestId)
    applyRawPayloads(detail.value)
    const result = await contentAuditAPI.listAttempts(props.requestId)
    attempts.value = result.items || []
    const ids = (detail.value.errors || []).map(item => Number(item.id || item.error_id)).filter(id => Number.isInteger(id) && id > 0)
    errorPayloads.value = (await Promise.all(ids.map(id => contentAuditAPI.getErrorPayload(id).catch(() => null)))).filter((payload): payload is NonNullable<typeof payload> => !!payload)
  }
  catch (err: any) { error.value = err?.response?.data?.detail || err?.message || t('admin.promptAudit.requests.detailLoadFailed') }
  finally { loading.value = false }
}
watch(() => [props.show, props.requestId], () => { if (props.show) void load() }, { immediate: true })
</script>
