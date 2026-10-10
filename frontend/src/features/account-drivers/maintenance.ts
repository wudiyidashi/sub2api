import type { AccountDriverMaintenanceStatus } from './types'
import { adminAPI } from '@/api/admin'

export function isAccountDriverMaintenanceActive(status: AccountDriverMaintenanceStatus | null | undefined): boolean {
  return !!status?.operation_id && !['completed', 'failed', 'cancelled', 'idle'].includes(status.state)
}

export async function canStartAccountDriverOperation(accountID: number): Promise<boolean> {
  if (typeof adminAPI.accountDrivers?.getMaintenance !== 'function') return true
  try { return !isAccountDriverMaintenanceActive(await adminAPI.accountDrivers.getMaintenance(accountID)) }
  catch (error: unknown) {
    const status = (error as { status?: number; response?: { status?: number } }).status ?? (error as { response?: { status?: number } }).response?.status
    if (status === 404) return true
    throw error
  }
}
