import { apiClient } from '../client'
import type { AccountDriverCatalog, AccountDriverMaintenanceStatus, AccountDriverRuntimeStatus } from '@/features/account-drivers/types'

export async function list(): Promise<AccountDriverCatalog> {
  const { data } = await apiClient.get<AccountDriverCatalog>('/admin/account-drivers')
  return data
}

export async function getRuntime(): Promise<AccountDriverRuntimeStatus> {
  const { data } = await apiClient.get<AccountDriverRuntimeStatus>('/admin/account-drivers/runtime')
  return data
}

export async function upgradeRuntime(payload: {
  executable_path: string
  sha256: string
  expected_generation: number
  drain_timeout_seconds: number
}): Promise<AccountDriverRuntimeStatus> {
  const { data } = await apiClient.post<AccountDriverRuntimeStatus>('/admin/account-drivers/runtime/upgrade', payload)
  return data
}

export async function rollbackRuntime(payload: { expected_generation: number; drain_timeout_seconds: number }): Promise<AccountDriverRuntimeStatus> {
  const { data } = await apiClient.post<AccountDriverRuntimeStatus>('/admin/account-drivers/runtime/rollback', payload)
  return data
}

export async function forceDrainGeneration(generation: number): Promise<AccountDriverRuntimeStatus> {
  const { data } = await apiClient.post<AccountDriverRuntimeStatus>('/admin/account-drivers/runtime/drain', { generation })
  return data
}

export async function getMaintenance(accountID: number): Promise<AccountDriverMaintenanceStatus | null> {
  const { data } = await apiClient.get<AccountDriverMaintenanceStatus | null>(`/admin/accounts/${accountID}/driver-maintenance`)
  return data
}

export async function switchDriver(accountID: number, payload: {
  target_driver_id: string
  expected_binding_revision: number
  drain_timeout_seconds: number
  operation_id: string
}): Promise<AccountDriverMaintenanceStatus> {
  const { data } = await apiClient.post<AccountDriverMaintenanceStatus>(`/admin/accounts/${accountID}/driver-switch`, payload)
  return data
}

export async function cancelMaintenance(accountID: number, operationID: string): Promise<AccountDriverMaintenanceStatus> {
  const { data } = await apiClient.post<AccountDriverMaintenanceStatus>(`/admin/accounts/${accountID}/driver-maintenance/cancel`, { operation_id: operationID })
  return data
}

export default { list, getRuntime, upgradeRuntime, rollbackRuntime, forceDrainGeneration, getMaintenance, switchDriver, cancelMaintenance }
