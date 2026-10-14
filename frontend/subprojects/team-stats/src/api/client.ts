import type { TeamStatsBridge, TeamStatsRequest } from '../../../../src/features/team-stats/bridge';

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}
interface RequestOptions {
  query?: object;
  signal?: AbortSignal;
  noCache?: boolean;
  cacheTtl?: number;
}
const cache = new Map<string, { expires: number; data: unknown }>();
let session = '';
function host(): TeamStatsBridge {
  try {
    if (window.parent !== window && window.parent.location.origin === window.location.origin) {
      const bridge = window.parent.__SUB2API_TEAM_STATS__;
      if (bridge) return bridge;
    }
  } catch { /* Only the same-origin host may supply the transport. */ }
  throw new ApiError(401, '请从主后台的“团队消耗看板”入口打开');
}
async function request<T>(path: string, method: TeamStatsRequest['method'], body: unknown, opts: RequestOptions): Promise<T> {
  const bridge = host();
  const currentSession = bridge.session();
  if (session !== currentSession) { cache.clear(); session = currentSession; }
  if (!currentSession) throw new ApiError(401, '请先登录主后台');
  const key = JSON.stringify([path, opts.query]);
  const ttl = opts.cacheTtl ?? (method === 'GET' ? 30_000 : 0);
  const hit = cache.get(key);
  if (!opts.noCache && ttl > 0 && hit && hit.expires > Date.now()) return hit.data as T;
  try {
    const data = await bridge.request<T>({ path, method, body, query: opts.query as Record<string, unknown>, signal: opts.signal });
    // Refreshing a token within the same login should succeed; a user switch must discard data.
    if (bridge.session() !== currentSession) { cache.clear(); session = bridge.session(); }
    if (!opts.noCache && ttl > 0) {
      if (cache.size >= 128) cache.clear();
      cache.set(key, { expires: Date.now() + ttl, data });
    }
    return data;
  } catch (error) {
    const e = error as { status?: number; message?: string };
    throw new ApiError(e.status ?? 0, e.message || '请求失败');
  }
}
export function clearApiCache(): void { cache.clear(); }
export const api = {
  get<T>(path: string, opts: RequestOptions = {}) { return request<T>(path, 'GET', undefined, opts); },
  post<T>(path: string, body?: unknown, opts: RequestOptions = {}) { return request<T>(path, 'POST', body, opts); },
  put<T>(path: string, body?: unknown, opts: RequestOptions = {}) { return request<T>(path, 'PUT', body, opts); },
};
