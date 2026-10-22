import { describe, expect, it } from 'vitest'
import { resolveCPRConsoleRequest } from '../bridge'

describe('CPR account console management boundary', () => {
  it('resolves only the pinned account-pool routes and their methods', () => {
    expect(resolveCPRConsoleRequest('/api/admin/accounts?provider=openai', 'GET')?.path).toBe('/admin/cpr/accounts')
    expect(resolveCPRConsoleRequest('/api/admin/accounts?provider=openai', 'GET')?.query.get('provider')).toBe('openai')
    expect(resolveCPRConsoleRequest('/api/admin/settings/client-profiles/openai/preview', 'POST')).not.toBeNull()
    expect(resolveCPRConsoleRequest('/api/admin/accounts/reset-credits', 'GET')).not.toBeNull()
    expect(resolveCPRConsoleRequest('/api/admin/accounts/reset-credits', 'POST')).not.toBeNull()
    expect(resolveCPRConsoleRequest('/api/admin/accounts', 'POST')).toBeNull()
  })
  it('rejects native business APIs, internal keys, external targets and path traversal', () => {
    for (const path of [
      '/api/v1/admin/users',
      '/api/admin/client-keys/reveal',
      '/api/admin/settings/admin-api-key',
      '/api/admin/system/restart',
      'https://other.example/api/admin/accounts',
      '//other.example/api/admin/accounts',
      '/api/admin/accounts/../settings/admin-api-key',
      '/api/admin/accounts#unexpected',
    ]) expect(resolveCPRConsoleRequest(path, 'GET')).toBeNull()
  })
  it('keeps event streams separate from JSON and binary requests', () => {
    const path = '/api/admin/accounts/connection-test?accountId=remote-one'
    expect(resolveCPRConsoleRequest(path, 'GET', 'stream')?.query.get('accountId')).toBe('remote-one')
    expect(resolveCPRConsoleRequest(path, 'GET')).toBeNull()
    expect(resolveCPRConsoleRequest('/api/admin/accounts', 'GET', 'stream')).toBeNull()
  })
})
