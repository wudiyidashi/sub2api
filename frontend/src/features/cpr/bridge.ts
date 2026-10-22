import contract from '../../../../backend/internal/cpr/management-endpoints.json'

export interface CPRConsoleRequest {
  path: string
  method: string
  query?: unknown
  body?: unknown
  responseType?: 'arraybuffer'
  signal?: AbortSignal
}
export interface CPRConsoleResponse {
  status: number
  headers: Record<string, string>
  data: unknown
}
export interface CPRConsoleBridge {
  session: () => Promise<{ role: 'admin'; expiresAt: string }>
  request: (input: CPRConsoleRequest) => Promise<CPRConsoleResponse>
  stream: (path: string, signal: AbortSignal) => Promise<ReadableStream<Uint8Array>>
  theme: () => 'light' | 'dark'
}
export function resolveCPRConsoleRequest(path: string, method: string, kind: 'request' | 'stream' = 'request') {
  if (!path.startsWith('/api/admin/') || path.includes('#')) return null
  const url = new URL(path, 'https://sub2api.invalid')
  if (url.origin !== 'https://sub2api.invalid') return null
  const endpoint = contract.endpoints.find(entry => entry.path === url.pathname && entry.method === method.toUpperCase())
  if (!endpoint || (kind === 'stream') !== (endpoint.kind === 'stream')) return null
  return { path: '/admin/cpr' + url.pathname.slice('/api/admin'.length), query: url.searchParams, endpoint }
}
declare global {
  interface Window {
    __SUB2API_CPR_BRIDGE__?: CPRConsoleBridge
  }
}
