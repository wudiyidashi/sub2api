export interface AccountDriverCapabilities {
  authorization: boolean
  import: boolean
  refresh: boolean
  quota: boolean
  test: boolean
  models: boolean
  settings: boolean
  switch: boolean
}

export interface AccountDriver {
  id: string
  name: string
  version: string
  available: boolean
  platforms: string[]
  account_types: string[]
  lifecycle_driver_id?: string
  execution_driver_id?: string
  transport_protocols?: string[]
  capabilities: AccountDriverCapabilities
}

export interface AccountDriverBinding {
  id: string
  name: string
  version: string
  available: boolean
  binding_revision: number
  lifecycle_driver_id?: string
  execution_driver_id?: string
  transport_protocols?: string[]
  capabilities: AccountDriverCapabilities
}

export interface AccountDriverCatalog {
  drivers: AccountDriver[]
  default_driver_id: string
}

export interface AccountDriverGenerationStatus {
  generation: number
  state: string
  available: boolean
  executable_path: string
  sha256: string
  version: string
  upstream_commit: string
  transports?: string[]
  started_at?: string | null
  published_at?: string | null
  drain_deadline?: string | null
  http_requests: number
  websocket_connections: number
  exit_reason?: string
}

export interface AccountDriverRuntimeStatus {
  enabled: boolean
  updating: boolean
  active_generation: number
  active: AccountDriverGenerationStatus | null
  candidate: AccountDriverGenerationStatus | null
  retired: AccountDriverGenerationStatus[]
  rollback_available: boolean
  last_error?: string
  persisted: boolean
}

export interface AccountDriverMaintenanceStatus {
  operation_id: string
  account_id: number
  source_driver_id: string
  target_driver_id: string
  expected_binding_revision: number
  state: 'preparing' | 'draining' | 'committing' | 'completed' | 'failed' | 'cancelled' | string
  started_at?: string | null
  updated_at?: string | null
  expires_at?: string | null
  error?: string
  active_operations: number
}
