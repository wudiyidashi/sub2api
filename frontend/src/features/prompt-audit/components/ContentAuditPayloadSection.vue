<template>
  <section class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 px-4 py-3 dark:border-dark-700">
      <div class="min-w-0">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ title }}</h3>
        <div v-if="payload" class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500">
          <span class="rounded bg-gray-100 px-1.5 py-0.5 font-medium dark:bg-dark-800">{{ formatLabel }}</span>
          <span>{{ payload.bytes ?? 0 }} B</span>
          <span v-if="frameCount > 0">{{ frameCount }} 帧</span>
          <span v-if="payload.truncated" class="text-amber-600 dark:text-amber-300">{{ t('admin.promptAudit.requests.truncated') }}</span>
        </div>
      </div>
      <div v-if="payload" class="flex shrink-0 items-center gap-1">
        <button type="button" class="btn btn-ghost btn-sm" :class="{ 'bg-gray-100 dark:bg-dark-800': view === 'formatted' }" @click="view = 'formatted'">JSON</button>
        <button type="button" class="btn btn-ghost btn-sm" :class="{ 'bg-gray-100 dark:bg-dark-800': view === 'raw' }" @click="view = 'raw'">{{ t('admin.promptAudit.requests.raw') }}</button>
        <button type="button" class="btn btn-ghost btn-sm" @click="copyRaw">{{ copied ? '✓' : t('common.copy') }}</button>
      </div>
    </div>

    <div v-if="!payload" class="p-5 text-sm text-gray-500">{{ t('admin.promptAudit.requests.notPersisted') }}</div>
    <div v-else class="space-y-3 p-4">
      <details v-if="payload.headers" open class="rounded-lg border border-gray-200 dark:border-dark-700">
        <summary class="cursor-pointer px-3 py-2 text-xs font-semibold uppercase tracking-wide text-gray-500">{{ t('admin.promptAudit.requests.headers') }}</summary>
        <pre class="max-h-48 overflow-auto border-t border-gray-200 bg-gray-50 p-3 text-xs dark:border-dark-700 dark:bg-dark-900">{{ pretty(payload.headers) }}</pre>
      </details>

      <template v-if="view === 'formatted' && frameItems.length">
        <div class="space-y-2">
          <div v-for="(frame, index) in frameItems" :key="`${index}-${frame.type}`" class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700">
            <div class="flex items-center justify-between gap-2 bg-gray-50 px-3 py-2 text-xs dark:bg-dark-900">
              <span class="font-semibold text-gray-600 dark:text-dark-300">#{{ index + 1 }}<span v-if="frame.type" class="ml-2 rounded bg-primary-100 px-1.5 py-0.5 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ frame.type }}</span></span>
              <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" @click="copyText(frame.raw)">{{ t('common.copy') }}</button>
            </div>
            <pre class="max-h-72 overflow-auto whitespace-pre-wrap break-words border-t border-gray-200 p-3 text-xs dark:border-dark-700">{{ frame.formatted }}</pre>
          </div>
        </div>
      </template>
      <pre v-else class="max-h-[34rem] overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-950 p-4 font-mono text-xs leading-5 text-gray-100">{{ view === 'raw' ? rawText : formattedText }}</pre>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ContentAuditPayload } from '@/api/admin/contentAudit'

const props = defineProps<{ title: string; payload?: ContentAuditPayload }>()
const { t } = useI18n()
const view = ref<'formatted' | 'raw'>('formatted')
const copied = ref(false)

const rawText = computed(() => String(props.payload?.raw ?? props.payload?.body ?? ''))
const formatLabel = computed(() => props.payload?.format === 'websocket' ? 'WebSocket' : props.payload?.format === 'sse' ? 'SSE' : 'JSON')
const frameItems = computed(() => {
  if (props.payload?.format !== 'websocket') return []
  return rawText.value.split(/\n+/).map(value => value.trim()).filter(Boolean).map(raw => {
    try {
      const parsed = JSON.parse(raw) as Record<string, unknown>
      return { raw, type: typeof parsed.type === 'string' ? parsed.type : '', formatted: JSON.stringify(parsed, null, 2) }
    } catch {
      return { raw, type: '', formatted: raw }
    }
  })
})
const frameCount = computed(() => frameItems.value.length)
const formattedText = computed(() => {
  if (props.payload?.format === 'websocket') return rawText.value
  try { return JSON.stringify(JSON.parse(rawText.value), null, 2) } catch { return rawText.value }
})

async function copyText(value: string) {
  if (!value) return
  try {
    await navigator.clipboard?.writeText(value)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1200)
  } catch { /* clipboard permissions are optional */ }
}
function copyRaw() { void copyText(rawText.value) }
function pretty(value: unknown) {
  if (typeof value === 'string') {
    try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value }
  }
  try { return JSON.stringify(value, null, 2) } catch { return String(value) }
}
</script>
