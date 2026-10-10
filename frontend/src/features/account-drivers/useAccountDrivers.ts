import { ref } from 'vue'
import { adminAPI } from '@/api/admin'
import type { AccountDriver } from './types'

let inFlight: Promise<{ drivers: AccountDriver[] }> | null = null

// Older servers have no catalogue or binding. Their existing account flow is native.
export const NATIVE_DRIVER_ID = 'native'
export const CODEX_HTTP_DRIVER_ID = 'codex-rs-http'

export function useAccountDrivers() {
  const drivers = ref<AccountDriver[]>([])
  const loading = ref(false)
  const unavailable = ref(false)
  const resolved = ref(false)
  let generation = 0

  async function load() {
    if (typeof adminAPI.accountDrivers?.list !== 'function') return
    const currentGeneration = ++generation
    loading.value = true
    unavailable.value = false
    try {
      if (!inFlight) inFlight = adminAPI.accountDrivers.list()
      const catalog = await inFlight
      if (currentGeneration !== generation) return
      drivers.value = catalog.drivers
    } catch (error: unknown) {
      if (currentGeneration !== generation) return
      drivers.value = []
      const status = (error as { response?: { status?: number }; status?: number })?.response?.status
        ?? (error as { status?: number })?.status
      unavailable.value = status !== 404
    } finally {
      inFlight = null
      if (currentGeneration === generation) {
        loading.value = false
        resolved.value = true
      }
    }
  }

  return { drivers, loading, unavailable, resolved, load }
}
