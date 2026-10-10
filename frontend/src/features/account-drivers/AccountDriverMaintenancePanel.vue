<template>
  <section v-if="supported || error || status" class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <div class="flex items-center justify-between gap-3"><span class="text-sm font-medium">{{ tr('title') }}</span><button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button></div>
    <p v-if="error" class="text-sm text-amber-600 dark:text-amber-400" role="status">{{ error }}</p>
    <div v-if="status?.operation_id && status.state !== 'idle'" class="space-y-1 text-sm">
      <p>{{ stateLabel(status.state) }} <span v-if="active" class="text-amber-600 dark:text-amber-400">{{ tr('blockedHint') }}</span></p>
      <p>{{ tr('target') }}: {{ accountDriverLabel({ id: status.target_driver_id }, t) }}</p>
      <p v-if="active">{{ tr('activeOperations') }}: {{ status.active_operations }}</p>
      <p v-if="status.error" class="text-red-600 dark:text-red-400">{{ status.error }}</p>
      <button v-if="active && status.state !== 'committing'" type="button" class="btn btn-secondary btn-sm" :disabled="submitting" @click="cancel">{{ tr('cancel') }}</button>
    </div>
    <div v-if="canSwitch" class="space-y-3">
      <p class="input-hint">{{ tr('switchHint') }}</p>
      <Select v-model="targetDriverID" :options="targetOptions" :aria-label="tr('target')" />
      <div><label class="input-label">{{ tr('drainTimeout') }}</label><input v-model.number="drainTimeout" class="input max-w-40" type="number" min="1" max="3600" step="1" /></div>
      <button type="button" class="btn btn-secondary" :disabled="!targetDriverID || submitting" @click="confirmSwitch = true">{{ tr('switch') }}</button>
    </div>
  </section>
  <ConfirmDialog :show="confirmSwitch" :title="tr('switch')" :message="tr('confirmSwitch')" @confirm="begin" @cancel="confirmSwitch = false" />
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'
import Select from '@/components/common/Select.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { accountDriverLabel } from './display'
import { isAccountDriverMaintenanceActive } from './maintenance'
import { useAccountDrivers } from './useAccountDrivers'
import type { AccountDriverMaintenanceStatus } from './types'

const props = defineProps<{ account: Account; show: boolean }>()
const emit = defineEmits<{ blocked: [value: boolean]; status: [value: AccountDriverMaintenanceStatus | null]; updated: [value: Account] }>()
const { t } = useI18n()
const tr = (key: string) => t(`admin.accounts.accountDriver.maintenance.${key}`)
const app = useAppStore()
const { drivers, load: loadDrivers } = useAccountDrivers()
const status = ref<AccountDriverMaintenanceStatus | null>(null)
const supported = ref(false)
const checked = ref(false)
const loading = ref(false)
const submitting = ref(false)
const error = ref('')
const targetDriverID = ref('')
const drainTimeout = ref(120)
const confirmSwitch = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
let requestRevision = 0
let completedOperationID = ''
let pendingOperationID = ''
const active = computed(() => isAccountDriverMaintenanceActive(status.value))
const blocked = computed(() => active.value || submitting.value || (!checked.value && loading.value) || (supported.value && !!error.value))
const targetOptions = computed(() => drivers.value.filter(driver => driver.available && driver.capabilities?.switch &&
  driver.id !== (props.account.account_driver?.id || 'native') && driver.platforms.includes(props.account.platform) && driver.account_types.includes(props.account.type))
  .map(driver => ({ value: driver.id, label: accountDriverLabel(driver, t) })))
const canSwitch = computed(() => supported.value && !blocked.value && props.account.account_driver?.capabilities?.switch === true && targetOptions.value.length > 0)
const stateLabel = (state: string) => ['preparing', 'draining', 'committing', 'completed', 'failed', 'cancelled'].includes(state)
  ? tr(`states.${state}`) : state

async function applyStatus(next: AccountDriverMaintenanceStatus | null) {
  status.value = next
  emit('status', next)
  if (next?.state === 'completed' && completedOperationID !== next.operation_id) {
    const account = await adminAPI.accounts.getById(props.account.id)
    completedOperationID = next.operation_id
    emit('updated', account)
    void loadDrivers()
  }
}
async function refresh() {
  if (!props.show || loading.value || submitting.value) return
  if (typeof adminAPI.accountDrivers?.getMaintenance !== 'function') { supported.value = false; return }
  const revision = ++requestRevision
  const accountID = props.account.id
  loading.value = true
  try {
    const next = await adminAPI.accountDrivers.getMaintenance(accountID)
    if (revision !== requestRevision || accountID !== props.account.id) return
    supported.value = true
    checked.value = true
    error.value = ''
    await applyStatus(next)
  } catch (err: unknown) {
    if (revision !== requestRevision) return
    const code = (err as { status?: number; response?: { status?: number } }).status ?? (err as { response?: { status?: number } }).response?.status
    supported.value = code !== 404
    checked.value = true
    error.value = code === 404 ? '' : extractApiErrorMessage(err, tr('loadFailed'))
  } finally { if (revision === requestRevision) loading.value = false }
}
function uuid() {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6] & 15) | 64
  bytes[8] = (bytes[8] & 63) | 128
  const hex = [...bytes].map(value => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
async function begin() {
  confirmSwitch.value = false
  if (!canSwitch.value || !targetOptions.value.some(option => option.value === targetDriverID.value)) return
  if (!Number.isInteger(drainTimeout.value) || drainTimeout.value < 1 || drainTimeout.value > 3600) { app.showError(tr('invalidDrainTimeout')); return }
  if (!pendingOperationID) pendingOperationID = uuid()
  submitting.value = true
  requestRevision++
  loading.value = false
  try {
    await applyStatus(await adminAPI.accountDrivers.switchDriver(props.account.id, {
      target_driver_id: targetDriverID.value,
      expected_binding_revision: props.account.account_driver?.binding_revision ?? 0,
      drain_timeout_seconds: drainTimeout.value,
      operation_id: pendingOperationID
    }))
    pendingOperationID = ''
    if (status.value?.state === 'failed') app.showError(status.value.error || tr('operationFailed'))
    else app.showSuccess(tr('started'))
  } catch (err) { app.showError(extractApiErrorMessage(err, tr('operationFailed'))) }
  finally { submitting.value = false; await refresh() }
}
async function cancel() {
  if (!status.value || !active.value || status.value.state === 'committing' || submitting.value) return
  submitting.value = true
  requestRevision++
  loading.value = false
  try { await applyStatus(await adminAPI.accountDrivers.cancelMaintenance(props.account.id, status.value.operation_id)) }
  catch (err) { app.showError(extractApiErrorMessage(err, tr('operationFailed'))) }
  finally { submitting.value = false; await refresh() }
}
watch(blocked, value => emit('blocked', value), { immediate: true })
watch([() => props.show, () => props.account.id], ([show]) => {
  if (timer) clearInterval(timer)
  timer = undefined
  requestRevision++
  loading.value = false
  status.value = null
  supported.value = false
  checked.value = false
  error.value = ''
  targetDriverID.value = ''
  pendingOperationID = ''
  if (show) { void refresh(); void loadDrivers(); timer = setInterval(() => { void refresh() }, 2000) }
}, { immediate: true })
onUnmounted(() => { requestRevision++; if (timer) clearInterval(timer) })
</script>
