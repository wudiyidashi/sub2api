/** Same-origin subproject transport. Authentication stays in the host application. */
export interface TeamStatsRequest {
  path: string
  method: 'GET' | 'POST' | 'PUT'
  query?: Record<string, unknown>
  body?: unknown
  signal?: AbortSignal
}

export interface TeamStatsBridge {
  request<T>(request: TeamStatsRequest): Promise<T>
  session(): string
}

declare global {
  interface Window {
    __SUB2API_TEAM_STATS__?: TeamStatsBridge
  }
}

export function isTeamStatsRequestAllowed({ path, method }: TeamStatsRequest): boolean {
  if (path === '/admin/team-stats/config') return method === 'GET' || method === 'PUT'
  if (path === '/admin/dashboard/api-keys-usage') return method === 'POST'
  if (method !== 'GET') return false
  return [
    '/auth/me', '/admin/dashboard/stats', '/admin/dashboard/trend',
    '/admin/dashboard/models', '/admin/dashboard/users-ranking',
    '/admin/usage', '/admin/usage/stats', '/admin/usage/search-api-keys',
    '/admin/usage/search-users', '/admin/groups/all',
  ].includes(path) || /^\/admin\/groups\/[1-9]\d*\/api-keys$/.test(path)
}
