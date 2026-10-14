import { describe, expect, it } from 'vitest'
import { isTeamStatsRequestAllowed } from './bridge'

describe('team statistics transport boundary', () => {
  it('permits statistics reads and shared configuration edits', () => {
    expect(isTeamStatsRequestAllowed({ path: '/admin/usage', method: 'GET' })).toBe(true)
    expect(isTeamStatsRequestAllowed({ path: '/admin/groups/42/api-keys', method: 'GET' })).toBe(true)
    expect(isTeamStatsRequestAllowed({ path: '/admin/dashboard/api-keys-usage', method: 'POST' })).toBe(true)
    expect(isTeamStatsRequestAllowed({ path: '/admin/team-stats/config', method: 'PUT' })).toBe(true)
  })
  it.each(['/admin/users', '/admin/settings/admin-api-key', '/admin/groups/42', '/admin/groups/42/api-keys?foo=bar', 'https://example.com/admin/usage'])('rejects unrelated or noncanonical paths %s', path => {
    expect(isTeamStatsRequestAllowed({ path, method: 'GET' })).toBe(false)
  })
  it('rejects writes to usage and group APIs', () => {
    expect(isTeamStatsRequestAllowed({ path: '/admin/usage', method: 'PUT' })).toBe(false)
    expect(isTeamStatsRequestAllowed({ path: '/admin/groups/42/api-keys', method: 'POST' })).toBe(false)
  })
})
