<script setup lang="ts">
import { ref, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'

const props = defineProps<{ accountId: number; proxyId: number | null }>()
const { t } = useI18n()
interface Environment { ip: string; country: string; country_code: string; region: string; city: string; timezone: string; checked_at: string }
const environment = ref<Environment | null>(null)
const error = ref('')
const loading = ref(false)
let controller: AbortController | null = null
let sequence = 0
async function load(refresh = false) {
  const current = ++sequence
  controller?.abort()
  controller = new AbortController()
  environment.value = null
  error.value = ''
  loading.value = true
  try {
    const { data } = await apiClient.get<Environment>(`/admin/accounts/${props.accountId}/request-environment`, {
      params: { proxy_id: props.proxyId || 0, refresh: refresh ? 1 : undefined }, signal: controller.signal,
    })
    if (current === sequence) environment.value = data
  } catch (e) {
    if (current === sequence && !controller?.signal.aborted) error.value = (e as Error).message || t('admin.accounts.requestEnvironment.failed')
  } finally { if (current === sequence) loading.value = false }
}
watch(() => [props.accountId, props.proxyId], () => { void load() }, { immediate: true })
onBeforeUnmount(() => { ++sequence; controller?.abort() })
</script>

<template>
  <div class="mt-2 rounded-lg border border-gray-200 bg-gray-50 p-3 text-xs dark:border-dark-600 dark:bg-dark-800" aria-live="polite">
    <div class="flex items-center justify-between gap-2">
      <span class="font-medium text-gray-700 dark:text-gray-200">{{ t('admin.accounts.requestEnvironment.title') }}</span>
      <button type="button" class="text-primary-600 hover:text-primary-700 disabled:opacity-50" :disabled="loading" @click="load(true)">{{ t('admin.accounts.requestEnvironment.retry') }}</button>
    </div>
    <p v-if="loading" class="mt-1 text-gray-500">{{ t('admin.accounts.requestEnvironment.loading') }}</p>
    <p v-else-if="error" class="mt-1 text-red-600 dark:text-red-400">{{ error }}</p>
    <div v-else-if="environment" class="mt-1 space-y-1 text-gray-600 dark:text-gray-300">
      <p>{{ environment.ip }} · {{ [environment.country, environment.region, environment.city].filter(Boolean).join(' / ') }}</p>
      <p>{{ environment.timezone }} · {{ new Date().toLocaleDateString('sv-SE', { timeZone: environment.timezone }) }}</p>
    </div>
    <p class="mt-2 text-gray-500">{{ t('admin.accounts.requestEnvironment.hint') }}</p>
  </div>
</template>
