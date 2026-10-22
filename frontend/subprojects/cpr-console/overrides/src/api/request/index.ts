import type { AxiosRequestConfig } from 'axios'
import type { RequestContext, RequestNext, RequestPlugin } from './plugins'
import type { RawResponse, RawResponseOptions } from './plugins/raw'
import { hostTransport } from '@/sub2api/bridge'
import { errorPlugin } from './plugins/error'
import { feedbackPlugin } from './plugins/feedback'
import { rawPlugin } from './plugins/raw'
import { retryPlugin } from './plugins/retry'
import { sessionPlugin } from './plugins/session'

export { ApiError } from './plugins/error'
export type { RawResponse, RawResponseMeta, RawResponseOptions } from './plugins/raw'
export { resetUnauthorizedHandling, setSessionRecoveryHandler, setUnauthorizedHandler } from './plugins/session'
export interface RequestOptions {
  silent?: boolean
  signal?: AbortSignal
  timeout?: number
  retry?: boolean
  skipSessionRecovery?: boolean
}
export type RequestConfig = AxiosRequestConfig & RequestOptions
const plugins: RequestPlugin[] = [feedbackPlugin, sessionPlugin, retryPlugin, errorPlugin, rawPlugin]
function execute(config: RequestConfig, raw?: RawResponseOptions) {
  const context: RequestContext = { config, raw, isCurrent: () => !config.signal?.aborted }
  const transport: RequestNext = () => hostTransport(context.config)
  return plugins.reduceRight<RequestNext>((next, plugin) => () => plugin(context, next), transport)()
}
export default async function request<T = unknown>(config: RequestConfig): Promise<T> {
  const { data } = await execute(config)
  const envelope = data !== null && typeof data === 'object' && 'data' in data && 'code' in data
  return (envelope ? data.data : data) as T
}
export async function requestRaw(config: RequestConfig, options: RawResponseOptions): Promise<RawResponse> {
  const { data } = await execute({ retry: false, ...config }, options)
  return data as RawResponse
}
