import { onUnmounted, ref, watch } from 'vue'
import { adminAPI } from '@/api/admin'
import type { AccountDriverMaintenanceStatus } from './types'
import { isAccountDriverMaintenanceActive } from './maintenance'

export function useAccountMaintenanceStatuses(accountIDs: () => number[]) {
  const statuses = ref<Record<string, AccountDriverMaintenanceStatus | null>>({})
  let stopped = false
  let loading = false
  let supported = true
  let timer: ReturnType<typeof setInterval> | undefined

  async function refresh() {
    if (stopped || loading || !supported || typeof adminAPI.accountDrivers?.getMaintenance !== 'function') return
    loading = true
    const ids = [...accountIDs()]
    try {
      for (let offset = 0; offset < ids.length; offset += 4) {
        const results = await Promise.allSettled(ids.slice(offset, offset + 4).map(async id => ({ id, status: await adminAPI.accountDrivers.getMaintenance(id) })))
        if (stopped) return
        for (const result of results) {
          if (result.status === 'fulfilled') statuses.value[String(result.value.id)] = result.value.status
          else {
            const error = result.reason as { status?: number; response?: { status?: number } }
            if ((error.status ?? error.response?.status) === 404) supported = false
          }
        }
        if (!supported) break
      }
    } finally { loading = false }
  }
  watch(() => accountIDs().join(','), () => {
    void refresh()
    if (!timer && typeof adminAPI.accountDrivers?.getMaintenance === 'function') timer = setInterval(() => { void refresh() }, 5000)
  }, { immediate: true })
  onUnmounted(() => { stopped = true; if (timer) clearInterval(timer) })
  return {
    statuses, refresh,
    busy: (id: number) => isAccountDriverMaintenanceActive(statuses.value[String(id)]),
    update: (status: AccountDriverMaintenanceStatus | null, id: number) => { statuses.value[String(id)] = status }
  }
}
