import type { AxiosResponse } from 'axios'
import type { RequestConfig } from '@/api/request'
import { AxiosHeaders } from 'axios'
import { ApiError } from '@/api/request/plugins/error'

interface HostResponse {
  status: number
  headers: Record<string, string>
  data: unknown
}
interface HostBridge {
  session: () => Promise<{ role: 'admin', expiresAt: string }>
  request: (input: { path: string, method: string, query?: unknown, body?: unknown, responseType?: 'arraybuffer', signal?: AbortSignal }) => Promise<HostResponse>
  stream: (path: string, signal: AbortSignal) => Promise<ReadableStream<Uint8Array>>
  theme: () => 'light' | 'dark'
}
declare global {
  interface Window {
    __SUB2API_CPR_BRIDGE__?: HostBridge
  }
}
export function hostBridge(): HostBridge {
  if (window.parent === window || window.parent.location.origin !== window.location.origin)
    throw new ApiError('请从管理面板打开账号池', 403)
  const bridge = window.parent.__SUB2API_CPR_BRIDGE__
  if (!bridge)
    throw new ApiError('账号池连接尚未就绪，请重新打开页面', 503)
  return bridge
}

export async function hostTransport(config: RequestConfig): Promise<AxiosResponse<unknown>> {
  try {
    const host = hostBridge()
    const path = config.url || ''
    if (path === '/api/auth/status' || path === '/api/auth/refresh') {
      const session = await host.session()
      return response(config, 200, { code: 200, message: 'OK', data: { authenticated: true, session } }, {})
    }
    const result = await host.request({
      path, method: (config.method || 'GET').toUpperCase(), query: config.params, body: config.data,
      responseType: config.responseType === 'arraybuffer' ? 'arraybuffer' : undefined,
      signal: config.signal instanceof AbortSignal ? config.signal : undefined,
    })
    return response(config, result.status, config.responseType === 'arraybuffer' ? result.data : { code: 200, message: 'OK', data: result.data }, result.headers)
  }
  catch (error) {
    if (error instanceof ApiError) throw error
    const value = error as { status?: number, message?: string, code?: number | string, name?: string, metadata?: { cpr_code?: number } }
    const code = value.status === 401 ? 40101 : value.metadata?.cpr_code ?? (typeof value.code === 'number' ? value.code : undefined)
    const cancelled = value.name === 'AbortError' || value.name === 'CanceledError' || value.code === 'STEP_UP_CANCELLED' || value.code === 'ERR_CANCELED'
    throw new ApiError(value.message || '账号池请求失败', value.status || 0, code, undefined, cancelled ? 'cancelled' : 'api')
  }
}
function response(config: RequestConfig, status: number, data: unknown, headers: Record<string, string>): AxiosResponse<unknown> {
  return { status, statusText: String(status), data, headers: new AxiosHeaders(headers), config: { ...config, headers: new AxiosHeaders() } }
}
