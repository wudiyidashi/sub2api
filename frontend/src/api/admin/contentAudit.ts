import { apiClient } from '../client'
import type { OpsRequestDetail, OpsRequestDetailsParams, OpsRequestDetailsResponse } from './ops'

export interface ContentAuditRequestDetail {
  request?: OpsRequestDetail | null
  errors?: Array<Record<string, unknown>>
  prompt_events?: Array<Record<string, unknown>>
  client_request?: ContentAuditPayload | null
  upstream_request?: ContentAuditPayload | null
  upstream_response?: ContentAuditPayload | null
  client_response?: ContentAuditPayload | null
  risk_flags?: Array<Record<string, unknown>>
  attempts?: Array<Record<string, unknown>>
  sanitized_error_payload_available?: boolean
  raw_payloads?: ContentAuditRawPayload[]
}

export interface ContentAuditRawPayload {
  request_id: string
  client_request_id?: string
  direction: 'client_request' | 'client_response' | 'upstream_request' | 'upstream_response' | string
  content_type?: string
  headers?: Record<string, string | string[]>
  body?: string
  original_bytes?: number
  chunk_index?: number
  final?: boolean
  truncated?: boolean
  created_at?: string
}

export interface ContentAuditPayload {
  headers?: Record<string, string | string[]>
  body?: unknown
  raw?: string
  format?: 'json' | 'sse' | 'websocket' | 'text' | string
  events?: unknown[]
  truncated?: boolean
  bytes?: number
  [key: string]: unknown
}

export interface ContentAuditErrorPayload {
  error_id: number
  sanitized_error_body?: string
  payload_available: boolean
  upstream_status_code?: number
  upstream_error_message?: string
  upstream_error_detail?: string
  upstream_attempts?: Array<Record<string, unknown>>
}

export async function listRequests(params: OpsRequestDetailsParams): Promise<OpsRequestDetailsResponse> {
  const { data } = await apiClient.get<OpsRequestDetailsResponse>('/admin/content-audit/requests', { params })
  return data
}

export async function getRequest(requestID: string): Promise<ContentAuditRequestDetail> {
  const { data } = await apiClient.get<ContentAuditRequestDetail>(`/admin/content-audit/requests/${encodeURIComponent(requestID)}`)
  return data
}

export async function listAttempts(requestID: string, clientRequestID?: string): Promise<{ items: Array<Record<string, unknown>>; total: number }> {
  const { data } = await apiClient.get<{ items: Array<Record<string, unknown>>; total: number }>(`/admin/content-audit/requests/${encodeURIComponent(requestID)}/attempts`, {
    params: clientRequestID ? { client_request_id: clientRequestID } : undefined,
  })
  return data
}

export async function getErrorPayload(errorID: number): Promise<ContentAuditErrorPayload> {
  const { data } = await apiClient.get<ContentAuditErrorPayload>(`/admin/content-audit/errors/${errorID}/payload`)
  return data
}

export default { listRequests, getRequest, listAttempts, getErrorPayload }
