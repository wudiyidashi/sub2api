<template>
  <BaseDialog :show="show" :title="tr('title')" width="wide" @close="$emit('close')">
    <div class="space-y-5">
      <p class="input-hint">{{ tr('description') }}</p>
      <p v-if="error" class="text-sm text-amber-600 dark:text-amber-400" role="status">{{ error }}</p>
      <template v-if="status">
        <div class="flex flex-wrap gap-3 text-sm">
          <span>{{ tr(status.enabled ? 'enabled' : 'disabled') }}</span>
          <span v-if="status.updating" class="text-amber-600 dark:text-amber-400">{{ tr('updating') }}</span>
          <span>{{ tr(status.persisted ? 'persisted' : 'notPersisted') }}</span>
        </div>
        <p v-if="status.last_error" class="text-sm text-red-600 dark:text-red-400" role="status">{{ status.last_error }}</p>
        <div class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead><tr class="border-b border-gray-200 dark:border-dark-600">
              <th class="p-2">{{ tr('generation') }}</th><th class="p-2">{{ tr('state') }}</th>
              <th class="p-2">{{ tr('version') }}</th><th class="p-2">{{ tr('httpRequests') }}</th>
              <th class="p-2">{{ tr('websocketConnections') }}</th><th class="p-2">{{ tr('drainDeadline') }}</th><th class="p-2"></th>
            </tr></thead>
            <tbody>
              <tr v-for="row in generations" :key="row.generation" class="border-b border-gray-100 dark:border-dark-700">
                <td class="p-2">{{ row.generation }} <span class="text-xs text-gray-500">{{ tr(row.role) }}</span></td>
                <td class="p-2">{{ row.state }}</td><td class="p-2"><span>{{ row.version }}</span><div class="text-xs text-gray-500">{{ row.transports?.join(' / ') }}</div></td>
                <td class="p-2 tabular-nums">{{ row.http_requests }}</td><td class="p-2 tabular-nums">{{ row.websocket_connections }}</td>
                <td class="p-2 text-xs">{{ row.drain_deadline ? formatDateTime(row.drain_deadline) : '-' }}</td>
                <td class="p-2"><button v-if="row.role === 'retired' && row.available" type="button" class="btn btn-danger btn-sm" :disabled="!canMutate" @click="prepare('drain', row.generation)">{{ tr('forceDrain') }}</button></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="!status.retired.length" class="input-hint">{{ tr('noRetired') }}</p>
        <form class="space-y-4" @submit.prevent="prepare('upgrade')">
          <div><label class="input-label">{{ tr('executablePath') }}</label><input v-model="executablePath" class="input" type="text" :disabled="!canMutate" /><p class="input-hint">{{ tr('executablePathHint') }}</p></div>
          <div><label class="input-label">SHA256</label><input v-model="sha256" class="input font-mono" type="text" maxlength="64" :disabled="!canMutate" /></div>
          <div><label class="input-label">{{ tr('drainTimeout') }}</label><input v-model.number="drainTimeout" class="input max-w-40" type="number" min="1" max="600" step="1" :disabled="!canMutate" /></div>
          <div class="flex flex-wrap gap-2">
            <button type="submit" class="btn btn-primary" :disabled="!canMutate">{{ tr('upgrade') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="!canMutate || !status.rollback_available" @click="prepare('rollback')">{{ tr('rollback') }}</button>
          </div>
        </form>
      </template>
    </div>
    <template #footer><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button><button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button></div></template>
  </BaseDialog>
  <ConfirmDialog :show="pending !== null" :title="tr(pending?.kind === 'drain' ? 'forceDrain' : pending?.kind || 'upgrade')" :message="confirmation" :danger="pending?.kind === 'drain'" @confirm="execute" @cancel="pending = null" />
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import type { AccountDriverRuntimeStatus } from './types'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: []; changed: [] }>()
const { t } = useI18n()
const tr = (key: string) => t(`admin.accounts.accountDriver.runtime.${key}`)
const app = useAppStore()
const status = ref<AccountDriverRuntimeStatus | null>(null)
const loading = ref(false)
const submitting = ref(false)
const error = ref('')
const executablePath = ref('')
const sha256 = ref('')
const drainTimeout = ref(30)
const pending = ref<{ kind: 'upgrade' | 'rollback' | 'drain'; generation: number; path: string; hash: string; timeout: number } | null>(null)
let timer: ReturnType<typeof setInterval> | undefined
let requestRevision = 0
const canMutate = computed(() => !!status.value?.enabled && !status.value.updating && !submitting.value && !error.value)
const generations = computed(() => [
  ...(status.value?.active ? [{ ...status.value.active, role: 'active' }] : []),
  ...(status.value?.candidate ? [{ ...status.value.candidate, role: 'candidate' }] : []),
  ...(status.value?.retired || []).map(row => ({ ...row, role: 'retired' }))
])
const confirmation = computed(() => pending.value?.kind === 'drain'
  ? t('admin.accounts.accountDriver.runtime.forceDrainConfirm', { generation: pending.value.generation })
  : tr(pending.value?.kind === 'rollback' ? 'rollbackConfirm' : 'upgradeConfirm'))

async function refresh() {
  if (!props.show || loading.value || submitting.value) return
  const revision = ++requestRevision
  loading.value = true
  try {
    if (typeof adminAPI.accountDrivers?.getRuntime !== 'function') throw new Error(tr('notSupported'))
    const next = await adminAPI.accountDrivers.getRuntime()
    if (revision !== requestRevision) return
    status.value = next
    error.value = ''
  } catch (err) {
    if (revision === requestRevision) error.value = extractApiErrorMessage(err, tr('loadFailed'))
  } finally {
    if (revision === requestRevision) loading.value = false
  }
}
function prepare(kind: 'upgrade' | 'rollback' | 'drain', generation?: number) {
  if (!canMutate.value || !status.value) return
  if (!Number.isInteger(drainTimeout.value) || drainTimeout.value < 1 || drainTimeout.value > 600) { app.showError(tr('invalidDrainTimeout')); return }
  if (kind === 'upgrade' && (!executablePath.value.trim() || !/^[a-f\d]{64}$/i.test(sha256.value.trim()))) { app.showError(tr('invalidPackage')); return }
  pending.value = { kind, generation: generation ?? status.value.active_generation, path: executablePath.value.trim(), hash: sha256.value.trim().toLowerCase(), timeout: drainTimeout.value }
}
async function execute() {
  const action = pending.value
  pending.value = null
  if (!action || submitting.value) return
  requestRevision++
  loading.value = false
  submitting.value = true
  try {
    status.value = action.kind === 'upgrade'
      ? await adminAPI.accountDrivers.upgradeRuntime({ executable_path: action.path, sha256: action.hash, expected_generation: action.generation, drain_timeout_seconds: action.timeout })
      : action.kind === 'rollback'
        ? await adminAPI.accountDrivers.rollbackRuntime({ expected_generation: action.generation, drain_timeout_seconds: action.timeout })
        : await adminAPI.accountDrivers.forceDrainGeneration(action.generation)
    error.value = ''
    app.showSuccess(tr(`${action.kind}Success`))
    emit('changed')
  } catch (err) {
    app.showError(extractApiErrorMessage(err, tr('operationFailed')))
  } finally { submitting.value = false; await refresh() }
}
watch(() => props.show, show => {
  if (timer) clearInterval(timer)
  timer = undefined
  pending.value = null
  if (show) {
    void refresh()
    timer = setInterval(() => { void refresh() }, 3000)
  } else {
    requestRevision++
    loading.value = false
  }
}, { immediate: true })
onUnmounted(() => { requestRevision++; if (timer) clearInterval(timer) })
</script>
